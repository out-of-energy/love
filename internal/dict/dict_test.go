package dict

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/out-of-energy/love/internal/llm"
)

// validEntry is what the model is expected to return, escaped for embedding
// inside a chat-completions envelope.
const validEntry = `{\"word\":\"book\",\"ipa\":\"/bʊk/\",\"phonics\":\"book → /bʊk/\",\"parts\":\"no clear prefix or suffix (whole word from Old English)\",\"eli5\":\"Thing with pages.\",\"chinese\":\"书\"}`

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewClient("test-key", srv.URL, llm.DefaultModel, 5*time.Second)
}

func TestGenerateReturnsTheAnchorLayer(t *testing.T) {
	var gotBody string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		io.WriteString(w, `{"choices":[{"message":{"content":"`+validEntry+`"}}]}`)
	})

	entry, err := c.Generate(context.Background(), "book", Options{})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if entry.Word != "book" || entry.IPA != "/bʊk/" || entry.ELI5 != "Thing with pages." || entry.Chinese != "书" {
		t.Errorf("unexpected entry: %+v", entry)
	}
	if entry.Phonics != "book → /bʊk/" || entry.Parts == "" {
		t.Errorf("the form layer should be part of the anchor: %+v", entry)
	}
	// The anchor must be asked for in child-simple language; that register is
	// the whole reason the anchor is a separate layer from the expansion.
	if !strings.Contains(gotBody, "three-year-old") {
		t.Error("the prompt should ask for a child-simple explanation")
	}
	// And it must ask for the two form lines in the agreed notation, because a
	// later backfill has no other way to know what shape to expect.
	for _, want := range []string{"phonics", "parts", "·", "⇒"} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("the prompt should ask for %q", want)
		}
	}
}

func TestGeneratePrefersTheCallersSpelling(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		// The model answers with a different case; the caller's form must win.
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"word\":\"BOOK\",\"ipa\":\"/bʊk/\",\"phonics\":\"book → /bʊk/\",\"parts\":\"no clear prefix or suffix\",\"eli5\":\"Thing with pages.\",\"chinese\":\"书\"}"}}]}`)
	})
	// The caller supplies the canonical key, and this package must not silently
	// rewrite it: normalization belongs to whoever owns the word file.
	entry, err := c.Generate(context.Background(), "ice cream", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if entry.Word != "ice cream" {
		t.Errorf("word = %q, want the caller's key echoed back", entry.Word)
	}
}

func TestGenerateRejectsIncompleteEntries(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"word\":\"book\",\"ipa\":\"/bʊk/\",\"phonics\":\"book → /bʊk/\",\"parts\":\"no clear prefix or suffix\",\"eli5\":\"Thing with pages.\",\"chinese\":\"\"}"}}]}`)
	})

	_, err := c.Generate(context.Background(), "book", Options{})
	if !errors.Is(err, llm.ErrBadResponse) {
		t.Fatalf("want ErrBadResponse, got %v", err)
	}
}

