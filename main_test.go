package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/out-of-energy/love/internal/memory"
	"github.com/out-of-energy/love/internal/schedule"
	"github.com/out-of-energy/love/internal/storage"
)

// ---------------------------------------------------------------------------
// Guards on the grammar.
//
// These tests are what make the action registry safe to extend. `love <word>`
// accepts any word at all, and words are an open set, so an action may only be
// reachable through something that cannot be a word. Each invariant below
// exists because breaking it silently removes a word from the dictionary.
// ---------------------------------------------------------------------------

func TestEveryRegisteredActionIsReachable(t *testing.T) {
	for _, a := range actions {
		for _, flag := range a.flags {
			var out, errOut bytes.Buffer
			cmd, done, code := parseArgs([]string{flag}, &out, &errOut)
			if done || code != exitOK {
				t.Errorf("%s: done=%v code=%d stderr=%s", flag, done, code, errOut.String())
				continue
			}
			if cmd.action == nil || cmd.action.name != a.name {
				t.Errorf("%s did not resolve to action %q", flag, a.name)
			}
		}
	}
}

// The invariant the whole design rests on. A flag that did not begin with "-"
// would be consumed by the word branch first, so the action would be silently
// unreachable — and every word equal to that flag would become unlookupable.
func TestActionFlagsCannotBeConfusedWithWords(t *testing.T) {
	for _, a := range actions {
		if len(a.flags) == 0 {
			t.Errorf("action %q has no flag, so it can never be reached", a.name)
		}
		for _, flag := range a.flags {
			if !strings.HasPrefix(flag, "-") {
				t.Errorf("action %q uses %q, which the word branch would swallow", a.name, flag)
			}
		}
	}
}

func TestActionFlagsAndNamesAreUnique(t *testing.T) {
	flagOwner := map[string]string{}
	names := map[string]bool{}

	for _, a := range actions {
		if names[a.name] {
			t.Errorf("action %q is registered more than once", a.name)
		}
		names[a.name] = true

		for _, flag := range a.flags {
			if owner, taken := flagOwner[flag]; taken {
				t.Errorf("flag %q is claimed by both %q and %q", flag, owner, a.name)
			}
			flagOwner[flag] = a.name
		}
	}
}

// The concrete regression this design exists to prevent. Every one of these was
// a planned subcommand name, and each must remain an ordinary lookup.
func TestWordsThatLookLikeCommandNamesStillResolveToLookup(t *testing.T) {
	for _, word := range []string{"review", "daily", "stats", "export", "add", "import", "rm", "list"} {
		var out, errOut bytes.Buffer
		cmd, done, code := parseArgs([]string{word}, &out, &errOut)
		if done {
			t.Errorf("%q terminated early with code %d: %s", word, code, errOut.String())
			continue
		}
		if cmd.action != nil {
			t.Errorf("%q resolved to action %q instead of a lookup", word, cmd.action.name)
			continue
		}
		if cmd.word != word {
			t.Errorf("%q resolved to word %q", word, cmd.word)
		}
	}
}

func TestActionRejectsAWordArgument(t *testing.T) {
	var out, errOut bytes.Buffer
	_, done, code := parseArgs([]string{"--review", "maintain"}, &out, &errOut)
	if !done || code != exitUsage {
		t.Fatalf("done=%v code=%d", done, code)
	}
	if !strings.Contains(errOut.String(), "不接受单词参数") {
		t.Errorf("stderr = %q", errOut.String())
	}
}

func TestTwoActionsAtOnceIsAUsageError(t *testing.T) {
	if len(actions) < 2 {
		t.Skip("needs at least two registered actions")
	}
	var out, errOut bytes.Buffer
	args := []string{actions[0].flags[0], actions[1].flags[0]}
	if _, done, code := parseArgs(args, &out, &errOut); !done || code != exitUsage {
		t.Fatalf("done=%v code=%d", done, code)
	}
}

func TestUnknownOptionListsTheAvailableActions(t *testing.T) {
	var out, errOut bytes.Buffer
	_, done, code := parseArgs([]string{"--nope"}, &out, &errOut)
	if !done || code != exitUsage {
		t.Fatalf("done=%v code=%d", done, code)
	}
	if !strings.Contains(errOut.String(), "unknown option") {
		t.Errorf("stderr = %q", errOut.String())
	}
	for _, a := range actions {
		if !strings.Contains(errOut.String(), a.flags[0]) {
			t.Errorf("the error should list %s, got %q", a.flags[0], errOut.String())
		}
	}
}

