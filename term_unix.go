//go:build darwin || linux

package main

import (
	"os"
	"syscall"
	"unsafe"
)

// Terminal queries over ioctl; raw mode itself is in term_darwin.go and term_linux.go,
// which differ only in the termios request numbers.

type winsize struct{ Row, Col, Xpixel, Ypixel uint16 }

func getWinsize(f *os.File) (winsize, bool) {
	var ws winsize
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws)))
	return ws, e == 0
}

// termSize returns the window size in cells, 80x24 when stdout is not a terminal.
func termSize() (cols, rows int) {
	ws, ok := getWinsize(os.Stdout)
	if !ok || ws.Col == 0 || ws.Row == 0 {
		return 80, 24 // not a terminal (pipe, file): a sane default
	}
	return int(ws.Col), int(ws.Row)
}

func isTTY(f *os.File) bool {
	_, ok := getWinsize(f)
	return ok
}
