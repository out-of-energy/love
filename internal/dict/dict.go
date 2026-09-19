// Package dict asks a model for the anchor layer of a word: its IPA, how it is
// sounded out, how it is built from a prefix, a root and a suffix, a
// deliberately child-simple English explanation, and one common Chinese
// meaning.
//
// This is the layer a learner reads in a single glance during review, and it is
// written once and never regenerated. The expansion layer — example sentences
// and dialogue — lives in package ai, because it answers a different question
// (how do I use this word?) at a different moment (after recall succeeds).
package dict

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/out-of-energy/love/internal/llm"
)

// Defaults are re-exported so callers need only one import for configuration.
const (
	DefaultBaseURL = llm.DefaultBaseURL
	DefaultModel   = llm.DefaultModel
)

// ErrBadResponse and APIError are re-exported for the same reason.
var ErrBadResponse = llm.ErrBadResponse

// APIError is a non-2xx response from the API.
type APIError = llm.APIError

// systemPrompt encodes the product's whole editorial stance: one word, one
// meaning, explained so simply that a small child would follow it — plus the
// two form lines that make the word pronounceable and its construction visible.
const systemPrompt = `You are a concise English word learning assistant.

The user sends one English word or short phrase. Reply with ONLY a JSON object
containing exactly these six string fields:

{"word":"...","ipa":"...","phonics":"...","parts":"...","eli5":"...","chinese":"..."}

Rules:
- "word": the input word or phrase, lowercased, otherwise unchanged.
- "ipa": a standard IPA transcription wrapped in slashes, e.g. /ˈkjʊəriəs/.
- "phonics": the word split into syllables or sound chunks for sounding out,
  with each chunk's pronunciation. Give the chunked spelling first, then the
  arrow, then the sounds joined by the middle dot, e.g.
  ex·pose → /ɪk/ · /ˈspəʊz/
  Split by sound and not by letter, and keep it to one to four chunks.
- "parts": the word split into its prefix, root and suffix with each part's
  original meaning, joined by the middle dot, then the literal sense in double
  quotes, e.g.
  ex- (out) · pos (put) · -e ⇒ "put out"
  The literal sense sits inside the JSON string, so its quotes are escaped.
  Only split what is really there, and never invent an etymology to fill the
  line: a word with no clear parts says so and names its origin — no clear
  prefix or suffix (borrowed whole from French) — and a compound is marked as
  one, e.g. blind · side · -ing (compound word).
- "eli5": explain the meaning in VERY simple English that a three-year-old could
  understand. Use common words and short sentences. Write "very, very big",
  never "extremely large in scale". Never use dictionary or academic wording.
- "chinese": the single most common Chinese meaning, a few characters. No part
  of speech, no numbered senses, no alternatives.
Output the JSON object only. No markdown, no code fences, no commentary.`

// Entry is the anchor layer for one word.
//
// It is deliberately independent of any storage or scheduling type: this
// package talks to a model and nothing else, so a change to the word file's
// schema can never ripple in here.
type Entry struct {
	Word    string
	IPA     string
	Phonics string
	Parts   string
	ELI5    string
	Chinese string
}

// Segment is one morpheme of an analysis supplied by the data layer.
type Segment struct {
	// Form is written as a learner reads it: "un-", "happy", "-ness".
	Form string
	// Kind is "prefix", "suffix" or "root".
	Kind string
	// Gloss is the meaning the curated table already knows, empty otherwise.
	Gloss string
}

// Sound is a pronunciation supplied by the data layer.
//
// It is separate from Segments because the two are repaired separately: a
// pronunciation dictionary can rebuild the sound line with no request at all,
// while a segmentation can only be re-derived by asking.
type Sound struct {
	// IPA is the full transcription, slashes included.
	IPA string
	// SoundChunks are the syllables of IPA, each with its own slashes and with
	// the stress mark leading the chunk.
	SoundChunks []string
	// SpellingChunks are the orthographic syllables, empty when the data had
	// none.
	SpellingChunks []string
	// Source names the dictionary, e.g. "uk+moby".
	Source string
}

