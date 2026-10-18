package main

import (
	"fmt"
	"strings"
)

type rect struct{ X, Y, W, H int }

func (r rect) empty() bool { return r.W <= 0 || r.H <= 0 }

// ---------------- canvas helpers ----------------

// blit copies src into c with src's top-left at r.X,r.Y, clipped to r and to c.
func (c *canvas) blit(src *canvas, r rect) {
	for y := 0; y < src.h && y < r.H; y++ {
		cy := r.Y + y
		if cy < 0 || cy >= c.h {
			continue
		}
		for x := 0; x < src.w && x < r.W; x++ {
			cx := r.X + x
			if cx < 0 || cx >= c.w {
				continue
			}
			c.buf[cy][cx] = src.buf[y][x]
		}
	}
}

// fill paints r (clipped) with ch in style sty.
func (c *canvas) fill(r rect, ch rune, sty string) {
	for y := r.Y; y < r.Y+r.H; y++ {
		if y < 0 || y >= c.h {
			continue
		}
		for x := r.X; x < r.X+r.W; x++ {
			if x >= 0 && x < c.w {
				c.buf[y][x] = cell{ch: ch, sty: sty}
			}
		}
	}
}

// textStyled is text with base prepended to every segment's own colour.
func (c *canvas) textStyled(row, col int, l line, maxW int, base string) {
	if row < 0 || row >= c.h {
		return
	}
	n := 0
	for _, s := range l {
		for _, ch := range s.text {
			if n >= maxW {
				return
			}
			if x := col + n; x >= 0 && x < c.w {
				c.buf[row][x] = cell{ch: ch, sty: base + s.color}
			}
			n++
		}
	}
}

// text draws l at row,col, writing at most maxW cells (clipped at the canvas edge too).
func (c *canvas) text(row, col int, l line, maxW int) { c.textStyled(row, col, l, maxW, "") }

// truncLine cuts l to at most w cells, ending in "…" when something was dropped.
func truncLine(l line, w int) line {
	if w <= 0 {
		return nil
	}
	if l.vis() <= w {
		return l
	}
	var out line
	left := w - 1
	for _, s := range l {
		r := []rune(s.text)
		if len(r) >= left {
			out = append(out, seg{text: string(r[:left]), color: s.color})
			left = 0
			break
		}
		out = append(out, s)
		left -= len(r)
	}
	return append(out, S("…"))
}

// ---------------- pane ----------------

type pane struct {
	Num           int
	Title, Footer string
	Focused       bool
}

const paneFocusSty = cBold + cGreen

// drawPane draws a rounded border (focused = bold green, lazygit style) with "[Num]-Title"
// in the top edge and Footer at the bottom right, and returns the inner rect.
func drawPane(c *canvas, r rect, p pane) rect {
	if r.W < 2 || r.H < 2 {
		return rect{X: r.X, Y: r.Y}
	}
	bs, ts := cGray, ""
	if p.Focused {
		bs, ts = paneFocusSty, cBold
	}
	c.fill(rect{r.X, r.Y, r.W, 1}, '─', bs)
	c.fill(rect{r.X, r.Y + r.H - 1, r.W, 1}, '─', bs)
	c.fill(rect{r.X, r.Y, 1, r.H}, '│', bs)
	c.fill(rect{r.X + r.W - 1, r.Y, 1, r.H}, '│', bs)
	c.fill(rect{r.X, r.Y, 1, 1}, '╭', bs)
	c.fill(rect{r.X + r.W - 1, r.Y, 1, 1}, '╮', bs)
	c.fill(rect{r.X, r.Y + r.H - 1, 1, 1}, '╰', bs)
	c.fill(rect{r.X + r.W - 1, r.Y + r.H - 1, 1, 1}, '╯', bs)
	c.fill(rect{r.X + 1, r.Y + 1, r.W - 2, r.H - 2}, ' ', "")

	if p.Title != "" || p.Num > 0 {
		t := p.Title
		if p.Num > 0 {
			t = "[" + itoa(p.Num) + "] " + t
		}
		t = " " + strings.TrimRight(t, " ") + " "
		c.textStyled(r.Y, r.X+2, truncLine(line{S(t)}, r.W-4), r.W-4, ts)
	}
	if p.Footer != "" {
		f := " " + p.Footer + " "
		if w := runeLen(f); w <= r.W-4 {
			c.text(r.Y+r.H-1, r.X+r.W-2-w, line{C(f, bs)}, w)
		} else {
			c.text(r.Y+r.H-1, r.X+2, truncLine(line{C(f, bs)}, r.W-4), r.W-4)
		}
	}
	return rect{r.X + 1, r.Y + 1, r.W - 2, r.H - 2}
}

