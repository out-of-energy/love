// Package lexicon answers "how is this word built?" from data instead of from a
// model.
//
// The problem it solves is narrow and concrete. Asked to split a word into a
// prefix, a root and a suffix, a language model produces something plausible
// every time — including for words that have no parts at all. It split
// "symlink" into "sym- (together) · link", which is invention, and it split
// "fundamentals" as "-amental", which is not a morpheme. Nothing in the prompt
// fixes that reliably, because the model has no way to know which analyses are
// real.
//
// So this package supplies the analyses that are real, and the model is left
// with the two jobs it is actually good at: choosing between candidates that
// exist, and glossing a free root. The facts come from Wiktionary through
// MorphyNet (see scripts/build_morphology.py), which is where the segmentations
// live; the morpheme meanings come from the small curated table embedded beside
// this file.
//
// Four answers are possible, and the difference matters:
//
//	the word has derivations   -> here are its real segmentations, pick one
//	the word is known, no chain-> there is no classical split (or it is a compound)
//	the word is outside the data, but the curated table can still read it
//	                           -> here is the table's analysis, pick one
//	neither can say anything   -> no data; fall back to the model alone
//
// A miss is not a failure. It means the data has nothing to say, and the caller
// should go back to asking the model — which is exactly what love did before
// this package existed.
package lexicon

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Part kinds, as they are written in the data layer and printed to a learner.
const (
	KindPrefix = "prefix"
	KindSuffix = "suffix"
	KindRoot   = "root"
)

// maxDepth bounds how far a derivation chain is followed. Four is generous:
// "un- + predict + -able" is two steps, and anything deeper is usually the
// data wandering through an obsolete intermediate that nobody wants to read.
const maxDepth = 4

// maxCandidates bounds how many analyses are offered to the chooser. Two or
// three is a judgement; a dozen is a quiz.
const maxCandidates = 3

// Part is one morpheme of a word.
type Part struct {
	// Form is written the way a learner reads it: prefixes carry a trailing
	// hyphen ("un-"), suffixes a leading one ("-ness"), roots neither ("happy").
	Form string
	// Kind is KindPrefix, KindSuffix or KindRoot.
	Kind string
	// Gloss is the morpheme's short original meaning, empty when the table does
	// not know it — a free root like "happy" is glossed by the model instead.
	Gloss string
}

// Candidate is one complete segmentation of a word.
type Candidate struct {
	Parts []Part
}

// Forms returns the surface forms without their hyphen markers, which is what a
// model's answer is compared against.
func (c Candidate) Forms() []string {
	forms := make([]string, 0, len(c.Parts))
	for _, p := range c.Parts {
		forms = append(forms, strings.Trim(p.Form, "-"))
	}
	return forms
}

// Words returns the candidate as a plain "a · b · c" spelling, which is what the
// prompt shows and what the model is told to reproduce.
func (c Candidate) Words() string {
	forms := make([]string, 0, len(c.Parts))
	for _, p := range c.Parts {
		forms = append(forms, strings.Trim(p.Form, "-"))
	}
	return strings.Join(forms, " · ")
}

// String renders the candidate the way it will be printed, glosses included.
func (c Candidate) String() string {
	rendered := make([]string, 0, len(c.Parts))
	for _, p := range c.Parts {
		if p.Gloss == "" {
			rendered = append(rendered, p.Form)
			continue
		}
		rendered = append(rendered, p.Form+" ("+p.Gloss+")")
	}
	return strings.Join(rendered, " · ")
}

// Analysis is what the data layer has to say about one word.
type Analysis struct {
	// Known reports whether the word appears in the morphology data at all.
	// When it is false the caller has no facts to constrain the model with.
	Known bool
	// FromOverride reports that a hand correction supplied the analysis, which
	// is worth recording: it is the one source a later rebuild must not undo.
	FromOverride bool
	// Candidates are real segmentations, best first. A single one-part
	// candidate means "this word has no classical split".
	Candidates []Candidate
}

// Lexicon is the data layer, loaded from a directory.
//
// Nothing is read until something is asked: a cache hit must never pay for
// this, since a hit is promised to be offline and instant.
type Lexicon struct {
	dir         string
	inflections *keysFile
	derivations *keysFile
	lemmas      *keysFile
	affixes     map[string]string
}

// Open returns a lexicon rooted at dir. A directory that does not exist, or one
// holding only some of the files, yields a lexicon that answers "unknown" to
// everything: the data layer is an optional upgrade, never a dependency.
func Open(dir string) *Lexicon {
	l := &Lexicon{dir: dir, affixes: affixTable}
	l.inflections = openKeys(filepath.Join(dir, "inflections.tsv"), 3)
	l.derivations = openKeys(filepath.Join(dir, "derivations.tsv"), 4)
	l.lemmas = openKeys(filepath.Join(dir, "lemmas.txt"), 1)
	return l
}

