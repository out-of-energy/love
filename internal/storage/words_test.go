package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, time.September, 13, 10, 0, 0, 0, time.UTC)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"  Book ":     "book",
		"BOOK":        "book",
		"Ice   Cream": "ice cream",
		"Well-Known":  "well-known",
		"\tEvil\n":    "evil",
		"":            "",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoadReadsTheCurrentSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "words.jsonl")
	writeFile(t, path, `{"id":"a1b2c3d4","word":"maintain","normalized":"maintain","ipa":"/meɪnˈteɪn/","eli5":"To keep something working well.","chinese":"维护","source":"cli","created_at":"2026-09-13T10:20:30Z"}`+"\n")

	words, warnings, err := OpenWords(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	if len(words) != 1 {
		t.Fatalf("got %d words, want 1", len(words))
	}
	w := words[0]
	if w.ID != "a1b2c3d4" || w.Normalized != "maintain" || w.Source != "cli" {
		t.Errorf("unexpected word: %+v", w)
	}
	if w.NeedsMigration() {
		t.Error("a current-schema record must not be flagged for migration")
	}
}

// The whole point of M1 is that the 20 words already in the user's file must
// survive the schema change.
func TestLoadAcceptsLegacyFourFieldRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "words.jsonl")
	writeFile(t, path, `{"word":"evil","ipa":"/ˈiːvəl/","eli5":"Very, very bad.","chinese":"邪恶的"}`+"\n")

	words, warnings, err := OpenWords(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("a legacy record must load cleanly, got warnings: %v", warnings)
	}
	if len(words) != 1 {
		t.Fatalf("got %d words, want 1", len(words))
	}
	w := words[0]
	if w.Word != "evil" || w.IPA != "/ˈiːvəl/" || w.Chinese != "邪恶的" {
		t.Errorf("legacy fields were lost: %+v", w)
	}
	if w.Normalized != "evil" {
		t.Errorf("Normalized = %q, want it derived from the word", w.Normalized)
	}
	if !w.NeedsMigration() {
		t.Error("a legacy record must be flagged for migration")
	}
}

func TestLoadSkipsDamagedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "words.jsonl")
	writeFile(t, path, `{"word":"evil","ipa":"/ˈiːvəl/","eli5":"bad","chinese":"邪恶的"}`+"\n"+
		"\n"+
		"this is not json\n"+
		`{"ipa":"/x/","eli5":"no word","chinese":"空"}`+"\n"+
		`{"word":"sign","ipa":"/saɪn/","eli5":"A sign.","chinese":"标志"}`+"\n")

	words, warnings, err := OpenWords(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(words) != 2 {
		t.Fatalf("got %d words, want 2", len(words))
	}
	if len(warnings) != 2 {
		t.Errorf("got %d warnings, want 2: %v", len(warnings), warnings)
	}
}

func TestLoadCollapsesDuplicatesKeepingTheFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "words.jsonl")
	writeFile(t, path,
		`{"word":"evil","eli5":"first","chinese":"邪恶的"}`+"\n"+
			`{"word":"EVIL","eli5":"second","chinese":"邪恶的"}`+"\n"+
			`{"word":" evil ","eli5":"third","chinese":"邪恶的"}`+"\n")

	words, warnings, err := OpenWords(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(words) != 1 {
		t.Fatalf("got %d words, want 1 after collapsing duplicates", len(words))
	}
	if words[0].ELI5 != "first" {
		t.Errorf("kept %q, want the first occurrence", words[0].ELI5)
	}
	if len(warnings) != 2 {
		t.Errorf("got %d warnings, want 2", len(warnings))
	}
}