func TestGenerateErrorsAreClassifiableByCallers(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"Authentication Fails","code":"invalid_api_key"}}`)
	})

	_, err := c.Generate(context.Background(), "book", Options{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("callers must be able to recognise an API error, got %v", err)
	}
	if apiErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d", apiErr.StatusCode)
	}
}

func TestEntryValidate(t *testing.T) {
	full := Entry{
		Word:    "book",
		IPA:     "/bʊk/",
		Phonics: "book → /bʊk/",
		Parts:   "no clear prefix or suffix",
		ELI5:    "Thing with pages.",
		Chinese: "书",
	}
	if err := full.Validate(); err != nil {
		t.Errorf("a complete entry should validate: %v", err)
	}
	for name, entry := range map[string]Entry{
		"no word":    {IPA: "/b/", Phonics: "b", Parts: "b", ELI5: "x", Chinese: "书"},
		"no ipa":     {Word: "book", Phonics: "b", Parts: "b", ELI5: "x", Chinese: "书"},
		"no phonics": {Word: "book", IPA: "/b/", Parts: "b", ELI5: "x", Chinese: "书"},
		"no parts":   {Word: "book", IPA: "/b/", Phonics: "b", ELI5: "x", Chinese: "书"},
		"no eli5":    {Word: "book", IPA: "/b/", Phonics: "b", Parts: "b", Chinese: "书"},
		"no chinese": {Word: "book", IPA: "/b/", Phonics: "b", Parts: "b", ELI5: "x"},
	} {
		if err := entry.Validate(); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
}

// ---------------------------------------------------------------------------
// The morphology constraint.
//
// The failure this guards against is specific: a model asked to split a word
// invents a plausible split, and a plausible split teaches the wrong thing.
// When the data layer knows the real analyses, the model must choose one of
// them — and if it does not, the answer is rewritten rather than trusted.
// ---------------------------------------------------------------------------

func unHappyNess() Options {
	return Options{
		Known: true,
		Segments: [][]Segment{{
			{Form: "un-", Kind: "prefix", Gloss: "not"},
			{Form: "happy", Kind: "root"},
			{Form: "-ness", Kind: "suffix", Gloss: "state of"},
		}},
	}
}

func TestWithoutDataThePromptIsUnchanged(t *testing.T) {
	if got := systemPromptFor(Options{}); got != systemPrompt {
		t.Error("an unknown word should keep the original prompt")
	}
}

func TestWithDataThePromptNamesTheRealAnalyses(t *testing.T) {
	prompt := systemPromptFor(unHappyNess())
	for _, want := range []string{"Morphology data", "un- · happy · -ness", "do not invent"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the constraint should mention %q:\n%s", want, prompt)
		}
	}
	// The base contract is still there: the constraint is an addition, not a
	// replacement for the six fields.
	if !strings.Contains(prompt, `"chinese"`) {
		t.Error("the field contract must survive the constraint")
	}
}

// A recorded analysis that has no derivation is not a licence to split the
// word: it is the opposite.
func TestAWholeWordCandidateForbidsInventingAffixes(t *testing.T) {
	opts := Options{Known: true, Segments: [][]Segment{{{Form: "symlink", Kind: "root"}}}}
	prompt := systemPromptFor(opts)
	if !strings.Contains(prompt, "Do not invent a prefix, root or suffix") {
		t.Errorf("the whole-word case should forbid invention:\n%s", prompt)
	}
	if !strings.Contains(prompt, "compound") {
		t.Errorf("a compound of two ordinary words should stay possible:\n%s", prompt)
	}
}

func TestApplyKeepsTheModelWordingWhenItAgreed(t *testing.T) {
	got, source := unHappyNess().Apply(
		`un- (not) · happy (feeling good) · -ness (state of) ⇒ "the state of feeling good"`)

	want := `un- (not) · happy (feeling good) · -ness (state of) ⇒ "the state of feeling good"`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if source != "morphology" {
		t.Errorf("source = %q, want morphology", source)
	}
}

// The model's glosses belong to the segmentation it wrote. When it drifted, the
// recorded analysis wins and the drifted wording is dropped, because a gloss
// that describes a different split is worse than no gloss at all.
func TestApplyForcesTheRecordedAnalysisWhenTheModelDrifts(t *testing.T) {
	got, source := unHappyNess().Apply(`un- (against) · happiness (joy) ⇒ "not joy"`)

	if strings.Contains(got, "against") || strings.Contains(got, "joy") {
		t.Errorf("the drifted wording should not survive: %q", got)
	}
	if !strings.HasPrefix(got, "un- (not) · happy") {
		t.Errorf("the recorded analysis should be used: %q", got)
	}
	if source != "morphology-forced" {
		t.Errorf("source = %q, want morphology-forced", source)
	}
}

// With no data there is nothing to enforce, and the model's line is what the
// learner gets — which is exactly how the tool behaved before the data layer.
func TestApplyWithoutDataChangesNothing(t *testing.T) {
	line := `sym- (together) · link (a loop) ⇒ "linked together"`
	got, source := Options{}.Apply(line)
	if got != line || source != "model" {
		t.Errorf("Apply = (%q, %q)", got, source)
	}
}

// The literal sense is the model's job even when the split is the data's, so it
// has to survive a forced rebuild when the model did use a recorded analysis.
func TestApplyKeepsTheLiteralSenseOnAMatch(t *testing.T) {
	got, _ := unHappyNess().Apply(`un · happy · ness ⇒ "not happy"`)
	if !strings.HasSuffix(got, `⇒ "not happy"`) {
		t.Errorf("the literal sense was lost: %q", got)
	}
}

// The data can be wrong in a way only a model will notice — "sym" in "symlink"
// is a clipping of "symbolic", not the Greek prefix — so the model must be able
// to refuse every recorded analysis rather than pick the least bad one.
func TestTheModelMayDeclineEveryAnalysis(t *testing.T) {
	got, source := unHappyNess().Apply("no clear prefix or suffix")
	if got != "no clear prefix or suffix" {
		t.Errorf("the refusal must survive verbatim, got %q", got)
	}
	if source != "morphology-declined" {
		t.Errorf("source = %q, want morphology-declined", source)
	}

	prompt := systemPromptFor(unHappyNess())
	if !strings.Contains(prompt, "no clear prefix or suffix") {
		t.Error("the prompt should offer the way out it is expected to accept")
	}
}
