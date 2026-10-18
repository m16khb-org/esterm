//go:build darwin

package main

import (
	"syscall"
	"unsafe"
)

const (
	ioctlGetTermios = 0x40487413 // TIOCGETA
	ioctlSetTermios = 0x80487414 // TIOCSETA
)

// makeRaw puts fd into raw mode and returns the previous termios to restore.
func makeRaw(fd int) (*syscall.Termios, bool) {
	var old syscall.Termios
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd),
		uintptr(ioctlGetTermios), uintptr(unsafe.Pointer(&old))); e != 0 {
		return nil, false
	}
	raw := old
	raw.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.ISIG
	raw.Iflag &^= syscall.IXON | syscall.ICRNL
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd),
		uintptr(ioctlSetTermios), uintptr(unsafe.Pointer(&raw))); e != 0 {
		return nil, false
	}
	return &old, true
}

func restoreTerm(fd int, old *syscall.Termios) {
	if old == nil {
		return
	}
	syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd),
		uintptr(ioctlSetTermios), uintptr(unsafe.Pointer(old)))
}
