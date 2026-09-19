// Command love is a cache-first command line English dictionary.
//
//	love <word>          look a word up
//	love --review        work through today's review
//
// The first lookup for a word asks DeepSeek for its IPA, how to sound it out,
// how it is built from a prefix, a root and a suffix, a deliberately
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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/out-of-energy/love/internal/ai"
	"github.com/out-of-energy/love/internal/dict"
	"github.com/out-of-energy/love/internal/lexicon"
	"github.com/out-of-energy/love/internal/mail"
	"github.com/out-of-energy/love/internal/memory"
	"github.com/out-of-energy/love/internal/render"
	"github.com/out-of-energy/love/internal/review"
	"github.com/out-of-energy/love/internal/schedule"
	"github.com/out-of-energy/love/internal/storage"
)

// version is a var so release builds can override it with
// -ldflags "-X main.version=...".
var version = "0.4.0"

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
	{
		name:  "daily",
		flags: []string{"--daily"},
		usage: "生成今天的邮件并发送",
		run:   runDaily,
	},
	{
		name:  "backfill",
		flags: []string{"--backfill"},
		usage: "为旧词补齐拼读与构词（需要 API key）",
		run:   runBackfill,
	},
	{
		name:  "install-schedule",
		flags: []string{"--install-schedule"},
		usage: "安装每日定时任务（launchd，默认 07:30）",
		run:   runInstallSchedule,
	},
	{
		name:  "uninstall-schedule",
		flags: []string{"--uninstall-schedule"},
		usage: "移除每日定时任务",
		run:   runUninstallSchedule,
	},
}

// option is a modifier that actions may accept.
//
// Options are registered for the same reason actions are: the guards in
// main_test.go can then prove mechanically that nothing claims a flag twice and
// that no flag can be mistaken for a word.
type option struct {
	flag       string
	usage      string
	takesValue bool
}