func TestAddIsIdempotentAcrossCasing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "words.jsonl")
	store := OpenWords(path)

	first, err := store.Add(Word{Word: "Maintain", IPA: "/meɪnˈteɪn/", ELI5: "keep it working", Chinese: "维护"}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created {
		t.Fatal("the first add should create a record")
	}

	second, err := store.Add(Word{Word: "maintain", IPA: "/x/", ELI5: "different", Chinese: "维护"}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if second.Created {
		t.Error("adding the same word again must not create a second record")
	}
	if second.Word.ID != first.Word.ID {
		t.Errorf("id = %q, want the existing %q", second.Word.ID, first.Word.ID)
	}
	if second.Word.ELI5 != "keep it working" {
		t.Errorf("the stored record should win, got %q", second.Word.ELI5)
	}

	words, _, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(words) != 1 {
		t.Fatalf("file has %d records, want 1", len(words))
	}
}

func TestAddAssignsDistinctIDsAndDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "words.jsonl")
	store := OpenWords(path)

	seen := map[string]bool{}
	for _, w := range []string{"alpha", "beta", "gamma", "delta"} {
		res, err := store.Add(Word{Word: w}, testNow)
		if err != nil {
			t.Fatal(err)
		}
		if res.Word.ID == "" {
			t.Fatalf("%s got no id", w)
		}
		if seen[res.Word.ID] {
			t.Fatalf("duplicate id %q", res.Word.ID)
		}
		seen[res.Word.ID] = true
		if res.Word.Source != "cli" {
			t.Errorf("%s source = %q, want cli", w, res.Word.Source)
		}
		if res.Word.CreatedAt.IsZero() {
			t.Errorf("%s got no created_at", w)
		}
	}
}

func TestAddRejectsAnEmptyWord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "words.jsonl")
	if _, err := OpenWords(path).Add(Word{Word: "   "}, testNow); err == nil {
		t.Error("expected an error for a blank word")
	}
}

func TestRewritePreservesOrderAndIsReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "words.jsonl")
	store := OpenWords(path)

	want := []Word{
		{ID: "1", Word: "alpha", Normalized: "alpha", Source: "cli", CreatedAt: testNow},
		{ID: "2", Word: "beta", Normalized: "beta", Source: "cli", CreatedAt: testNow},
		{ID: "3", Word: "gamma", Normalized: "gamma", Source: "cli", CreatedAt: testNow},
	}
	if err := store.Rewrite(want); err != nil {
		t.Fatal(err)
	}

	got, _, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d words, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Word != want[i].Word {
			t.Errorf("position %d = %q, want %q", i, got[i].Word, want[i].Word)
		}
	}
}

func TestMigrateUpgradesLegacyRecordsAndKeepsABackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "words.jsonl")
	legacy := `{"word":"evil","ipa":"/ˈiːvəl/","eli5":"Very, very bad.","chinese":"邪恶的"}` + "\n" +
		`{"word":"sign","ipa":"/saɪn/","eli5":"A sign.","chinese":"标志"}` + "\n"
	writeFile(t, path, legacy)

	store := OpenWords(path)
	report, err := store.Migrate(testNow)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Performed || report.Migrated != 2 || report.Total != 2 {
		t.Fatalf("unexpected report: %+v", report)
	}

	// The pre-migration bytes must still exist somewhere.
	backup, err := os.ReadFile(report.Backup)
	if err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if string(backup) != legacy {
		t.Error("backup does not match the original file")
	}

	words, warnings, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("migrated file should load cleanly, got %v", warnings)
	}
	if len(words) != 2 {
		t.Fatalf("got %d words, want 2", len(words))
	}
	for _, w := range words {
		if w.NeedsMigration() {
			t.Errorf("%q is still legacy after migration: %+v", w.Word, w)
		}
	}
	// The original spelling and content must be untouched.
	if words[0].Word != "evil" || words[0].Chinese != "邪恶的" || words[0].IPA != "/ˈiːvəl/" {
		t.Errorf("migration altered the data: %+v", words[0])
	}
}

