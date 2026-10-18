package main

import (
	"reflect"
	"testing"
)

func TestDecodeKeys(t *testing.T) {
	r := func(c rune) key { return key{Kind: keyRune, Rune: c} }
	k := func(kd keyKind) key { return key{Kind: kd} }
	tests := []struct {
		name string
		in   string
		want []key
		rest string
	}{
		{"csi arrows", "\x1b[A\x1b[B\x1b[C\x1b[D", []key{k(keyUp), k(keyDown), k(keyRight), k(keyLeft)}, ""},
		{"ss3 arrows", "\x1bOA\x1bOB\x1bOC\x1bOD", []key{k(keyUp), k(keyDown), k(keyRight), k(keyLeft)}, ""},
		{"modified arrow", "\x1b[1;5C", []key{k(keyRight)}, ""},
		{"home end csi", "\x1b[H\x1b[F", []key{k(keyHome), k(keyEnd)}, ""},
		{"home end ss3", "\x1bOH\x1bOF", []key{k(keyHome), k(keyEnd)}, ""},
		{"home end tilde", "\x1b[1~\x1b[4~\x1b[7~\x1b[8~", []key{k(keyHome), k(keyEnd), k(keyHome), k(keyEnd)}, ""},
		{"pgup pgdn delete", "\x1b[5~\x1b[6~\x1b[3~", []key{k(keyPgUp), k(keyPgDn), k(keyDelete)}, ""},
		{"shift tab", "\x1b[Z", []key{k(keyBackTab)}, ""},
		{"tab", "\t", []key{k(keyTab)}, ""},
		{"enter", "\r\n", []key{k(keyEnter), k(keyEnter)}, ""},
		{"backspace", "\x7f\x08", []key{k(keyBackspace), k(keyBackspace)}, ""},
		{"ctrl", "\x03\x04", []key{k(keyCtrlC), {Kind: keyCtrl, Rune: 'd'}}, ""},
		{"space and runes", "q 1", []key{r('q'), k(keySpace), r('1')}, ""},
		{"lone esc is incomplete", "\x1b", nil, "\x1b"},
		{"esc then rune is alt", "\x1bx", []key{{Kind: keyAlt, Rune: 'x'}}, ""},
		{"esc esc", "\x1b\x1b", []key{k(keyEsc)}, "\x1b"},
		{"incomplete csi", "a\x1b[1;", []key{r('a')}, "\x1b[1;"},
		{"incomplete ss3", "\x1bO", nil, "\x1bO"},
		{"unknown csi dropped", "\x1b[99ZZ", []key{k(keyBackTab), r('Z')}, ""},
		{"unknown tilde dropped", "\x1b[2~x", []key{r('x')}, ""},
		{"korean", "한글", []key{r('한'), r('글')}, ""},
		{"split utf8", "a\xed\x95", []key{r('a')}, "\xed\x95"},
		{"alt korean", "\x1b한", []key{{Kind: keyAlt, Rune: '한'}}, ""},
		{"sequence then rest", "\x1b[Aj\x1b", []key{k(keyUp), r('j')}, "\x1b"},
	}
	for _, tc := range tests {
		got, rest := decodeKeys([]byte(tc.in))
		if !reflect.DeepEqual(got, tc.want) || string(rest) != tc.rest {
			t.Errorf("%s: got %v rest %q, want %v rest %q", tc.name, got, rest, tc.want, tc.rest)
		}
	}
}

func TestDecodeKeysSplitReads(t *testing.T) {
	_, rest := decodeKeys([]byte("\x1b"))
	got, rest := decodeKeys(append(rest, "[B"...))
	if len(got) != 1 || got[0].Kind != keyDown || len(rest) != 0 {
		t.Fatalf("got %v rest %q", got, rest)
	}
}

func TestFlushKeys(t *testing.T) {
	if got := flushKeys([]byte("\x1b")); !reflect.DeepEqual(got, []key{{Kind: keyEsc}}) {
		t.Errorf("lone esc: %v", got)
	}
	// abandoned CSI: Escape, then the '[' typed as a rune
	if got := flushKeys([]byte("\x1b[")); !reflect.DeepEqual(got, []key{{Kind: keyEsc}, {Kind: keyRune, Rune: '['}}) {
		t.Errorf("esc [: %v", got)
	}
	if got := flushKeys([]byte("\xed\x95")); got != nil {
		t.Errorf("broken utf8 should be dropped: %v", got)
	}
}
