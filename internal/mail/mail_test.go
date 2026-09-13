package mail

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/out-of-energy/love/internal/ai"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// fixedDigest is the input every golden comparison is built from. It includes
// one word with expansion content and one without, so the golden output also
// pins the degraded layout that a failed generation produces.
func fixedDigest() Digest {
	return Digest{
		Date: time.Date(2026, time.September, 14, 0, 0, 0, 0, time.UTC),
		Words: []Word{
			{
				Word:    "maintain",
				IPA:     "/meɪnˈteɪn/",
				ELI5:    "To keep something working well.",
				Chinese: "维护，保持",
				Content: &ai.Content{
					Meaning:  "to keep something in good condition, or to continue it over time",
					Examples: []string{"I maintain my bicycle every month.", "She maintains a small garden behind the house."},
					Scene:    "Someone taking care of the things they own, so they last longer.",
					Dialogue: []ai.Line{
						{Speaker: "A", Line: "Your bike still looks new."},
						{Speaker: "B", Line: "I maintain it every month."},
						{Speaker: "A", Line: "That explains it. Mine is falling apart."},
					},
				},
			},
			{
				Word:    "fragile",
				IPA:     "/ˈfrædʒaɪl/",
				ELI5:    "Easily broken. Be careful with it.",
				Chinese: "易碎的",
				// No expansion: generation failed, and the anchor must still ship.
			},
		},
	}
}

func golden(t *testing.T, name string, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)

	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes)", path, len(got))
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden file; run: go test ./internal/mail -update (%v)", err)
	}
	if got != string(want) {
		t.Errorf("output differs from %s\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

func TestRenderHTMLMatchesTheGoldenFile(t *testing.T) {
	html, err := RenderHTML(fixedDigest())
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "golden.html", html)
}

func TestRenderTextMatchesTheGoldenFile(t *testing.T) {
	text, err := RenderText(fixedDigest())
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "golden.txt", text)
}

// The point of using a template at all: the same content must always produce
// exactly the same bytes, on this machine and any other.
func TestRenderIsByteForByteReproducible(t *testing.T) {
	first, err := RenderHTML(fixedDigest())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		again, err := RenderHTML(fixedDigest())
		if err != nil {
			t.Fatal(err)
		}
		if again != first {
			t.Fatalf("run %d produced different HTML", i+1)
		}
	}
}

// Model output ends up inside an email, so it must never be able to add markup.
func TestRenderEscapesModelOutput(t *testing.T) {
	d := fixedDigest()
	d.Words = []Word{{
		Word:    "acme",
		IPA:     "/x/",
		ELI5:    `<script>alert("xss")</script>`,
		Chinese: `a & b < c > d`,
		Content: &ai.Content{
			Meaning:  `She said "hello" & left`,
			Examples: []string{"<b>not bold</b>"},
			Dialogue: []ai.Line{{Speaker: "A", Line: "<img src=x onerror=alert(1)>"}},
		},
	}}

	html, err := RenderHTML(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"<script>", "<img src=x", "<b>not bold</b>"} {
		if strings.Contains(html, forbidden) {
			t.Errorf("unescaped markup reached the HTML: %q", forbidden)
		}
	}
	for _, want := range []string{"&lt;script&gt;", "&amp;", "&lt;img"} {
		if !strings.Contains(html, want) {
			t.Errorf("expected %q in the escaped output", want)
		}
	}
}

// A failed generation must cost the expansion, never the day's review.
func TestRenderWithoutExpansionStillShowsTheAnchor(t *testing.T) {
	d := Digest{Date: time.Date(2026, time.September, 14, 0, 0, 0, 0, time.UTC), Words: []Word{{
		Word: "fragile", IPA: "/ˈfrædʒaɪl/", ELI5: "Easily broken.", Chinese: "易碎的",
	}}}

	html, err := RenderHTML(d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, "fragile") || !strings.Contains(html, "Easily broken.") {
		t.Error("the anchor layer must survive a missing expansion")
	}
	if strings.Contains(html, "扩展") {
		t.Error("no expansion section should be rendered when there is no content")
	}

	text, err := RenderText(d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "fragile") || strings.Contains(text, "扩展") {
		t.Errorf("text version mishandled the missing expansion:\n%s", text)
	}
}

func TestRenderHandlesAnEmptyDigest(t *testing.T) {
	d := Digest{Date: time.Date(2026, time.September, 14, 0, 0, 0, 0, time.UTC)}

	html, err := RenderHTML(d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, "共 0 个词") {
		t.Error("an empty digest should still render its header")
	}
	if _, err := RenderText(d); err != nil {
		t.Fatal(err)
	}
}

func TestRenderIncludesBothLayers(t *testing.T) {
	html, err := RenderHTML(fixedDigest())
	if err != nil {
		t.Fatal(err)
	}
	// Part one: the anchor, read in one glance.
	for _, want := range []string{"maintain", "/meɪnˈteɪn/", "To keep something working well.", "维护，保持"} {
		if !strings.Contains(html, want) {
			t.Errorf("anchor field %q is missing", want)
		}
	}
	// Part two: the expansion, read afterwards.
	for _, want := range []string{"to keep something in good condition", "I maintain my bicycle every month.", "Your bike still looks new.", "扩展"} {
		if !strings.Contains(html, want) {
			t.Errorf("expansion field %q is missing", want)
		}
	}
}

func TestSubjectNamesTheDayAndTheCount(t *testing.T) {
	got := Subject(fixedDigest())
	want := "英语复习 · 2 个词 · 9月14日"
	if got != want {
		t.Errorf("Subject = %q, want %q", got, want)
	}
}

// A message with no text part is more likely to be filtered as bulk mail.
func TestTextVersionCarriesBothLayers(t *testing.T) {
	text, err := RenderText(fixedDigest())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"maintain", "/meɪnˈteɪn/", "ELI5: To keep something working well.", "中文：维护，保持",
		"扩展", "to keep something in good condition",
		"I maintain my bicycle every month.",
		"A  Your bike still looks new.",
		"fragile",
		"love review",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text version is missing %q:\n%s", want, text)
		}
	}
}
