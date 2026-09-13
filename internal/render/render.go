// Package render prints a dictionary entry in the tool's stable shape.
package render

import (
	"fmt"
	"io"
	"os"
)

// IsTerminal reports whether f is an interactive terminal. Color is only used
// when a person is actually looking at the output.
func IsTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// Record writes the fixed three-part block:
//
//	word /ipa/
//
//	ELI5: ...
//
//	中文：...
//
// Nothing else is printed: no JSON, no file paths, no cache status. A lookup
// should feel like reading a dictionary, not like reading a log.
//
// The fields arrive individually rather than in a struct so that this package
// depends on no storage or scheduling type. What a word is stored as can change
// without touching how it is displayed.
func Record(w io.Writer, word, ipa, eli5, chinese string, color bool) {
	head := word + " " + ipa
	if color {
		head = "\x1b[1m" + head + "\x1b[0m"
	}
	fmt.Fprintf(w, "%s\n\nELI5: %s\n\n中文：%s\n", head, eli5, chinese)
}
