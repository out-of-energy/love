//go:build linux

package render

import (
	"syscall"
	"unsafe"
)

// TCGETS asks the kernel for a terminal's attributes. If the descriptor is not a
// terminal the call fails, which is exactly the answer we want.
const tcgets = 0x5401

func isTerminal(fd uintptr) bool {
	var termios syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, tcgets, uintptr(unsafe.Pointer(&termios)))
	return errno == 0
}
