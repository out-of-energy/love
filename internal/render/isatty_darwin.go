//go:build darwin

package render

import (
	"syscall"
	"unsafe"
)

// TIOCGETA asks the kernel for a terminal's attributes. If the descriptor is not
// a terminal the call fails, which is exactly the answer we want.
const tiocgeta = 0x40487413

func isTerminal(fd uintptr) bool {
	var termios syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, tiocgeta, uintptr(unsafe.Pointer(&termios)))
	return errno == 0
}
