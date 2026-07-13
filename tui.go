package main

import (
	"syscall"
	"unsafe"
)

// Raw terminal mode for reading single keystrokes (tab/b/s/o/q) without Enter.
// Uses darwin termios ioctls (the user is on macOS). On other platforms
// makeRaw fails gracefully => toggles are simply unavailable, CLI still runs.

const (
	ioctlGetTermios = 0x40487413 // TIOCGETA (darwin)
	ioctlSetTermios = 0x80487414 // TIOCSETA (darwin)
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