// ---------------------------------------------------------------------------
// Lookup behaviour.
// ---------------------------------------------------------------------------

func writeWords(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunServesACacheHitWithoutAnAPIKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "words.jsonl")
	writeWords(t, path, `{"word":"evil","ipa":"/ˈiːvəl/",`+
		`"phonics":"e·vil → /ˈiː/ · /vəl/",`+
		`"parts":"no clear prefix or suffix (whole word from Old English)",`+
		`"eli5":"Very, very bad.","chinese":"邪恶的"}`+"\n")
	t.Setenv("EWH_CACHE", path)
	t.Setenv("DEEPSEEK_API_KEY", "")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"EVIL"}, strings.NewReader(""), &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitOK, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "Very, very bad.") || !strings.Contains(out, "Phonics: e·vil") {
		t.Errorf("stdout = %q", out)
	}
	// The gloss is stored and never printed: the terminal is where a word is
	// recalled, and the gloss is the answer to that test.
	if strings.Contains(out, "中文") || strings.Contains(out, "邪恶的") {
		t.Errorf("the terminal block must not carry the gloss: %q", out)
	}
}

// A word file written before the form layer existed must stay readable, and a
// hit on it must stay offline — the missing lines are filled by --backfill or
// by the next daily run, never by a lookup.
func TestRunPrintsALegacyRecordWithoutTheFormLayer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "words.jsonl")
	writeWords(t, path, `{"id":"a1b2c3d4","word":"evil","normalized":"evil","ipa":"/ˈiːvəl/",`+
		`"eli5":"Very, very bad.","chinese":"邪恶的","source":"cli",`+
		`"created_at":"2026-01-02T03:04:05+08:00"}`+"\n")
	t.Setenv("EWH_CACHE", path)
	t.Setenv("DEEPSEEK_API_KEY", "")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"evil"}, strings.NewReader(""), &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d (stderr: %s)", code, stderr.String())
	}
	want := "evil /ˈiːvəl/\nELI5: Very, very bad.\n"
	if stdout.String() != want {
		t.Errorf("got %q, want %q", stdout.String(), want)
	}
}

func TestRunNeedsAKeyOnACacheMiss(t *testing.T) {
	t.Setenv("EWH_CACHE", filepath.Join(t.TempDir(), "words.jsonl"))
	t.Setenv("DEEPSEEK_API_KEY", "")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"serendipity"}, strings.NewReader(""), &stdout, &stderr); code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "DEEPSEEK_API_KEY") {
		t.Errorf("stderr should explain the missing key: %q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("nothing should be printed on stdout, got %q", stdout.String())
	}
}

func TestRunRejectsEmptyInput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(nil, strings.NewReader(""), &stdout, &stderr); code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "Usage: love <word>") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunReportsDamagedLinesButStillServesHits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "words.jsonl")
	writeWords(t, path, "not json\n"+`{"word":"sign","ipa":"/saɪn/","eli5":"A sign.","chinese":"标志"}`+"\n")
	t.Setenv("EWH_CACHE", path)
	t.Setenv("DEEPSEEK_API_KEY", "")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"sign"}, strings.NewReader(""), &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d, want %d", code, exitOK)
	}
	if !strings.Contains(stderr.String(), "skipping invalid JSON") {
		t.Errorf("stderr should warn about the damaged line: %q", stderr.String())
	}
	if !strings.Contains(stdout.String(), "A sign.") {
		t.Errorf("the healthy record should still print: %q", stdout.String())
	}
}

func TestParseArgsJoinsPhraseWords(t *testing.T) {
	var out, errOut bytes.Buffer
	cmd, done, code := parseArgs([]string{"Ice", "Cream"}, &out, &errOut)
	if done || code != exitOK {
		t.Fatalf("done = %v, code = %d", done, code)
	}
	if cmd.word != "ice cream" {
		t.Errorf("word = %q, want %q", cmd.word, "ice cream")
	}
	if cmd.action != nil {
		t.Errorf("a phrase must not resolve to an action")
	}
}

