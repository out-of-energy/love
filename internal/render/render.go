// Package render prints a record in the tool's stable human-readable shape.
package render

import (
	"fmt"
	"io"
	"os"

	"github.com/kk/love/internal/cache"
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
func Record(w io.Writer, rec cache.Record, color bool) {
	head := rec.Word + " " + rec.IPA
	if color {
		head = "\x1b[1m" + head + "\x1b[0m"
	}
	fmt.Fprintf(w, "%s\n\nELI5: %s\n\n中文：%s\n", head, rec.ELI5, rec.Chinese)
}
