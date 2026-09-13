package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"  Book ":     "book",
		"BOOK":        "book",
		"Ice   Cream": "ice cream",
		"Well-Known":  "well-known",
		"\tEvil\n":    "evil",
		"SERENDIPITY": "serendipity",
		"":            "",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLookupFirstRecordWins(t *testing.T) {
	records := []Record{
		{Word: "book", IPA: "/bʊk/", ELI5: "first", Chinese: "书"},
		{Word: "book", IPA: "/bʊk/", ELI5: "second", Chinese: "书"},
	}
	got, ok := Lookup(records, "book")
	if !ok {
		t.Fatal("expected a hit")
	}
	if got.ELI5 != "first" {
		t.Errorf("expected the first record to win, got %q", got.ELI5)
	}
}

func TestLookupMiss(t *testing.T) {
	if _, ok := Lookup(nil, "book"); ok {
		t.Error("empty cache must not report a hit")
	}
}

func TestLoadSkipsDamagedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "words.jsonl")
	content := `{"word":"evil","ipa":"/ˈiːvəl/","eli5":"Very, very bad.","chinese":"邪恶的"}

this is not json
{"word":"","ipa":"/x/","eli5":"no word here","chinese":"空"}
{"word":"sign","ipa":"/saɪn/","eli5":"A picture or words that tell you something.","chinese":"标志"}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	records, warnings, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned an error: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2: %+v", len(records), records)
	}
	if len(warnings) != 2 {
		t.Errorf("got %d warnings, want 2: %v", len(warnings), warnings)
	}
}

func TestLoadMissingFileIsEmptyNotAnError(t *testing.T) {
	records, warnings, err := Load(filepath.Join(t.TempDir(), "does-not-exist.jsonl"))
	if err != nil {
		t.Fatalf("a missing cache must not be an error: %v", err)
	}
	if len(records) != 0 || len(warnings) != 0 {
		t.Fatalf("expected an empty result, got %d records and %d warnings", len(records), len(warnings))
	}
}

func TestSaveAppendsOnceThenDeduplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "words.jsonl")
	first := Record{Word: "book", IPA: "/bʊk/", ELI5: "Thing with pages.", Chinese: "书"}

	canonical, written, err := Save(path, first)
	if err != nil {
		t.Fatal(err)
	}
	if !written || canonical.Word != "book" {
		t.Fatalf("the first Save should have written, got written=%v canonical=%+v", written, canonical)
	}

	// A second Save for the same word must not create a duplicate line.
	again, written, err := Save(path, Record{Word: "book", IPA: "/x/", ELI5: "other", Chinese: "书"})
	if err != nil {
		t.Fatal(err)
	}
	if written {
		t.Error("the second Save must not write a duplicate")
	}
	if again.ELI5 != "Thing with pages." {
		t.Errorf("the stored record should win, got %q", again.ELI5)
	}

	records, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
}

func TestSavedLineHasExactlyFourFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "words.jsonl")
	rec := Record{Word: "sign", IPA: "/saɪn/", ELI5: "A picture or words that tell you something.", Chinese: "标志"}
	if _, _, err := Save(path, rec); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("stored line is not valid JSON: %v", err)
	}
	if len(raw) != 4 {
		t.Errorf("got %d fields, want exactly 4: %v", len(raw), raw)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Error("stored line must end with a newline")
	}
}

func TestSaveLeavesNoLockBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "words.jsonl")
	if _, _, err := Save(path, Record{Word: "sign", IPA: "/saɪn/", ELI5: "A sign.", Chinese: "标志"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Errorf("lock file should have been released, stat err = %v", err)
	}
}

func TestRecordValidate(t *testing.T) {
	full := Record{Word: "book", IPA: "/bʊk/", ELI5: "Thing with pages.", Chinese: "书"}
	if err := full.Validate(); err != nil {
		t.Errorf("complete record should validate: %v", err)
	}
	for name, rec := range map[string]Record{
		"no word":    {IPA: "/b/", ELI5: "x", Chinese: "书"},
		"no ipa":     {Word: "book", ELI5: "x", Chinese: "书"},
		"no eli5":    {Word: "book", IPA: "/b/", Chinese: "书"},
		"no chinese": {Word: "book", IPA: "/b/", ELI5: "x"},
	} {
		if err := rec.Validate(); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
}