func TestParseArgsHelpAndVersion(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"help"}} {
		var out, errOut bytes.Buffer
		_, done, code := parseArgs(args, &out, &errOut)
		if !done || code != exitOK {
			t.Errorf("%v: done = %v, code = %d", args, done, code)
		}
		if !strings.Contains(out.String(), "Usage:") {
			t.Errorf("%v: help text missing", args)
		}
	}
	for _, args := range [][]string{{"--version"}, {"-V"}, {"version"}} {
		var out, errOut bytes.Buffer
		_, done, code := parseArgs(args, &out, &errOut)
		if !done || code != exitOK {
			t.Errorf("%v: done = %v, code = %d", args, done, code)
		}
		if !strings.Contains(out.String(), version) {
			t.Errorf("%v: version missing from %q", args, out.String())
		}
	}
}

// The help text is the only place the grammar is explained, so it has to name
// every part of it — and it has to keep naming it. --install-schedule lived in
// the registry for a while without appearing in the help at all, because the
// help was a second hand-maintained list. This walks the registries instead, so
// adding an action or an option and forgetting the help fails here.
func TestHelpNamesEveryRegisteredFlag(t *testing.T) {
	var out bytes.Buffer
	printHelp(&out)
	help := out.String()

	for _, want := range []string{"love <word>", "Actions:", "Options:"} {
		if !strings.Contains(help, want) {
			t.Errorf("help does not mention %q", want)
		}
	}

	for _, a := range actions {
		if !strings.Contains(help, a.flags[0]) {
			t.Errorf("action %q is registered but missing from the help", a.name)
		}
		if !strings.Contains(help, a.usage) {
			t.Errorf("action %q is listed without its description", a.name)
		}
	}
	for _, o := range options {
		if !strings.Contains(help, o.flag) {
			t.Errorf("option %q is registered but missing from the help", o.flag)
		}
	}
}

// The environment variables the code actually reads should be the ones the help
// promises, so a reader can configure what exists.
func TestHelpDocumentsTheEnvironmentTheCodeReads(t *testing.T) {
	var out bytes.Buffer
	printHelp(&out)
	help := out.String()

	for _, name := range []string{
		"DEEPSEEK_API_KEY", "DEEPSEEK_API_KEY_FILE", "DEEPSEEK_BASE_URL",
		"EWH_DIR", "EWH_CACHE", "EWH_MODEL", "NO_COLOR",
		"SMTP_HOST", "SMTP_PORT", "SMTP_USER", "SMTP_PASSWORD_FILE",
		"QQ_SMTP_AUTH_CODE", "LOVE_MAIL_TO",
	} {
		if !strings.Contains(help, name) {
			t.Errorf("the code reads %s but the help does not mention it", name)
		}
	}
}

// A review session cannot run without a terminal to type into.
func TestReviewRefusesToRunNonInteractively(t *testing.T) {
	t.Setenv("EWH_CACHE", filepath.Join(t.TempDir(), "words.jsonl"))

	var stdout, stderr bytes.Buffer
	code := run([]string{"--review"}, strings.NewReader(""), &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "终端") {
		t.Errorf("stderr should explain that a terminal is required: %q", stderr.String())
	}
}

// The persistence path is the part that decides whether an evening's work
// survives, and it is deliberately separated from the interactive loop so it can
// be tested without a terminal.
func TestGradingIsPersistedAsAnEventAndACache(t *testing.T) {
	paths := storage.PathsIn(t.TempDir())
	writeWords(t, paths.Words, `{"id":"a1","word":"maintain","normalized":"maintain",`+
		`"ipa":"/meɪnˈteɪn/","eli5":"To keep something working well.","chinese":"维护",`+
		`"source":"cli","created_at":"2026-09-01T00:00:00Z"}`+"\n")

	cfg := config{paths: paths}
	ports, err := buildReviewPorts(cfg, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if ports.Plan.Total() != 1 || len(ports.Plan.New) != 1 {
		t.Fatalf("plan = %+v, want the one unstudied word", ports.Plan)
	}
	if ports.Expansion("a1") != nil {
		t.Error("no expansion was cached, so none should be offered")
	}

	if _, err := ports.Grade("a1", memory.State{}, memory.Again); err != nil {
		t.Fatalf("grading failed: %v", err)
	}

	// The event log is the source of truth and must contain the grading.
	events, _, err := storage.OpenReviews(paths.Reviews).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].WordID != "a1" || events[0].BeforeBox != 0 || events[0].AfterBox != 1 {
		t.Errorf("event = %+v, want a first exposure landing in box 1", events[0])
	}

	// The cache must exist, agree, and not be stale — otherwise every run would
	// rebuild it forever.
	cache, err := storage.LoadMemory(paths.Memory)
	if err != nil {
		t.Fatalf("the state cache was not written: %v", err)
	}
	states, err := cache.States()
	if err != nil {
		t.Fatal(err)
	}
	if states["a1"].Box != 1 {
		t.Errorf("cached box = %d, want 1", states["a1"].Box)
	}
	current, err := storage.Fingerprint(paths)
	if err != nil {
		t.Fatal(err)
	}
	if cache.Stale(current) {
		t.Error("the cache written after grading is immediately stale")
	}

	// A second session must now see the word as already started, not new.
	again, err := buildReviewPorts(cfg, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Plan.New) != 0 {
		t.Errorf("the graded word is still being offered as new: %+v", again.Plan)
	}
}