// Options carries what the morphology data knows about the word.
//
// The zero value means "no data", which is the behaviour this tool had before
// the data layer existed: the model works the word out on its own. Anything
// else narrows the question, and narrowing it is the entire point. A model
// asked to split a word invents something plausible; a model asked to choose
// between two recorded analyses and gloss them has no room to invent a third.
type Options struct {
	// Segments are real analyses, best first. A single one-part analysis means
	// the data records no classical split for this word.
	Segments [][]Segment
	// Known reports that the word was found in the morphology data at all.
	Known bool
	// Source names where the analyses came from, and is what the caller
	// records when the model agrees with one. It defaults to "morphology".
	Source string
	// Sound is the recorded pronunciation, when a dictionary has one. A nil
	// Sound means the model writes the two sound lines as before.
	Sound *Sound
}

// ApplySound returns the ipa and phonics lines the record should keep, and where
// they came from.
//
// When the data has a pronunciation it wins outright, and unlike a segmentation
// there is nothing to salvage from the model's version. The failure this guards
// against is not a matter of wording: a syllable boundary in the wrong place
// teaches a letter-to-sound rule that is false — the model read "profiling" as
// /faɪl/ · /ɪŋ/ because the letters put the l there, while English puts that /l/
// in the next syllable's onset.
func (o Options) ApplySound(word, ipa, phonics string) (string, string, string) {
	if o.Sound == nil {
		return ipa, phonics, "model"
	}
	return o.Sound.IPA, renderSound(word, *o.Sound, phonics), "dictionary"
}

// renderSound builds the printed line:
//
//	pro·fil·ing → /ˈprəʊ/ · /faɪ/ · /lɪŋ/
//
// The orthographic side is the one part of the old line worth keeping. It is a
// statement about spelling rather than about sound, the model's version of it
// was never the problem, and a dictionary index without hyphenation data would
// otherwise have to print the word whole. So it is carried over whenever it
// already has one chunk per syllable — and dropped when it does not, because a
// spelling split that disagrees with the sound split is worse than none.
func renderSound(word string, s Sound, previous string) string {
	left := word
	switch {
	case len(s.SpellingChunks) > 0:
		left = strings.Join(s.SpellingChunks, "·")
	default:
		if side, ok := spellingSide(previous); ok && chunkCount(side) == len(s.SoundChunks) {
			left = side
		}
	}
	if len(s.SoundChunks) == 0 {
		return left + " → " + s.IPA
	}
	return left + " → " + strings.Join(s.SoundChunks, " · ")
}

// spellingSide returns the part of a phonics line before the arrow.
func spellingSide(phonics string) (string, bool) {
	idx := strings.Index(phonics, "→")
	if idx < 0 {
		return "", false
	}
	side := strings.TrimSpace(phonics[:idx])
	return side, side != ""
}

// chunkCount counts the chunks in a "pro·fil·ing" spelling side.
func chunkCount(side string) int {
	if strings.TrimSpace(side) == "" {
		return 0
	}
	return strings.Count(side, "·") + 1
}

// choice returns the analyses rendered for the prompt.
func (o Options) choice() []string {
	rendered := make([]string, 0, len(o.Segments))
	for _, segments := range o.Segments {
		parts := make([]string, 0, len(segments))
		for _, s := range segments {
			parts = append(parts, s.Form)
		}
		rendered = append(rendered, strings.Join(parts, " · "))
	}
	return rendered
}

// Validate reports whether the model returned everything we asked for, and
// whether its answer is one the data allows.
func (e Entry) Validate() error {
	switch {
	case e.Word == "":
		return fmt.Errorf("missing word")
	case e.IPA == "":
		return fmt.Errorf("missing ipa")
	case e.Phonics == "":
		return fmt.Errorf("missing phonics")
	case e.Parts == "":
		return fmt.Errorf("missing parts")
	case e.ELI5 == "":
		return fmt.Errorf("missing eli5")
	case e.Chinese == "":
		return fmt.Errorf("missing chinese")
	}
	return nil
}