// ---------------- list ----------------

type listItem struct {
	Line line
	Dim  bool
}

type listView struct {
	Items    []listItem
	Sel, Top int
}

func (l *listView) clamp() {
	if l.Sel >= len(l.Items) {
		l.Sel = len(l.Items) - 1
	}
	if l.Sel < 0 {
		l.Sel = 0
	}
}

func (l *listView) Move(n int) { l.Sel += n; l.clamp() }
func (l *listView) Home()      { l.Sel = 0 }
func (l *listView) End()       { l.Sel = len(l.Items) - 1; l.clamp() }

// Page moves the selection by n pages of height h.
func (l *listView) Page(n, h int) {
	if h < 1 {
		h = 1
	}
	l.Move(n * h)
}

// follow scrolls Top so the selection is inside a window of h rows.
func (l *listView) follow(h int) {
	l.clamp()
	if h < 1 {
		return
	}
	if l.Sel < l.Top {
		l.Top = l.Sel
	}
	if l.Sel >= l.Top+h {
		l.Top = l.Sel - h + 1
	}
	if max := len(l.Items) - h; l.Top > max {
		l.Top = max
	}
	if l.Top < 0 {
		l.Top = 0
	}
}

const listSelFocused = "\033[44m\033[97m"
const listSelBlurred = "\033[7m"

// Draw renders the visible window into r: a full-width selection bar (blue when the pane
// is focused, reverse video otherwise). When the list overflows, a thumb ('█') replaces
// the cells of the pane border just right of r, lazygit style.
func (l *listView) Draw(c *canvas, r rect, focused bool) {
	if r.empty() {
		return
	}
	l.follow(r.H)
	for i := 0; i < r.H; i++ {
		idx := l.Top + i
		if idx >= len(l.Items) {
			break
		}
		it := l.Items[idx]
		base := ""
		if it.Dim {
			base = cDim
		}
		if idx == l.Sel {
			base = listSelBlurred
			if focused {
				base = listSelFocused
			}
			c.fill(rect{r.X, r.Y + i, r.W, 1}, ' ', base)
		}
		c.textStyled(r.Y+i, r.X, truncLine(it.Line, r.W), r.W, base)
	}
	if n := len(l.Items); n > r.H && r.X+r.W < c.w {
		size := max(1, r.H*r.H/n)
		start := l.Top * (r.H - size) / (n - r.H)
		for i := start; i < start+size && i < r.H; i++ {
			c.buf[r.Y+i][r.X+r.W] = cell{ch: '█'}
		}
	}
}

// ---------------- hints ----------------

type hint struct{ Key, Label string }

// drawHints draws "key label  key label" on row, lazygit style (key in green, label
// default), dropping whole hints that no longer fit and ending in "…" when any were cut.
func drawHints(c *canvas, row, w int, hs []hint) {
	if w <= 0 {
		return
	}
	c.fill(rect{0, row, w, 1}, ' ', "")
	x := 0
	for i, h := range hs {
		sep := 0
		if i > 0 {
			sep = 2
		}
		need := sep + runeLen(h.Key) + 1 + runeLen(h.Label)
		more := i < len(hs)-1
		reserve := 0
		if more {
			reserve = 2 // room for " …"
		}
		if x+need+reserve > w {
			if x+2 <= w && x > 0 {
				c.text(row, x, line{C(" …", cGray)}, w-x)
			} else if x == 0 {
				c.text(row, 0, truncLine(line{C(h.Key, cGreen), S(" " + h.Label)}, w), w)
			}
			return
		}
		x += sep
		c.text(row, x, line{C(h.Key, cGreen), S(" " + h.Label)}, w-x)
		x += need - sep
	}
}

