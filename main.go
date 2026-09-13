// Command love is a cache-first command line English dictionary.
//
//	love <word>          look a word up
//	love --review        work through today's review
//
// The first lookup for a word asks DeepSeek for its IPA, a deliberately
// child-simple English explanation, and one common Chinese meaning, then stores
// the result in a personal word file. Every later lookup is served from that
// file with no network request and no cost.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/out-of-energy/love/internal/ai"
	"github.com/out-of-energy/love/internal/dict"
	"github.com/out-of-energy/love/internal/memory"
	"github.com/out-of-energy/love/internal/render"
	"github.com/out-of-energy/love/internal/review"
	"github.com/out-of-energy/love/internal/storage"
)

// version is a var so release builds can override it with
// -ldflags "-X main.version=...".
var version = "0.3.0"

// Documented exit codes. Scripts rely on these, so they are part of the
// interface rather than an implementation detail.
const (
	exitOK         = 0
	exitError      = 1
	exitUsage      = 2
	exitAPI        = 3
	exitCacheWrite = 4
)

// action is one engine operation, reachable through a mode flag.
//
// Actions are flags and never subcommands, and that is a structural decision
// rather than a stylistic one. `love <word>` accepts any word at all, and words
// are an open set that cannot be enumerated: a subcommand named "review" would
// make that word permanently unlookupable, and every future subcommand would
// consume another word. A flag cannot collide with a word, so the two spaces
// never overlap and no reserved-word list is needed.
//
// The same shape is what `tar` (-c/-t/-x), `emerge`, and `pacman` (-S/-R/-Q)
// use, for the same reason: their positional argument is also an open set of
// names.
type action struct {
	name  string
	flags []string
	usage string
	run   func(*app) int
}

// actions is the single registry of engine operations.
//
// Adding an operation means adding exactly one row here. The guard tests in
// main_test.go then enforce the invariants that keep the grammar safe: every
// row must be reachable, every flag must be distinguishable from a word, and no
// flag may be claimed twice.
var actions = []action{
	{
		name:  "review",
		flags: []string{"--review"},
		usage: "开始今天的复习",
		run:   runReview,
	},
}

// lookupAction returns the action a flag belongs to.
func lookupAction(arg string) *action {
	for i := range actions {
		for _, flag := range actions[i].flags {
			if arg == flag {
				return &actions[i]
			}
		}
	}
	return nil
}

type config struct {
	paths   storage.Paths
	baseURL string
	model   string
	timeout time.Duration
	color   bool
}

// app carries what an action needs, so actions stay free functions and the
// registry stays a plain table.
type app struct {
	cfg    config
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run holds the whole program so tests can drive it without spawning a process.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cmd, done, code := parseArgs(args, stdout, stderr)
	if done {
		return code
	}

	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(stderr, "love: %v\n", err)
		return exitError
	}
	a := &app{cfg: cfg, stdin: stdin, stdout: stdout, stderr: stderr}

	if cmd.action != nil {
		return cmd.action.run(a)
	}
	return lookup(a, cmd.word)
}

// command is what the arguments resolved to: either a word to look up, or an
// engine operation.
type command struct {
	word   string
	action *action
}

// parseArgs resolves arguments.
//
// A word and an action can never both be given: the two grammars are disjoint,
// so `love --review maintain` is a usage error rather than a guess about which
// one the user meant.
func parseArgs(args []string, stdout, stderr io.Writer) (cmd command, done bool, code int) {
	var words []string

	for _, arg := range args {
		switch arg {
		case "-h", "--help", "help":
			printHelp(stdout)
			return command{}, true, exitOK
		case "-V", "--version", "version":
			fmt.Fprintf(stdout, "love %s\n", version)
			return command{}, true, exitOK
		case "-":
			fmt.Fprintln(stderr, "love: reading from stdin is not supported yet")
			return command{}, true, exitUsage
		}

		if found := lookupAction(arg); found != nil {
			if cmd.action != nil && cmd.action != found {
				fmt.Fprintf(stderr, "love: %s 与 %s 不能同时使用\n", cmd.action.flags[0], arg)
				return command{}, true, exitUsage
			}
			cmd.action = found
			continue
		}

		if strings.HasPrefix(arg, "-") {
			fmt.Fprintf(stderr, "love: unknown option %q\n", arg)
			if len(actions) > 0 {
				fmt.Fprintln(stderr, "可用的动作:")
				for _, a := range actions {
					fmt.Fprintf(stderr, "  %-12s %s\n", strings.Join(a.flags, ", "), a.usage)
				}
			}
			fmt.Fprintln(stderr, "Try 'love --help' for more information.")
			return command{}, true, exitUsage
		}

		words = append(words, arg)
	}

	cmd.word = storage.Normalize(strings.Join(words, " "))

	if cmd.action != nil && cmd.word != "" {
		fmt.Fprintf(stderr, "love: %s 不接受单词参数\n", cmd.action.flags[0])
		fmt.Fprintln(stderr, "Try 'love --help' for more information.")
		return command{}, true, exitUsage
	}
	return cmd, false, exitOK
}

