package lexicon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture writes a miniature data layer. The real one is 14 MB of Wiktionary
// data; these tests are about the logic that reads it, so a dozen words are
// enough — and unlike the real files they can be read by a human.
//
// The rows are sorted by their first column, which is not tidiness: the reader
// binary-searches the file, and unsorted input would simply not be found.
// scripts/build_morphology.py sorts for the same reason.
func fixture(t *testing.T) *Lexicon {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "derivations.tsv"), strings.Join([]string{
		"happiness\thappy\tness\tsuffix",
		"international\tnation\tinter\tprefix",
		"international\tnational\tal\tsuffix",
		"national\tnation\tal\tsuffix",
		"sadness\tsad\tness\tsuffix",
		"teacher\tteach\ter\tsuffix",
		"unhappiness\tunhappy\tness\tsuffix",
		"unhappy\thappy\tun\tprefix",
		"unpredictable\tpredict\tpre\tprefix",
		"unpredictable\tpredictable\tun\tprefix",
	}, "\n")+"\n")
	write(t, filepath.Join(dir, "inflections.tsv"), strings.Join([]string{
		"infusing\tinfuse\ting",
		"spreads\tspread\ts",
	}, "\n")+"\n")
	write(t, filepath.Join(dir, "lemmas.txt"), strings.Join([]string{
		"happy", "happiness", "infuse", "international", "kettle", "nation",
		"national", "predict", "predictable", "sad", "sadness", "spread",
		"teach", "teacher", "unhappiness", "unhappy",
	}, "\n")+"\n")
	return Open(dir)
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func forms(c Candidate) string { return strings.Join(c.Forms(), " · ") }

// An unknown word is not a failure: it only means the data has nothing to say,
// and the caller should fall back to the model.
func TestAnUnknownWordReportsNoData(t *testing.T) {
	analysis := fixture(t).Analyze("florble")
	if analysis.Known {
		t.Errorf("florble is not in this fixture, got %+v", analysis)
	}
	if len(analysis.Candidates) != 0 {
		t.Errorf("no data means no candidates: %+v", analysis)
	}
}

// A word the data records but never derives is underived, not unexplained. The
// single-part candidate is how the caller learns to say "no clear parts".
func TestAnUnderivedWordYieldsOneWholeCandidate(t *testing.T) {
	analysis := fixture(t).Analyze("kettle")
	if !analysis.Known {
		t.Fatal("kettle should be known")
	}
	if len(analysis.Candidates) != 1 || forms(analysis.Candidates[0]) != "kettle" {
		t.Errorf("want one whole-word candidate, got %+v", analysis.Candidates)
	}
}

// Etymologies live on the lemma. "infusing" has none on Wiktionary at all, so
// resolving the inflection is what makes the data usable for half the words a
// learner actually types.
func TestAnInflectedFormIsResolvedToItsLemma(t *testing.T) {
	analysis := fixture(t).Analyze("infusing")
	if !analysis.Known {
		t.Fatal("infusing should be known through its lemma")
	}
	if got := forms(analysis.Candidates[0]); got != "infuse · ing" {
		t.Errorf("got %q, want %q", got, "infuse · ing")
	}
	last := analysis.Candidates[0].Parts[len(analysis.Candidates[0].Parts)-1]
	if last.Form != "-ing" || last.Kind != KindSuffix {
		t.Errorf("the inflection should be a suffix part: %+v", last)
	}
}

// A chain is assembled from the inside out, and prefixes have to end up at the
// front however deep the recursion went. The shallower "un- · happiness" is
// offered too — it is transparent and shorter — but the full chain must be
// there, because that is the one that explains where the word came from.
func TestAChainIsAssembledInReadingOrder(t *testing.T) {
	analysis := fixture(t).Analyze("unhappiness")
	if len(analysis.Candidates) == 0 {
		t.Fatal("no candidates")
	}
	found := false
	for _, c := range analysis.Candidates {
		if forms(c) == "un · happy · ness" {
			found = true
		}
	}
	if !found {
		t.Errorf("the full chain is missing from %+v", analysis.Candidates)
	}
}

// More than one real analysis is normal — "international" is both
// inter+nation+al and national+al in the data — so all of them are offered and
// the chooser decides.
func TestEveryRecordedAnalysisIsOffered(t *testing.T) {
	analysis := fixture(t).Analyze("international")
	if len(analysis.Candidates) < 1 {
		t.Fatal("no candidates")
	}
	got := map[string]bool{}
	for _, c := range analysis.Candidates {
		got[forms(c)] = true
	}
	if !got["inter · nation · al"] && !got["inter · nation"] {
		t.Errorf("the inter+...+al analysis is missing: %v", got)
	}
}