// ---------------------------------------------------------------------------
// Guards on the options, for the same reason as the guards on the actions: an
// option that could be mistaken for a word would remove that word from the
// dictionary.
// ---------------------------------------------------------------------------

func TestOptionFlagsCannotBeConfusedWithWords(t *testing.T) {
	for _, o := range options {
		if !strings.HasPrefix(o.flag, "-") {
			t.Errorf("option %q does not begin with \"-\", so the word branch would swallow it", o.flag)
		}
	}
}

func TestOptionFlagsAreUniqueAndDoNotCollideWithActions(t *testing.T) {
	claimed := map[string]string{}

	for _, a := range actions {
		for _, flag := range a.flags {
			if owner, taken := claimed[flag]; taken {
				t.Errorf("flag %q is claimed by both %q and action %q", flag, owner, a.name)
			}
			claimed[flag] = "action " + a.name
		}
	}
	for _, o := range options {
		if owner, taken := claimed[o.flag]; taken {
			t.Errorf("flag %q is claimed by both %q and option %q", o.flag, owner, o.flag)
		}
		claimed[o.flag] = "option"
	}
}

func TestAnOptionNeedingAValueReportsWhenItIsMissing(t *testing.T) {
	var out, errOut bytes.Buffer
	_, done, code := parseArgs([]string{"--daily", "--out"}, &out, &errOut)
	if !done || code != exitUsage {
		t.Fatalf("done=%v code=%d", done, code)
	}
	if !strings.Contains(errOut.String(), "需要一个值") {
		t.Errorf("stderr = %q", errOut.String())
	}
}

func TestABooleanOptionRejectsAValue(t *testing.T) {
	var out, errOut bytes.Buffer
	_, done, code := parseArgs([]string{"--daily", "--dry-run=yes"}, &out, &errOut)
	if !done || code != exitUsage {
		t.Fatalf("done=%v code=%d", done, code)
	}
	if !strings.Contains(errOut.String(), "不接受值") {
		t.Errorf("stderr = %q", errOut.String())
	}
}

// An option on its own names no operation, so there is nothing to do.
func TestAnOptionWithoutAnActionIsAUsageError(t *testing.T) {
	var out, errOut bytes.Buffer
	_, done, code := parseArgs([]string{"--dry-run"}, &out, &errOut)
	if !done || code != exitUsage {
		t.Fatalf("done=%v code=%d", done, code)
	}
	if !strings.Contains(errOut.String(), "必须与动作一起使用") {
		t.Errorf("stderr = %q", errOut.String())
	}
}

func TestOptionsAcceptBothSpellings(t *testing.T) {
	for _, args := range [][]string{
		{"--daily", "--out", "/tmp/x.html"},
		{"--daily", "--out=/tmp/x.html"},
	} {
		var out, errOut bytes.Buffer
		cmd, done, code := parseArgs(args, &out, &errOut)
		if done || code != exitOK {
			t.Fatalf("%v: done=%v code=%d stderr=%s", args, done, code, errOut.String())
		}
		if path, ok := cmd.optionValue("--out"); !ok || path != "/tmp/x.html" {
			t.Errorf("%v: --out = %q (present=%v)", args, path, ok)
		}
	}
}