// lookup serves one word: from the store if it is there, from the API if not.
func lookup(a *app, word string) int {
	if word == "" {
		fmt.Fprintln(a.stderr, "love: no word given")
		fmt.Fprintln(a.stderr, "Usage: love <word>")
		fmt.Fprintln(a.stderr, "Try 'love --help' for more information.")
		return exitUsage
	}

	store := storage.OpenWords(a.cfg.paths.Words)

	// Read first, so a damaged line is reported before anything else happens.
	words, warnings, err := store.Load()
	for _, warning := range warnings {
		fmt.Fprintf(a.stderr, "love: warning: %s\n", warning)
	}
	if err != nil {
		fmt.Fprintf(a.stderr, "love: warning: cannot read %s: %v\n", a.cfg.paths.Words, err)
	}

	// Upgrade a pre-v0.3 file in place. This is the only write the tool performs
	// without being asked, so it is deliberately conservative: only a clean file
	// is rewritten, and the original is copied aside first.
	if err == nil && len(warnings) == 0 {
		if report, migrateErr := store.Migrate(time.Now()); migrateErr != nil {
			fmt.Fprintf(a.stderr, "love: warning: %v\n", migrateErr)
		} else if report.Performed {
			fmt.Fprintf(a.stderr, "love: upgraded %d word(s) to the current format (backup: %s)\n",
				report.Migrated, report.Backup)
			if reloaded, _, reloadErr := store.Load(); reloadErr == nil {
				words = reloaded
			}
		}
	}

	// Cache first. A hit costs nothing and must never touch the network.
	if existing, ok := storage.Find(words, word); ok {
		render.Record(a.stdout, existing.Word, existing.IPA, existing.ELI5, existing.Chinese, a.cfg.color)
		return exitOK
	}

	// Only now, on a genuine miss, do we need a key.
	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(a.stderr, "love: DEEPSEEK_API_KEY is not set")
		fmt.Fprintln(a.stderr)
		fmt.Fprintln(a.stderr, "Export it in your shell profile, then open a new terminal:")
		fmt.Fprintln(a.stderr, `  export DEEPSEEK_API_KEY="sk-..."`)
		return exitUsage
	}

	// Ctrl-C aborts an in-flight request instead of leaving the user waiting.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	client := dict.NewClient(apiKey, a.cfg.baseURL, a.cfg.model, a.cfg.timeout)
	entry, err := client.Generate(ctx, word)
	if err != nil {
		fmt.Fprintf(a.stderr, "love: %v\n", err)
		return exitAPI
	}

	added, err := store.Add(storage.Word{
		Word:    entry.Word,
		IPA:     entry.IPA,
		ELI5:    entry.ELI5,
		Chinese: entry.Chinese,
	}, time.Now())
	if err != nil {
		// Never swallow a result the user already paid for, but never pretend
		// it was remembered either.
		render.Record(a.stdout, entry.Word, entry.IPA, entry.ELI5, entry.Chinese, a.cfg.color)
		fmt.Fprintf(a.stderr, "love: warning: result was not saved: %v\n", err)
		return exitCacheWrite
	}

	saved := added.Word
	render.Record(a.stdout, saved.Word, saved.IPA, saved.ELI5, saved.Chinese, a.cfg.color)
	return exitOK
}

// runReview assembles a session and runs it interactively.
func runReview(a *app) int {
	stdinFile, isFile := a.stdin.(*os.File)
	if !isFile || !render.IsTerminal(stdinFile) {
		// clig.dev: never prompt unless stdin is a terminal. A script that runs
		// this by accident must be told, not left hanging on a read.
		fmt.Fprintln(a.stderr, "love: --review 需要在终端里交互运行")
		return exitUsage
	}

	ports, err := buildReviewPorts(a.cfg, time.Now())
	if err != nil {
		fmt.Fprintf(a.stderr, "love: %v\n", err)
		return exitError
	}

	if _, err := ports.Run(a.stdin, a.stdout); err != nil {
		fmt.Fprintf(a.stderr, "love: %v\n", err)
		return exitError
	}
	return exitOK
}