// drawHintBar draws the context hints on the left and the fixed ones (help, quit, back)
// right-aligned: when the row is too narrow the context hints are cut first, then the
// fixed ones from the front, so the last fixed hint always survives.
func drawHintBar(c *canvas, row, w int, ctx, fixed []hint) {
	if w <= 0 {
		return
	}
	width := func(hs []hint) int {
		n := 0
		for i, h := range hs {
			if i > 0 {
				n += 2
			}
			n += runeLen(h.Key) + 1 + runeLen(h.Label)
		}
		return n
	}
	for len(fixed) > 1 && width(fixed) > w {
		fixed = fixed[1:]
	}
	fw := width(fixed)
	if fw > w {
		fw = w
	}
	room := w - fw
	if len(ctx) > 0 && fw > 0 {
		room -= 2 // gap between the two groups
	}
	drawHints(c, row, max(room, 0), ctx)
	x := w - fw
	for i, h := range fixed {
		if i > 0 {
			x += 2
		}
		c.text(row, x, line{C(h.Key, cGreen), S(" " + h.Label)}, w-x)
		x += runeLen(h.Key) + 1 + runeLen(h.Label)
	}
}

// tooSmallCanvas is the whole screen when it is under the minimum size: a centred notice
// at the real size, shortened to what fits.
func tooSmallCanvas(w, h, needW, needH int) *canvas {
	c := newCanvas(w, h)
	msg := fmt.Sprintf("terminal too small (need ≥ %d×%d)", needW, needH)
	for _, m := range []string{msg, "too small", "small"} {
		if runeLen(m) <= w {
			msg = m
			break
		}
	}
	l := truncLine(line{C(msg, cYel)}, w)
	c.text(max(h-1, 0)/2, max(w-l.vis(), 0)/2, l, w)
	return c
}

// ---------------- popup / menu ----------------

// drawPopup draws a centred modal sized to its body (title included), clipped to screen,
// and returns the inner rect. The frame uses the focused pane style.
func drawPopup(c *canvas, screen rect, title string, body []line) rect {
	w := runeLen(title) + 4
	for _, l := range body {
		if v := l.vis() + 4; v > w {
			w = v
		}
	}
	h := len(body) + 2
	if w > screen.W {
		w = screen.W
	}
	if h > screen.H {
		h = screen.H
	}
	r := rect{screen.X + (screen.W-w)/2, screen.Y + (screen.H-h)/2, w, h}
	in := drawPane(c, r, pane{Title: title, Focused: true})
	for i := 0; i < in.H && i < len(body); i++ {
		c.text(in.Y+i, in.X+1, truncLine(body[i], in.W-2), in.W-2)
	}
	return in
}

type menuItem struct{ Label, Value, Desc string }

type menu struct {
	Title string
	Items []menuItem
	Sel   int
}

// Move steps the selection by n, wrapping around.
func (m *menu) Move(n int) {
	if k := len(m.Items); k > 0 {
		m.Sel = ((m.Sel+n)%k + k) % k
	}
}

// Draw renders the picker as a popup: one row per item ("▸ Label   Value"), the selected
// row highlighted, and the selected item's Desc dim under a rule.
func (m *menu) Draw(c *canvas, screen rect) {
	lw := 0
	for _, it := range m.Items {
		if v := runeLen(it.Label); v > lw {
			lw = v
		}
	}
	var body []line
	for i, it := range m.Items {
		mark, sty := "  ", ""
		if i == m.Sel {
			mark, sty = "▸ ", cBold+cGreen
		}
		l := line{C(mark+padR(it.Label, lw), sty)}
		if it.Value != "" {
			l = append(l, C("  "+it.Value, cCyan))
		}
		body = append(body, l)
	}
	if m.Sel >= 0 && m.Sel < len(m.Items) && m.Items[m.Sel].Desc != "" {
		body = append(body, nil, line{C(m.Items[m.Sel].Desc, cGray)})
	}
	drawPopup(c, screen, m.Title, body)
}
