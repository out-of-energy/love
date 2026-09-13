package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func sampleGenerated(wordID, word string) Generated {
	return Generated{
		WordID:   wordID,
		Word:     word,
		Provider: "deepseek",
		Content: Content{
			Meaning:  "to keep something in good condition",
			Examples: []string{"I maintain my bicycle every month."},
			Scene:    "Someone taking care of what they own.",
			Dialogue: []DialogueLine{
				{Speaker: "A", Line: "Your bike still looks new."},
				{Speaker: "B", Line: "I maintain it every month."},
			},
		},
	}
}

func TestUsesWord(t *testing.T) {
	cases := []struct {
		text, word string
		want       bool
	}{
		{"I maintain it every month.", "maintain", true},
		{"She MAINTAINS a garden.", "maintain", true},
		{"I maintain it.", "MAINTAIN", true},
		{"Nothing relevant here.", "maintain", false},
		{"anything", "", false},
	}
	for _, tc := range cases {
		if got := UsesWord(tc.text, tc.word); got != tc.want {
			t.Errorf("UsesWord(%q, %q) = %v, want %v", tc.text, tc.word, got, tc.want)
		}
	}
}

func TestGeneratedValidate(t *testing.T) {
	if err := sampleGenerated("w1", "maintain").Validate(); err != nil {
		t.Errorf("a complete record should validate: %v", err)
	}

	cases := map[string]func(g *Generated){
		"no word id":      func(g *Generated) { g.WordID = "" },
		"no word":         func(g *Generated) { g.Word = "" },
		"no meaning":      func(g *Generated) { g.Content.Meaning = "" },
		"no examples":     func(g *Generated) { g.Content.Examples = nil },
		"no dialogue":     func(g *Generated) { g.Content.Dialogue = nil },
		"empty line":      func(g *Generated) { g.Content.Dialogue[1].Line = "  " },
		"dialogue misses": func(g *Generated) { g.Content.Dialogue = []DialogueLine{{Speaker: "A", Line: "Hello there."}} },
	}
	for name, mutate := range cases {
		g := sampleGenerated("w1", "maintain")
		mutate(&g)
		if err := g.Validate(); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
}

// The dialogue check is the one worth its own test: a model can produce a
// perfectly good-looking definition, example and scene while the conversation
// never uses the target word, which defeats the point of the expansion layer.
func TestGeneratedRejectsADialogueThatNeverUsesTheWord(t *testing.T) {
	g := sampleGenerated("w1", "maintain")
	g.Content.Examples = []string{"I maintain my bicycle."}
	g.Content.Dialogue = []DialogueLine{
		{Speaker: "A", Line: "Do you like riding?"},
		{Speaker: "B", Line: "Yes, very much."},
	}
	err := g.Validate()
	if err == nil {
		t.Fatal("expected the dialogue to be rejected")
	}
	if !UsesWord(err.Error(), "maintain") {
		t.Errorf("the error should name the word, got %q", err.Error())
	}
}

func TestGeneratedSaveIsIdempotentAndRefusesInvalidContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "generated.jsonl")
	store := OpenGenerated(path)

	first, created, err := store.Save(sampleGenerated("w1", "maintain"), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if !created || first.CreatedAt.IsZero() {
		t.Fatalf("first save should create, got created=%v %+v", created, first)
	}

	// A second save must keep the original: regenerating daily would destroy
	// the stable context that review depends on.
	second := sampleGenerated("w1", "maintain")
	second.Content.Meaning = "completely different"
	saved, created, err := store.Save(second, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("a second save for the same word must not create a record")
	}
	if saved.Content.Meaning != "to keep something in good condition" {
		t.Errorf("the original content should win, got %q", saved.Content.Meaning)
	}

	items, _, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("file has %d records, want 1", len(items))
	}

	// Invalid content must never reach the file.
	bad := sampleGenerated("w2", "fragile")
	bad.Content.Dialogue = nil
	if _, _, err := store.Save(bad, testNow); err == nil {
		t.Error("invalid content must be refused")
	}
	items, _, err = store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Errorf("refused content was written anyway: %d records", len(items))
	}
}

func TestGeneratedLoadToleratesDamageAndDuplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "generated.jsonl")
	a, _ := json.Marshal(sampleGenerated("w1", "maintain"))
	b, _ := json.Marshal(sampleGenerated("w2", "fragile"))
	writeFile(t, path, string(a)+"\n"+
		"not json\n"+
		`{"provider":"deepseek"}`+"\n"+
		string(a)+"\n"+
		string(b)+"\n")

	items, warnings, err := OpenGenerated(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d records, want 2", len(items))
	}
	// Three lines are rejected: unparseable JSON, a record with no word_id, and
	// the repeated w1.
	if len(warnings) != 3 {
		t.Errorf("got %d warnings, want 3: %v", len(warnings), warnings)
	}
	if _, ok := FindGenerated(items, "w1"); !ok {
		t.Error("w1 should be findable")
	}
	if _, ok := FindGenerated(items, "missing"); ok {
		t.Error("an unknown word id must not be found")
	}
}

func TestGeneratedRoundTripsTheWholeContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "generated.jsonl")
	want := sampleGenerated("w1", "maintain")
	want.CreatedAt = time.Date(2026, time.September, 14, 9, 0, 0, 0, time.UTC)
	if _, _, err := OpenGenerated(path).Save(want, testNow); err != nil {
		t.Fatal(err)
	}

	items, _, err := OpenGenerated(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	got := items[0]
	if got.Provider != "deepseek" || len(got.Content.Examples) != 1 || len(got.Content.Dialogue) != 2 {
		t.Errorf("content did not survive the round trip: %+v", got)
	}
	if got.Content.Dialogue[1].Speaker != "B" || got.Content.Dialogue[1].Line != "I maintain it every month." {
		t.Errorf("dialogue was mangled: %+v", got.Content.Dialogue)
	}
}

func TestGeneratedIsNotPartOfTheMemoryFingerprint(t *testing.T) {
	dir := t.TempDir()
	paths := PathsIn(dir)

	before, err := Fingerprint(paths)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenGenerated(paths.Generated).Save(sampleGenerated("w1", "maintain"), testNow); err != nil {
		t.Fatal(err)
	}
	after, err := Fingerprint(paths)
	if err != nil {
		t.Fatal(err)
	}

	// Expansion content cannot change when a word is due, so it must not
	// invalidate the schedule cache.
	if before != after {
		t.Error("caching expansion content must not invalidate the memory cache")
	}
}

func TestGeneratedPathLivesBesideTheOtherFiles(t *testing.T) {
	paths := PathsIn("/tmp/example")
	if filepath.Dir(paths.Generated) != "/tmp/example" {
		t.Errorf("generated file is at %q, want it beside the others", paths.Generated)
	}
	if _, err := os.Stat(paths.Generated); !os.IsNotExist(err) {
		t.Errorf("PathsIn must not create files, stat err = %v", err)
	}
}
