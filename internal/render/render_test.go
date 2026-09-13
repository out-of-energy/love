package render

import (
	"strings"
	"testing"
)

func TestRecordKeepsTheAgreedShape(t *testing.T) {
	var b strings.Builder
	Record(&b, "evil", "/ˈiːvəl/", "Very, very bad.", "邪恶的", false)

	want := "evil /ˈiːvəl/\n\nELI5: Very, very bad.\n\n中文：邪恶的\n"
	if b.String() != want {
		t.Errorf("got %q, want %q", b.String(), want)
	}
}

func TestRecordWithColorAddsOnlyBoldHead(t *testing.T) {
	var b strings.Builder
	Record(&b, "sign", "/saɪn/", "A sign.", "标志", true)

	out := b.String()
	if !strings.HasPrefix(out, "\x1b[1msign /saɪn/\x1b[0m\n\n") {
		t.Errorf("head should be bold and reset: %q", out)
	}
	if !strings.Contains(out, "\n\nELI5: A sign.\n\n中文：标志\n") {
		t.Errorf("body should be plain and uncolored: %q", out)
	}
}

// Phrases must reach the output intact, since a word and a phrase go through
// exactly the same path.
func TestRecordRendersAPhrase(t *testing.T) {
	var b strings.Builder
	Record(&b, "ice cream", "/ˌaɪs ˈkriːm/", "Cold sweet food.", "冰淇淋", false)

	if !strings.Contains(b.String(), "ice cream /ˌaɪs ˈkriːm/") {
		t.Errorf("phrase was mangled: %q", b.String())
	}
}
