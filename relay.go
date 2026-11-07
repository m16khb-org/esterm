package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// ---------------- ANSI ----------------

const (
	cReset  = "\033[0m"
	cBold   = "\033[1m"
	cDim    = "\033[2m"
	cRed    = "\033[31m"
	cGreen  = "\033[32m"
	cYel    = "\033[33m"
	cBlue   = "\033[34m"
	cPurple = "\033[35m"
	cCyan   = "\033[36m"
	cGray   = "\033[90m"
)

// The smallest screen the app and the boards draw on; below it they show a notice at the
// real size instead of a squeezed or wrapped layout.
const (
	minTermW  = 40
	minTermH  = 12
	minBoardW = 50 // the standalone relay/replay boards
)

// ---------------- line/segment model (logical content) ----------------

type seg struct{ text, color string }
type line []seg

func S(s string) seg    { return seg{text: s} }
func C(s, c string) seg { return seg{text: s, color: c} }

func (l line) vis() int {
	w := 0
	for _, s := range l {
		w += runeLen(s.text)
	}
	return w
}

func joinLR(l, r line, width int) line {
	gap := width - l.vis() - r.vis()
	if gap < 1 {
		gap = 1
	}
	out := append(line{}, l...)
	out = append(out, S(strings.Repeat(" ", gap)))
	out = append(out, r...)
	return out
}
func joinLCR(l, c, r line, width int) line {
	remain := width - c.vis()
	if remain < 1 {
		remain = 1
	}
	lgap := remain/2 - l.vis()
	rgap := remain - remain/2 - r.vis()
	if lgap < 1 {
		lgap = 1
	}
	if rgap < 1 {
		rgap = 1
	}
	out := append(line{}, l...)
	out = append(out, S(strings.Repeat(" ", lgap)))
	out = append(out, c...)
	out = append(out, S(strings.Repeat(" ", rgap)))
	out = append(out, r...)
	return out
}
func padLineTo(l line, width int) line {
	p := width - l.vis()
	if p <= 0 {
		return l
	}
	out := append(line{}, l...)
	out = append(out, S(strings.Repeat(" ", p)))
	return out
}

// ---------------- cell grid (framebuffer) + diff renderer ----------------

type cell struct {
	ch  rune
	sty string // SGR prefix (e.g. "\033[1m\033[31m") or "" for plain
}
type canvas struct {
	w, h int
	buf  [][]cell
}

// newCanvas allocates the rows from one flat array: a full board builds three canvases per
// tick, and h+1 allocations were most of the garbage. The row slices are capped so an
// append can never spill into the next row.
func newCanvas(w, h int) *canvas {
	flat := make([]cell, w*h)
	for i := range flat {
		flat[i].ch = ' '
	}
	buf := make([][]cell, h)
	for i := range buf {
		buf[i] = flat[i*w : (i+1)*w : (i+1)*w]
	}
	return &canvas{w: w, h: h, buf: buf}
}

func (c *canvas) drawLine(row int, l line) {
	if row < 0 || row >= c.h {
		return
	}
	c.text(row, 0, l, c.w)
}

func linesToCanvas(rows []line, w int) *canvas {
	c := newCanvas(w, len(rows))
	for r, l := range rows {
		c.drawLine(r, l)
	}
	return c
}

// writeStyle switches the terminal from style *cur ("" = default) to sty. SGR attributes
// accumulate, so leaving a styled cell starts from a reset instead of leaking bold/colour.
func writeStyle(b *strings.Builder, cur *string, sty string) {
	if sty == *cur {
		return
	}
	if *cur != "" {
		b.WriteString(cReset)
	}
	b.WriteString(sty)
	*cur = sty
}

// renderDiff emits ONLY the cells that changed since prev.
// prev==nil or any size change => full repaint (clear + draw). Otherwise a
// cell-by-cell diff with cursor addressing (the "SPA" minimal-update path).
func renderDiff(prev, cur *canvas) string {
	var b strings.Builder
	ls := "\x00" // sentinel: terminal style unknown, forces the first style emit
	emit := func(ce cell) {
		if ce.ch == 0 { // right half of a wide rune: the terminal already moved past it
			return
		}
		writeStyle(&b, &ls, ce.sty)
		b.WriteRune(ce.ch)
	}

	// full repaint
	if prev == nil || prev.w != cur.w || prev.h != cur.h {
		b.WriteString("\033[H\033[2J")
		for r := 0; r < cur.h; r++ {
			fmt.Fprintf(&b, "\033[%d;1H", r+1)
			for c := 0; c < cur.w; c++ {
				emit(cur.buf[r][c])
			}
		}
		return b.String()
	}

	// diff
	rows := cur.h
	if prev.h > rows {
		rows = prev.h
	}
	for r := 0; r < rows; r++ {
		if r >= cur.h {
			fmt.Fprintf(&b, "\033[%d;1H\033[2K", r+1) // row vanished: clear it
			continue
		}
		crow := cur.buf[r]
		if r >= prev.h {
			fmt.Fprintf(&b, "\033[%d;1H", r+1) // new row: write whole
			for c := 0; c < cur.w; c++ {
				emit(crow[c])
			}
			b.WriteString("\033[K")
			continue
		}
		prow := prev.buf[r]
		inRun := false
		for c := 0; c < cur.w; c++ {
			if prow[c] != crow[c] {
				if !inRun {
					fmt.Fprintf(&b, "\033[%d;%dH", r+1, c+1)
					inRun = true
				}
				emit(crow[c])
			} else {
				inRun = false
			}
		}
	}
	return b.String()
}

// canvasString renders the whole canvas to a plain string (for demo/screenshot).
func canvasString(c *canvas) string {
	var b strings.Builder
	ls := ""
	for r := 0; r < c.h; r++ {
		for col := 0; col < c.w; col++ {
			ce := c.buf[r][col]
			if ce.ch == 0 {
				continue
			}
			writeStyle(&b, &ls, ce.sty)
			b.WriteRune(ce.ch)
		}
		b.WriteString(cReset + "\n")
		ls = ""
	}
	return b.String()
}