// The whole point of --dry-run: the content and layout can be produced and
// checked without a mail server, and without a model if the content is already
// cached.
func TestDailyDryRunRendersWithoutSending(t *testing.T) {
	paths := storage.PathsIn(t.TempDir())
	writeWords(t, paths.Words,
		`{"id":"a1","word":"maintain","normalized":"maintain","ipa":"/meɪnˈteɪn/",`+
			`"eli5":"To keep something working well.","chinese":"维护","source":"cli",`+
			`"created_at":"2026-09-01T00:00:00Z"}`+"\n")
	t.Setenv("EWH_CACHE", paths.Words)
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("QQ_SMTP_AUTH_CODE", "")
	t.Setenv("SMTP_PASSWORD_FILE", "")

	var stdout, stderr bytes.Buffer
	code := run([]string{"--daily", "--dry-run"}, strings.NewReader(""), &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitOK, stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "maintain") || !strings.Contains(out, "ELI5: To keep something working well.") {
		t.Errorf("the digest is missing its anchor layer:\n%s", out)
	}
	// No expansion was cached and no API key was set, so the digest must
	// degrade rather than fail.
	if strings.Contains(out, "扩展\n") {
		t.Errorf("an expansion was rendered without any content:\n%s", out)
	}
	if !strings.Contains(stderr.String(), "DEEPSEEK_API_KEY") {
		t.Errorf("the degraded run should say why there is no expansion: %q", stderr.String())
	}
}

func TestDailySaysSoWhenThereIsNothingToDo(t *testing.T) {
	paths := storage.PathsIn(t.TempDir())
	writeWords(t, paths.Words, "")
	t.Setenv("EWH_CACHE", paths.Words)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--daily", "--dry-run"}, strings.NewReader(""), &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d (stderr: %s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "没有需要复习的词") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

// Sending without configuration must fail with an explanation, not a stack of
// unhelpful output — and it must name the dry run as the way out.
func TestDailyWithoutMailConfigurationExplainsItself(t *testing.T) {
	paths := storage.PathsIn(t.TempDir())
	writeWords(t, paths.Words,
		`{"id":"a1","word":"maintain","normalized":"maintain","ipa":"/x/","eli5":"e",`+
			`"chinese":"维护","source":"cli","created_at":"2026-09-01T00:00:00Z"}`+"\n")
	t.Setenv("EWH_CACHE", paths.Words)
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("SMTP_USER", "")
	t.Setenv("SMTP_PASSWORD_FILE", "")
	t.Setenv("QQ_SMTP_AUTH_CODE", "")

	var stdout, stderr bytes.Buffer
	code := run([]string{"--daily"}, strings.NewReader(""), &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "SMTP_USER") {
		t.Errorf("stderr should name what is missing: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "--dry-run") {
		t.Errorf("stderr should point at the dry run: %q", stderr.String())
	}
}

// ---------------------------------------------------------------------------
// The form layer: phonics and word parts, and the upgrade that adds them to
// records written before they existed.
// ---------------------------------------------------------------------------

// fakeDeepSeek answers the two kinds of request the tool makes: an anchor
// lookup (which carries the form layer) and expansion content. The counter
// proves how many requests a run actually spent.
func fakeDeepSeek(t *testing.T, anchor string) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		content := anchor
		if strings.Contains(string(body), "dialogue") {
			content = `{"meaning":"to keep something in good condition","examples":["I maintain my bicycle."],"scene":"Someone caring for what they own.","dialogue":[{"speaker":"A","line":"I maintain it every month."}]}`
		}
		payload, err := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
		})
		if err != nil {
			t.Errorf("cannot build a fake reply: %v", err)
		}
		w.Write(payload)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

const maintainAnchor = `{"word":"maintain","ipa":"/meɪnˈteɪn/",` +
	`"phonics":"main·tain → /meɪn/ · /ˈteɪn/",` +
	`"parts":"main- (hand) · tain (hold) ⇒ \"to hold by hand\"",` +
	`"eli5":"To keep something working well.","chinese":"维护"}`

