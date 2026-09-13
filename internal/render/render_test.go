package render

import (
	"strings"
	"testing"

	"github.com/kk/love/internal/cache"
)

func TestRecordKeepsTheAgreedShape(t *testing.T) {
	var b strings.Builder
	Record(&b, cache.Record{
		Word:    "evil",
		IPA:     "/ˈiːvəl/",
		ELI5:    "Very, very bad.",
		Chinese: "邪恶的",
	}, false)

	want := "evil /ˈiːvəl/\n\nELI5: Very, very bad.\n\n中文：邪恶的\n"
	if b.String() != want {
		t.Errorf("got %q, want %q", b.String(), want)
	}
}

func TestRecordWithColorAddsOnlyBoldHead(t *testing.T) {
	var b strings.Builder
	Record(&b, cache.Record{Word: "sign", IPA: "/saɪn/", ELI5: "A sign.", Chinese: "标志"}, true)

	out := b.String()
	if !strings.HasPrefix(out, "\x1b[1msign /saɪn/\x1b[0m\n\n") {
		t.Errorf("head should be bold and reset: %q", out)
	}
	if !strings.Contains(out, "\n\nELI5: A sign.\n\n中文：标志\n") {
		t.Errorf("body should be plain and uncolored: %q", out)
	}
}
