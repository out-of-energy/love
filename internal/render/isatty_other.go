//go:build !darwin && !linux

package render

import "os"

// isTerminal is a best-effort fallback for platforms whose terminal ioctl is not
// defined here. It is the character-device heuristic, which is wrong for
// /dev/null, so on these platforms a redirected review session may start and
// then read nothing.
func isTerminal(fd uintptr) bool {
	f := os.NewFile(fd, "tty")
	if f == nil {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