// ---------------- helpers ----------------

// runeWidth is the number of terminal cells a rune occupies. The UI is laid out in
// cells, so Korean and Chinese labels (two cells per rune) must count double or every
// border and mouse target after them drifts.
func runeWidth(r rune) int {
	if r == 0 {
		return 0
	}
	if r >= 0x1100 {
		switch {
		case r <= 0x115f, // Hangul Jamo
			r >= 0x2e80 && r <= 0xa4cf, // CJK radicals, Kangxi, CJK, Yi
			r >= 0xac00 && r <= 0xd7a3, // Hangul syllables
			r >= 0xf900 && r <= 0xfaff, // CJK compatibility ideographs
			r >= 0xfe10 && r <= 0xfe19,
			r >= 0xfe30 && r <= 0xfe6f,
			r >= 0xff00 && r <= 0xff60, // fullwidth forms
			r >= 0xffe0 && r <= 0xffe6,
			r >= 0x1f300 && r <= 0x1faff, // emoji and pictographs
			r >= 0x20000 && r <= 0x3fffd:
			return 2
		}
	}
	return 1
}

// runeLen is the display width of s in terminal cells.
func runeLen(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

// cutWidth returns the longest prefix of s that fits in n cells.
func cutWidth(s string, n int) string {
	var b strings.Builder
	left := n
	for _, r := range s {
		w := runeWidth(r)
		if w > left {
			break
		}
		b.WriteRune(r)
		left -= w
	}
	return b.String()
}

func padR(s string, n int) string {
	if runeLen(s) > n {
		if n <= 1 {
			return cutWidth(s, n)
		}
		return cutWidth(s, n-1) + "…"
	}
	return s + strings.Repeat(" ", n-runeLen(s))
}
func padL(s string, n int) string {
	if runeLen(s) > n {
		return cutWidth(s, n)
	}
	return strings.Repeat(" ", n-runeLen(s)) + s
}
func trim(s string, n int) string {
	if runeLen(s) <= n {
		return s
	}
	if n <= 1 {
		return cutWidth(s, n)
	}
	return cutWidth(s, n-1) + "…"
}

func barSegs(left, right, width int) []seg {
	total := left + right
	if total == 0 {
		return []seg{C(strings.Repeat("·", width), cGray)}
	}
	thr := int(math.Round(float64(left) / float64(total) * float64(width)))
	if thr < 0 {
		thr = 0
	}
	if thr > width {
		thr = width
	}
	out := []seg{}
	if thr > 0 {
		out = append(out, C(strings.Repeat("█", thr), cCyan))
	}
	if width-thr > 0 {
		out = append(out, C(strings.Repeat("█", width-thr), cRed))
	}
	return out
}

func hpSeg(cur, max int) seg {
	const blocks = 8
	if max <= 0 {
		return C(strings.Repeat("░", blocks), cGray)
	}
	frac := float64(cur) / float64(max)
	n := int(math.Round(frac * blocks))
	if n < 0 {
		n = 0
	}
	if n > blocks {
		n = blocks
	}
	col := cGreen
	if frac < 0.25 {
		col = cRed
	} else if frac < 0.6 {
		col = cYel
	}
	return C(strings.Repeat("█", n)+strings.Repeat("░", blocks-n), col)
}

// deadSeg is the HP cell of a dead champion: the respawn countdown instead of the bar. A
// countdown that is the death timer's estimate, not the feed's own respawn, starts with ~.
// It is at most 7 cells wide, so it fits the 8-cell bar it replaces.
func deadSeg(lf champLife) seg {
	est := "~"
	if lf.Exact {
		est = ""
	}
	return C(fmt.Sprintf("☠ %s%ds", est, int(math.Ceil(lf.Secs))), cRed)
}

func shortPatch(p string) string {
	parts := strings.SplitN(p, ".", 3)
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}
	return p
}

type pmeta struct{ name, champ string }

func metaMap(m teamMeta) map[int]pmeta {
	out := make(map[int]pmeta, len(m.Participants))
	for _, p := range m.Participants {
		out[p.ParticipantID] = pmeta{p.SummonerName, p.ChampionID}
	}
	return out
}

// ---------------- column layout (width-responsive) ----------------

type cols struct{ name, champ, kda, cs, gold, hpbar, hpnum int }

func pickCols(budget int) cols {
	c := cols{name: 14, champ: 10, kda: 7, cs: 4, gold: 6, hpbar: 8, hpnum: 5}
	if budget < 24 {
		budget = 24
	}
	w := func() int {
		sum, n := 0, 0
		for _, v := range []int{c.name, c.champ, c.kda, c.cs, c.gold, c.hpbar, c.hpnum} {
			if v > 0 {
				sum += v
				n++
			}
		}
		return sum + max(0, n-1)
	}
	if w() > budget {
		c.hpnum = 0
	}
	if w() > budget {
		c.cs = 0
	}
	if w() > budget {
		c.champ = 7
	}
	if w() > budget {
		c.name = 11
	}
	if w() > budget {
		c.champ = 0
	}
	if w() > budget {
		c.hpbar = 0
	}
	if w() > budget {
		c.gold = 5
	}
	if w() > budget {
		c.name = 8
	}
	return c
}

type pfield struct {
	text, color string
	w           int
	left        bool
}

