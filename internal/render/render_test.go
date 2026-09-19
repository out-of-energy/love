package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The bug this guards against: /dev/null is a character device, so a
// stat-based check reports it as a terminal. A review session started with
// stdin from /dev/null would then begin, print its prompt, read nothing, and
// exit as though the learner had quit.
func TestDevNullIsNotATerminal(t *testing.T) {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Skipf("cannot open %s: %v", os.DevNull, err)
	}
	defer devNull.Close()

	if IsTerminal(devNull) {
		t.Errorf("%s must not be reported as an interactive terminal", os.DevNull)
	}
}

func TestARegularFileIsNotATerminal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if IsTerminal(f) {
		t.Error("a regular file must not be reported as an interactive terminal")
	}
}

func TestNilIsNotATerminal(t *testing.T) {
	if IsTerminal(nil) {
		t.Error("a nil file must not be reported as a terminal")
	}
}

func TestRecordKeepsTheAgreedShape(t *testing.T) {
	var b strings.Builder
	Record(&b, Anchor{
		Word:    "expose",
		IPA:     "/ɪkˈspəʊz/",
		Phonics: "ex·pose → /ɪk/ · /ˈspəʊz/",
		Parts:   `ex- (out) · pos (put) · -e ⇒ "put out"`,
		ELI5:    "To show something that was hidden.",
	}, false)

	want := "expose /ɪkˈspəʊz/\n" +
		"Phonics: ex·pose → /ɪk/ · /ˈspəʊz/\n" +
		"Parts: ex- (out) · pos (put) · -e ⇒ \"put out\"\n" +
		"ELI5: To show something that was hidden.\n"
	if b.String() != want {
		t.Errorf("got %q, want %q", b.String(), want)
	}
}

// A record that predates the form layer prints two lines rather than two empty
// labels. Until `love --backfill` runs, that is the honest shape.
func TestRecordOmitsAFormLayerItDoesNotHave(t *testing.T) {
	var b strings.Builder
	Record(&b, Anchor{Word: "evil", IPA: "/ˈiːvəl/", ELI5: "Very, very bad."}, false)

	want := "evil /ˈiːvəl/\nELI5: Very, very bad.\n"
	if b.String() != want {
		t.Errorf("got %q, want %q", b.String(), want)
	}
}

// The terminal is where a word is recalled, so the gloss stays out of it. It is
// still stored, and the daily email still prints it.
func TestRecordNeverPrintsTheChineseGloss(t *testing.T) {
	var b strings.Builder
	Record(&b, Anchor{Word: "sign", IPA: "/saɪn/", ELI5: "A sign."}, false)

	if strings.Contains(b.String(), "中文") {
		t.Errorf("the terminal block must not carry the gloss: %q", b.String())
	}
}

func TestRecordWithColorAddsOnlyBoldHead(t *testing.T) {
	var b strings.Builder
	Record(&b, Anchor{
		Word:    "sign",
		IPA:     "/saɪn/",
		Phonics: "sign → /saɪn/",
		Parts:   "no clear prefix or suffix",
		ELI5:    "A sign.",
	}, true)

	out := b.String()
	if !strings.HasPrefix(out, "\x1b[1msign /saɪn/\x1b[0m\nPhonics: ") {
		t.Errorf("head should be bold and reset: %q", out)
	}
	if strings.Contains(strings.TrimPrefix(out, "\x1b[1msign /saɪn/\x1b[0m\n"), "\x1b[") {
		t.Errorf("body should be plain and uncolored: %q", out)
	}
}

// Phrases must reach the output intact, since a word and a phrase go through
// exactly the same path.
func TestRecordRendersAPhrase(t *testing.T) {
	var b strings.Builder
	Record(&b, Anchor{
		Word:    "ice cream",
		IPA:     "/ˌaɪs ˈkriːm/",
		Phonics: "ice·cream → /ˌaɪs/ · /ˈkriːm/",
		Parts:   "ice (frozen water) · cream (rich milk) ⇒ \"frozen sweet food\"",
		ELI5:    "Cold sweet food.",
	}, false)

	if !strings.Contains(b.String(), "ice cream /ˌaɪs ˈkriːm/") {
		t.Errorf("phrase was mangled: %q", b.String())
	}
}