// Meanings come from the curated table, keyed by kind: "un-" and "-ness" must
// not be confused with the free words "un" and "ness".
func TestGlossesComeFromTheCuratedTable(t *testing.T) {
	parts := fixture(t).Analyze("unhappy").Candidates[0].Parts
	if len(parts) != 2 {
		t.Fatalf("expected un- plus a root, got %+v", parts)
	}
	if parts[0].Gloss == "" || !strings.Contains(strings.ToLower(parts[0].Gloss), "not") {
		t.Errorf("un- should be glossed as a negative: %+v", parts[0])
	}
	if parts[1].Gloss != "" {
		t.Errorf("a free root is glossed by the model, not by the table: %+v", parts[1])
	}
}

// The y-to-i respelling is why "happiness" must not be split as "happi · -ness":
// the stem a learner recognises is "happy", and finding it means undoing the
// spelling change before looking it up.
func TestTheYToIRespellingIsUndone(t *testing.T) {
	got := fixture(t).Analyze("happiness")
	if len(got.Candidates) == 0 {
		t.Fatal("no candidates")
	}
	if forms(got.Candidates[0]) != "happy · ness" {
		t.Errorf("got %q, want %q", forms(got.Candidates[0]), "happy · ness")
	}
	for _, c := range got.Candidates {
		if strings.HasPrefix(forms(c), "happi ") {
			t.Errorf("a stem that is not a word reached the candidate list: %q", forms(c))
		}
	}
}

// The table is the half of the data layer that cannot be downloaded, so its
// presence is worth asserting: an empty table would degrade every segmentation
// to a bare list of morphemes without failing anything.
func TestTheCuratedTableShipped(t *testing.T) {
	if AffixCount() < 100 {
		t.Fatalf("the embedded table holds %d morphemes; it looks truncated", AffixCount())
	}
}

// The data layer is optional. A missing directory must read as "no data", not
// as an error and not as a crash.
func TestAMissingDirectoryIsSimplyNoData(t *testing.T) {
	lex := Open(filepath.Join(t.TempDir(), "absent"))
	if lex.Available() {
		t.Error("an absent data layer must not report itself available")
	}
	if analysis := lex.Analyze("happy"); analysis.Known {
		t.Errorf("no data layer means no analysis: %+v", analysis)
	}
}

func TestStorePathFollowsTheWordsFile(t *testing.T) {
	if got := StorePath("/tmp/store/words.jsonl"); got != filepath.Join("/tmp/store", "lexicon") {
		t.Errorf("StorePath = %q", got)
	}
}

// A hand correction outranks both halves of the data layer, and it has to work
// with no data layer at all: it is compiled in precisely so that the words the
// data gets wrong stay fixed however the store is set up.
func TestAHandCorrectionOutranksEverything(t *testing.T) {
	if OverrideCount() == 0 {
		t.Fatal("the shipped overrides file is empty")
	}
	lex := Open(filepath.Join(t.TempDir(), "absent"))
	analysis := lex.Analyze("symlink")
	if !analysis.Known || len(analysis.Candidates) != 1 {
		t.Fatalf("the correction did not apply: %+v", analysis)
	}
	if got := forms(analysis.Candidates[0]); got != "symlink" {
		t.Errorf("got %q, want the corrected whole word", got)
	}
}

func TestParseFormsReadsTheHyphens(t *testing.T) {
	parts := parseForms("un- · happy · -ness")
	want := []Part{
		{Form: "un-", Kind: KindPrefix},
		{Form: "happy", Kind: KindRoot},
		{Form: "-ness", Kind: KindSuffix},
	}
	if len(parts) != len(want) {
		t.Fatalf("got %+v", parts)
	}
	for i := range want {
		if parts[i].Form != want[i].Form || parts[i].Kind != want[i].Kind {
			t.Errorf("part %d = %+v, want %+v", i, parts[i], want[i])
		}
	}
}

// A stored parts line is compared with what the data would produce today by its
// morphemes alone: the glosses are the model's wording and must not make an
// otherwise correct split look stale.
func TestStemsIgnoreTheWording(t *testing.T) {
	line := `un- (not) · happy (feeling good) · -ness (state of) ⇒ "the state of not feeling good"`
	if !SameStems(line, []string{"un-", "happy", "-ness"}) {
		t.Errorf("the split should match: %v", Stems(line))
	}
	if SameStems(line, []string{"un-", "happiness"}) {
		t.Error("a different split must not match")
	}
	if SameStems("no clear prefix or suffix", []string{"un-", "happy"}) {
		t.Error("a refusal is not a split")
	}
}