func playerRow(p participant, m pmeta, c cols, tc string, lives champLives) line {
	fs := []pfield{}
	if c.name > 0 {
		fs = append(fs, pfield{m.name, tc, c.name, true})
	}
	if c.champ > 0 {
		fs = append(fs, pfield{m.champ, cDim, c.champ, true})
	}
	if c.kda > 0 {
		fs = append(fs, pfield{fmt.Sprintf("%d/%d/%d", p.Kills, p.Deaths, p.Assists), "", c.kda, false})
	}
	if c.cs > 0 {
		fs = append(fs, pfield{fmt.Sprintf("%d", p.CreepScore), "", c.cs, false})
	}
	if c.gold > 0 {
		fs = append(fs, pfield{gold(p.TotalGold), "", c.gold, false})
	}
	if c.hpbar > 0 {
		hp := hpSeg(p.CurrentHealth, p.MaxHealth)
		if lf := lives[p.ParticipantID]; lf.Dead {
			hp = deadSeg(lf)
		}
		fs = append(fs, pfield{hp.text, hp.color, c.hpbar, false})
	}
	if c.hpnum > 0 {
		fs = append(fs, pfield{fmt.Sprintf("%d", p.CurrentHealth), "", c.hpnum, false})
	}
	return buildFields(fs)
}
func playerHeader(c cols) line {
	fs := []pfield{}
	if c.name > 0 {
		fs = append(fs, pfield{tr("player"), cDim, c.name, true})
	}
	if c.champ > 0 {
		fs = append(fs, pfield{tr("champion"), cDim, c.champ, true})
	}
	if c.kda > 0 {
		fs = append(fs, pfield{"K/D/A", cDim, c.kda, false})
	}
	if c.cs > 0 {
		fs = append(fs, pfield{"CS", cDim, c.cs, false})
	}
	if c.gold > 0 {
		fs = append(fs, pfield{tr("gold"), cDim, c.gold, false})
	}
	if c.hpbar > 0 {
		fs = append(fs, pfield{"HP", cDim, c.hpbar, false})
	}
	if c.hpnum > 0 {
		fs = append(fs, pfield{"#", cDim, c.hpnum, false})
	}
	return buildFields(fs)
}
func buildFields(fs []pfield) line {
	var l line
	for i, x := range fs {
		if i > 0 {
			l = append(l, S(" "))
		}
		t := x.text
		if x.left {
			t = padR(t, x.w)
		} else {
			t = padL(t, x.w)
		}
		l = append(l, C(t, x.color))
	}
	return l
}

// ---------------- bordering (return full-width lines) ----------------

func topBorderL(t int) line { return line{S("╔" + strings.Repeat("═", t-2) + "╗")} }
func botBorderL(t int) line { return line{S("╚" + strings.Repeat("═", t-2) + "╝")} }
func blankL(t int) line     { return borderedL(line{}, t) }

// borderedL frames content in the board's side borders and pads it to t cells. Content that
// would overflow is cut with "…", so no row can ever lose its right border.
func borderedL(content line, t int) line {
	content = truncLine(content, t-4)
	out := line{S("║ ")}
	out = append(out, content...)
	if pad := (t - 4) - content.vis(); pad > 0 {
		out = append(out, S(strings.Repeat(" ", pad)))
	}
	out = append(out, S(" ║"))
	return out
}
func sideLine(lp, rp line, side, t int) line {
	content := padLineTo(lp, side)
	content = append(content, S(" "), C("│", cGray), S(" "))
	content = append(content, rp...)
	return borderedL(content, t)
}

func statBarLine(label string, leftVal, rightVal int, leftDisp, rightDisp string, w int) line {
	L := line{C(label, cBold), S("  "), C(leftDisp, cCyan), S(" ")}
	R := line{S(" "), C(rightDisp, cRed)}
	bw := w - L.vis() - R.vis()
	if bw < 6 {
		bw = 6
	}
	mid := line(barSegs(leftVal, rightVal, bw))
	out := append(line{}, L...)
	out = append(out, mid...)
	out = append(out, R...)
	return out
}

// panelHint is the one line a toggled-on panel gets when it has no room: how many rows it
// still needs. A panel that disappears silently reads as a broken key.
func panelHint(name string, need, have int) line {
	n := need - have
	key := "panel_needs_rows"
	if n == 1 {
		key = "panel_needs_row"
	}
	return line{C(fmt.Sprintf(tr(key), name, n), cDim)}
}

// objItem is one objective count: "DRG 2 ★SOUL ── 4 ★SOUL". The soul badges are decoration
// and go first when the row runs out of width; the numbers never do.
func objItem(label string, lv, rv int, soulL, soulR string, badges bool) line {
	if !badges {
		soulL, soulR = "", ""
	}
	return line{C(label, cDim), S(" "), C(fmt.Sprintf("%d%s", lv, soulL), cCyan),
		S(" ── "), C(fmt.Sprintf("%d%s", rv, soulR), cRed)}
}

// objectivesRow fits the objective counts greedily at item boundaries: a count is dropped
// whole (soul badges first) instead of being cut in the middle of a number.
func objectivesRow(b, r teamStats, t int) line {
	type o struct {
		label  string
		lv, rv int
		soulL  string
		soulR  string
	}
	all := []o{
		{"DRG", len(b.Dragons), len(r.Dragons), soulStr(b.Dragons), soulStr(r.Dragons)},
		{"BRN", b.Barons, r.Barons, "", ""},
		{"TWR", b.Towers, r.Towers, "", ""},
		{"INB", b.Inhibitors, r.Inhibitors, "", ""},
	}
	W := t - 4
	var l line
	for _, o := range all {
		sep := 0
		if len(l) > 0 {
			sep = 5
		}
		it := objItem(o.label, o.lv, o.rv, o.soulL, o.soulR, true)
		if l.vis()+sep+it.vis() > W {
			it = objItem(o.label, o.lv, o.rv, o.soulL, o.soulR, false)
		}
		if len(l) > 0 && l.vis()+sep+it.vis() > W {
			break
		}
		if sep > 0 {
			l = append(l, S(strings.Repeat(" ", sep)))
		}
		l = append(l, it...)
	}
	return l
}