var options = []option{
	{flag: "--dry-run", usage: "只渲染，不发送"},
	{flag: "--out", usage: "把邮件 HTML 写入文件", takesValue: true},
	{flag: "--at", usage: "运行时间，多个用逗号分隔（如 07:30,12:30,20:30）", takesValue: true},
	{flag: "--now", usage: "安装后立刻试跑一次"},
	{flag: "--recheck", usage: "与 --backfill 同用：重算已不符合数据的构词（拼读每次免费校对）"},
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

// lookupOption returns the option a flag belongs to, and whether it was found.
func lookupOption(arg string) (option, bool) {
	for _, o := range options {
		if arg == o.flag {
			return o, true
		}
	}
	return option{}, false
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
	cmd    command
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
	a := &app{cfg: cfg, cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr}

	if cmd.action != nil {
		return cmd.action.run(a)
	}
	return lookup(a, cmd.word)
}

// command is what the arguments resolved to: either a word to look up, or an
// engine operation with its options.
type command struct {
	word    string
	action  *action
	options map[string]string
}

// optionValue returns the value of an option, and whether it was given.
func (c command) optionValue(flag string) (string, bool) {
	v, ok := c.options[flag]
	return v, ok
}

// has reports whether a flag was given.
func (c command) has(flag string) bool {
	_, ok := c.options[flag]
	return ok
}

// parseArgs resolves arguments.
//
// A word and an action can never both be given: the two grammars are disjoint,
// so `love --review maintain` is a usage error rather than a guess about which
// one the user meant.
func parseArgs(args []string, stdout, stderr io.Writer) (cmd command, done bool, code int) {
	cmd.options = map[string]string{}

	for i := 0; i < len(args); i++ {
		arg := args[i]

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

		// An option may be written as --flag=value, which is why the name is
		// split off before it is looked up.
		name, inlineValue, hasInline := strings.Cut(arg, "=")
		if parsed, ok := lookupOption(name); ok {
			if parsed.takesValue && !hasInline {
				if i+1 >= len(args) {
					fmt.Fprintf(stderr, "love: %s 需要一个值\n", parsed.flag)
					return command{}, true, exitUsage
				}
				i++
				inlineValue = args[i]
			}
			if !parsed.takesValue && hasInline {
				fmt.Fprintf(stderr, "love: %s 不接受值\n", parsed.flag)
				return command{}, true, exitUsage
			}
			cmd.options[parsed.flag] = inlineValue
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
			if len(options) > 0 {
				fmt.Fprintln(stderr, "可用的选项:")
				for _, o := range options {
					fmt.Fprintf(stderr, "  %-12s %s\n", o.flag, o.usage)
				}
			}
			fmt.Fprintln(stderr, "Try 'love --help' for more information.")
			return command{}, true, exitUsage
		}

		cmd.word = storage.Normalize(strings.Join([]string{cmd.word, arg}, " "))
	}

	if cmd.action == nil && len(cmd.options) > 0 {
		fmt.Fprintln(stderr, "love: 选项必须与动作一起使用")
		fmt.Fprintln(stderr, "Try 'love --help' for more information.")
		return command{}, true, exitUsage
	}

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

	// Cache first. A hit costs nothing and must never touch the network — not
	// even to upgrade a record to the current schema. Filling in a missing form
	// layer costs a request, so it belongs to the paths that already spend
	// requests (--daily and --backfill), never here.
	if existing, ok := storage.Find(words, word); ok {
		render.Record(a.stdout, anchorOf(existing), a.cfg.color)
		return exitOK
	}

	// Only now, on a genuine miss, do we need a key.
	apiKey := deepSeekKey()
	if apiKey == "" {
		explainMissingKey(a.stderr)
		return exitUsage
	}

	// Ctrl-C aborts an in-flight request instead of leaving the user waiting.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	client := dict.NewClient(apiKey, a.cfg.baseURL, a.cfg.model, a.cfg.timeout)
	opts := dataOptions(a, word)
	entry, err := client.Generate(ctx, word, opts)
	if err != nil {
		fmt.Fprintf(a.stderr, "love: %v\n", err)
		return exitAPI
	}
	ipa, phonics, phonicsSource := opts.ApplySound(word, entry.IPA, entry.Phonics)
	parts, partsSource := opts.Apply(entry.Parts)

	added, err := store.Add(storage.Word{
		Word:          entry.Word,
		IPA:           ipa,
		Phonics:       phonics,
		PhonicsSource: phonicsSource,
		Parts:         parts,
		PartsSource:   partsSource,
		ELI5:          entry.ELI5,
		Chinese:       entry.Chinese,
	}, time.Now())
	if err != nil {
		// Never swallow a result the user already paid for, but never pretend
		// it was remembered either.
		render.Record(a.stdout, anchorOf(storage.Word{
			Word:    entry.Word,
			IPA:     ipa,
			Phonics: phonics,
			Parts:   parts,
			ELI5:    entry.ELI5,
		}), a.cfg.color)
		fmt.Fprintf(a.stderr, "love: warning: result was not saved: %v\n", err)
		return exitCacheWrite
	}

	render.Record(a.stdout, anchorOf(added.Word), a.cfg.color)
	return exitOK
}

// needsResplit reports whether the recorded data now disagrees with the
// segmentation stored for w.
//
// This is what makes --recheck cheap enough to run often. A word whose split
// the data already agrees with needs no request at all, so revisiting a whole
// dictionary costs nothing until something actually changes: a rebuilt data
// layer, a corrected table, or a hand correction added after the word was first
// looked up. Only then does the word get looked at again.
func needsResplit(lex *lexicon.Lexicon, w storage.Word) bool {
	switch w.PartsSource {
	case "morphology-compound", "morphology-declined":
		// The data allowed this answer and the model took it — a compound, or a
		// refusal. It will never match the data's preferred analysis, so
		// re-asking would spend a request only to get the same answer back.
		return false
	}

	analysis := lex.Analyze(w.Word)
	if !analysis.Known || len(analysis.Candidates) == 0 {
		return false
	}
	if w.Parts == "" {
		return true
	}
	// Any recorded analysis counts as current, not just the preferred one. The
	// model is allowed to choose among them, and a choice it was allowed to make
	// must not read as stale — that would re-ask forever and, worse, quietly
	// replace a correct analysis with the same answer it rejected.
	for _, candidate := range analysis.Candidates {
		if lexicon.SameStems(w.Parts, candidate.Forms()) {
			return false
		}
	}
	return true
}

// dataOptions translates the data layer's answers into the constraint the model
// is given.
//
// An unknown word yields the zero Options, which is the old behaviour: no data,
// so the model works it out alone. That is not a failure, it is the tail — and
// the tail is why the model is still here.
//
// The two halves are looked up independently, because they answer different
// questions and are repaired by different means: the morphology index says how
// the word is built, the pronunciation index says how it sounds, and a store can
// have one without the other.
func dataOptions(a *app, word string) dict.Options {
	lex := lexicon.Open(lexicon.StorePath(a.cfg.paths.Words))

	var opts dict.Options
	if analysis := lex.Analyze(word); analysis.Known {
		opts.Known = true
		if analysis.FromOverride {
			opts.Source = "override"
		}
		for _, candidate := range analysis.Candidates {
			segments := make([]dict.Segment, 0, len(candidate.Parts))
			for _, part := range candidate.Parts {
				segments = append(segments, dict.Segment{Form: part.Form, Kind: part.Kind, Gloss: part.Gloss})
			}
			opts.Segments = append(opts.Segments, segments)
		}
	}
	if p, ok := lex.Pronounce(word); ok {
		opts.Sound = &dict.Sound{
			IPA:            p.IPA,
			SoundChunks:    p.SoundChunks,
			SpellingChunks: p.SpellingChunks,
			Source:         p.Source,
		}
	}
	return opts
}

// anchorOf projects a stored word onto the block the terminal prints.
func anchorOf(w storage.Word) render.Anchor {
	return render.Anchor{
		Word:    w.Word,
		IPA:     w.IPA,
		Phonics: w.Phonics,
		Parts:   w.Parts,
		ELI5:    w.ELI5,
	}
}

// explainMissingKey prints the one message that says how to fix it.
func explainMissingKey(w io.Writer) {
	fmt.Fprintln(w, "love: DEEPSEEK_API_KEY is not set")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Export it in your shell profile, then open a new terminal:")
	fmt.Fprintln(w, `  export DEEPSEEK_API_KEY="sk-..."`)
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

// runDaily builds today's digest, generating any missing expansion content, and
// delivers it.
//
// Generation and delivery are deliberately separate steps: a word whose
// expansion failed to generate still appears in the email with its anchor
// layer, because a broken model call must never cost the learner the day's
// review. The same split is why --dry-run exists — the content and the layout
// can be inspected without sending anything.
func runDaily(a *app) int {
	now := time.Now()
	session := memory.DefaultConfig()

	words, warnings, err := storage.OpenWords(a.cfg.paths.Words).Load()
	for _, w := range warnings {
		fmt.Fprintf(a.stderr, "love: warning: %s\n", w)
	}
	if err != nil {
		fmt.Fprintf(a.stderr, "love: cannot read words: %v\n", err)
		return exitError
	}

	reviews, _, err := storage.OpenReviews(a.cfg.paths.Reviews).Load()
	if err != nil {
		fmt.Fprintf(a.stderr, "love: cannot read review history: %v\n", err)
		return exitError
	}

	states := session.Reduce(reviews)

	byID := make(map[string]storage.Word, len(words))
	unlearned := make([]string, 0, len(words))
	for _, w := range words {
		byID[w.ID] = w
		if _, started := states[w.ID]; !started {
			unlearned = append(unlearned, w.ID)
		}
	}

	plan := session.PlanDay(now, states, unlearned)
	if plan.Total() == 0 {
		fmt.Fprintln(a.stdout, "今天没有需要复习的词。")
		return exitOK
	}
	today := append(append([]string{}, plan.Review...), plan.New...)

	generated, _, err := storage.OpenGenerated(a.cfg.paths.Generated).Load()
	if err != nil {
		fmt.Fprintf(a.stderr, "love: warning: cannot read generated content: %v\n", err)
	}
	content := make(map[string]ai.Content, len(generated))
	for _, g := range generated {
		content[g.WordID] = g.Content
	}

	// Only words the digest will actually show are worth generating for, and
	// each is generated at most once ever: the cache is what keeps a word's
	// practice material stable from day to day.
	var missing []string
	for _, id := range today {
		if _, ok := content[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		generatedCount, genErr := generateMissing(a, missing, byID, content)
		if genErr != nil {
			fmt.Fprintf(a.stderr, "love: warning: %v\n", genErr)
		}
		if generatedCount > 0 {
			fmt.Fprintf(a.stderr, "love: 生成了 %d 个词的扩展内容\n", generatedCount)
		}
	}

	// The form layer is an upgrade of records that already exist, and an upgrade
	// costs one request per word — so it happens here, in the run that is
	// already paying for model calls, and never in a lookup. Only the words the
	// digest will actually show are worth filling in.
	var needForm []string
	for _, id := range today {
		if w, ok := byID[id]; ok && w.NeedsForm() {
			needForm = append(needForm, id)
		}
	}
	if len(needForm) > 0 && deepSeekKey() != "" {
		stats := backfillForm(a, byID, needForm)
		for _, failure := range stats.Failures {
			fmt.Fprintf(a.stderr, "love: warning: %s\n", failure)
		}
		if stats.Filled > 0 {
			fmt.Fprintf(a.stderr, "love: 补齐了 %d 个词的拼读与构词（%d 个来自词法数据）\n",
				stats.Filled, stats.FromData)
		}
	}

	digest := mail.Digest{Date: now}
	for _, id := range today {
		w, ok := byID[id]
		if !ok {
			continue
		}
		entry := mail.Word{
			Word:    w.Word,
			IPA:     w.IPA,
			Phonics: w.Phonics,
			Parts:   w.Parts,
			ELI5:    w.ELI5,
			Chinese: w.Chinese,
		}
		if c, ok := content[id]; ok {
			copied := c
			entry.Content = &copied
		}
		digest.Words = append(digest.Words, entry)
	}

	htmlBody, err := mail.RenderHTML(digest)
	if err != nil {
		fmt.Fprintf(a.stderr, "love: %v\n", err)
		return exitError
	}
	textBody, err := mail.RenderText(digest)
	if err != nil {
		fmt.Fprintf(a.stderr, "love: %v\n", err)
		return exitError
	}

	if path, ok := a.cmd.optionValue("--out"); ok {
		if err := os.WriteFile(path, []byte(htmlBody), 0o644); err != nil {
			fmt.Fprintf(a.stderr, "love: cannot write %s: %v\n", path, err)
			return exitError
		}
		fmt.Fprintf(a.stdout, "已写入 %s\n", path)
	}

	if a.cmd.has("--dry-run") {
		fmt.Fprint(a.stdout, textBody)
		return exitOK
	}

	smtp, err := mail.ConfigFromEnv()
	if err != nil {
		fmt.Fprintf(a.stderr, "love: %v\n", err)
		fmt.Fprintln(a.stderr, "用 --dry-run 可以只渲染不发送，用 --out FILE 可以存成 HTML。")
		return exitUsage
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := mail.NewSender(smtp).Send(ctx, mail.Subject(digest), htmlBody, textBody); err != nil {
		fmt.Fprintf(a.stderr, "love: %v\n", err)
		return exitAPI
	}
	fmt.Fprintf(a.stdout, "已发送到 %s\n", smtp.To)
	return exitOK
}

// runBackfill fills the form layer of every word that predates it.
//
// This is the one action whose whole job is an upgrade, so it is explicit
// rather than automatic: it costs one request per word and needs a key, and the
// user should get to decide when to spend that. A daily run fills the same gap
// for the words it happens to show; this fills the whole file at once.
//
// With --recheck it also revisits words whose segmentation came from a model
// alone, which is what makes the morphology data worth installing after the
// fact: the data improves, the words that need it are exactly the ones marked
// parts_source=model, and nothing else has to be regenerated.
func runBackfill(a *app) int {
	words, warnings, err := storage.OpenWords(a.cfg.paths.Words).Load()
	for _, warning := range warnings {
		fmt.Fprintf(a.stderr, "love: warning: %s\n", warning)
	}
	if err != nil {
		fmt.Fprintf(a.stderr, "love: cannot read words: %v\n", err)
		return exitError
	}

	lex := lexicon.Open(lexicon.StorePath(a.cfg.paths.Words))
	if !lex.Available() && !lex.PhonicsAvailable() {
		fmt.Fprintln(a.stderr, "love: 未找到数据层，构词与拼读仍由模型生成")
		fmt.Fprintln(a.stderr, "      构建：python3 scripts/build_morphology.py && python3 scripts/build_phonics.py")
	}

	// The sounds are repaired first, and for every word, because a dictionary
	// lookup costs nothing: no request, no key, no spend. That is what lets a
	// whole dictionary of existing records have its syllable boundaries fixed
	// at once — including on a machine with no API key at all.
	if repaired, repairErr := repairPhonics(a, lex, words); repairErr != nil {
		fmt.Fprintf(a.stderr, "love: warning: %v\n", repairErr)
	} else if repaired > 0 {
		fmt.Fprintf(a.stdout, "按词典修正了 %d 个词的音标与音节切分。\n", repaired)
		if reloaded, _, reloadErr := storage.OpenWords(a.cfg.paths.Words).Load(); reloadErr == nil {
			words = reloaded
		}
	}

	recheck := a.cmd.has("--recheck")
	byID := make(map[string]storage.Word, len(words))
	var missing []string
	for _, w := range words {
		byID[w.ID] = w
		switch {
		case w.NeedsForm():
			missing = append(missing, w.ID)
		case recheck && needsResplit(lex, w):
			// Has both lines, but the data no longer agrees with them.
			missing = append(missing, w.ID)
		}
	}
	if len(missing) == 0 {
		if recheck {
			fmt.Fprintln(a.stdout, "每个词的构词都已经来自词法数据，没有需要重算的。")
		} else {
			fmt.Fprintln(a.stdout, "每个词都已经有拼读和构词，没有需要补齐的。")
		}
		printProvisional(a, byID)
		return exitOK
	}

	if deepSeekKey() == "" {
		explainMissingKey(a.stderr)
		return exitUsage
	}

	action := "缺少拼读或构词，开始补齐"
	if recheck {
		action = "的构词需要按词法数据重算"
	}
	fmt.Fprintf(a.stdout, "%d 个词%s。\n", len(missing), action)

	stats := backfillForm(a, byID, missing)
	for _, failure := range stats.Failures {
		fmt.Fprintf(a.stderr, "love: warning: %s\n", failure)
	}
	fmt.Fprintf(a.stdout, "已处理 %d/%d 个词，其中 %d 个的构词来自词法数据。\n",
		stats.Filled, len(missing), stats.FromData)
	printProvisional(a, byID)
	if stats.Filled == 0 && len(stats.Failures) > 0 {
		return exitAPI
	}
	return exitOK
}

// repairPhonics rewrites the sound lines a pronunciation dictionary can improve,
// without spending a single request.
//
// It exists because the two halves of the data layer have different costs. A
// segmentation that is missing or stale can only be rebuilt by asking a model,
// so that stays behind --recheck and a key. A pronunciation is a lookup: the
// dictionary already knows, the only question is whether what is stored differs
// from what it says. So this runs unconditionally, over every word, and it is
// what turned "profiling → /faɪl/ · /ɪŋ/" into "/faɪ/ · /lɪŋ/" for the whole
// store in one pass with no API calls.
func repairPhonics(a *app, lex *lexicon.Lexicon, words []storage.Word) (int, error) {
	if !lex.PhonicsAvailable() {
		return 0, nil
	}

	forms := make(map[string]storage.Form)
	for _, w := range words {
		p, ok := lex.Pronounce(w.Word)
		if !ok {
			continue
		}
		// The existing line is passed in so the spelling side can be kept: the
		// model's chunking of the letters was never the mistake.
		_, line, _ := dict.Options{Sound: &dict.Sound{
			IPA:            p.IPA,
			SoundChunks:    p.SoundChunks,
			SpellingChunks: p.SpellingChunks,
			Source:         p.Source,
		}}.ApplySound(w.Word, w.IPA, w.Phonics)
		if w.IPA == p.IPA && w.Phonics == line && w.PhonicsSource == "dictionary" {
			continue
		}
		forms[w.Normalized] = storage.Form{IPA: p.IPA, Phonics: line, PhonicsSource: "dictionary"}
	}
	if len(forms) == 0 {
		return 0, nil
	}

	changed, missing, err := storage.OpenWords(a.cfg.paths.Words).SetForms(forms)
	if err != nil {
		return 0, err
	}
	if len(missing) > 0 {
		fmt.Fprintf(a.stderr, "love: warning: %d 条记录在修正过程中消失\n", len(missing))
	}
	return changed, nil
}

// printProvisional names the words whose segmentation only a model has ever had
// an opinion on.
//
// They are the tail: coinages, proper nouns, typos, and anything nobody has
// written an etymology for. Nothing in this tool can verify them — a second
// opinion would only produce another unverifiable answer — so the honest thing
// is to name them rather than let a plausible guess sit in the dictionary
// looking exactly like a recorded fact.
func printProvisional(a *app, byID map[string]storage.Word) {
	var words []string
	for _, w := range byID {
		if w.PartsSource == "model" {
			words = append(words, w.Word)
		}
	}
	if len(words) == 0 {
		return
	}
	sort.Strings(words)

	fmt.Fprintf(a.stdout, "\n以下 %d 个词的构词只有模型给过意见，词法数据无法验证：\n", len(words))
	for _, word := range words {
		fmt.Fprintf(a.stdout, "  %s\n", word)
	}
	fmt.Fprintln(a.stdout, "核对后可写进 internal/lexicon/overrides.jsonl（一行一个，格式见该文件），")
	fmt.Fprintln(a.stdout, "那条更正优先级最高，--recheck 不会推翻它。")
}

// backfillStats reports what a form-layer pass did.
type backfillStats struct {
	Filled   int
	FromData int
	Failures []string
}

// backfillForm asks the model for the form layer of the given words and writes
// it back in place, updating byID so a caller that builds a digest from it sees
// the new values.
//
// Failures are collected rather than fatal, for the same reason they are
// collected during expansion generation: eleven words upgraded and one failure
// is a better outcome than none.
//
// Only the missing fields are written. The anchor is a user asset, and an
// upgrade has no business rewriting an explanation the learner has already read
// a hundred times — or an ipa line, or a phonics line, that is already there.
func backfillForm(a *app, byID map[string]storage.Word, ids []string) backfillStats {
	apiKey := deepSeekKey()
	if apiKey == "" {
		return backfillStats{Failures: []string{"DEEPSEEK_API_KEY is not set"}}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	client := dict.NewClient(apiKey, a.cfg.baseURL, a.cfg.model, a.cfg.timeout)
	store := storage.OpenWords(a.cfg.paths.Words)

	var stats backfillStats

	for i, id := range ids {
		w, ok := byID[id]
		if !ok {
			continue
		}
		if a.cfg.color {
			fmt.Fprintf(a.stderr, "\r  %d/%d 补齐拼读与构词…", i+1, len(ids))
		}

		opts := dataOptions(a, w.Word)
		entry, err := client.Generate(ctx, w.Word, opts)
		if err != nil {
			stats.Failures = append(stats.Failures, fmt.Sprintf("%s: %v", w.Word, err))
			continue
		}
		parts, source := opts.Apply(entry.Parts)
		form := storage.Form{Parts: parts, Source: source}

		// Each field is written only when it is missing, or when a dictionary
		// supplied it. A model's fresh wording must not replace an ipa or a
		// phonics line the learner has already read — the anchor exists to stay
		// still — and the free dictionary pass has already run by this point,
		// so whatever is left here is the model's own answer.
		ipa, phonics, phonicsSource := opts.ApplySound(w.Word, entry.IPA, entry.Phonics)
		if w.Phonics == "" || phonicsSource == "dictionary" {
			form.Phonics, form.PhonicsSource = phonics, phonicsSource
		}
		if w.IPA == "" || phonicsSource == "dictionary" {
			form.IPA = ipa
		}

		saved, err := store.SetForm(w.Normalized, form)
		if err != nil {
			stats.Failures = append(stats.Failures, fmt.Sprintf("%s: %v", w.Word, err))
			continue
		}
		if source != "model" {
			stats.FromData++
		}
		byID[id] = saved
		stats.Filled++
	}
	if a.cfg.color && len(ids) > 0 {
		fmt.Fprint(a.stderr, "\r\033[K")
	}
	return stats
}

// generateMissing fills in expansion content for the given words.
//
// Failures are collected rather than fatal: a digest with three anchors and two
// expansions is worth far more than no digest at all.
func generateMissing(a *app, missing []string, byID map[string]storage.Word, content map[string]ai.Content) (int, error) {
	apiKey := deepSeekKey()
	if apiKey == "" {
		fmt.Fprintln(a.stderr, "love: DEEPSEEK_API_KEY is not set; 本次邮件只包含锚点层")
		return 0, nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	provider := ai.NewDeepSeek(apiKey, a.cfg.baseURL, a.cfg.model, 90*time.Second)
	store := storage.OpenGenerated(a.cfg.paths.Generated)

	generatedCount := 0
	var failures []string

	for i, id := range missing {
		w, ok := byID[id]
		if !ok {
			continue
		}
		if a.cfg.color {
			fmt.Fprintf(a.stderr, "\r  %d/%d 生成扩展内容…", i+1, len(missing))
		}

		c, err := provider.GenerateContent(ctx, w.Word)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", w.Word, err))
			continue
		}
		saved, _, err := store.Save(storage.Generated{
			WordID:   id,
			Word:     w.Word,
			Provider: provider.Name(),
			Content:  c,
		}, time.Now())
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", w.Word, err))
			continue
		}
		content[id] = saved.Content
		generatedCount++
	}
	if a.cfg.color && len(missing) > 0 {
		fmt.Fprint(a.stderr, "\r\033[K")
	}

	if len(failures) > 0 {
		return generatedCount, fmt.Errorf("%d 个词的扩展内容生成失败（邮件已降级为锚点层）：%s",
			len(failures), strings.Join(failures, "; "))
	}
	return generatedCount, nil
}

// runInstallSchedule registers the daily run with launchd.
func runInstallSchedule(a *app) int {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(a.stderr, "love: cannot determine the executable path: %v\n", err)
		return exitError
	}
	// launchd stores the path verbatim, so recording a symlink that later moves
	// would leave a job that silently never runs.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	times := []schedule.TimeOfDay{{Hour: 7, Minute: 30}}
	if value, ok := a.cmd.optionValue("--at"); ok {
		parsed, err := parseTimes(value)
		if err != nil {
			fmt.Fprintf(a.stderr, "love: %v\n", err)
			return exitUsage
		}
		times = parsed
	}

	env, err := scheduleEnv()
	if err != nil {
		fmt.Fprintf(a.stderr, "love: %v\n", err)
		return exitUsage
	}

	job := schedule.Job{
		Binary: exe,
		Times:  times,
		Env:    env,
		LogDir: filepath.Join(filepath.Dir(a.cfg.paths.Words), "logs"),
	}
	path, err := job.Install()
	if err != nil {
		fmt.Fprintf(a.stderr, "love: %v\n", err)
		return exitError
	}

	fmt.Fprintf(a.stdout, "已安装 %s\n", path)
	fmt.Fprintf(a.stdout, "每天 %s 运行 %s --daily（共 %d 次）\n", formatTimes(times), exe, len(times))
	fmt.Fprintf(a.stdout, "日志：%s\n", filepath.Join(job.LogDir, "daily.err"))
	fmt.Fprintln(a.stdout, "笔记本休眠错过时间时，launchd 会在唤醒后补跑。")

	if a.cmd.has("--now") {
		if err := schedule.RunNow(); err != nil {
			fmt.Fprintf(a.stderr, "love: %v\n", err)
			return exitError
		}
		fmt.Fprintln(a.stdout, "已触发一次试跑，稍后看日志和邮箱。")
	}
	return exitOK
}

func runUninstallSchedule(a *app) int {
	removed, err := schedule.Uninstall()
	if err != nil {
		fmt.Fprintf(a.stderr, "love: %v\n", err)
		return exitError
	}
	if !removed {
		fmt.Fprintln(a.stdout, "定时任务本来就没有安装。")
		return exitOK
	}
	fmt.Fprintln(a.stdout, "已移除定时任务。")
	return exitOK
}

// formatTimes renders a schedule for a human.
func formatTimes(times []schedule.TimeOfDay) string {
	parts := make([]string, 0, len(times))
	for _, t := range times {
		parts = append(parts, t.String())
	}
	return strings.Join(parts, " · ")
}

// parseTimes reads one or more HH:MM times, comma-separated.
//
// More than one time is how a thrice-daily push is expressed: launchd takes an
// array of intervals, so three sends are one job rather than three agents.
func parseTimes(value string) ([]schedule.TimeOfDay, error) {
	var times []schedule.TimeOfDay
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		// A trailing or doubled comma is a typo. Skipping it silently would drop
		// a run time the user believed they had set.
		if part == "" {
			return nil, fmt.Errorf("时间应为 HH:MM，多个用逗号分隔（如 07:30,12:30,20:30），收到 %q", value)
		}
		hour, minute, err := parseClock(part)
		if err != nil {
			return nil, fmt.Errorf("时间应为 HH:MM，多个用逗号分隔（如 07:30,12:30,20:30），收到 %q", value)
		}
		times = append(times, schedule.TimeOfDay{Hour: hour, Minute: minute})
	}
	if len(times) == 0 {
		return nil, fmt.Errorf("没有解析出任何时间：%q", value)
	}
	return times, nil
}

// parseClock reads an HH:MM time.
func parseClock(value string) (hour, minute int, err error) {
	bad := fmt.Errorf("时间格式应为 HH:MM，收到 %q", value)
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 {
		return 0, 0, bad
	}
	hour, hourErr := strconv.Atoi(strings.TrimSpace(parts[0]))
	minute, minuteErr := strconv.Atoi(strings.TrimSpace(parts[1]))
	if hourErr != nil || minuteErr != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, bad
	}
	return hour, minute, nil
}

// scheduleEnv builds the environment the scheduled run will receive.
//
// It refuses to embed a secret. A property list is readable by anything running
// as the user and `launchctl print` reproduces it in full, so the only safe
// thing to record is the path to a file that holds the secret. Refusing loudly
// beats writing the key somewhere it will later be printed into a bug report.
func scheduleEnv() (map[string]string, error) {
	env := map[string]string{
		"PATH": "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin",
	}
	for _, name := range []string{
		"SMTP_HOST", "SMTP_PORT", "SMTP_USER", "SMTP_FROM", "LOVE_MAIL_TO", "EWH_DIR", "EWH_CACHE",
	} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			env[name] = value
		}
	}

	if path := strings.TrimSpace(os.Getenv("SMTP_PASSWORD_FILE")); path != "" {
		env["SMTP_PASSWORD_FILE"] = expandHome(path)
	} else if os.Getenv("SMTP_PASSWORD") != "" || os.Getenv("QQ_SMTP_AUTH_CODE") != "" {
		return nil, fmt.Errorf("定时任务不会把密码写进 plist（`launchctl print` 会原样打印它）。\n" +
			"  先写入文件再安装：\n" +
			"    printf '%%s' '你的16位授权码' > $HOME/.ewh/smtp-password && chmod 600 $HOME/.ewh/smtp-password\n" +
			"    export SMTP_PASSWORD_FILE=$HOME/.ewh/smtp-password")
	}

	if path := strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY_FILE")); path != "" {
		env["DEEPSEEK_API_KEY_FILE"] = expandHome(path)
	} else if strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY")) != "" {
		return nil, fmt.Errorf("同理，API key 也只接受文件路径。\n" +
			"    printf '%%s' \"$DEEPSEEK_API_KEY\" > $HOME/.ewh/deepseek-key && chmod 600 $HOME/.ewh/deepseek-key\n" +
			"    export DEEPSEEK_API_KEY_FILE=$HOME/.ewh/deepseek-key")
	}

	return env, nil
}