// DefaultDir returns where the build script writes by default, given the store
// directory the rest of the tool uses.
func DefaultDir(storeDir string) string { return filepath.Join(storeDir, "lexicon") }

// Available reports whether the segmentations were found. The lemma list alone
// is not enough: without segmentations there is nothing to constrain.
func (l *Lexicon) Available() bool {
	if l == nil {
		return false
	}
	return l.inflections.valid() || l.derivations.valid()
}

// Analyze returns the data layer's answer for word.
func (l *Lexicon) Analyze(word string) Analysis {
	key := normalize(word)
	if key == "" {
		return Analysis{}
	}

	// A hand correction outranks everything, and works even when no data layer
	// is installed, because it is embedded rather than downloaded.
	if override, ok := overrideTable[key]; ok {
		return Analysis{
			Known:        true,
			FromOverride: true,
			Candidates:   []Candidate{{Parts: append([]Part{}, override.Parts...)}},
		}
	}

	if l == nil || !l.Available() {
		return Analysis{}
	}

	base, inflection := key, ""
	if lemma, morpheme, ok := l.inflection(key); ok {
		base, inflection = lemma, morpheme
	}

	known := inflection != "" || l.known(base)
	seen := map[string]bool{}

	var candidates []Candidate

	if known {
		chains := l.chains(base, 0, seen)

		// A chain that is just the word itself says "the data records no
		// derivation", which is worth knowing but is not an analysis.
		analysed := !(len(chains) == 1 && len(chains[0].Parts) == 1 && chains[0].Parts[0].Form == base)

		// The table comes first so that a tie in ranking is settled in favour
		// of the analysis built from recorded morphemes rather than a
		// derivation chain, which is the one that occasionally wanders
		// ("curious" through "curium").
		candidates = append(candidates, l.tableSplit(base)...)
		if analysed {
			candidates = append(candidates, chains...)
		}
		if len(candidates) == 0 {
			candidates = chains
		}

		// An inflected form is worth keeping whole. "infusing" is "infuse" plus
		// an ending long before it is "in-" plus "fuse", and a learner meeting
		// the word wants the lemma they can look up, not the deepest true story.
		if inflection != "" {
			whole := Candidate{Parts: []Part{{Form: base, Kind: KindRoot}}}
			candidates = append([]Candidate{whole}, candidates...)
		}
	} else {
		// The word is outside the morphology data: a coinage, a proper noun, a
		// typo, or simply something nobody has written an etymology for.
		//
		// The curated table is not data about this word, but it is still
		// evidence. "tele-" plus "working" can be checked without any
		// dictionary entry for "teleworking": the prefix is recorded, and the
		// remainder is a word the data has seen. When the table can ground an
		// analysis, it does, and the model is constrained to it like any other.
		// When it cannot, the model is free — which is the honest answer for a
		// word nobody has described yet, and the reason the model is still here.
		candidates = l.tableSplit(base)
		if len(candidates) == 0 {
			return Analysis{}
		}
	}

	for i := range candidates {
		if inflection != "" {
			candidates[i].Parts = append(candidates[i].Parts, Part{Form: "-" + inflection, Kind: KindSuffix})
		}
		candidates[i].Parts = l.glossed(candidates[i].Parts)
	}
	candidates = rank(candidates)

	if len(candidates) == 0 {
		// Known, but nothing derives it: either it is underived or it was
		// borrowed whole. Both mean the same thing to a learner.
		parts := []Part{{Form: key, Kind: KindRoot}}
		if inflection != "" {
			parts = []Part{{Form: base, Kind: KindRoot}, {Form: "-" + inflection, Kind: KindSuffix}}
		}
		candidates = []Candidate{{Parts: l.glossed(parts)}}
	}
	return Analysis{Known: true, Candidates: candidates}
}

