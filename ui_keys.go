package main

import (
	"context"
	"os"
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
)

type key struct {
	Kind keyKind
	Rune rune
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
	switch c := buf[1]; {
	case c == '[':
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
	case c == 'O':
		if len(buf) < 3 {
			return 0, key{}, false, false
		}
		k, ok = csiKey(nil, buf[2])
		return 3, k, ok, true
	case c == 0x1b: // ESC ESC: first is a plain Escape
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
	case 'M': // SS3 M = keypad enter
		if len(params) == 0 {
			return key{Kind: keyEnter}, true
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