// expandHome resolves a leading ~, which a shell would have expanded but an
// environment variable does not.
func expandHome(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}

// deepSeekKey returns the API key, preferring a file over the environment.
//
// The environment is only a fallback. A launch agent inherits no shell profile,
// so a scheduled run needs the key somewhere else — and writing it into the
// property list would put a secret where `launchctl print` reproduces it in
// full.
func deepSeekKey() string {
	if path := strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY_FILE")); path != "" {
		if data, err := os.ReadFile(expandHome(path)); err == nil {
			if key := strings.TrimSpace(string(data)); key != "" {
				return key
			}
		}
	}
	return strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY"))
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
default) for the word's IPA, how to sound it out, how it is built from a prefix,
a root and a suffix, a child-simple explanation and one Chinese meaning, then
stores the answer in your personal word file; every lookup after that is served
from that file with no network request and no cost.

`)

	// The lists come from the registries rather than from this string. A second
	// hand-maintained copy is a copy that drifts: --install-schedule existed
	// for a while without appearing here at all.
	fmt.Fprintln(w, "Actions:")
	for _, a := range actions {
		fmt.Fprintf(w, "  %-24s %s\n", strings.Join(a.flags, ", "), a.usage)
	}
	fmt.Fprint(w, `
  Actions are flags rather than subcommands on purpose: any word at all can be
  looked up, so a subcommand named after a word would make that word
  unlookupable.