// ---------------- frame -> canvas ----------------

// buildCanvas draws the board at t×h. Embedded boards (tg.embed) live inside the app's own
// pane: they lose the outer double border and the footer and use the whole area.
func buildCanvas(ev event, w windowResp, det detailsResp, game time.Duration, now time.Time, t, h int, interval time.Duration, tg toggles, series []goldPt, bv boardView) *canvas {
	if !tg.embed {
		return buildFramed(ev, w, det, game, now, t, h, interval, tg, series, bv)
	}
	hh := h
	if hh > 0 {
		hh += 2
	}
	fc := buildFramed(ev, w, det, game, now, t+2, hh, interval, tg, series, bv)
	if fc.h < 2 || fc.w < 3 {
		return fc
	}
	c := newCanvas(t, fc.h-2)
	for y := range c.buf {
		copy(c.buf[y], fc.buf[y+1][1:fc.w-1])
	}
	return c
}

func buildFramed(ev event, w windowResp, det detailsResp, game time.Duration, now time.Time, t, h int, interval time.Duration, tg toggles, series []goldPt, bv boardView) *canvas {
	W := t - 4
	compact := h > 0 && h <= 28
	wide := t >= 104
	pulse := now.Second()%2 == 0

	var rows []line
	rows = append(rows, topBorderL(t))
	bl := func() {
		if !compact {
			rows = append(rows, blankL(t))
		}
	}

	if ev.ID == "" {
		rows = append(rows, blankL(t))
		rows = append(rows, borderedL(joinLR(line{}, line{C("○  NO LIVE GAMES RIGHT NOW", cBold+cYel)}, W), t))
		bl()
		rows = append(rows, borderedL(joinLR(line{}, line{C("polling for a match to start…", cDim)}, W), t))
		rows = append(rows, borderedL(joinLR(line{}, line{C("try: lolterm lol schedule", cDim)}, W), t))
		rows = append(rows, blankL(t))
		return finishCanvas(rows, t, h, now, compact, interval, tg)
	}

	dot := C("●", cDim)
	if pulse {
		dot = C("●", cBold+cRed)
	}
	clk := "—:—"
	if game >= 0 {
		clk = fmt.Sprintf("%d:%02d", int(game.Minutes()), int(game.Seconds())%60)
	}
	state := C("LIVE", cBold+cRed)
	if tg.replay != nil {
		dot, state = C("▶", cBold+cPurple), C("REPLAY", cBold+cPurple)
		if tg.replay.Paused {
			dot = C("⏸", cBold+cYel)
		}
	}
	hdL := line{dot, S(" "), state, S("  "),
		C(strings.ToUpper(ev.League.Slug), cBold),
		S(" · " + or(ev.BlockName, "Match"))}
	hdR := line{C("◷", cGray), S(" " + clk)}
	if t >= 88 {
		hdR = append(hdR, C("  patch "+shortPatch(w.GameMetadata.PatchVersion), cDim))
	}
	// The block name truncates: the clock and the right border always survive.
	rows = append(rows, borderedL(joinLR(truncLine(hdL, max(1, W-hdR.vis()-1)), hdR, W), t))
	bl()

	lname, rname := "TBD", "TBD"
	lwins, rwins := "0", "0"
	if tg.replay != nil {
		lwins, rwins = "-", "-" // a replay knows the series score only when the final game is shown
	}
	if ev.Match != nil && len(ev.Match.Teams) >= 2 {
		lt, rt := ev.Match.Teams[0], ev.Match.Teams[1]
		// getLive lists the series' teams in a fixed order; the game decides who is blue.
		// Kills, gold and the players below are blue left, so the names must be too.
		if blue := w.GameMetadata.BlueTeamMetadata.EsportsTeamID; blue != "" && rt.ID == blue {
			lt, rt = rt, lt
		}
		lname = or(lt.Name, or(lt.Code, "TBD"))
		rname = or(rt.Name, or(rt.Code, "TBD"))
		if lt.Result != nil {
			lwins = itoa(lt.Result.GameWins)
		}
		if rt.Result != nil {
			rwins = itoa(rt.Result.GameWins)
		}
	}
	gameNo, totalGames := 0, 0
	if ev.Match != nil {
		totalGames = len(ev.Match.Games)
		for _, g := range ev.Match.Games {
			if g.State != "unstarted" {
				gameNo++
			}
		}
	}
	nameW := 18
	if t < 100 {
		nameW = 14
	}
	if t < 84 {
		nameW = 11
	}
	if t < 70 {
		nameW = 8
	}
	bL := line{C(trim(lname, nameW), cBold+cCyan), S(" "), C("["+lwins+"]", cBold)}
	bR := line{C("["+rwins+"]", cBold), S(" "), C(trim(rname, nameW), cBold+cRed)}
	bC := line{C(fmt.Sprintf("BO%d·G%d", max(1, totalGames), max(1, gameNo)), cDim)}
	rows = append(rows, borderedL(joinLCR(bL, bC, bR, W), t))
	bl()

	if len(w.Frames) > 0 {
		f := w.Frames[len(w.Frames)-1]
		b := f.BlueTeam
		r := f.RedTeam
		rows = append(rows, borderedL(statBarLine(tr("kills"), b.TotalKills, r.TotalKills,
			fmt.Sprintf("%d", b.TotalKills), fmt.Sprintf("%d", r.TotalKills), W), t))
		rows = append(rows, borderedL(statBarLine(tr("gold"), b.TotalGold, r.TotalGold,
			gold(b.TotalGold), gold(r.TotalGold), W), t))
		bl()
		rows = append(rows, borderedL(objectivesRow(b, r, t), t))
		if tl := objTimerRow(bv.Obj, t); tl != nil {
			rows = append(rows, borderedL(tl, t))
		}
		bl()
		// Rows the players table and footer need; optional panels get whatever is left.
		nb, nr := len(b.Participants), len(r.Participants)
		need := 1 + nb + nr + 2
		if tg.view == 0 && wide {
			need = 1 + max(nb, nr) + 2
		}
		spacer := 0
		if !compact {
			need++
			spacer = 1
			if tg.view != 0 || !wide {
				need++
			}
		}
		room := func() int { // rows available to an optional panel, excluding its spacer
			if h <= 0 {
				return 1 << 30
			}
			return h - len(rows) - need - spacer
		}
		if tg.omap {
			if pl := objMapPanel(b, r, t); len(pl) <= room() {
				rows = append(rows, pl...)
				bl()
			} else {
				rows = append(rows, borderedL(panelHint(tr("obj"), len(pl), room()), t))
			}
		}
		if tg.spark {
			if room() >= 1 {
				rows = append(rows, borderedL(sparkline(series, W), t))
				bl()
			} else {
				rows = append(rows, borderedL(panelHint(tr("spark"), 1, room()), t))
			}
		}
		bm := metaMap(w.GameMetadata.BlueTeamMetadata)
		rm := metaMap(w.GameMetadata.RedTeamMetadata)
		dm := detFrom(det)

		// The table is built aside so a short board can report the players it dropped
		// instead of ending in the middle of a team.
		var tbl []line
		var cnt []int
		add := func(l line, players int) {
			tbl = append(tbl, l)
			cnt = append(cnt, players)
		}

		switch {
		case tg.view != 0:
			var hdr line
			var row func(string, string, detailsParticipant, string, int) line
			if tg.view == 1 {
				hdr, row = itemsPlayerHeader(W), itemsPlayerRow
			} else {
				hdr, row = runesPlayerHeader(W), runesPlayerRow
			}
			add(borderedL(hdr, t), 0)
			for _, p := range b.Participants {
				m := bm[p.ParticipantID]
				add(borderedL(row(m.name, m.champ, dm[p.ParticipantID], cCyan, W), t), 1)
			}
			if !compact {
				add(borderedL(line{C(strings.Repeat("─", W), cGray)}, t), 0)
			}
			for _, p := range r.Participants {
				m := rm[p.ParticipantID]
				add(borderedL(row(m.name, m.champ, dm[p.ParticipantID], cRed, W), t), 1)
			}
		case wide:
			side := (W - 3) / 2
			cL := pickCols(side)
			cR := pickCols(W - 3 - side)
			add(sideLine(playerHeader(cL), playerHeader(cR), side, t), 0)
			n := len(b.Participants)
			if len(r.Participants) > n {
				n = len(r.Participants)
			}
			for i := 0; i < n; i++ {
				var lp, rp line
				players := 0
				if i < len(b.Participants) {
					lp = playerRow(b.Participants[i], bm[b.Participants[i].ParticipantID], cL, cCyan, bv.Lives)
					players++
				}
				if i < len(r.Participants) {
					rp = playerRow(r.Participants[i], rm[r.Participants[i].ParticipantID], cR, cRed, bv.Lives)
					players++
				}
				add(sideLine(lp, rp, side, t), players)
			}
		default:
			c := pickCols(W)
			add(borderedL(playerHeader(c), t), 0)
			for i := range b.Participants {
				add(borderedL(playerRow(b.Participants[i], bm[b.Participants[i].ParticipantID], c, cCyan, bv.Lives), t), 1)
			}
			if !compact {
				add(borderedL(line{C(strings.Repeat("─", W), cGray)}, t), 0)
			}
			for i := range r.Participants {
				add(borderedL(playerRow(r.Participants[i], rm[r.Participants[i].ParticipantID], c, cRed, bv.Lives), t), 1)
			}
		}
		if h > 0 {
			tail := 2 // footer + bottom border
			if tg.embed {
				tail = 1
			} else if !compact {
				tail++ // the blank above the footer
			}
			fit := h - tail
			more := 0
			for len(rows)+len(tbl) > fit && len(tbl) > 0 {
				more += cnt[len(cnt)-1]
				tbl, cnt = tbl[:len(tbl)-1], cnt[:len(cnt)-1]
			}
			// The notice takes a row of its own, so a table cut to the last row loses one more.
			for more > 0 && len(rows)+len(tbl)+1 > fit && len(tbl) > 0 {
				more += cnt[len(cnt)-1]
				tbl, cnt = tbl[:len(tbl)-1], cnt[:len(cnt)-1]
			}
			if more > 0 {
				key := "more_players"
				if more == 1 {
					key = "more_player"
				}
				tbl = append(tbl, borderedL(line{C(fmt.Sprintf(tr(key), more), cDim)}, t))
			}
		}
		rows = append(rows, tbl...)
	} else {
		rows = append(rows, borderedL(joinLR(line{}, line{C("◷  waiting for in-game data…", cYel)}, W), t))
	}

	return finishCanvas(rows, t, h, now, compact, interval, tg)
}

