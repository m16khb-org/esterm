package main

import (
	"strings"
	"testing"
)

func rowText(c *canvas, y int) string {
	var b strings.Builder
	for _, ce := range c.buf[y] {
		b.WriteRune(ce.ch)
	}
	return b.String()
}

func TestDrawPaneExact(t *testing.T) {
	c := newCanvas(20, 5)
	in := drawPane(c, rect{0, 0, 20, 5}, pane{Num: 2, Title: "Matches", Footer: "1 of 3"})
	if in != (rect{1, 1, 18, 3}) {
		t.Fatalf("inner %v", in)
	}
	if got := rowText(c, 0); got != "╭─ [2] Matches ────╮" {
		t.Errorf("top %q", got)
	}
	if got := rowText(c, 2); got != "│"+strings.Repeat(" ", 18)+"│" {
		t.Errorf("mid %q", got)
	}
	if got := rowText(c, 4); got != "╰"+strings.Repeat("─", 9)+" 1 of 3 ─╯" {
		t.Errorf("bottom %q", got)
	}
	for y := 0; y < 5; y++ {
		if n := len([]rune(rowText(c, y))); n != 20 {
			t.Errorf("row %d width %d", y, n)
		}
	}
}

func TestDrawPaneFocusStyle(t *testing.T) {
	c := newCanvas(10, 4)
	drawPane(c, rect{0, 0, 10, 4}, pane{Title: "x", Focused: true})
	if c.buf[0][0].sty != paneFocusSty || c.buf[2][0].sty != paneFocusSty {
		t.Errorf("focused border style %q", c.buf[0][0].sty)
	}
	if !strings.Contains(c.buf[0][3].sty, cBold) {
		t.Errorf("focused title not bold: %q", c.buf[0][3].sty)
	}
	d := newCanvas(10, 4)
	drawPane(d, rect{0, 0, 10, 4}, pane{Title: "x"})
	if d.buf[0][0].sty == paneFocusSty {
		t.Error("unfocused border must differ")
	}
}

func TestDrawPaneTinyAndOffset(t *testing.T) {
	c := newCanvas(10, 6)
	drawPane(c, rect{3, 2, 2, 2}, pane{Title: "long title"})
	if rowText(c, 2) != "   ╭╮     " || rowText(c, 3) != "   ╰╯     " {
		t.Errorf("2x2: %q %q", rowText(c, 2), rowText(c, 3))
	}
	if in := drawPane(c, rect{0, 0, 1, 1}, pane{}); !in.empty() {
		t.Errorf("1x1 inner %v", in)
	}
	// long title truncated inside the border
	d := newCanvas(12, 3)
	drawPane(d, rect{0, 0, 12, 3}, pane{Num: 1, Title: "very long title here"})
	if got := rowText(d, 0); len([]rune(got)) != 12 || !strings.HasSuffix(got, "─╮") || !strings.Contains(got, "…") {
		t.Errorf("title %q", got)
	}
}

func items(n int) []listItem {
	var it []listItem
	for i := 0; i < n; i++ {
		it = append(it, listItem{Line: line{S("item" + itoa(i))}})
	}
	return it
}

func TestListScrollKeepsSelectionVisible(t *testing.T) {
	l := &listView{Items: items(30)}
	c := newCanvas(20, 6)
	r := rect{0, 0, 20, 5}
	for _, step := range []struct{ f func() }{
		{func() { l.Move(1) }}, {func() { l.Move(7) }}, {func() { l.Page(1, 5) }},
		{func() { l.End() }}, {func() { l.Move(-3) }}, {func() { l.Page(-1, 5) }},
		{func() { l.Home() }}, {func() { l.Move(-5) }}, {func() { l.Move(100) }},
	} {
		step.f()
		l.Draw(c, r, true)
		if l.Sel < 0 || l.Sel >= 30 {
			t.Fatalf("sel out of range %d", l.Sel)
		}
		if l.Sel < l.Top || l.Sel >= l.Top+5 {
			t.Fatalf("sel %d outside window top %d", l.Sel, l.Top)
		}
		if l.Top < 0 || l.Top > 25 {
			t.Fatalf("top %d", l.Top)
		}
		if !strings.Contains(rowText(c, l.Sel-l.Top), "item"+itoa(l.Sel)) {
			t.Fatalf("selected row not drawn: %q", rowText(c, l.Sel-l.Top))
		}
	}
	if l.Sel != 29 {
		t.Errorf("after Move(100) sel %d", l.Sel)
	}
}

func TestListDrawStyleScrollAndEmpty(t *testing.T) {
	l := &listView{Items: items(10), Sel: 2}
	c := newCanvas(13, 4)
	l.Draw(c, rect{0, 0, 12, 4}, true)
	if c.buf[2-l.Top][0].sty != listSelFocused || c.buf[2-l.Top][10].sty != listSelFocused {
		t.Error("selection bar must span the text width")
	}
	l.Draw(c, rect{0, 0, 12, 4}, false)
	if c.buf[2-l.Top][0].sty != listSelBlurred {
		t.Error("unfocused selection style")
	}
	if c.buf[0][12].ch != '█' && c.buf[1][12].ch != '█' {
		t.Error("scroll thumb missing")
	}
	short := &listView{Items: items(2)}
	d := newCanvas(13, 4)
	short.Draw(d, rect{0, 0, 12, 4}, true)
	if d.buf[0][12].ch != ' ' {
		t.Error("no scroll indicator when list fits")
	}
	empty := &listView{}
	empty.Draw(d, rect{0, 0, 12, 4}, true)
	empty.Move(3)
	empty.End()
	if empty.Sel != 0 {
		t.Errorf("empty sel %d", empty.Sel)
	}
	l.Items[1].Line = line{S("a very long item that must be truncated")}
	l.Draw(c, rect{0, 0, 12, 4}, true)
	if got := rowText(c, 1-l.Top); !strings.Contains(got, "…") {
		t.Errorf("no ellipsis: %q", got)
	}
}