// tableSplit proposes analyses built from the curated table alone.
//
// It exists because Wiktionary's derivation data is thin exactly where English
// is most productive. "biology", "telegraph" and "microphone" are Greek
// compounds that no derivation row records, so the data layer on its own calls
// them underived — while the table has known bio-, tele-, micro-, -logy and
// -graph the whole time. Combining a recorded morpheme with a stem that is
// itself a recorded word or root is arithmetic, not invention, and it is the
// difference between explaining those words and shrugging at them.
//
// It is deliberately conservative: a stem must be at least three letters and
// must be a word the data has seen or a root the table records, which is what
// keeps "naive" from becoming "na · -ive" and "evil" from becoming "e · -vil".
func (l *Lexicon) tableSplit(word string) []Candidate {
	if len(word) < 5 {
		return nil
	}
	var out []Candidate

	for _, prefix := range prefixStems {
		if !strings.HasPrefix(word, prefix) {
			continue
		}
		rest := word[len(prefix):]
		if len(rest) < 3 {
			continue
		}
		if l.isStem(rest) {
			out = append(out, Candidate{Parts: []Part{
				{Form: prefix + "-", Kind: KindPrefix},
				{Form: rest, Kind: KindRoot},
			}})
		}
		for _, suffix := range suffixStems {
			if !strings.HasSuffix(rest, suffix) {
				continue
			}
			for _, stem := range l.validStems(rest, suffix) {
				out = append(out, Candidate{Parts: []Part{
					{Form: prefix + "-", Kind: KindPrefix},
					{Form: stem, Kind: KindRoot},
					{Form: "-" + suffix, Kind: KindSuffix},
				}})
			}
		}
	}

	for _, suffix := range suffixStems {
		if !strings.HasSuffix(word, suffix) {
			continue
		}
		for _, stem := range l.validStems(word, suffix) {
			out = append(out, Candidate{Parts: []Part{
				{Form: stem, Kind: KindRoot},
				{Form: "-" + suffix, Kind: KindSuffix},
			}})
		}
	}
	return out
}

// stemsBefore returns the stems a word could have had before a suffix was
// attached, most likely first.
//
// English respells a final -y as -i before most suffixes: happy becomes
// happiness, beauty becomes beautiful. Stripping mechanically yields "happi",
// so the spelling change has to be undone to find the stem a learner would
// recognise.
func stemsBefore(word, suffix string) []string {
	stem := word[:len(word)-len(suffix)]
	if strings.HasSuffix(stem, "i") {
		return []string{stem[:len(stem)-1] + "y", stem}
	}
	return []string{stem}
}

// validStems returns the stems of word before suffix that the data can vouch
// for, most likely first.
//
// When undoing the y-to-i respelling yields a real stem, the mechanical strip
// is dropped rather than offered as an equally good reading: "happy · -ness" is
// the analysis, and "happi · -ness" is a spelling artefact that happens to sit
// in the lemma list.
func (l *Lexicon) validStems(word, suffix string) []string {
	stems := stemsBefore(word, suffix)
	var out []string
	for _, stem := range stems {
		if len(stem) < 3 || !l.isStem(stem) {
			continue
		}
		out = append(out, stem)
		if len(stems) > 1 && stem == stems[0] {
			break
		}
	}
	return out
}

// isStem reports whether a candidate stem is something the data can vouch for:
// a word the morphology data has seen, or a root the table records. Anything
// else is a guess, and a guess is what this package exists to avoid.
func (l *Lexicon) isStem(stem string) bool {
	if l.known(stem) {
		return true
	}
	return l.affixes[KindRoot+":"+strings.ToLower(stem)] != ""
}

// inflection returns the lemma and the inflectional morpheme of an inflected
// form, e.g. "infusing" -> "infuse", "ing".
func (l *Lexicon) inflection(form string) (string, string, bool) {
	row, ok := l.inflections.first(form)
	if !ok {
		return "", "", false
	}
	fields := strings.Split(row, "\t")
	if len(fields) < 3 {
		return "", "", false
	}
	return fields[1], fields[2], true
}

// known reports whether word is a lemma the data has seen.
func (l *Lexicon) known(word string) bool {
	if _, ok := l.lemmas.first(word); ok {
		return true
	}
	if _, ok := l.derivations.first(word); ok {
		return true
	}
	if _, ok := l.inflections.first(word); ok {
		return true
	}
	return false
}

// chains returns every segmentation of word that the derivation data supports,
// innermost base first.
func (l *Lexicon) chains(word string, depth int, seen map[string]bool) []Candidate {
	if depth >= maxDepth || seen[word] {
		return nil
	}
	seen[word] = true
	defer delete(seen, word)

	rows := l.derivations.all(word)
	if len(rows) == 0 {
		return []Candidate{{Parts: []Part{{Form: word, Kind: KindRoot}}}}
	}

	var out []Candidate
	for _, row := range rows {
		fields := strings.Split(row, "\t")
		if len(fields) < 4 {
			continue
		}
		source, morpheme, kind := fields[1], fields[2], fields[3]
		for _, inner := range l.chains(source, depth+1, seen) {
			parts := append([]Part{}, inner.Parts...)
			if kind == KindPrefix {
				parts = append([]Part{{Form: morpheme + "-", Kind: KindPrefix}}, parts...)
			} else {
				parts = append(parts, Part{Form: "-" + morpheme, Kind: KindSuffix})
			}
			out = append(out, Candidate{Parts: parts})
		}
	}
	return out
}