// A record written before the form layer existed gets one request and one
// in-place update — never a second record, and never a rewritten anchor.
func TestBackfillFillsTheFormLayerWithoutTouchingTheAnchor(t *testing.T) {
	paths := storage.PathsIn(t.TempDir())
	writeWords(t, paths.Words, `{"id":"a1b2c3d4","word":"maintain","normalized":"maintain",`+
		`"ipa":"/meɪnˈteɪn/","eli5":"To keep something working well.","chinese":"维护",`+
		`"source":"cli","created_at":"2026-09-01T00:00:00Z"}`+"\n")
	t.Setenv("EWH_CACHE", paths.Words)
	t.Setenv("DEEPSEEK_API_KEY", "test-key")
	srv, calls := fakeDeepSeek(t, maintainAnchor)
	t.Setenv("DEEPSEEK_BASE_URL", srv.URL)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--backfill"}, strings.NewReader(""), &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitOK, stderr.String())
	}
	if *calls != 1 {
		t.Errorf("want exactly one request for one word, got %d", *calls)
	}

	words, warnings, err := storage.OpenWords(paths.Words).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("the rewrite produced warnings: %v", warnings)
	}
	if len(words) != 1 {
		t.Fatalf("got %d records, want 1: an upgrade must update in place, not append", len(words))
	}

	w := words[0]
	if w.Phonics != "main·tain → /meɪn/ · /ˈteɪn/" {
		t.Errorf("phonics = %q", w.Phonics)
	}
	if w.Parts != `main- (hand) · tain (hold) ⇒ "to hold by hand"` {
		t.Errorf("parts = %q", w.Parts)
	}
	// The anchor is a user asset. An upgrade may add what is missing and
	// nothing else — not the id, not the explanation the learner has read a
	// hundred times, not the creation time that decides study order.
	if w.ID != "a1b2c3d4" || w.IPA != "/meɪnˈteɪn/" || w.ELI5 != "To keep something working well." ||
		w.Chinese != "维护" || w.Source != "cli" || w.CreatedAt.IsZero() {
		t.Errorf("the backfill rewrote part of the anchor: %+v", w)
	}
}

func TestBackfillWithoutAKeyExplainsItself(t *testing.T) {
	paths := storage.PathsIn(t.TempDir())
	writeWords(t, paths.Words, `{"id":"a1","word":"maintain","normalized":"maintain","ipa":"/x/",`+
		`"eli5":"e","chinese":"维护","source":"cli","created_at":"2026-09-01T00:00:00Z"}`+"\n")
	t.Setenv("EWH_CACHE", paths.Words)
	t.Setenv("DEEPSEEK_API_KEY", "")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--backfill"}, strings.NewReader(""), &stdout, &stderr); code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "DEEPSEEK_API_KEY") {
		t.Errorf("stderr should explain the missing key: %q", stderr.String())
	}
}

// Nothing to upgrade means nothing to spend: a complete file must not need a
// key at all.
func TestBackfillSaysSoWhenNothingIsMissing(t *testing.T) {
	paths := storage.PathsIn(t.TempDir())
	writeWords(t, paths.Words, `{"id":"a1","word":"maintain","normalized":"maintain","ipa":"/x/",`+
		`"phonics":"main·tain → /meɪn/ · /ˈteɪn/","parts":"main- (hand) · tain (hold)",`+
		`"eli5":"e","chinese":"维护","source":"cli","created_at":"2026-09-01T00:00:00Z"}`+"\n")
	t.Setenv("EWH_CACHE", paths.Words)
	t.Setenv("DEEPSEEK_API_KEY", "")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--backfill"}, strings.NewReader(""), &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d (stderr: %s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "没有需要补齐的") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

// The daily run is where old records get upgraded without a separate command:
// it is already spending requests, and it only touches the words it will show.
func TestDailyFillsTheFormLayerOfTodaysWords(t *testing.T) {
	paths := storage.PathsIn(t.TempDir())
	writeWords(t, paths.Words, `{"id":"a1b2c3d4","word":"maintain","normalized":"maintain",`+
		`"ipa":"/meɪnˈteɪn/","eli5":"To keep something working well.","chinese":"维护",`+
		`"source":"cli","created_at":"2026-09-01T00:00:00Z"}`+"\n")
	t.Setenv("EWH_CACHE", paths.Words)
	t.Setenv("DEEPSEEK_API_KEY", "test-key")
	srv, _ := fakeDeepSeek(t, maintainAnchor)
	t.Setenv("DEEPSEEK_BASE_URL", srv.URL)
	t.Setenv("QQ_SMTP_AUTH_CODE", "")
	t.Setenv("SMTP_PASSWORD_FILE", "")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--daily", "--dry-run"}, strings.NewReader(""), &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitOK, stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "Phonics: main·tain → /meɪn/ · /ˈteɪn/") {
		t.Errorf("the digest should carry the form layer it just filled:\n%s", out)
	}
	// The email is read on a phone, where the gloss is help rather than a
	// spoiler, so it keeps it even though the terminal does not.
	if !strings.Contains(out, "中文：维护") {
		t.Errorf("the digest should keep the gloss:\n%s", out)
	}

	// The fill is persisted: the next lookup is a hit with the form layer.
	words, _, err := storage.OpenWords(paths.Words).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(words) != 1 || !strings.HasPrefix(words[0].Phonics, "main·tain") {
		t.Errorf("the daily run did not persist the form layer: %+v", words)
	}
}