`)
	fmt.Fprintln(w, "Options:")
	for _, o := range options {
		fmt.Fprintf(w, "  %-24s %s\n", o.flag, o.usage)
	}

	fmt.Fprint(w, `
Environment:
  DEEPSEEK_API_KEY    your API key; required for a new word, never stored
  DEEPSEEK_API_KEY_FILE  path to a file holding the key; preferred
  DEEPSEEK_BASE_URL   API base URL (default https://api.deepseek.com/v1)
  EWH_DIR             store directory (default ~/.ewh)
  EWH_CACHE           words file path, for backwards compatibility
  EWH_MODEL           model id (default `+dict.DefaultModel+`)
  NO_COLOR            disable colored output

Mail (needed for --daily to send):
  SMTP_HOST           default smtp.qq.com
  SMTP_PORT           default 465 (implicit TLS)
  SMTP_USER           the account, used as sender and default recipient
  SMTP_PASSWORD_FILE  path to a file holding the password (preferred)
  QQ_SMTP_AUTH_CODE   16-digit QQ authorization code, if no file is given
  LOVE_MAIL_TO        recipient, default SMTP_USER

Files (under the store directory):
  words.jsonl         your words: ipa, phonics, parts, eli5, chinese
  reviews.jsonl       your learning history
  memory.json         derived state, rebuildable from the two above
  generated.jsonl     cached example sentences and dialogue
  lexicon/            how words are built and how they sound (optional)

Flags:
  -h, --help          show this help
  -V, --version       show version

Examples:
  love serendipity
  love ice cream
  love --review
  love --daily --dry-run
  love --daily --out /tmp/today.html
  love --backfill
  love --backfill --recheck
  love --install-schedule --at 07:30,12:30,20:30
`)
}
