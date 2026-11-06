//go:build darwin || linux

package main

import (
	"bytes"
	"fmt"
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

// winsizeCellPx returns the cell size from the tty's pixel size, or 0, 0 when it reports none.
func winsizeCellPx() (cellW, cellH int) {
	ws, ok := getWinsize(os.Stdout)
	if !ok || ws.Col == 0 || ws.Row == 0 || ws.Xpixel == 0 || ws.Ypixel == 0 {
		return 0, 0
	}
	return max(int(ws.Xpixel)/int(ws.Col), 1), max(int(ws.Ypixel)/int(ws.Row), 1)
}

// ttyQuery writes query to the terminal and returns what comes back in raw mode: it reads
// until a DA1 reply (CSI ? ... c, which the query must end with) or until `reads` reads of
// 100 ms found none. Nil when stdin cannot be put in raw mode.
func ttyQuery(query string, reads int) []byte {
	fd := int(os.Stdin.Fd())
	var old syscall.Termios
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(ioctlGetTermios), uintptr(unsafe.Pointer(&old))); e != 0 {
		return nil
	}
	raw := old
	raw.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.ISIG
	raw.Cc[syscall.VMIN] = 0
	raw.Cc[syscall.VTIME] = 1 // 100 ms per read
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(ioctlSetTermios), uintptr(unsafe.Pointer(&raw))); e != 0 {
		return nil
	}
	defer syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(ioctlSetTermios), uintptr(unsafe.Pointer(&old)))
	fmt.Print(query)
	var got []byte
	buf := make([]byte, 256)
	for i := 0; i < reads; i++ {
		n, _ := syscall.Read(fd, buf)
		got = append(got, buf[:max(n, 0)]...)
		if bytes.Contains(got, []byte("\x1b[?")) && bytes.HasSuffix(got, []byte("c")) {
			break
		}
	}
	return got
}