// The tail is the part no data can verify, so --backfill names it instead of
// leaving a plausible guess indistinguishable from a recorded fact.
func TestBackfillNamesTheWordsOnlyAModelExplained(t *testing.T) {
	paths := storage.PathsIn(t.TempDir())
	writeWords(t, paths.Words, `{"id":"a1","word":"literrally","normalized":"literrally","ipa":"/x/",`+
		`"phonics":"lit·er·al·ly → /ˈlɪt/","parts":"litter (letter) · -al · -ly","parts_source":"model",`+
		`"eli5":"e","chinese":"字面上","source":"cli","created_at":"2026-09-01T00:00:00Z"}`+"\n")
	t.Setenv("EWH_CACHE", paths.Words)
	t.Setenv("DEEPSEEK_API_KEY", "")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--backfill"}, strings.NewReader(""), &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d (stderr: %s)", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "literrally") {
		t.Errorf("the provisional word should be named:\n%s", out)
	}
	if !strings.Contains(out, "overrides.jsonl") {
		t.Errorf("the way to correct it should be named too:\n%s", out)
	}
}

// A word the data explains is not provisional, so it must not be listed.
func TestBackfillDoesNotCallDataBackedWordsProvisional(t *testing.T) {
	paths := storage.PathsIn(t.TempDir())
	writeWords(t, paths.Words, `{"id":"a1","word":"unhappy","normalized":"unhappy","ipa":"/x/",`+
		`"phonics":"un·hap·py → /ʌn/","parts":"un- (not) · happy","parts_source":"morphology",`+
		`"eli5":"e","chinese":"不快乐","source":"cli","created_at":"2026-09-01T00:00:00Z"}`+"\n")
	t.Setenv("EWH_CACHE", paths.Words)
	t.Setenv("DEEPSEEK_API_KEY", "")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--backfill"}, strings.NewReader(""), &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d (stderr: %s)", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "overrides.jsonl") {
		t.Errorf("a data-backed word must not be listed as provisional:\n%s", stdout.String())
	}
}

// ---------------------------------------------------------------------------
// Scheduling.
// ---------------------------------------------------------------------------

func TestParseClock(t *testing.T) {
	for input, want := range map[string][2]int{
		"7:30":  {7, 30},
		"07:30": {7, 30},
		"0:00":  {0, 0},
		"23:59": {23, 59},
		" 8:5 ": {8, 5},
	} {
		hour, minute, err := parseClock(input)
		if err != nil {
			t.Errorf("parseClock(%q) failed: %v", input, err)
			continue
		}
		if hour != want[0] || minute != want[1] {
			t.Errorf("parseClock(%q) = %d:%d, want %d:%d", input, hour, minute, want[0], want[1])
		}
	}

	for _, bad := range []string{"", "7", "7:", ":30", "24:00", "7:60", "-1:00", "seven:30", "7:30:00"} {
		if _, _, err := parseClock(bad); err == nil {
			t.Errorf("parseClock(%q) should have failed", bad)
		}
	}
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	if got := expandHome("~/x"); got != filepath.Join(home, "x") {
		t.Errorf("expandHome(~/x) = %q", got)
	}
	if got := expandHome("~"); got != home {
		t.Errorf("expandHome(~) = %q", got)
	}
	if got := expandHome("/absolute"); got != "/absolute" {
		t.Errorf("an absolute path must be untouched, got %q", got)
	}
	if got := expandHome("relative"); got != "relative" {
		t.Errorf("a relative path must be untouched, got %q", got)
	}
}

// A property list is readable by anything running as the user, and
// `launchctl print` reproduces it in full. The installer therefore refuses to
// record a secret rather than quietly writing one somewhere it will later be
// pasted into a bug report.
func TestScheduleEnvRefusesToEmbedSecrets(t *testing.T) {
	t.Setenv("SMTP_USER", "me@example.com")
	t.Setenv("SMTP_PASSWORD_FILE", "")
	t.Setenv("SMTP_PASSWORD", "")
	t.Setenv("QQ_SMTP_AUTH_CODE", "live-auth-code")
	t.Setenv("DEEPSEEK_API_KEY_FILE", "")
	t.Setenv("DEEPSEEK_API_KEY", "")

	_, err := scheduleEnv()
	if err == nil {
		t.Fatal("expected the installer to refuse an inline password")
	}
	if !strings.Contains(err.Error(), "SMTP_PASSWORD_FILE") {
		t.Errorf("the error should name the file variable: %q", err)
	}
	if strings.Contains(err.Error(), "live-auth-code") {
		t.Error("the error must not echo the secret back")
	}
}