// finishCanvas appends the footer and fits the board to h rows (h <= 0: unbounded) by
// dropping rows above the footer.
func finishCanvas(rows []line, t, h int, now time.Time, compact bool, interval time.Duration, tg toggles) *canvas {
	W := t - 4
	if !compact && !tg.embed {
		rows = append(rows, blankL(t))
	}
	if tg.embed {
		rows = append(rows, botBorderL(t))
		if h > 0 && len(rows) > h {
			rows = append(append([]line{}, rows[:h-1]...), rows[len(rows)-1])
		}
		return linesToCanvas(rows, t)
	}
	clock := now.In(kst).Format("15:04:05")
	fL := line{C("◷", cGreen), S(" live feed · " + clock + " KST")}
	if rs := tg.replay; rs != nil {
		fL = line{C("◷", cPurple), S(fmt.Sprintf(" replay %s / %s · %g× · played %s KST", fmtClock(rs.Pos.Seconds()), fmtClock(rs.Total.Seconds()), rs.Speed, rs.Start.In(kst).Format("01-02 15:04")))}
	}
	mode, next := "stats", "items"
	switch tg.view {
	case 1:
		mode, next = "items", "runes"
	case 2:
		mode, next = "runes", "stats"
	}
	// Longest key hint that still fits next to the clock; narrow terminals get the short
	// forms, and the clock itself shortens so "q quit" and the border always survive.
	var fR line
	hints := []string{
		fmt.Sprintf("b:%s→%s · s spark · o obj · q quit", mode, next),
		"b s o · q quit",
		"q quit",
	}
	shortL := line{C("◷", cGreen), S(" " + now.In(kst).Format("15:04") + " KST")}
	for i, hint := range hints {
		fR = line{C(fmt.Sprintf("↻ %s", interval), cDim), S("  "), C(hint, cDim)}
		if tg.replay != nil {
			fR = line{C(hint, cDim)}
		}
		if i == len(hints)-1 && tg.replay == nil {
			fL = shortL
		}
		if fL.vis()+1+fR.vis() <= W {
			break
		}
	}
	if fL.vis()+1+fR.vis() > W {
		fL = truncLine(fL, max(1, W-1-fR.vis())) // a replay footer has no short form
	}
	body := rows
	rows = append(rows, borderedL(joinLR(fL, fR, W), t), botBorderL(t))
	if h > 0 && len(rows) > h {
		foot := rows[len(body):]
		if h < len(foot) {
			rows = foot[len(foot)-h:]
		} else {
			rows = append(append([]line{}, body[:h-len(foot)]...), foot...)
		}
	}
	return linesToCanvas(rows, t)
}