func TestMigrateIsANoOpWhenTheSchemaIsCurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "words.jsonl")
	store := OpenWords(path)
	if _, err := store.Add(Word{Word: "evil", IPA: "/x/", ELI5: "e", Chinese: "邪恶的"}, testNow); err != nil {
		t.Fatal(err)
	}

	report, err := store.Migrate(testNow)
	if err != nil {
		t.Fatal(err)
	}
	if report.Performed {
		t.Error("a current file must not be rewritten")
	}
	if report.Backup != "" {
		t.Errorf("no backup should be made, got %q", report.Backup)
	}
}

func TestMigratedTimestampsPreserveFileOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "words.jsonl")
	writeFile(t, path,
		`{"word":"first","eli5":"1","chinese":"一"}`+"\n"+
			`{"word":"second","eli5":"2","chinese":"二"}`+"\n")

	store := OpenWords(path)
	if _, err := store.Migrate(testNow); err != nil {
		t.Fatal(err)
	}
	words, _, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(words) != 2 || words[0].Word != "first" || words[1].Word != "second" {
		t.Fatalf("order changed: %+v", words)
	}
	// New-word introduction relies on this order, so it must be stable.
	if words[0].CreatedAt.After(words[1].CreatedAt) {
		t.Error("created_at ordering contradicts the file order")
	}
}

func TestSavedLinesHaveExactlyTheDocumentedFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "words.jsonl")
	if _, err := OpenWords(path).Add(Word{Word: "maintain", IPA: "/x/", ELI5: "e", Chinese: "维护"}, testNow); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	want := []string{"id", "word", "normalized", "ipa", "eli5", "chinese", "source", "created_at"}
	for _, field := range want {
		if _, ok := raw[field]; !ok {
			t.Errorf("missing field %q in %v", field, raw)
		}
	}
	if len(raw) != len(want) {
		t.Errorf("got %d fields, want %d: %v", len(raw), len(want), raw)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Error("the file must end with a newline")
	}
}

// A record that has the form layer writes it, and one that does not omits it
// rather than storing two empty strings. That is what lets an older line keep
// its exact bytes through a rewrite.
func TestTheFormLayerIsWrittenWhenItExists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "words.jsonl")
	store := OpenWords(path)
	if _, err := store.Add(Word{
		Word:    "expose",
		IPA:     "/ɪkˈspəʊz/",
		Phonics: "ex·pose → /ɪk/ · /ˈspəʊz/",
		Parts:   `ex- (out) · pos (put) · -e ⇒ "put out"`,
		ELI5:    "To show something that was hidden.",
		Chinese: "暴露",
	}, testNow); err != nil {
		t.Fatal(err)
	}

	words, warnings, err := store.Load()
	if err != nil || len(warnings) != 0 {
		t.Fatalf("Load = %v, warnings %v", err, warnings)
	}
	if len(words) != 1 || words[0].Phonics == "" || words[0].Parts == "" {
		t.Fatalf("the form layer did not survive the round trip: %+v", words)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 10 {
		t.Errorf("a complete record has 10 fields, got %d: %v", len(raw), raw)
	}
}

func TestNeedsForm(t *testing.T) {
	for name, tc := range map[string]struct {
		word Word
		want bool
	}{
		"complete":   {Word{Phonics: "b", Parts: "b"}, false},
		"no phonics": {Word{Parts: "b"}, true},
		"no parts":   {Word{Phonics: "b"}, true},
		"neither":    {Word{}, true},
	} {
		if got := tc.word.NeedsForm(); got != tc.want {
			t.Errorf("%s: NeedsForm = %v, want %v", name, got, tc.want)
		}
	}
}

