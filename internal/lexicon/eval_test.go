package lexicon

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goldFloor is the share of the answer key the data layer must get right before
// the model is asked to choose. It is a floor, not a target: it exists so a
// change to the chain builder that quietly costs accuracy fails here instead of
// showing up as worse cards weeks later.
const goldFloor = 0.95

// gold is one hand-checked word. Several analyses are accepted where more than
// one is genuinely correct: "international" really is inter+nation+al, and
// "misunderstanding" is defensibly mis+understand+ing and mis+under+stand+ing.
type gold struct {
	Word   string     `json:"word"`
	Accept [][]string `json:"accept"`
}

// TestGoldSet measures the data layer against a hand-checked answer key.
//
// It runs only when a built data layer is present, because the segmentations
// live beside the user's word file rather than in the repository — they are
// derived from Wiktionary and carry their own licence. Build one with
// scripts/build_morphology.py, or point LOVE_LEXICON at an existing one.
//
// What is measured is whether a correct answer is *reachable*: the tool offers
// up to three analyses and the model then chooses among them, so a word passes
// when any offered analysis is one a person accepted.
func TestGoldSet(t *testing.T) {
	dir := os.Getenv("LOVE_LEXICON")
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, ".ewh", "lexicon")
		}
	}
	if !Exists(dir) {
		t.Skipf("no morphology data at %s; run scripts/build_morphology.py", dir)
	}

	file, err := os.Open(filepath.Join("testdata", "parts_gold.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	var (
		total, covered, correct int
		missing, wrong          []string
	)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var want gold
		if err := json.Unmarshal([]byte(line), &want); err != nil {
			t.Fatalf("bad gold line %q: %v", line, err)
		}
		total++

		analysis := Open(dir).Analyze(want.Word)
		if !analysis.Known {
			missing = append(missing, want.Word)
			continue
		}
		covered++

		offered := make([]string, 0, len(analysis.Candidates))
		hit := false
		for _, candidate := range analysis.Candidates {
			forms := strings.Join(candidate.Forms(), " · ")
			offered = append(offered, forms)
			for _, accept := range want.Accept {
				if forms == strings.Join(accept, " · ") {
					hit = true
				}
			}
		}
		if hit {
			correct++
			continue
		}
		wrong = append(wrong, want.Word+"\n      offered: "+strings.Join(offered, "\n               "))
	}

	if covered == 0 {
		t.Fatalf("the data layer at %s answered nothing", dir)
	}

	rate := float64(correct) / float64(covered)
	t.Logf("gold set: %d words, data layer has an opinion on %d, correct on %d (%.0f%%)",
		total, covered, correct, rate*100)
	if len(missing) > 0 {
		t.Logf("no data (the model answers these alone): %s", strings.Join(missing, ", "))
	}
	if len(wrong) > 0 {
		t.Logf("data layer disagreed with the answer key on %d:\n      %s",
			len(wrong), strings.Join(wrong, "\n      "))
	}
	if rate < goldFloor {
		t.Errorf("accuracy %.0f%% is below the %.0f%% floor", rate*100, goldFloor*100)
	}
}
