package lexicon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// phonicsFixture writes a two-row pronunciation index.
func phonicsFixture(t *testing.T) *Lexicon {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "phonics.tsv"), strings.Join([]string{
		"profile\t/ˈpɹəʊfaɪl/\t/ˈpɹəʊ|faɪl/\tpro·file\tuk+moby",
		"profiling\t/ˈpɹəʊfaɪlɪŋ/\t/ˈpɹəʊ|faɪ|lɪŋ/\tpro·fil·ing\tuk+moby",
		"winter\t/ˈwɪntɐ/\t/ˈwɪn|tɐ/\twinter\tuk",
	}, "\n")+"\n")
	return Open(dir)
}

// The index keeps one pair of slashes around the whole transcription, and the
// line prints one pair per chunk, so the slashes have to move.
func TestParseSoundChunksMovesTheSlashes(t *testing.T) {
	got := parseSoundChunks("/ˈpɹəʊ|faɪ|lɪŋ/")
	want := []string{"/ˈpɹəʊ/", "/faɪ/", "/lɪŋ/"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("chunk %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// The case that started all of this: "profile" is /ˈpɹəʊ.faɪl/, and adding -ing
// hands the /l/ to the next syllable as its onset. Splitting by letters gives
// /faɪl/ · /ɪŋ/, which teaches that "fil" says /faɪl/ — it does not.
func TestTheLGoesToTheNextSyllable(t *testing.T) {
	p, ok := phonicsFixture(t).Pronounce("profiling")
	if !ok {
		t.Fatal("profiling should be in the index")
	}
	if got := strings.Join(p.SoundChunks, " · "); got != "/ˈpɹəʊ/ · /faɪ/ · /lɪŋ/" {
		t.Errorf("sound chunks = %q", got)
	}
	if p.IPA != "/ˈpɹəʊfaɪlɪŋ/" {
		t.Errorf("ipa = %q", p.IPA)
	}
}

func TestPhrasePairsTheSpellingWithTheSounds(t *testing.T) {
	p, _ := phonicsFixture(t).Pronounce("profiling")
	want := "pro·fil·ing → /ˈpɹəʊ/ · /faɪ/ · /lɪŋ/"
	if got := p.Phrase(); got != want {
		t.Errorf("Phrase = %q, want %q", got, want)
	}
}

// Without hyphenation data the word is printed whole rather than split by a
// guess: this package's whole argument is that invented boundaries mislead.
func TestPhraseWithoutSpellingChunksKeepsTheWordWhole(t *testing.T) {
	p, _ := phonicsFixture(t).Pronounce("winter")
	if got := p.Phrase(); got != "winter → /ˈwɪn/ · /tɐ/" {
		t.Errorf("Phrase = %q", got)
	}
}

func TestAnAbsentIndexIsSimplyNoPronunciation(t *testing.T) {
	lex := Open(filepath.Join(t.TempDir(), "absent"))
	if lex.PhonicsAvailable() {
		t.Error("an absent index must not report itself available")
	}
	if _, ok := lex.Pronounce("profiling"); ok {
		t.Error("no index means no pronunciation")
	}
}

// A word that is not in the dictionary is not composed from its lemma: putting
// "profile" and "-ing" back together is exactly the mistake this file exists to
// prevent, because only the dictionary knows where the /l/ lands.
func TestAnUnknownWordIsNotComposedFromItsLemma(t *testing.T) {
	if _, ok := phonicsFixture(t).Pronounce("profilingly"); ok {
		t.Error("a word outside the index must not be guessed at")
	}
}

// The answer key is the ground truth the pipeline is measured against. It lives
// in the repository; the index it measures is built locally, so this skips when
// no index has been built.
func TestPhonicsGoldSet(t *testing.T) {
	dir := os.Getenv("LOVE_LEXICON")
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, ".ewh", "lexicon")
		}
	}
	lex := Open(dir)
	if !lex.PhonicsAvailable() {
		t.Skipf("no pronunciation index at %s; run scripts/build_phonics.py", dir)
	}

	data, err := os.ReadFile(filepath.Join("testdata", "phonics_gold.tsv"))
	if err != nil {
		t.Skipf("no answer key yet: %v", err)
	}

	var total, correct int
	var misses []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			continue
		}
		word, want := fields[0], strings.TrimSpace(fields[1])
		total++

		p, ok := lex.Pronounce(word)
		if !ok {
			misses = append(misses, word+"\n      not in the index")
			continue
		}
		if got := strings.Join(p.SoundChunks, "|"); got != want {
			misses = append(misses, word+"\n      got  "+got+"\n      want "+want)
			continue
		}
		correct++
	}
	if total == 0 {
		t.Fatal("the answer key is empty")
	}

	rate := float64(correct) / float64(total)
	t.Logf("phonics gold set: %d words, %d correct (%.0f%%)", total, correct, rate*100)
	if len(misses) > 0 {
		t.Logf("misses:\n      %s", strings.Join(misses, "\n      "))
	}
	if rate < 0.95 {
		t.Errorf("accuracy %.0f%% is below the 95%% floor", rate*100)
	}
}
