package main

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type keyKind int

const (
	keyRune keyKind = iota // printable rune (Rune)
	keyUp
	keyDown
	keyLeft
	keyRight
	keyEnter
	keyEsc
	keyTab
	keyBackTab
	keyBackspace
	keyDelete
	keyPgUp
	keyPgDn
	keyHome
	keyEnd
	keyCtrlC
	keySpace
	keyCtrl // ctrl+letter other than ctrl-c (Rune = lower-case letter)
	keyAlt  // alt+Rune (ESC prefix)
	keyMouse
)

// Mouse buttons as reported by the SGR mouse protocol (xterm 1006). Wheel values are
// offset by 64 so the low bits stay unambiguous.
const (
	mouseLeft      = 0
	mouseWheelUp   = 64
	mouseWheelDown = 65
)

type mouseEvent struct {
	X, Y    int // zero-based cell
	Btn     int
	Release bool
}

type key struct {
	Kind  keyKind
	Rune  rune
	Mouse mouseEvent
}

// escTimeout is how long a lone ESC byte waits for the rest of a sequence before it is a
// plain Escape key.
const escTimeout = 30 * time.Millisecond

// decodeKeys turns raw terminal bytes into keys. A trailing incomplete escape sequence or
// UTF-8 rune is returned as rest so the caller can prepend it to the next read (or, after
// escTimeout, resolve it with flushKeys). Unknown CSI/SS3 sequences are dropped.
func decodeKeys(buf []byte) (keys []key, rest []byte) {
	i := 0
	for i < len(buf) {
		b := buf[i]
		switch {
		case b == 0x1b:
			n, k, ok, complete := decodeEsc(buf[i:])
			if !complete {
				return keys, buf[i:]
			}
			if ok {
				keys = append(keys, k)
			}
			i += n
		case b == '\r' || b == '\n':
			keys = append(keys, key{Kind: keyEnter})
			i++
		case b == '\t':
			keys = append(keys, key{Kind: keyTab})
			i++
		case b == 0x7f || b == 0x08:
			keys = append(keys, key{Kind: keyBackspace})
			i++
		case b == 0x03:
			keys = append(keys, key{Kind: keyCtrlC})
			i++
		case b == ' ':
			keys = append(keys, key{Kind: keySpace})
			i++
		case b < 0x20:
			keys = append(keys, key{Kind: keyCtrl, Rune: rune(b) + 'a' - 1})
			i++
		default:
			if !utf8.FullRune(buf[i:]) {
				return keys, buf[i:]
			}
			r, n := utf8.DecodeRune(buf[i:])
			keys = append(keys, key{Kind: keyRune, Rune: r})
			i += n
		}
	}
	return keys, nil
}

// decodeEsc decodes one sequence starting at buf[0]==ESC. complete=false means more bytes
// are needed; ok=false means a complete but unrecognised sequence to skip.
func decodeEsc(buf []byte) (n int, k key, ok, complete bool) {
	if len(buf) == 1 {
		return 0, key{}, false, false
	}
	switch buf[1] {
	case '[':
		j := 2
		for j < len(buf) && buf[j] >= 0x30 && buf[j] <= 0x3f {
			j++
		}
		for j < len(buf) && buf[j] >= 0x20 && buf[j] <= 0x2f {
			j++
		}
		if j >= len(buf) {
			return 0, key{}, false, false
		}
		if buf[j] < 0x40 || buf[j] > 0x7e { // malformed: drop what we consumed
			return j + 1, key{}, false, true
		}
		k, ok = csiKey(buf[2:j], buf[j])
		return j + 1, k, ok, true
	case 'O':
		if len(buf) < 3 {
			return 0, key{}, false, false
		}
		k, ok = csiKey(nil, buf[2])
		return 3, k, ok, true
	case 0x1b: // ESC ESC: first is a plain Escape
		return 1, key{Kind: keyEsc}, true, true
	default:
		if !utf8.FullRune(buf[1:]) {
			return 0, key{}, false, false
		}
		r, sz := utf8.DecodeRune(buf[1:])
		if r == ' ' || r < 0x20 || r == 0x7f {
			return 1, key{Kind: keyEsc}, true, true // not an Alt+printable: Esc, then the byte
		}
		return 1 + sz, key{Kind: keyAlt, Rune: r}, true, true
	}
}