// ---------------- data fetch ----------------

// fetchLiveEvent returns the in-progress event (of league lid, of match matchID when set)
// and its running game. The error is kept apart: a failed lookup says nothing about the
// game, so callers keeping board state must not read it as "none".
func fetchLiveEvent(lid, matchID string) (event, string, bool, error) {
	url := gwBase + "/getLive?hl=en-US"
	if lid != "" {
		url += "&leagueId=" + lid
	}
	var lr scheduleResp
	if err := getJSON(url, &lr); err != nil {
		return event{}, "", false, err
	}
	for _, ev := range lr.Data.Schedule.Events {
		// getLive ignores leagueId and lists every league's live events.
		if ev.State != "inProgress" || ev.Match == nil || (lid != "" && ev.League.ID != lid) || (matchID != "" && ev.Match.ID != matchID) {
			continue
		}
		for _, g := range ev.Match.Games {
			if g.State == "inProgress" {
				return ev, g.ID, true, nil
			}
		}
		return ev, "", true, nil
	}
	return event{}, "", false, nil
}

func fetchSpawn(gameID string) time.Time {
	var w windowResp
	if err := getJSON(feedBase+"/window/"+gameID, &w); err != nil || len(w.Frames) == 0 {
		return time.Time{}
	}
	return parseTime(w.Frames[0].RFC460Timestamp)
}

// ---------------- command ----------------

// enterFullscreen switches to the alternate screen with raw single-key input. SIGINT,
// SIGTERM and SIGHUP (the terminal window closing) restore the terminal and exit. leave
// restores the terminal and is safe to call more than once. Without a terminal on stdin
// and stdout nobody could see the screen or press q: the process exits with an error.
func enterFullscreen() (keys <-chan key, leave func()) {
	return enterFullscreenMode(false)
}

// enterFullscreenMouse is enterFullscreen plus click and wheel reporting (SGR 1006), so
// the app can focus panes and select rows with the mouse. The standalone boards keep
// text selection and scrollback by using the plain variant.
func enterFullscreenMouse() (keys <-chan key, leave func()) {
	return enterFullscreenMode(true)
}

func enterFullscreenMode(mouse bool) (keys <-chan key, leave func()) {
	mouseOn, mouseOff := "", ""
	if mouse {
		mouseOn, mouseOff = "\033[?1000h\033[?1006h", "\033[?1006l\033[?1000l"
	}
	fd := int(os.Stdin.Fd())
	if !isTTY(os.Stdin) || !isTTY(os.Stdout) {
		fmt.Fprintln(os.Stderr, "error: this screen needs a terminal on stdin and stdout (one frame without it: lolterm lol relay -1)")
		os.Exit(1)
	}
	oldTerm, raw := makeRaw(fd)
	if !raw {
		fmt.Fprintln(os.Stderr, "error: cannot switch the terminal to raw key input")
		os.Exit(1)
	}
	fmt.Print("\033[?1049h\033[?25l" + mouseOn)
	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	leave = func() {
		once.Do(func() {
			cancel()
			restoreTerm(fd, oldTerm)
			fmt.Print(mouseOff + "\033[?25h\033[?1049l")
		})
	}
	leaveTerm.Store(leave)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		<-sig
		leave()
		os.Exit(0)
	}()
	return startKeys(ctx), leave
}

// leaveTerm holds the leave func of the running full-screen screen (unset outside one).
var leaveTerm atomic.Value

// crashGuard, deferred in a background goroutine, restores the terminal before a panic
// there ends the process: the main goroutine's deferred cleanup does not run then, and
// the shell would be left in raw mode on the alternate screen.
func crashGuard() {
	if r := recover(); r != nil {
		if leave, _ := leaveTerm.Load().(func()); leave != nil {
			leave()
		}
		panic(r)
	}
}