func TestDrawHints(t *testing.T) {
	hs := []hint{{"q", "quit"}, {"?", "help"}, {"enter", "open"}}
	c := newCanvas(40, 2)
	drawHints(c, 1, 40, hs)
	if got := strings.TrimRight(rowText(c, 1), " "); got != "q quit  ? help  enter open" {
		t.Errorf("full %q", got)
	}
	d := newCanvas(20, 1)
	drawHints(d, 0, 20, hs)
	got := rowText(d, 0)
	if !strings.HasPrefix(got, "q quit  ? help") || !strings.Contains(got, "…") || strings.Contains(got, "enter") {
		t.Errorf("truncated %q", got)
	}
	if n := len([]rune(got)); n != 20 {
		t.Errorf("width %d", n)
	}
	e := newCanvas(6, 1)
	drawHints(e, 0, 6, []hint{{"enter", "open selected"}})
	if got := rowText(e, 0); got != "enter…" {
		t.Errorf("single long %q", got)
	}
	drawHints(e, 0, 0, hs) // no panic
}

func TestDrawPopupCenteredAndClipped(t *testing.T) {
	c := newCanvas(40, 12)
	in := drawPopup(c, rect{0, 0, 40, 12}, "Help", []line{{S("q  quit")}, {S("? help")}})
	// width = 7+4 = 11, height = 4
	if in.W != 9 || in.H != 2 || in.X != 15 || in.Y != 5 {
		t.Fatalf("inner %v", in)
	}
	if c.buf[4][14].ch != '╭' || c.buf[7][24].ch != '╯' {
		t.Errorf("corners %q %q", c.buf[4][14].ch, c.buf[7][24].ch)
	}
	d := newCanvas(10, 5)
	var body []line
	for i := 0; i < 10; i++ {
		body = append(body, line{S("a long body line")})
	}
	in = drawPopup(d, rect{0, 0, 10, 5}, "T", body)
	if in != (rect{1, 1, 8, 3}) {
		t.Fatalf("clipped inner %v", in)
	}
	if got := rowText(d, 1); !strings.Contains(got, "…") || len([]rune(got)) != 10 {
		t.Errorf("clipped line %q", got)
	}
}

func TestMenuNavigationAndDraw(t *testing.T) {
	m := &menu{Title: "Options", Items: []menuItem{
		{"Minimap", "auto", "drawing mode"}, {"Video", "on", "sync"}, {"Poll", "2s", ""}}}
	m.Move(-1)
	if m.Sel != 2 {
		t.Fatalf("wrap up: %d", m.Sel)
	}
	m.Move(1)
	m.Move(1)
	if m.Sel != 1 {
		t.Fatalf("wrap down: %d", m.Sel)
	}
	m.Move(-7)
	if m.Sel != 0 {
		t.Fatalf("big step: %d", m.Sel)
	}
	c := newCanvas(50, 14)
	m.Draw(c, rect{0, 0, 50, 14})
	var all string
	for y := 0; y < c.h; y++ {
		all += rowText(c, y) + "\n"
	}
	for _, s := range []string{"Options", "▸ Minimap  auto", "  Video    on", "drawing mode"} {
		if !strings.Contains(all, s) {
			t.Errorf("missing %q in\n%s", s, all)
		}
	}
	(&menu{}).Move(1) // empty menu: no panic
}

func TestBlitFillText(t *testing.T) {
	c := newCanvas(10, 4)
	src := newCanvas(5, 3)
	src.fill(rect{0, 0, 5, 3}, 'x', "")
	c.blit(src, rect{8, 2, 5, 3}) // clipped right/bottom
	if rowText(c, 2) != "        xx" || rowText(c, 3) != "        xx" || rowText(c, 1) != "          " {
		t.Errorf("blit clip: %q %q", rowText(c, 2), rowText(c, 3))
	}
	c.blit(src, rect{-3, -1, 2, 2}) // clipped left/top, limited by r.W/H
	if rowText(c, 0) != "          " {
		t.Errorf("blit neg: %q", rowText(c, 0))
	}
	c.fill(rect{-2, -2, 4, 3}, '#', "")
	if rowText(c, 0) != "##        " {
		t.Errorf("fill: %q", rowText(c, 0))
	}
	c.text(3, 0, line{S("hello"), C("world", cRed)}, 7)
	if rowText(c, 3)[:7] != "hellowo" || c.buf[3][5].sty != cRed || c.buf[3][7].ch == 'r' {
		t.Errorf("text: %q", rowText(c, 3))
	}
	c.text(9, 0, line{S("x")}, 3) // off-canvas row: no panic
}