// Matches reports whether the model's parts line uses one of the analyses the
// data layer offered.
func (o Options) Matches(parts string) bool {
	if len(o.Segments) == 0 {
		return true
	}
	got, _ := parseParts(partsBefore(parts, "⇒"))
	for _, segments := range o.Segments {
		if equalStems(got, stemsOf(segments)) {
			return true
		}
	}
	return false
}

// Apply returns the parts line the data layer will accept, together with where
// it came from.
//
// The data decides the segmentation; the model is kept for what it added —
// the glosses of free roots and the literal sense. When the model used a
// recorded analysis, its wording survives and only the morpheme marks are
// normalised. When it drifted, the best recorded analysis is used instead and
// the model's wording for a different split is dropped, because a gloss that
// belongs to another segmentation is worse than no gloss.
//
// Sources are "model" (no data), "morphology" (the model agreed) and
// "morphology-forced" (it did not).
func (o Options) Apply(parts string) (string, string) {
	if len(o.Segments) == 0 {
		return parts, "model"
	}

	body, literal := partsBefore(parts, "⇒"), literalOf(parts)
	// The model is allowed to reject every recorded analysis. It is the only
	// participant that can see that "sym" in "symlink" is not the Greek prefix
	// but a clipping of "symbolic", and a learner is better served by "no clear
	// prefix or suffix" than by a neat, wrong story.
	if declined(body) {
		return strings.TrimSpace(parts), "morphology-declined"
	}
	// A compound is visible in the word itself — "bomb" plus "shell" needs no
	// data layer to be true — so a model that spots one keeps it even when the
	// data records no derivation. Without this the whole-word analysis the data
	// offers would overwrite a correct answer with a shrug.
	if compound(body) {
		return strings.TrimSpace(parts), "morphology-compound"
	}
	got, glosses := parseParts(body)
	for _, candidate := range o.Segments {
		if equalStems(got, stemsOf(candidate)) {
			return renderParts(candidate, glosses, literal), o.source()
		}
	}
	return renderParts(o.Segments[0], nil, ""), "morphology-forced"
}

// source names the origin of an analysis the model agreed with.
func (o Options) source() string {
	if o.Source != "" {
		return o.Source
	}
	return "morphology"
}

// declined reports whether the model answered that the word has no parts.
func declined(body string) bool {
	return strings.Contains(strings.ToLower(body), "no clear")
}

// compound reports whether the model offered a compound reading.
func compound(body string) bool {
	return strings.Contains(strings.ToLower(body), "compound")
}

// literalOf returns the quoted literal sense, marker included, if present.
func literalOf(parts string) string {
	idx := strings.Index(parts, "⇒")
	if idx < 0 {
		return ""
	}
	return strings.TrimSpace(parts[idx+len("⇒"):])
}

// renderParts rebuilds a parts line from a recorded analysis, preferring the
// model's gloss for each position and falling back to the curated table.
func renderParts(segments []Segment, modelGlosses []string, literal string) string {
	rendered := make([]string, 0, len(segments))
	for i, segment := range segments {
		gloss := ""
		if i < len(modelGlosses) {
			gloss = modelGlosses[i]
		}
		if gloss == "" {
			gloss = segment.Gloss
		}
		if gloss == "" {
			rendered = append(rendered, segment.Form)
			continue
		}
		rendered = append(rendered, segment.Form+" ("+gloss+")")
	}
	out := strings.Join(rendered, " · ")
	if literal != "" {
		out += " ⇒ " + literal
	}
	return out
}

// stemsOf returns the bare morphemes of a recorded analysis.
func stemsOf(segments []Segment) []string {
	out := make([]string, 0, len(segments))
	for _, s := range segments {
		out = append(out, strings.ToLower(strings.Trim(s.Form, "-")))
	}
	return out
}