// boardKey applies the view toggles every board shares (b views, s spark, o objectives)
// and reports whether k was one of them; tab also cycles the views when tab is set.
func boardKey(tg *toggles, k key, tab bool) bool {
	switch {
	case k.Kind == keyTab && tab:
		tg.view = (tg.view + 1) % 3
	case k.Kind != keyRune:
		return false
	case k.Rune == 'b' || k.Rune == 'B':
		tg.view = (tg.view + 1) % 3
	case k.Rune == 's' || k.Rune == 'S':
		tg.spark = !tg.spark
	case k.Rune == 'o' || k.Rune == 'O':
		tg.omap = !tg.omap
	default:
		return false
	}
	return true
}

// boardTick is the render cadence: the feed plays a frame out every 0.5 s.
const boardTick = 500 * time.Millisecond

// relayPlayout decides what one render tick of a live board shows from the feed.
type relayPlayout struct {
	fd   *feed
	gid  string // game the shown position belongs to
	game time.Duration
}

// holdBack is the largest step back of the header clock that is held instead of shown;
// a bigger one is a genuine resync of a different clock.
const holdBack = 30 * time.Second

// holdGame keeps the header clock from stepping back (a pause learned late, a re-probed
// live edge): it stands still until the new reading catches up.
func (p *relayPlayout) holdGame(g time.Duration) time.Duration {
	if g >= 0 && g < p.game && p.game-g < holdBack {
		return p.game
	}
	if g >= 0 {
		p.game = g
	}
	return g
}

// Tick returns the feed state to draw at now.
func (p *relayPlayout) Tick(now time.Time) feedSnap {
	if gid := p.fd.Meta().GID; gid != p.gid {
		p.gid, p.game = gid, 0
	}
	snap := p.fd.Snapshot(now)
	snap.Game = p.holdGame(snap.Game)
	return snap
}

// view is what a feed snapshot adds to the board besides the frame itself.
func (s feedSnap) view() boardView { return boardView{Lives: s.Lives, Obj: s.Obj} }

func cmdRelay(args []string) {
	fs := flag.NewFlagSet("lol relay", flag.ExitOnError)
	league := fs.String("l", "", "league slug")
	once := fs.Bool("1", false, "render a single frame and exit (for screenshots/scripts)")
	interval := fs.Duration("i", 2*time.Second, "feed poll interval (frames play out every 0.5s)")
	fs.Parse(args)
	if *interval <= 0 {
		fmt.Fprintln(os.Stderr, "error: -i must be > 0")
		os.Exit(1)
	}

	lid, err := leagueID(*league)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	t, h := termSize()

	fd := newFeed(lid, *interval)
	fd.FetchOnce()

	tg := toggles{}
	var prev *canvas
	pl := &relayPlayout{fd: fd}
	draw := func() {
		now := time.Now()
		fd.SetDetailsView(tg.view != 0)
		s := pl.Tick(now)
		c := buildCanvas(s.Ev, s.W, s.Det, s.Game, now, max(t, minBoardW), h, *interval, tg, s.Series, s.view())
		if !*once && (t < minBoardW || h < minTermH) {
			c = tooSmallCanvas(t, h, minBoardW, minTermH)
		}
		if *once {
			fmt.Print(canvasString(c))
			return
		}
		fmt.Print(renderDiff(prev, c))
		prev = c
	}

	if *once {
		draw()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	keys, leave := enterFullscreen()
	defer leave()

	draw()
	go func() {
		// A panic here would kill the process without the main goroutine's deferred
		// cleanup: restore the terminal first, then crash as usual.
		defer func() {
			if r := recover(); r != nil {
				leave()
				panic(r)
			}
		}()
		fd.Run(ctx)
	}()
	tick := time.NewTicker(boardTick)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			if nt, nh := termSize(); nt != t || nh != h {
				t, h = nt, nh
				prev = nil
			}
			draw()
		case k, ok := <-keys:
			if !ok { // stdin closed (pipe or redirect): nobody can press q
				return
			}
			if k.Kind == keyCtrlC || (k.Kind == keyRune && (k.Rune == 'q' || k.Rune == 'Q')) {
				return
			}
			boardKey(&tg, k, true)
			if k.Kind == keyCtrl && k.Rune == 'l' {
				prev = nil // ctrl+l: full repaint; other keys draw only the cells that changed
			}
			draw()
		}
	}
}

// ---------------- demo (synthetic frame for preview/screenshot) ----------------

func cmdDemo(args []string) {
	fs := flag.NewFlagSet("lol demo", flag.ExitOnError)
	w := fs.Int("w", 0, "force terminal width (0 = detect)")
	hh := fs.Int("h", 0, "force terminal height (0 = detect)")
	rf := fs.Bool("r", false, "runes view")
	bf := fs.Bool("b", false, "items view")
	sf := fs.Bool("s", false, "show gold sparkline")
	of := fs.Bool("o", false, "show objective map")
	fs.Parse(args)
	tw, th := termSize()
	if *w > 0 {
		tw = *w
	}
	if *hh > 0 {
		th = *hh
	}
	if tw < 50 {
		tw = 50
	}
	v := 0
	if *rf {
		v = 2
	} else if *bf {
		v = 1
	}
	tg := toggles{view: v, spark: *sf, omap: *of}
	fmt.Print(canvasString(demoCanvasAt(tw, th, time.Now(), 10*time.Second, detailsResp{}, tg, nil, boardView{})))
}

