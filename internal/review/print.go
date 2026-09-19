package review

import (
	"fmt"
	"io"

	"github.com/out-of-energy/love/internal/ai"
	"github.com/out-of-energy/love/internal/render"
	"github.com/out-of-energy/love/internal/storage"
)

// writeAnchor prints the first layer using the same block the dictionary
// prints, so a word looks identical wherever it appears.
//
// The Chinese gloss is not part of that block: review is where a word is
// recalled, and the gloss is the answer. It stays in the file, and it stays in
// the daily email, which is read away from the desk.
func writeAnchor(out io.Writer, w storage.Word, color bool) {
	render.Record(out, render.Anchor{
		Word:    w.Word,
		IPA:     w.IPA,
		Phonics: w.Phonics,
		Parts:   w.Parts,
		ELI5:    w.ELI5,
	}, color)
}

// writeExpansion prints the second layer: the fuller meaning, the scene, and
// the material that shows the word being used.
func writeExpansion(out io.Writer, c ai.Content) {
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  %s\n", c.Meaning)
	if c.Scene != "" {
		fmt.Fprintf(out, "  %s\n", c.Scene)
	}
	if len(c.Examples) > 0 {
		fmt.Fprintln(out, "  例句")
		for _, example := range c.Examples {
			fmt.Fprintf(out, "    · %s\n", example)
		}
	}
	if len(c.Dialogue) > 0 {
		fmt.Fprintln(out, "  对话")
		for _, line := range c.Dialogue {
			fmt.Fprintf(out, "    %s  %s\n", line.Speaker, line.Line)
		}
	}
	fmt.Fprintln(out)
}