// SetForm is the only write in the tool that touches a record which already
// exists, so what it may change is exactly as important as what it changes.
func TestSetFormFillsOnlyTheFormLayer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "words.jsonl")
	store := OpenWords(path)
	body := `{"id":"w1","word":"evil","normalized":"evil","ipa":"/ˈiːvəl/","eli5":"Very, very bad.","chinese":"邪恶的","source":"cli","created_at":"2026-01-02T03:04:05Z"}` + "\n" +
		`{"id":"w2","word":"sign","normalized":"sign","ipa":"/saɪn/","eli5":"A sign.","chinese":"标志","source":"cli","created_at":"2026-01-03T03:04:05Z"}` + "\n"
	writeFile(t, path, body)

	updated, err := store.SetForm("sign", Form{
		Phonics: "sign → /saɪn/",
		Parts:   "no clear prefix or suffix",
		Source:  "model",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != "w2" || updated.CreatedAt.IsZero() {
		t.Errorf("the identity fields should come back intact: %+v", updated)
	}

	words, warnings, err := store.Load()
	if err != nil || len(warnings) != 0 {
		t.Fatalf("Load = %v, warnings %v", err, warnings)
	}
	if len(words) != 2 {
		t.Fatalf("an update must not append: got %d records", len(words))
	}
	if words[0].Word != "evil" || words[0].Phonics != "" {
		t.Errorf("the untouched record changed: %+v", words[0])
	}
	if words[1].Phonics != "sign → /saɪn/" || words[1].Parts != "no clear prefix or suffix" {
		t.Errorf("the form layer was not written: %+v", words[1])
	}
	if words[1].PartsSource != "model" {
		t.Errorf("where the segmentation came from should be recorded: %+v", words[1])
	}
	if words[1].IPA != "/saɪn/" || words[1].ELI5 != "A sign." || words[1].Chinese != "标志" {
		t.Errorf("the anchor must not change: %+v", words[1])
	}
	if words[1].ID != "w2" || !words[1].CreatedAt.Equal(time.Date(2026, time.January, 3, 3, 4, 5, 0, time.UTC)) {
		t.Errorf("identity or creation time changed: %+v", words[1])
	}
}

func TestSetFormErrorsOnAnUnknownWord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "words.jsonl")
	writeFile(t, path, `{"id":"w1","word":"evil","normalized":"evil","ipa":"/x/","eli5":"e","chinese":"邪恶的","source":"cli","created_at":"2026-01-02T03:04:05Z"}`+"\n")

	if _, err := OpenWords(path).SetForm("absent", Form{Parts: "b"}); err == nil {
		t.Error("setting the form of a word that is not stored should fail")
	}
}

// An empty field means "leave it as it is". That is what lets a re-check of the
// segmentation rewrite the parts without regenerating a phonics line that was
// already right.
func TestSetFormLeavesEmptyFieldsAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "words.jsonl")
	store := OpenWords(path)
	writeFile(t, path, `{"id":"w1","word":"evil","normalized":"evil","ipa":"/x/","phonics":"e·vil → /ˈiː/ · /vəl/","parts":"evil","parts_source":"model","eli5":"e","chinese":"邪恶的","source":"cli","created_at":"2026-01-02T03:04:05Z"}`+"\n")

	if _, err := store.SetForm("evil", Form{Parts: "ev · il", Source: "morphology"}); err != nil {
		t.Fatal(err)
	}

	words, _, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if words[0].Parts != "ev · il" || words[0].PartsSource != "morphology" {
		t.Errorf("the segmentation should have been replaced: %+v", words[0])
	}
	if words[0].Phonics != "e·vil → /ˈiː/ · /vəl/" {
		t.Errorf("an empty phonics must not erase the existing one: %+v", words[0])
	}
}

// A damaged line may be the only copy of a word, so the upgrade refuses to
// rewrite the file — exactly as Migrate does.
func TestSetFormRefusesADamagedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "words.jsonl")
	body := `{"id":"w1","word":"evil","normalized":"evil","ipa":"/x/","eli5":"e","chinese":"邪恶的","source":"cli","created_at":"2026-01-02T03:04:05Z"}` + "\n" +
		"not json at all\n"
	writeFile(t, path, body)

	if _, err := OpenWords(path).SetForm("evil", Form{Phonics: "e·vil → /ˈiː/ · /vəl/", Parts: "no clear parts"}); err == nil {
		t.Error("a file with a damaged line must not be rewritten")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != body {
		t.Errorf("the file changed despite the refusal:\n%s", after)
	}
}