func demoCanvasAt(t, h int, now time.Time, interval time.Duration, det detailsResp, tg toggles, series []goldPt, bv boardView) *canvas {
	ev := event{ID: "x", State: "inProgress", BlockName: "Knockouts"}
	ev.League.Name = "MSI"
	ev.League.Slug = "msi"
	ev.Match = &match{ID: "m"}
	mk := func(id, name, code string, wins int) teamResult {
		tr := teamResult{ID: id, Name: name, Code: code}
		tr.Result = &struct {
			Outcome  *string `json:"outcome"`
			GameWins int     `json:"gameWins"`
		}{GameWins: wins}
		return tr
	}
	ev.Match.Teams = []teamResult{
		mk("98767991926151025", "G2 Esports", "G2", 2),
		mk("98767991853197861", "T1", "T1", 1),
	}
	for i, st := range []string{"completed", "completed", "completed", "inProgress", "unstarted"} {
		ev.Match.Games = append(ev.Match.Games, game{ID: "g" + itoa(i), State: st})
	}
	var w windowResp
	w.GameMetadata.PatchVersion = "16.13.790.6961"
	blueP := []struct{ n, c string }{{"G2 BrokenBlade", "Yasuo"}, {"G2 SkewMond", "Skarner"}, {"G2 Caps", "Aurora"}, {"G2 Hans Sama", "Varus"}, {"G2 Labrov", "Renata"}}
	redP := []struct{ n, c string }{{"T1 Doran", "Gnar"}, {"T1 Oner", "LeeSin"}, {"T1 Faker", "Sylas"}, {"T1 Peyz", "Xayah"}, {"T1 Keria", "Rakan"}}
	bm := teamMeta{EsportsTeamID: "98767991926151025"}
	rm := teamMeta{EsportsTeamID: "98767991853197861"}
	for i, p := range blueP {
		bm.Participants = append(bm.Participants, pmetaRow(i+1, p.n, p.c))
	}
	for i, p := range redP {
		rm.Participants = append(rm.Participants, pmetaRow(i+6, p.n, p.c))
	}
	w.GameMetadata.BlueTeamMetadata = bm
	w.GameMetadata.RedTeamMetadata = rm
	type pi struct{ k, d, a, cs, g, hp, mx int }
	blue := []pi{{5, 8, 3, 205, 10600, 0, 3200}, {4, 6, 10, 175, 11500, 0, 2800}, {3, 3, 5, 296, 12500, 0, 3000}, {6, 5, 8, 265, 15500, 0, 3100}, {0, 5, 13, 22, 7800, 0, 2600}}
	red := []pi{{4, 4, 12, 228, 12600, 1375, 3200}, {4, 5, 13, 211, 12800, 2356, 2900}, {6, 4, 15, 210, 12000, 2085, 3000}, {13, 4, 7, 305, 19000, 2348, 3100}, {0, 1, 20, 21, 8300, 636, 2500}}
	f := frame{RFC460Timestamp: time.Now().Format(time.RFC3339), GameState: "in_game"}
	f.BlueTeam = teamStats{TotalKills: 18, TotalGold: 57900, Barons: 0, Towers: 4, Inhibitors: 0, Dragons: []string{"mountain", "ocean"}}
	f.RedTeam = teamStats{TotalKills: 27, TotalGold: 64700, Barons: 1, Towers: 7, Inhibitors: 1, Dragons: []string{"infernal", "hextech", "mountain", "ocean"}}
	for i, p := range blue {
		f.BlueTeam.Participants = append(f.BlueTeam.Participants, participant{ParticipantID: i + 1, Level: 17, Kills: p.k, Deaths: p.d, Assists: p.a, CreepScore: p.cs, TotalGold: p.g, CurrentHealth: p.hp, MaxHealth: p.mx})
	}
	for i, p := range red {
		f.RedTeam.Participants = append(f.RedTeam.Participants, participant{ParticipantID: i + 6, Level: 17, Kills: p.k, Deaths: p.d, Assists: p.a, CreepScore: p.cs, TotalGold: p.g, CurrentHealth: p.hp, MaxHealth: p.mx})
	}
	w.Frames = []frame{f}
	game := 31*time.Minute + 42*time.Second
	if det.Frames == nil {
		det = syntheticDetails()
	}
	if tg.spark && len(series) == 0 {
		series = syntheticSeries()
	}
	return buildCanvas(ev, w, det, game, now, t, h, interval, tg, series, bv)
}

func pmetaRow(id int, n, c string) struct {
	ParticipantID int    `json:"participantId"`
	SummonerName  string `json:"summonerName"`
	ChampionID    string `json:"championId"`
	Role          string `json:"role"`
} {
	return struct {
		ParticipantID int    `json:"participantId"`
		SummonerName  string `json:"summonerName"`
		ChampionID    string `json:"championId"`
		Role          string `json:"role"`
	}{id, n, c, ""}
}

func itoa(i int) string { return fmt.Sprintf("%d", i) }

// cmdBench proves the double-buffer diff: full repaint vs 1-second diff byte cost.
func cmdBench(args []string) {
	fs := flag.NewFlagSet("lol bench", flag.ExitOnError)
	wf := fs.Int("w", 0, "force width")
	hf := fs.Int("h", 0, "force height")
	fs.Parse(args)
	t, h := termSize()
	if *wf > 0 {
		t = *wf
	}
	if *hf > 0 {
		h = *hf
	}
	if t < 50 {
		t = 50
	}
	now := time.Now()
	a := demoCanvasAt(t, h, now, 10*time.Second, detailsResp{}, toggles{}, nil, boardView{})
	b := demoCanvasAt(t, h, now.Add(1*time.Second), 10*time.Second, detailsResp{}, toggles{}, nil, boardView{}) // clock +1s

	full := len(renderDiff(nil, a))
	diff := len(renderDiff(a, b))
	noop := len(renderDiff(a, a)) // identical frames
	pct := 100 * float64(diff) / float64(full)
	fmt.Printf("canvas size : %dx%d (%d cells)\n", t, a.h, t*a.h)
	fmt.Printf("full repaint: %d bytes  (clear screen + every cell)\n", full)
	fmt.Printf("1-second diff: %d bytes  (%.1f%% of full)\n", diff, pct)
	fmt.Printf("no-change   : %d bytes\n", noop)
	fmt.Println()
	fmt.Println("=> render ticks 1s (smooth clock + LIVE pulse); data polled every -i (default 10s).")
	fmt.Println("   between polls the diff writes only clock/pulse cells - a few dozen bytes, no full clear.")
}