// glossed fills in every meaning the curated table knows.
func (l *Lexicon) glossed(parts []Part) []Part {
	out := make([]Part, 0, len(parts))
	for _, p := range parts {
		if p.Gloss == "" {
			p.Gloss = l.gloss(p.Form, p.Kind)
		}
		out = append(out, p)
	}
	return out
}

// gloss looks a morpheme up by kind. The table is keyed on the bare form, so a
// prefix written "un-" and a suffix written "-ness" both reduce to their stem.
func (l *Lexicon) gloss(form, kind string) string {
	stem := strings.Trim(form, "-")
	if stem == "" {
		return ""
	}
	return l.affixes[kind+":"+strings.ToLower(stem)]
}

// rank orders candidates so the most useful analysis is first: the one with
// fewest parts, and the order the candidates were built in settles a tie.
//
// That order is deliberate. Table splits are built before derivation chains
// because they combine a recorded morpheme with a stem the data vouches for,
// while a chain is only as good as the chain — Wiktionary derives "curious"
// from the element "curium", and a previous version of this function preferred
// that reading to the correct "cur · -ious" because it penalised a
// three-letter root.
func rank(candidates []Candidate) []Candidate {
	sort.SliceStable(candidates, func(i, j int) bool {
		return score(candidates[i]) < score(candidates[j])
	})

	seen := map[string]bool{}
	out := make([]Candidate, 0, len(candidates))
	for _, c := range candidates {
		key := strings.Join(c.Forms(), "\x00")
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
		if len(out) == maxCandidates {
			break
		}
	}
	return out
}

// score is smaller for better candidates: a split into fewer parts.
func score(c Candidate) int {
	return len(c.Parts) * 2
}

// Stems returns the morphemes of a printed parts line, which is how a stored
// segmentation is compared with what the data would produce today.
//
// It reads the same notation the tool prints — "un- (not) · happy · -ness" —
// and ignores the glosses, because a gloss is the model's wording and the
// question here is only whether the split still agrees.
func Stems(line string) []string {
	body := line
	if idx := strings.Index(body, "⇒"); idx >= 0 {
		body = body[:idx]
	}
	var out []string
	for _, chunk := range strings.Split(body, "·") {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		if open := strings.Index(chunk, "("); open >= 0 {
			chunk = chunk[:open]
		}
		chunk = strings.TrimSpace(strings.Trim(chunk, `"'`))
		chunk = strings.Trim(chunk, "-")
		if chunk != "" {
			out = append(out, strings.ToLower(chunk))
		}
	}
	return out
}

// SameStems reports whether a stored parts line uses the given segmentation.
func SameStems(line string, want []string) bool {
	got := Stems(line)
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != strings.ToLower(strings.Trim(want[i], "-")) {
			return false
		}
	}
	return true
}

// normalize lowercases and trims, matching how words are keyed everywhere else.
func normalize(word string) string {
	return strings.ToLower(strings.TrimSpace(word))
}

// affixStems returns the recorded stems of one kind, longest first, so that
// "inter-" is tried before "in-" and "-ation" before "-ion".
func affixStems(kind string) []string {
	marker := kind + ":"
	var out []string
	for key := range affixTable {
		if strings.HasPrefix(key, marker) {
			out = append(out, strings.TrimPrefix(key, marker))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i]) != len(out[j]) {
			return len(out[i]) > len(out[j])
		}
		return out[i] < out[j]
	})
	return out
}

// prefixStems and suffixStems are the table's inventory, ordered for longest
// match. Single-letter affixes are left out of splitting: they match almost
// anything and explain almost nothing ("a-" in "away" is not the Greek
// privative), and the morphology data covers the real cases.
var (
	prefixStems = withoutShort(affixStems(KindPrefix))
	suffixStems = withoutShort(affixStems(KindSuffix))
)

func withoutShort(stems []string) []string {
	out := make([]string, 0, len(stems))
	for _, stem := range stems {
		if len(stem) >= 2 {
			out = append(out, stem)
		}
	}
	return out
}

// StorePath is a small helper for callers that only have the words file path.
func StorePath(wordsFile string) string {
	return DefaultDir(filepath.Dir(wordsFile))
}

// Exists reports whether a built data layer is present in dir.
func Exists(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "derivations.tsv"))
	return err == nil && info.Size() > 0
}
