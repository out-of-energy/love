// Package render prints a dictionary entry in the tool's stable shape.
package render

import (
	"fmt"
	"io"
)

// Anchor is the layer of a word that is read in one glance: how it sounds, how
// it is built, and what it means.
//
// It is a struct rather than a list of parameters because these fields are
// positional neighbours and the list keeps growing — a seventh string argument
// would be an invitation to pass the etymology where the explanation belongs.
type Anchor struct {
	Word    string
	IPA     string
	Phonics string
	Parts   string
	ELI5    string
}

// Record writes the fixed block:
//
//	word /ipa/
//	Phonics: ...
//	Parts: ...
//	ELI5: ...
//
// Nothing else is printed: no JSON, no file paths, no cache status, and no
// Chinese gloss. The terminal is where recall is tested, and the gloss is the
// answer to that test; it is still stored, and the daily email still carries it,
// because reading on a phone is not the same act as remembering at a desk.
//
// A word that predates the form layer prints without those two lines rather
// than with an empty label. An upgraded file is honest about what it holds, and
// `love --backfill` (or the next daily run) is what fills the gap.
//
// The fields arrive in a struct rather than one by one so that this package
// still depends on no storage or scheduling type: what a word is stored as can
// change without touching how it is displayed.
func Record(w io.Writer, a Anchor, color bool) {
	head := a.Word + " " + a.IPA
	if color {
		head = "\x1b[1m" + head + "\x1b[0m"
	}
	fmt.Fprintln(w, head)
	if a.Phonics != "" {
		fmt.Fprintf(w, "Phonics: %s\n", a.Phonics)
	}
	if a.Parts != "" {
		fmt.Fprintf(w, "Parts: %s\n", a.Parts)
	}
	fmt.Fprintf(w, "ELI5: %s\n", a.ELI5)
}
