// Command love is a cache-first command line English dictionary.
//
//	love <word>
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

	"github.com/out-of-energy/love/internal/dict"
	"github.com/out-of-energy/love/internal/render"
	"github.com/out-of-energy/love/internal/storage"
)

// version is a var so release builds can override it with
// -ldflags "-X main.version=...".
var version = "0.2.0"

// Documented exit codes. Scripts rely on these, so they are part of the
// interface rather than an implementation detail.
const (
	exitOK         = 0
	exitError      = 1
	exitUsage      = 2
	exitAPI        = 3
	exitCacheWrite = 4
)

type config struct {
	paths   storage.Paths
	baseURL string
	model   string
	timeout time.Duration
	color   bool
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run holds the whole program so tests can drive it without spawning a process.
func run(args []string, stdout, stderr io.Writer) int {
	word, done, code := parseArgs(args, stdout, stderr)
	if done {
		return code
	}
	if word == "" {
		fmt.Fprintln(stderr, "love: no word given")
		fmt.Fprintln(stderr, "Usage: love <word>")
		fmt.Fprintln(stderr, "Try 'love --help' for more information.")
		return exitUsage
	}

	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(stderr, "love: %v\n", err)
		return exitError
	}

	store := storage.OpenWords(cfg.paths.Words)

	// Read first, so a damaged line is reported before anything else happens.
	words, warnings, err := store.Load()
	for _, warning := range warnings {
		fmt.Fprintf(stderr, "love: warning: %s\n", warning)
	}
	if err != nil {
		fmt.Fprintf(stderr, "love: warning: cannot read %s: %v\n", cfg.paths.Words, err)
	}

	// Upgrade a pre-v0.3 file in place. This is the only write the tool performs
	// without being asked, so it is deliberately conservative: only a clean file
	// is rewritten, and the original is copied aside first.
	if err == nil && len(warnings) == 0 {
		if report, migrateErr := store.Migrate(time.Now()); migrateErr != nil {
			fmt.Fprintf(stderr, "love: warning: %v\n", migrateErr)
		} else if report.Performed {
			fmt.Fprintf(stderr, "love: upgraded %d word(s) to the current format (backup: %s)\n",
				report.Migrated, report.Backup)
			if reloaded, _, reloadErr := store.Load(); reloadErr == nil {
				words = reloaded
			}
		}
	}

	// Cache first. A hit costs nothing and must never touch the network.
	if existing, ok := storage.Find(words, word); ok {
		render.Record(stdout, existing.Word, existing.IPA, existing.ELI5, existing.Chinese, cfg.color)
		return exitOK
	}

	// Only now, on a genuine miss, do we need a key.
	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(stderr, "love: DEEPSEEK_API_KEY is not set")
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "Export it in your shell profile, then open a new terminal:")
		fmt.Fprintln(stderr, `  export DEEPSEEK_API_KEY="sk-..."`)
		return exitUsage
	}

	// Ctrl-C aborts an in-flight request instead of leaving the user waiting.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	client := dict.NewClient(apiKey, cfg.baseURL, cfg.model, cfg.timeout)
	entry, err := client.Generate(ctx, word)
	if err != nil {
		fmt.Fprintf(stderr, "love: %v\n", err)
		return exitAPI
	}

	// Add is idempotent: if another run stored this word while we were waiting
	// on the API, its record wins and is what we print.
	added, err := store.Add(storage.Word{
		Word:    entry.Word,
		IPA:     entry.IPA,
		ELI5:    entry.ELI5,
		Chinese: entry.Chinese,
	}, time.Now())
	if err != nil {
		// Never swallow a result the user already paid for, but never pretend
		// it was remembered either.
		render.Record(stdout, entry.Word, entry.IPA, entry.ELI5, entry.Chinese, cfg.color)
		fmt.Fprintf(stderr, "love: warning: result was not saved: %v\n", err)
		return exitCacheWrite
	}

	saved := added.Word
	render.Record(stdout, saved.Word, saved.IPA, saved.ELI5, saved.Chinese, cfg.color)
	return exitOK
}

// parseArgs resolves the pre-word flags. Everything that is not a flag is part
// of the word, so both `love ice cream` and `love "ice cream"` work.
func parseArgs(args []string, stdout, stderr io.Writer) (word string, done bool, code int) {
	var words []string
	for _, arg := range args {
		switch arg {
		case "-h", "--help", "help":
			printHelp(stdout)
			return "", true, exitOK
		case "-V", "--version", "version":
			fmt.Fprintf(stdout, "love %s\n", version)
			return "", true, exitOK
		case "-":
			fmt.Fprintln(stderr, "love: reading from stdin is not supported yet")
			return "", true, exitUsage
		default:
			if strings.HasPrefix(arg, "-") {
				fmt.Fprintf(stderr, "love: unknown option %q\n", arg)
				fmt.Fprintln(stderr, "Try 'love --help' for more information.")
				return "", true, exitUsage
			}
			words = append(words, arg)
		}
	}
	return storage.Normalize(strings.Join(words, " ")), false, exitOK
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
		// names the words file, and the other two live beside it.
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
  love <word>

Look a word up. The first lookup asks DeepSeek (fastest, cheapest model by
default) and stores the answer in your personal word file; every lookup after
that is served from that file with no network request and no cost.

Environment:
  DEEPSEEK_API_KEY    required, your DeepSeek API key (never stored)
  DEEPSEEK_BASE_URL   API base URL (default https://api.deepseek.com/v1)
  EWH_DIR             store directory (default ~/.ewh)
  EWH_CACHE           words file path, for backwards compatibility
  EWH_MODEL           model id (default `+dict.DefaultModel+`)
  NO_COLOR            disable colored output

Files (under the store directory):
  words.jsonl         your words
  reviews.jsonl       your learning history
  memory.json         derived state, rebuildable from the two above

Flags:
  -h, --help          show this help
  -V, --version       show version

Examples:
  love serendipity
  love ice cream
`)
}