// parseParts splits a rendered parts fragment into bare morphemes and the
// glosses the model wrote for them.
func parseParts(fragment string) ([]string, []string) {
	var forms, glosses []string
	for _, chunk := range strings.Split(fragment, "·") {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		gloss := ""
		if open := strings.Index(chunk, "("); open >= 0 {
			if close := strings.LastIndex(chunk, ")"); close > open {
				gloss = strings.TrimSpace(chunk[open+1 : close])
			}
			chunk = chunk[:open]
		}
		chunk = strings.TrimSpace(strings.Trim(chunk, `"'`))
		chunk = strings.Trim(chunk, "-")
		if chunk == "" {
			continue
		}
		forms = append(forms, strings.ToLower(chunk))
		glosses = append(glosses, gloss)
	}
	return forms, glosses
}

// partsBefore returns the part of a parts line that lists the morphemes, i.e.
// everything before the literal sense.
func partsBefore(parts, marker string) string {
	if idx := strings.Index(parts, marker); idx >= 0 {
		return parts[:idx]
	}
	return parts
}

func equalStems(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Client looks words up.
type Client struct{ llm *llm.Client }

// NewClient builds a lookup client with a per-request timeout.
func NewClient(apiKey, baseURL, model string, timeout time.Duration) *Client {
	return &Client{llm: llm.NewClient(apiKey, baseURL, model, timeout)}
}

// Generate asks the model for the anchor layer of word, constrained by whatever
// the morphology data knows.
//
// word must already be the canonical normalized key; it is echoed into the
// result rather than taken from the model, so the model cannot invent a second
// key for a word the caller has already identified.
//
// The caller is expected to pass the result through Options.Apply, which is
// where the recorded segmentation is enforced. Enforcement lives there rather
// than here because drifting from an instruction is not a reason to throw away
// an otherwise good answer: the glosses and the literal sense are still worth
// keeping, and only the split is not.
func (c *Client) Generate(ctx context.Context, word string, opts Options) (Entry, error) {
	var entry Entry
	err := c.llm.Complete(ctx, llm.Request{
		System: systemPromptFor(opts),
		User:   word,
		Out:    &entry,
		Validate: func() error {
			entry.Word = word
			return entry.Validate()
		},
	})
	if err != nil {
		return Entry{}, err
	}
	return entry, nil
}

// systemPromptFor returns the base prompt plus the block that says what the data
// layer knows, if anything.
func systemPromptFor(opts Options) string {
	block := opts.constraint()
	if block == "" {
		return systemPrompt
	}
	return systemPrompt + "\n\n" + block
}

// constraint renders the data layer's knowledge as prompt text.
func (o Options) constraint() string {
	if len(o.Segments) == 0 {
		return ""
	}

	choices := o.choice()
	if len(choices) == 1 && len(o.Segments[0]) == 1 {
		// The word is known, and no derivation of it is recorded. That is not
		// the same as "the model may split it however it likes".
		return `Morphology data: this word is recorded, and no prefix, root or suffix
derivation of it is recorded either. It is underived, or it was borrowed whole.
So "parts" must be one of:
  - the whole word with its meaning, e.g. ` + o.Segments[0][0].Form + ` (what it means) ⇒ "<the literal sense>"
  - a compound of two ordinary English words, marked as such, e.g.
    blind · side · -ing (compound word) ⇒ "..."
Do not invent a prefix, root or suffix for it.`
	}

	var b strings.Builder
	b.WriteString("Morphology data: this word's real segmentations are on record. Choose exactly\n")
	b.WriteString("one of the following and use its parts, in that order, and do not invent any\n")
	b.WriteString("other part:\n")
	for i, choice := range choices {
		fmt.Fprintf(&b, "  %d. %s\n", i+1, choice)
	}
	b.WriteString(`Write "parts" in the usual notation, e.g.
  un- (not) · predict (say in advance) · -able (can be) ⇒ "can be predicted"
Any part whose meaning is given above keeps it; supply a short meaning for the
rest. The literal sense is the parts' meanings put together.
If none of those analyses is right for this word, write exactly this for "parts"
and nothing else: no clear prefix or suffix`)
	return b.String()
}
