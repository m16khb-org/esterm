package main

import (
	"strings"
	"testing"
)

func TestRuneLenCountsWideCells(t *testing.T) {
	for in, want := range map[string]int{
		"esterm": 6,
		"리그":     4,
		"中文":     4,
		"T1":     2,
		"리그 T1":  7,
	} {
		if got := runeLen(in); got != want {
			t.Errorf("runeLen(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestTruncLineKeepsWholeWideRunes(t *testing.T) {
	if got := plainText(truncLine(line{S("리그")}, 3)); got != "리…" {
		t.Errorf("trunc to 3 = %q, want 리…", got)
	}
	if got := plainText(truncLine(line{S("리그")}, 4)); got != "리그" {
		t.Errorf("trunc to 4 = %q, want 리그", got)
	}
	if got := plainText(truncLine(line{S("라이브")}, 5)); got != "라이…" {
		t.Errorf("trunc to 5 = %q, want 라이…", got)
	}
	if got := plainText(truncLine(line{S("T1 vs GEN")}, 4)); got != "T1 …" {
		t.Errorf("narrow trunc = %q", got)
	}
}

func plainText(l line) string {
	var b strings.Builder
	for _, s := range l {
		b.WriteString(s.text)
	}
	return b.String()
}

func TestPadAndTrimUseCells(t *testing.T) {
	if got := padR("리그", 6); got != "리그  " {
		t.Errorf("padR = %q", got)
	}
	if got := padL("리그", 6); got != "  리그" {
		t.Errorf("padL = %q", got)
	}
	if got := padR("리그", 3); got != "리…" {
		t.Errorf("padR narrow = %q", got)
	}
	if got := trim("라이브", 5); got != "라이…" {
		t.Errorf("trim = %q", got)
	}
}

func TestCanvasMarksSecondCellOfWideRune(t *testing.T) {
	c := newCanvas(6, 1)
	c.text(0, 0, line{S("리그")}, 6)
	if c.buf[0][0].ch != '리' || c.buf[0][1].ch != 0 || c.buf[0][2].ch != '그' || c.buf[0][3].ch != 0 {
		t.Fatalf("wide cells: %q %q %q %q", c.buf[0][0].ch, c.buf[0][1].ch, c.buf[0][2].ch, c.buf[0][3].ch)
	}
	// overwriting the second half either clears the whole rune or leaves no damaged half
	c.setCell(1, 0, 'X', "")
	if c.buf[0][0].ch != ' ' || c.buf[0][1].ch != 'X' {
		t.Fatalf("half overwrite: %q %q", c.buf[0][0].ch, c.buf[0][1].ch)
	}
}

func TestRenderWideRunesWithoutNUL(t *testing.T) {
	c := newCanvas(6, 1)
	c.text(0, 0, line{S("리그")}, 6)
	s := canvasString(c)
	if strings.ContainsRune(s, 0) {
		t.Fatal("canvas string contains the wide-rune placeholder")
	}
	if !strings.Contains(s, "리그") {
		t.Fatalf("canvas string = %q", s)
	}
	if out := renderDiff(nil, c); strings.ContainsRune(out, 0) {
		t.Fatal("renderDiff emits the wide-rune placeholder")
	}
}

func TestTabAtUsesWideWidths(t *testing.T) {
	old := appTabs
	defer func() { appTabs = old }()
	appTabs = [3]string{"라이브", "예정", "최근"}
	in := rect{X: 1, Y: 0, W: 20, H: 1}
	for x, want := range map[int]int{1: 0, 6: 0, 7: -1, 8: 1, 12: -1, 13: 2, 16: 2, 17: -1, 18: -1} {
		if got := tabAt(in, x); got != want {
			t.Errorf("tabAt(%d) = %d, want %d", x, got, want)
		}
	}
}
