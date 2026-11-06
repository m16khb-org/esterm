//go:build windows

package main

import (
	"os"
	"syscall"
	"unsafe"
)

// The Windows console: escape sequences need virtual terminal processing on the output and,
// for keys and the mouse to arrive as the same sequences a Unix terminal sends, virtual
// terminal input on the input (Windows 10 1809 and later, Windows Terminal).

var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procSetConsoleMode             = kernel32.NewProc("SetConsoleMode")
	procGetConsoleScreenBufferInfo = kernel32.NewProc("GetConsoleScreenBufferInfo")
)

const (
	conProcessedInput  = 0x0001
	conLineInput       = 0x0002
	conEchoInput       = 0x0004
	conQuickEdit       = 0x0040 // mouse selection by the console, which would swallow the clicks
	conExtendedFlags   = 0x0080 // required to change conQuickEdit
	conVTInput         = 0x0200
	conProcessedOutput = 0x0001
	conVTProcessing    = 0x0004
)

func setConsoleMode(h syscall.Handle, mode uint32) bool {
	r, _, _ := procSetConsoleMode.Call(uintptr(h), uintptr(mode))
	return r != 0
}

// Output escape sequences work from the start, for every command.
func init() {
	for _, f := range []*os.File{os.Stdout, os.Stderr} {
		h := syscall.Handle(f.Fd())
		var m uint32
		if syscall.GetConsoleMode(h, &m) == nil {
			setConsoleMode(h, m|conProcessedOutput|conVTProcessing)
		}
	}
}

// termState is the console input mode to restore.
type termState struct{ mode uint32 }

// makeRaw puts the console input fd into raw mode (no line editing, echo or Ctrl+C
// signal, keys as escape sequences) and returns the previous mode to restore.
func makeRaw(fd int) (*termState, bool) {
	h := syscall.Handle(fd)
	var m uint32
	if syscall.GetConsoleMode(h, &m) != nil {
		return nil, false
	}
	raw := m&^(conProcessedInput|conLineInput|conEchoInput|conQuickEdit) | conExtendedFlags | conVTInput
	if !setConsoleMode(h, raw) {
		return nil, false
	}
	return &termState{m}, true
}

func restoreTerm(fd int, old *termState) {
	if old == nil {
		return
	}
	setConsoleMode(syscall.Handle(fd), old.mode)
}

type conCoord struct{ X, Y int16 }

type conScreenBufferInfo struct {
	Size              conCoord
	CursorPosition    conCoord
	Attributes        uint16
	Window            struct{ Left, Top, Right, Bottom int16 }
	MaximumWindowSize conCoord
}

// termSize returns the window size in cells, 80x24 when stdout is not a console.
func termSize() (cols, rows int) {
	var info conScreenBufferInfo
	r, _, _ := procGetConsoleScreenBufferInfo.Call(os.Stdout.Fd(), uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		return 80, 24 // not a console (pipe, file): a sane default
	}
	cols, rows = int(info.Window.Right-info.Window.Left)+1, int(info.Window.Bottom-info.Window.Top)+1
	if cols <= 0 || rows <= 0 {
		return 80, 24
	}
	return cols, rows
}

func isTTY(f *os.File) bool {
	var m uint32
	return syscall.GetConsoleMode(syscall.Handle(f.Fd()), &m) == nil
}

// winsizeCellPx returns 0, 0: the console reports no pixel size.
func winsizeCellPx() (cellW, cellH int) { return 0, 0 }

// ttyQuery returns nil: console input has no timed read, so the terminal is not asked.
// Kitty graphics are then chosen from TERM_PROGRAM (WezTerm) only, with 8x16 cells.
func ttyQuery(query string, reads int) []byte { return nil }
