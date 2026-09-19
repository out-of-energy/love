// Phonics: how a word sounds, from a dictionary instead of from a model.
//
// The morphology data fixed `parts`; this fixes the line above it. The failure
// that prompted it is small and instructive:
//
//	profiling → /ˈprəʊ/ · /faɪl/ · /ɪŋ/     ← the model split by letters
//	profiling → /ˈprəʊ/ · /faɪ/ · /lɪŋ/     ← dictionaries split by sound
//
// "profile" is /ˈprəʊ.faɪl/: the /l/ closes the syllable because nothing follows
// it. Add -ing and English hands that /l/ to the new syllable as its onset. A
// model reading the spelling sees "fil" and keeps the /l/ where the letters are,
// which teaches a false letter-to-sound rule — "fil" is /fɪl/ in filter, film
// and filth.
//
// So the sounds come from ipa-dict and the syllable boundaries from a rule
// applied to those sounds, both built by scripts/build_phonics.py. The model
// keeps the explanation; it no longer gets a vote on where a syllable ends.
package lexicon

import (
	"strings"
)

// Pronunciation is the sound layer's answer for one word.
type Pronunciation struct {
	// Word is the headword the line is built around.
	Word string
	// IPA is the full transcription, slashes included, as the dictionary gives it.
	IPA string
	// SoundChunks are the syllables of IPA, each with its own slashes and with
	// the stress mark leading the chunk: ["/ˈprəʊ/", "/faɪ/", "/lɪŋ/"].
	SoundChunks []string
	// SpellingChunks are the orthographic syllables, when a hyphenation source
	// had them: ["pro", "fil", "ing"]. Empty when it did not, and the headword
	// is printed whole instead.
	SpellingChunks []string
	// Source names the dictionary that supplied it, e.g. "uk+moby".
	Source string
}

// Phrase renders the phonics line the tool prints:
//
//	pro·fil·ing → /ˈprəʊ/ · /faɪ/ · /lɪŋ/
//
// The spelling side is only chunked when a hyphenation source supplied the
// chunks; otherwise the whole word is printed, which is honest rather than a
// split this package invented.
func (p Pronunciation) Phrase() string {
	left := p.Word
	if len(p.SpellingChunks) > 0 {
		left = strings.Join(p.SpellingChunks, "·")
	}
	if len(p.SoundChunks) == 0 {
		return left + " → " + p.IPA
	}
	return left + " → " + strings.Join(p.SoundChunks, " · ")
}

// Pronounce returns the dictionary's answer for word.
//
// Only exact matches count. Composing "profiling" from "profile" plus an ending
// would reintroduce the very bug this file exists to remove: the ending moves
// the /l/, and no string concatenation knows that.
func (l *Lexicon) Pronounce(word string) (Pronunciation, bool) {
	if l == nil || !l.phonics.valid() {
		return Pronunciation{}, false
	}
	key := normalize(word)
	row, ok := l.phonics.first(key)
	if !ok {
		return Pronunciation{}, false
	}
	fields := strings.Split(row, "\t")
	if len(fields) < 5 {
		return Pronunciation{}, false
	}
	p := Pronunciation{
		Word:           fields[0],
		IPA:            fields[1],
		SoundChunks:    parseSoundChunks(fields[2]),
		SpellingChunks: parseSpellingChunks(fields[3]),
		Source:         fields[4],
	}
	if p.IPA == "" || len(p.SoundChunks) == 0 {
		return Pronunciation{}, false
	}
	return p, true
}

// PhonicsAvailable reports whether a pronunciation index was found. It is
// independent of Available, which is about segmentations: a store may have one
// data layer and not the other, and each degrades on its own.
func (l *Lexicon) PhonicsAvailable() bool {
	return l != nil && l.phonics.valid()
}

// parseSoundChunks turns "/ˈprəʊ|faɪ|lɪŋ/" into one slashed chunk per syllable.
//
// The index keeps a single pair of slashes around the whole transcription, so
// they have to be moved onto the chunks the line prints separately.
func parseSoundChunks(field string) []string {
	field = strings.TrimSpace(field)
	if field == "" {
		return nil
	}
	raw := strings.Split(field, "|")
	out := make([]string, 0, len(raw))
	for i, chunk := range raw {
		chunk = strings.TrimSpace(chunk)
		if i == 0 {
			chunk = strings.TrimPrefix(chunk, "/")
		}
		if i == len(raw)-1 {
			chunk = strings.TrimSuffix(chunk, "/")
		}
		if chunk == "" {
			continue
		}
		out = append(out, "/"+chunk+"/")
	}
	return out
}

// parseSpellingChunks turns "pro·fil·ing" into its parts.
func parseSpellingChunks(field string) []string {
	field = strings.TrimSpace(field)
	if field == "" {
		return nil
	}
	var out []string
	for _, chunk := range strings.Split(field, "·") {
		if chunk = strings.TrimSpace(chunk); chunk != "" {
			out = append(out, chunk)
		}
	}
	return out
}
