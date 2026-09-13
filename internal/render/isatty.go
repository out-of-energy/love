package render

import "os"

// IsTerminal reports whether f is an interactive terminal.
//
// A character-device check is not enough, and that is a bug worth stating
// plainly: /dev/null is a character device too. A command run as
// `love --review </dev/null`, or with its output sent to /dev/null, would
// believe it had a terminal — the review session would start and then read
// nothing, and colored output would be emitted into a file. The only reliable
// test is the terminal ioctl, which fails for /dev/null and for every other
// non-terminal character device.
func IsTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	return isTerminal(f.Fd())
}