// buildReviewPorts assembles everything a session needs, including the closures
// that persist what the learner does.
//
// It is separate from runReview so the persistence path — the part that decides
// whether an evening's work survives — can be tested without a terminal. The
// interactive loop needs a tty; storing a grading does not.
func buildReviewPorts(cfg config, now time.Time) (review.Ports, error) {
	session := memory.DefaultConfig()

	words, _, err := storage.OpenWords(cfg.paths.Words).Load()
	if err != nil {
		return review.Ports{}, fmt.Errorf("cannot read words: %w", err)
	}

	reviews, _, err := storage.OpenReviews(cfg.paths.Reviews).Load()
	if err != nil {
		return review.Ports{}, fmt.Errorf("cannot read review history: %w", err)
	}

	// The event log is the source of truth; memory.json is only a cache, so the
	// session replays rather than trusting it.
	states := session.Reduce(reviews)

	unlearned := make([]string, 0, len(words))
	for _, w := range words {
		if _, started := states[w.ID]; !started {
			unlearned = append(unlearned, w.ID)
		}
	}

	// Expansion content is loaded once, up front: a session must never stall on
	// the network, and a word without expansion simply shows its anchor alone.
	generated, _, err := storage.OpenGenerated(cfg.paths.Generated).Load()
	if err != nil {
		return review.Ports{}, fmt.Errorf("cannot read generated content: %w", err)
	}

	grade := func(wordID string, prev memory.State, rating memory.Rating) (memory.State, error) {
		at := time.Now()
		event := session.NewReview(fmt.Sprintf("r-%d-%s", at.UnixNano(), wordID), wordID, prev, rating, at)
		if err := storage.OpenReviews(cfg.paths.Reviews).Append(event); err != nil {
			return memory.State{}, fmt.Errorf("cannot record the review: %w", err)
		}

		next := session.Advance(prev, rating, at)
		states[wordID] = next

		// Rewrite the cache after every grading, so quitting midway — or the
		// machine dying midway — loses nothing already answered.
		from, err := storage.Fingerprint(cfg.paths)
		if err != nil {
			return memory.State{}, err
		}
		if err := storage.SaveMemory(cfg.paths.Memory, storage.NewMemoryFile(states, from)); err != nil {
			return memory.State{}, fmt.Errorf("cannot update the state cache: %w", err)
		}
		return next, nil
	}

	return review.Ports{
		Config: session,
		Words:  words,
		States: states,
		Color:  cfg.color,
		Plan:   session.PlanDay(now, states, unlearned),
		Expansion: func(wordID string) *ai.Content {
			if g, ok := storage.FindGenerated(generated, wordID); ok {
				content := g.Content
				return &content
			}
			return nil
		},
		Grade: grade,
	}, nil
}

func loadConfig() (config, error) {
	paths, err := storage.DefaultPaths()
	if err != nil {
		return config{}, err
	}
	cfg := config{
		paths:   paths,
		baseURL: dict.DefaultBaseURL,
		model:   dict.DefaultModel,
		timeout: 15 * time.Second,
		color:   render.IsTerminal(os.Stdout) && os.Getenv("NO_COLOR") == "",
	}
	if v := os.Getenv("EWH_DIR"); v != "" {
		cfg.paths = storage.PathsIn(v)
	}
	if v := os.Getenv("EWH_CACHE"); v != "" {
		// Backwards compatible: the flag that used to name the only file now
		// names the words file, and the other files live beside it.
		cfg.paths = storage.PathsIn(filepath.Dir(v))
		cfg.paths.Words = v
	}
	if v := os.Getenv("DEEPSEEK_BASE_URL"); v != "" {
		cfg.baseURL = v
	}
	if v := os.Getenv("EWH_MODEL"); v != "" {
		cfg.model = v
	}
	return cfg, nil
}

func printHelp(w io.Writer) {
	fmt.Fprint(w, `love - a cache-first command line English dictionary

Usage:
  love <word>              查词

Look a word up. The first lookup asks DeepSeek (fastest, cheapest model by
default) and stores the answer in your personal word file; every lookup after
that is served from that file with no network request and no cost.

Actions:
  --review                 开始今天的复习

  Actions are flags rather than subcommands on purpose: any word at all can be
  looked up, so a subcommand named after a word would make that word
  unlookupable.

Environment:
  DEEPSEEK_API_KEY    required for a new word; never stored
  DEEPSEEK_BASE_URL   API base URL (default https://api.deepseek.com/v1)
  EWH_DIR             store directory (default ~/.ewh)
  EWH_CACHE           words file path, for backwards compatibility
  EWH_MODEL           model id (default `+dict.DefaultModel+`)
  NO_COLOR            disable colored output

Files (under the store directory):
  words.jsonl         your words
  reviews.jsonl       your learning history
  memory.json         derived state, rebuildable from the two above
  generated.jsonl     cached example sentences and dialogue

Flags:
  -h, --help          show this help
  -V, --version       show version

Examples:
  love serendipity
  love ice cream
  love --review
`)
}