// csiKey maps a CSI/SS3 final byte (+ numeric params, modifiers ignored) to a key.
func csiKey(params []byte, final byte) (key, bool) {
	switch final {
	case 'A':
		return key{Kind: keyUp}, true
	case 'B':
		return key{Kind: keyDown}, true
	case 'C':
		return key{Kind: keyRight}, true
	case 'D':
		return key{Kind: keyLeft}, true
	case 'H':
		return key{Kind: keyHome}, true
	case 'F':
		return key{Kind: keyEnd}, true
	case 'Z':
		return key{Kind: keyBackTab}, true
	case 'M': // SS3 M = keypad enter; CSI <...M = SGR mouse press
		if len(params) == 0 {
			return key{Kind: keyEnter}, true
		}
		if params[0] == '<' {
			return mouseCSI(params, false)
		}
	case 'm': // SGR mouse release
		if len(params) > 0 && params[0] == '<' {
			return mouseCSI(params, true)
		}
	case '~':
		num := 0
		for _, p := range params {
			if p == ';' {
				break
			}
			if p < '0' || p > '9' {
				return key{}, false
			}
			num = num*10 + int(p-'0')
		}
		switch num {
		case 1, 7:
			return key{Kind: keyHome}, true
		case 4, 8:
			return key{Kind: keyEnd}, true
		case 3:
			return key{Kind: keyDelete}, true
		case 5:
			return key{Kind: keyPgUp}, true
		case 6:
			return key{Kind: keyPgDn}, true
		}
	}
	return key{}, false
}

// mouseCSI parses the SGR mouse payload "<b;x;y". Coordinates are 1-based in the
// protocol and zero-based in a mouseEvent.
func mouseCSI(params []byte, release bool) (key, bool) {
	parts := strings.Split(string(params[1:]), ";")
	if len(parts) != 3 {
		return key{}, false
	}
	var v [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 100000 {
			return key{}, false
		}
		v[i] = n
	}
	if v[1] < 1 || v[2] < 1 {
		return key{}, false
	}
	if v[0]&32 != 0 { // motion report: the app enables clicks and wheel only
		return key{}, false
	}
	btn := v[0] & 3
	if v[0]&64 != 0 {
		btn += 64
	}
	return key{Kind: keyMouse, Mouse: mouseEvent{X: v[1] - 1, Y: v[2] - 1, Btn: btn, Release: release || v[0]&3 == 3}}, true
}

// flushKeys resolves an incomplete tail after escTimeout: a lone ESC is the Escape key,
// the bytes after an abandoned ESC are decoded normally, other fragments are dropped.
func flushKeys(rest []byte) []key {
	if len(rest) == 0 {
		return nil
	}
	if rest[0] != 0x1b {
		return nil
	}
	ks, _ := decodeKeys(rest[1:])
	return append([]key{{Kind: keyEsc}}, ks...)
}

// startKeys reads raw stdin (the terminal must already be in raw mode) and delivers keys
// until ctx ends. A lone ESC is reported after escTimeout so Alt/arrow sequences that
// arrive split across reads still decode.
func startKeys(ctx context.Context) <-chan key {
	out := make(chan key, 32)
	chunks := make(chan []byte, 8)
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				select {
				case chunks <- append([]byte(nil), buf[:n]...):
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				close(chunks)
				return
			}
		}
	}()
	go func() {
		defer close(out)
		send := func(ks []key) bool {
			for _, k := range ks {
				select {
				case out <- k:
				case <-ctx.Done():
					return false
				}
			}
			return true
		}
		var pending []byte
		timer := time.NewTimer(time.Hour)
		timer.Stop()
		for {
			var tick <-chan time.Time
			if len(pending) > 0 {
				timer.Reset(escTimeout)
				tick = timer.C
			}
			select {
			case <-ctx.Done():
				return
			case c, ok := <-chunks:
				if len(pending) > 0 && !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				if !ok {
					return
				}
				ks, rest := decodeKeys(append(pending, c...))
				pending = append([]byte(nil), rest...)
				if !send(ks) {
					return
				}
			case <-tick:
				ks := flushKeys(pending)
				pending = nil
				if !send(ks) {
					return
				}
			}
		}
	}()
	return out
}
