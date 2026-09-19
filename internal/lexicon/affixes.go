package lexicon

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"strings"
)

// affixes.jsonl is the curated half of the data layer: the meanings of the
// morphemes that segmentations are made of.
//
// MorphyNet knows that "censorship" is "censor + ship". It does not know that
// "ship" means "state of", and no amount of segmentation data will ever supply
// that, because it is a fact about the language rather than about this
// particular word. The table is small on purpose — a few hundred entries cover
// the productive affixes and the recurring Latin and Greek roots — and it is
// deliberately conservative: a form whose origin is uncertain is left out, so
// the tool can say "no clear parts" rather than invent one.
//
//go:embed affixes.jsonl
var affixesData string

// overrides.jsonl is the last word on the words the data gets wrong.
//
// It is tiny, and it is the escape hatch that makes the rest of the design
// safe. Both halves of the data layer can be confidently wrong: Wiktionary
// derives "curious" from the element "curium", and the table will happily read
// "symlink" as the Greek prefix "syn-". A re-check rewrites a word's parts, so
// a hand edit in the word file would be lost; an entry here survives, and
// outranks everything else.
//
//go:embed overrides.jsonl
var overridesData string

type overrideRow struct {
	Word  string `json:"word"`
	Parts string `json:"parts"`
	Note  string `json:"note"`
}

// overrideTable maps a word to the segmentation that is correct for it.
var overrideTable = parseOverrides(overridesData)

func parseOverrides(data string) map[string]Candidate {
	table := make(map[string]Candidate)
	scanner := bufio.NewScanner(strings.NewReader(data))
	scanner.Buffer(make([]byte, 0, 8*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var row overrideRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}
		word := normalize(row.Word)
		if word == "" || strings.TrimSpace(row.Parts) == "" {
			continue
		}
		table[word] = Candidate{Parts: parseForms(row.Parts)}
	}
	return table
}

// parseForms turns a written segmentation into parts, reading the hyphens the
// way a learner does: a trailing one marks a prefix, a leading one a suffix.
func parseForms(written string) []Part {
	var parts []Part
	for _, chunk := range strings.Split(written, "·") {
		form := strings.TrimSpace(chunk)
		if form == "" {
			continue
		}
		kind := KindRoot
		switch {
		case strings.HasSuffix(form, "-"):
			kind = KindPrefix
		case strings.HasPrefix(form, "-"):
			kind = KindSuffix
		}
		parts = append(parts, Part{Form: form, Kind: kind})
	}
	return parts
}

// OverrideCount reports how many words are corrected by hand.
func OverrideCount() int { return len(overrideTable) }

type affixRow struct {
	Form  string `json:"form"`
	Type  string `json:"type"`
	Gloss string `json:"gloss"`
	Lang  string `json:"lang"`
}

// affixTable maps "kind:stem" to a short meaning, e.g. "prefix:un" -> "not".
var affixTable = parseAffixes(affixesData)

func parseAffixes(data string) map[string]string {
	table := make(map[string]string, 600)
	scanner := bufio.NewScanner(strings.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var row affixRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}
		stem := strings.Trim(row.Form, "-")
		if stem == "" || row.Gloss == "" {
			continue
		}
		key := strings.ToLower(row.Type) + ":" + strings.ToLower(stem)
		if _, seen := table[key]; !seen {
			table[key] = row.Gloss
		}
	}
	return table
}

// AffixCount reports how many morphemes carry a meaning. It exists so a test can
// prove the table actually shipped, since an empty table would silently degrade
// every segmentation to "no gloss".
func AffixCount() int { return len(affixTable) }