func TestScheduleEnvRefusesToEmbedTheAPIToken(t *testing.T) {
	t.Setenv("SMTP_USER", "me@example.com")
	t.Setenv("SMTP_PASSWORD_FILE", "/tmp/pw")
	t.Setenv("SMTP_PASSWORD", "")
	t.Setenv("QQ_SMTP_AUTH_CODE", "")
	t.Setenv("DEEPSEEK_API_KEY_FILE", "")
	t.Setenv("DEEPSEEK_API_KEY", "sk-secret")

	_, err := scheduleEnv()
	if err == nil {
		t.Fatal("expected the installer to refuse an inline API key")
	}
	if strings.Contains(err.Error(), "sk-secret") {
		t.Error("the error must not echo the secret back")
	}
}

func TestScheduleEnvRecordsPathsAndExpandsHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	t.Setenv("SMTP_USER", "me@example.com")
	t.Setenv("SMTP_PASSWORD_FILE", "~/.ewh/smtp-password")
	t.Setenv("DEEPSEEK_API_KEY_FILE", "~/.ewh/deepseek-key")
	t.Setenv("LOVE_MAIL_TO", "me@example.com")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("QQ_SMTP_AUTH_CODE", "")

	env, err := scheduleEnv()
	if err != nil {
		t.Fatal(err)
	}
	if got := env["SMTP_PASSWORD_FILE"]; got != filepath.Join(home, ".ewh", "smtp-password") {
		t.Errorf("SMTP_PASSWORD_FILE = %q, want an expanded absolute path", got)
	}
	if got := env["DEEPSEEK_API_KEY_FILE"]; got != filepath.Join(home, ".ewh", "deepseek-key") {
		t.Errorf("DEEPSEEK_API_KEY_FILE = %q", got)
	}
	if env["SMTP_USER"] != "me@example.com" || env["LOVE_MAIL_TO"] != "me@example.com" {
		t.Errorf("the non-secret settings should pass through: %v", env)
	}
	if _, ok := env["PATH"]; !ok {
		t.Error("launchd consults no PATH of its own, so one must be recorded")
	}
}

func TestScheduleOptionsParse(t *testing.T) {
	var out, errOut bytes.Buffer
	cmd, done, code := parseArgs([]string{"--install-schedule", "--at", "06:45", "--now"}, &out, &errOut)
	if done || code != exitOK {
		t.Fatalf("done=%v code=%d stderr=%s", done, code, errOut.String())
	}
	if cmd.action == nil || cmd.action.name != "install-schedule" {
		t.Fatalf("action = %+v", cmd.action)
	}
	if v, ok := cmd.optionValue("--at"); !ok || v != "06:45" {
		t.Errorf("--at = %q (present=%v)", v, ok)
	}
	if !cmd.has("--now") {
		t.Error("--now should be recorded")
	}
}

func TestParseTimes(t *testing.T) {
	got, err := parseTimes("07:30,12:30,20:30")
	if err != nil {
		t.Fatal(err)
	}
	want := []schedule.TimeOfDay{{Hour: 7, Minute: 30}, {Hour: 12, Minute: 30}, {Hour: 20, Minute: 30}}
	if len(got) != len(want) {
		t.Fatalf("got %d times, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("time %d = %v, want %v", i, got[i], want[i])
		}
	}

	// One time is still the common case and must keep working.
	single, err := parseTimes("7:30")
	if err != nil || len(single) != 1 || single[0] != (schedule.TimeOfDay{Hour: 7, Minute: 30}) {
		t.Errorf("parseTimes(7:30) = %v, %v", single, err)
	}

	// Forgiving about spacing, strict about the times themselves.
	spaced, err := parseTimes(" 07:30 , 20:30 ")
	if err != nil || len(spaced) != 2 {
		t.Errorf("parseTimes with spaces = %v, %v", spaced, err)
	}

	for _, bad := range []string{"", "  ", "7:30,", "7:30,25:00", "morning", "7:30,,nope"} {
		if _, err := parseTimes(bad); err == nil {
			t.Errorf("parseTimes(%q) should have failed", bad)
		}
	}
}
