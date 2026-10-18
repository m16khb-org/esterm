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
	"syscall"
	"time"
	"unsafe"
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

// ---------------- terminal size ----------------
type winsize struct{ Row, Col, Xpixel, Ypixel uint16 }

func termSize() (int, int) {
	ws := &winsize{}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, os.Stdout.Fd(),
		uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(ws)))
	if errno != 0 || ws.Col == 0 || ws.Row == 0 {
		return 80, 24 // not a terminal (pipe, file): a sane default
	}
	return int(ws.Col), int(ws.Row)
}

// The smallest screen the app and the boards draw on; below it they show a notice at the
// real size instead of a squeezed or wrapped layout.
const (
	minTermW  = 40
	minTermH  = 10
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
		w += len([]rune(s.text))
	}
	return w
}
func (l line) render(width int) string {
	var b strings.Builder
	for _, s := range l {
		if s.color != "" {
			b.WriteString(s.color)
		}
		b.WriteString(s.text)
	}
	b.WriteString(cReset)
	if pad := width - l.vis(); pad > 0 {
		b.WriteString(strings.Repeat(" ", pad))
	}
	return b.String()
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
	mm   *mmSlot // kitty image slot of the minimap panel, if any
}

func newCanvas(w, h int) *canvas {
	buf := make([][]cell, h)
	space := cell{ch: ' '}
	for i := range buf {
		row := make([]cell, w)
		for j := range row {
			row[j] = space
		}
		buf[i] = row
	}
	return &canvas{w: w, h: h, buf: buf}
}

func (c *canvas) drawLine(row int, l line) {
	if row < 0 || row >= c.h {
		return
	}
	x := 0
	for _, s := range l {
		for _, ch := range []rune(s.text) {
			if x < c.w {
				c.buf[row][x] = cell{ch: ch, sty: s.color}
			}
			x++
		}
	}
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
			writeStyle(&b, &ls, ce.sty)
			b.WriteRune(ce.ch)
		}
		b.WriteString(cReset + "\n")
		ls = ""
	}
	return b.String()
}

// ---------------- helpers ----------------
func runeLen(s string) int { return len([]rune(s)) }

func padR(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		if n <= 1 {
			return string(r[:n])
		}
		return string(r[:n-1]) + "…"
	}
	return s + strings.Repeat(" ", n-len(r))
}
func padL(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return strings.Repeat(" ", n-len(r)) + s
}
func trim(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		if n <= 1 {
			return string(r[:n])
		}
		return string(r[:n-1]) + "…"
	}
	return s
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
func deadSeg(lf mmLife) seg {
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

func playerRow(p participant, m pmeta, c cols, tc string, lives mmLives) line {
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
		fs = append(fs, pfield{"PLAYER", cDim, c.name, true})
	}
	if c.champ > 0 {
		fs = append(fs, pfield{"CHAMP", cDim, c.champ, true})
	}
	if c.kda > 0 {
		fs = append(fs, pfield{"K/D/A", cDim, c.kda, false})
	}
	if c.cs > 0 {
		fs = append(fs, pfield{"CS", cDim, c.cs, false})
	}
	if c.gold > 0 {
		fs = append(fs, pfield{"GOLD", cDim, c.gold, false})
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

func borderedL(content line, t int) line {
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

func objectivesRow(b, r teamStats, t int) line {
	type o struct {
		label  string
		lv, rv int
	}
	all := []o{
		{"DRG", len(b.Dragons), len(r.Dragons)},
		{"BRN", b.Barons, r.Barons},
		{"TWR", b.Towers, r.Towers},
		{"INB", b.Inhibitors, r.Inhibitors},
	}
	budget := t - 4
	if budget < 58 {
		all = all[:3]
	}
	if budget < 44 {
		all = all[:2]
	}
	var l line
	for i, o := range all {
		if i > 0 {
			l = append(l, S("     "))
		}
		lvs, rvs := fmt.Sprintf("%d", o.lv), fmt.Sprintf("%d", o.rv)
		if o.label == "DRG" {
			lvs += soulStr(b.Dragons)
			rvs += soulStr(r.Dragons)
		}
		l = append(l, C(o.label, cDim), S(" "), C(lvs, cCyan), S(" ── "), C(rvs, cRed))
	}
	return l
}

// ---------------- frame -> canvas ----------------
// buildCanvas draws the board at t×h. Embedded boards (tg.embed) live inside the app's own
// pane: they lose the outer double border and the footer and use the whole area.
func buildCanvas(ev event, w windowResp, det detailsResp, game time.Duration, now time.Time, t, h int, interval time.Duration, tg toggles, series []goldPt, mm mmView) *canvas {
	if !tg.embed {
		return buildFramed(ev, w, det, game, now, t, h, interval, tg, series, mm)
	}
	hh := h
	if hh > 0 {
		hh += 2
	}
	fc := buildFramed(ev, w, det, game, now, t+2, hh, interval, tg, series, mm)
	if fc.h < 2 || fc.w < 3 {
		return fc
	}
	c := newCanvas(t, fc.h-2)
	for y := range c.buf {
		copy(c.buf[y], fc.buf[y+1][1:fc.w-1])
	}
	if s := fc.mm; s != nil {
		ns := *s
		ns.Row--
		ns.Col--
		c.mm = &ns
	}
	return c
}

func buildFramed(ev event, w windowResp, det detailsResp, game time.Duration, now time.Time, t, h int, interval time.Duration, tg toggles, series []goldPt, mm mmView) *canvas {
	W := t - 4
	compact := h > 0 && h <= 28
	wide := t >= 104
	pulse := now.Second()%2 == 0

	var rows []line
	var slot *mmSlot
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
		rows = append(rows, borderedL(joinLR(line{}, line{C("try: esterm lol schedule", cDim)}, W), t))
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
	rows = append(rows, borderedL(joinLR(hdL, hdR, W), t))
	bl()

	var lname, rname string = "TBD", "TBD"
	lwins, rwins := "0", "0"
	if tg.replay != nil {
		lwins, rwins = "-", "-" // a replay knows the series score only when the final game is shown
	}
	if ev.Match != nil && len(ev.Match.Teams) >= 2 {
		lname = or(ev.Match.Teams[0].Name, or(ev.Match.Teams[0].Code, "TBD"))
		rname = or(ev.Match.Teams[1].Name, or(ev.Match.Teams[1].Code, "TBD"))
		if ev.Match.Teams[0].Result != nil {
			lwins = itoa(ev.Match.Teams[0].Result.GameWins)
		}
		if ev.Match.Teams[1].Result != nil {
			rwins = itoa(ev.Match.Teams[1].Result.GameWins)
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
		rows = append(rows, borderedL(statBarLine("KILLS", b.TotalKills, r.TotalKills,
			fmt.Sprintf("%d", b.TotalKills), fmt.Sprintf("%d", r.TotalKills), W), t))
		rows = append(rows, borderedL(statBarLine("GOLD ", b.TotalGold, r.TotalGold,
			gold(b.TotalGold), gold(r.TotalGold), W), t))
		bl()
		rows = append(rows, borderedL(objectivesRow(b, r, t), t))
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
			}
		}
		if tg.spark && room() >= 1 {
			rows = append(rows, borderedL(sparkline(series, W), t))
			bl()
		}
		if tg.mm {
			ph := 17
			if mm.Mode != mmModeText {
				ph = 25
			}
			if compact {
				ph = 13
				if mm.Mode != mmModeText {
					ph = 17
				}
			}
			switch avail := room(); {
			case avail >= mmMinRows:
				panel, sl := mmPanelView(mm, t, min(ph, avail))
				if sl != nil {
					sl.Row += len(rows)
				}
				slot = sl
				rows = append(rows, panel...)
				bl()
			case avail >= 1:
				rows = append(rows, borderedL(line{C("minimap: enlarge terminal", cDim)}, t))
				bl()
			}
		}

		bm := metaMap(w.GameMetadata.BlueTeamMetadata)
		rm := metaMap(w.GameMetadata.RedTeamMetadata)
		dm := detFrom(det)

		if tg.view != 0 {
			var hdr line
			var row func(string, string, detailsParticipant, string, int) line
			if tg.view == 1 {
				hdr, row = itemsPlayerHeader(W), itemsPlayerRow
			} else {
				hdr, row = runesPlayerHeader(W), runesPlayerRow
			}
			rows = append(rows, borderedL(hdr, t))
			for _, p := range b.Participants {
				m := bm[p.ParticipantID]
				rows = append(rows, borderedL(row(m.name, m.champ, dm[p.ParticipantID], cCyan, W), t))
			}
			if !compact {
				rows = append(rows, borderedL(line{C(strings.Repeat("─", W), cGray)}, t))
			}
			for _, p := range r.Participants {
				m := rm[p.ParticipantID]
				rows = append(rows, borderedL(row(m.name, m.champ, dm[p.ParticipantID], cRed, W), t))
			}
		} else if wide {
			side := (W - 3) / 2
			cL := pickCols(side)
			cR := pickCols(W - 3 - side)
			rows = append(rows, sideLine(playerHeader(cL), playerHeader(cR), side, t))
			n := len(b.Participants)
			if len(r.Participants) > n {
				n = len(r.Participants)
			}
			for i := 0; i < n; i++ {
				var lp, rp line
				if i < len(b.Participants) {
					lp = playerRow(b.Participants[i], bm[b.Participants[i].ParticipantID], cL, cCyan, mm.Lives)
				}
				if i < len(r.Participants) {
					rp = playerRow(r.Participants[i], rm[r.Participants[i].ParticipantID], cR, cRed, mm.Lives)
				}
				rows = append(rows, sideLine(lp, rp, side, t))
			}
		} else {
			c := pickCols(W)
			rows = append(rows, borderedL(playerHeader(c), t))
			for i := range b.Participants {
				rows = append(rows, borderedL(playerRow(b.Participants[i], bm[b.Participants[i].ParticipantID], c, cCyan, mm.Lives), t))
			}
			if !compact {
				rows = append(rows, borderedL(line{C(strings.Repeat("─", W), cGray)}, t))
			}
			for i := range r.Participants {
				rows = append(rows, borderedL(playerRow(r.Participants[i], rm[r.Participants[i].ParticipantID], c, cRed, mm.Lives), t))
			}
		}
	} else {
		rows = append(rows, borderedL(joinLR(line{}, line{C("◷  waiting for in-game data…", cYel)}, W), t))
	}

	c := finishCanvas(rows, t, h, now, compact, interval, tg)
	if slot != nil && slot.Row+slot.Rows > c.h-2 {
		slot = nil // the image would overlap the footer or fall off the screen
	}
	c.mm = slot
	return c
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
	if tg.view == 1 {
		mode, next = "items", "runes"
	} else if tg.view == 2 {
		mode, next = "runes", "stats"
	}
	// Longest key hint that still fits next to the clock; narrow terminals get the short forms.
	var fR line
	hints := []string{
		fmt.Sprintf("b:%s→%s · s spark · o obj · m map · v video · q quit", mode, next),
		"b s o m v · q quit",
		"q quit",
	}
	for _, hint := range hints {
		fR = line{C(fmt.Sprintf("↻ %s", interval), cDim), S("  "), C(hint, cDim)}
		if tg.replay != nil {
			fR = line{C(hint, cDim)}
		}
		if fL.vis()+1+fR.vis() <= W {
			break
		}
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
func fetchLiveEvent(lid, matchID string) (event, string, bool) {
	url := gwBase + "/getLive?hl=en-US"
	if lid != "" {
		url += "&leagueId=" + lid
	}
	var lr scheduleResp
	if err := getJSON(url, &lr); err != nil {
		return event{}, "", false
	}
	for _, ev := range lr.Data.Schedule.Events {
		if ev.State != "inProgress" || ev.Match == nil || (matchID != "" && ev.Match.ID != matchID) {
			continue
		}
		for _, g := range ev.Match.Games {
			if g.State == "inProgress" {
				return ev, g.ID, true
			}
		}
		return ev, "", true
	}
	return event{}, "", false
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
// SIGTERM and SIGHUP (the terminal window closing) run stop (it must kill child processes) before the terminal is restored and the
// process exits. leave restores the terminal and is safe to call more than once.
func enterFullscreen(stop func()) (keys <-chan key, leave func()) {
	fmt.Print("\033[?1049h\033[?25l")
	fd := int(os.Stdin.Fd())
	oldTerm, _ := makeRaw(fd)
	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	leave = func() {
		once.Do(func() {
			cancel()
			restoreTerm(fd, oldTerm)
			fmt.Print("\033[?25h\033[?1049l")
		})
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		<-sig
		stop()
		leave()
		os.Exit(0)
	}()
	return startKeys(ctx), leave
}

// boardKey applies the view toggles every board shares (b views, s spark, o objectives,
// m minimap panel, v broadcast video) and reports whether k was one of them; tab also
// cycles the views when tab is set.
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
	case k.Rune == 'm' || k.Rune == 'M':
		tg.mm = !tg.mm || !tg.video // m starts the video when it is off
		tg.video = true
	case k.Rune == 'v' || k.Rune == 'V':
		tg.video = !tg.video
		tg.mm = tg.video
	default:
		return false
	}
	return true
}

// relayPlayout decides what one render tick shows from the feed and the broadcast
// minimap. Once video sync has locked, scoreboard, header clock and minimap all play out
// one shared feed time that never moves back; before that the feed plays out on its own
// adaptive delay and the minimap on its own buffer.
type relayPlayout struct {
	fd        *feed
	mm        *minimap
	sc        playClock
	sy        *mmSync // estimator the playout clock currently follows
	gid       string  // game the shown position belongs to
	aligned   bool    // the shown position has followed the video clock in this game
	killsSeen int     // feed kills already handed to sy
	game      time.Duration
	hidePanel bool // the minimap panel is off: the video pipeline skips tracking
}

// holdBack is the largest step back of the header clock that is held instead of shown;
// a bigger one is a genuine resync of a different clock.
const holdBack = 30 * time.Second

// holdGame keeps the header clock from stepping back (a learned skew, a re-estimated
// offset, a pipeline restart): it stands still until the new reading catches up.
func (p *relayPlayout) holdGame(g time.Duration) time.Duration {
	if g >= 0 && g < p.game && p.game-g < holdBack {
		return p.game
	}
	if g >= 0 {
		p.game = g
	}
	return g
}

// Tick starts or stops the video pipeline as needed and returns the feed state and the
// minimap view to draw at now. Mode and cell size of the view are the caller's to fill in.
func (p *relayPlayout) Tick(now time.Time, video bool) (feedSnap, mmView) {
	m := p.fd.Meta()
	if m.GID != p.gid {
		p.gid, p.game, p.aligned = m.GID, 0, false
		p.sc.Reset()
	}
	ready := m.GID != "" && !m.Spawn.IsZero() && len(m.W.GameMetadata.BlueTeamMetadata.Participants) > 0
	switch {
	case !video:
		p.mm.Stop()
	case ready:
		p.mm.Start(m.GID, eventID(m.Ev), m.W, m.Spawn, p.fd.Pauses)
	}
	p.mm.SetPanel(!p.hidePanel)
	if sy := p.mm.Sync(); sy != p.sy {
		p.sy = sy // a new pipeline: its offset and skew start from scratch, the shown position stays
		p.killsSeen = 0
		p.fd.SetSkew(0)
	}
	lf, feedOK := p.fd.Latest()
	if p.sy != nil && feedOK {
		var ks []feedKill
		ks, p.killsSeen = p.fd.KillsSince(p.killsSeen)
		p.sy.ObserveFeedKills(ks)
		p.sy.ObserveFeedLatest(lf)
	}
	// Once running, the shared clock outlives a passing loss of sync (stream restart,
	// stalled video): it keeps advancing on the feed alone instead of jumping to another
	// clock and back.
	if video && ready && feedOK && p.sy != nil && (p.sc.on || p.sy.Locked()) {
		return p.syncedTick(now, lf)
	}
	p.sc.Reset()
	snap := p.fd.Snapshot(now)
	snap.Game = p.holdGame(snap.Game)
	view := mmView{Status: "waiting for a live game"}
	if video && ready {
		view = p.mm.Snapshot(now, snap.Lives)
	}
	view.Lives = snap.Lives
	return snap, view
}

// syncedTick plays scoreboard, header and minimap out at the shared clock; lf is the
// feed's newest frame.
func (p *relayPlayout) syncedTick(now, lf time.Time) (feedSnap, mmView) {
	locked := p.sy.Locked()
	stall, stalled := p.mm.Stalled()
	p.fd.SetSkew(p.sy.Skew())
	edge := lf
	if locked && !stalled {
		if lv, ok := p.mm.VideoLatest(); ok {
			if ft, ok := p.sy.FeedTime(lv); ok && ft.Before(edge) {
				edge = ft
			}
		}
	}
	if !p.sc.on {
		// Hand over from the feed's own playout. The first time in a game the clock starts
		// at the video-aligned position when the video cannot show the viewer's position
		// yet: it is the opening alignment, the scoreboard steps back once. After that
		// (video toggled off and on, pipeline restarted) the clock starts at the position
		// the viewer sees and stands still until the video catches up; only a position
		// far ahead of anything the video can show is a genuine resync.
		if pos, ok := p.fd.Position(); ok {
			if (p.aligned && pos.Sub(edge) < holdBack) || !pos.After(edge) {
				p.sc.Start(now, pos)
			}
		}
	}
	if !p.aligned {
		p.game = 0 // the opening alignment moves scoreboard and header together; holding the header would show a clock ahead of the scoreboard
	}
	p.aligned = true
	T := p.sc.Advance(now, edge)
	p.fd.Need(T)
	snap := p.fd.SnapshotAt(now, T)
	snap.Game = p.holdGame(snap.Game)
	var view mmView
	switch {
	case stalled:
		view = stall
	case !locked:
		view = p.mm.Snapshot(now, snap.Lives)
	default:
		vt, _ := p.sy.VideoTime(T)
		view = p.mm.SnapshotAt(vt, snap.Lives)
	}
	view.Lives = snap.Lives
	return snap, view
}

func cmdRelay(args []string) {
	fs := flag.NewFlagSet("lol relay", flag.ExitOnError)
	league := fs.String("l", "", "league slug")
	once := fs.Bool("1", false, "render a single frame and exit (for screenshots/scripts)")
	interval := fs.Duration("i", 2*time.Second, "feed poll interval (frames play out every 0.5s)")
	src := fs.String("src", "", "minimap video source: page URL or file (default: the match's broadcast)")
	icons := fs.String("icons", mmModeAuto, "minimap drawing: auto|kitty|blocks|text")
	fs.Parse(args)

	lid, ok := leagueID(*league)
	if !ok {
		fmt.Fprintln(os.Stderr, "unknown league:", *league)
		os.Exit(1)
	}
	mode, err := mmResolveMode(*icons)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	t, h := termSize()

	fd := newFeed(lid, *interval)
	fd.FetchOnce()

	mm := newMinimap(minimapCfg{Src: *src})
	var kit kittyOut
	tg := toggles{}
	var prev *canvas
	pl := &relayPlayout{fd: fd, mm: mm}
	draw := func() {
		now := time.Now()
		pl.hidePanel = !tg.mm
		fd.SetDetailsView(tg.view != 0)
		s, view := pl.Tick(now, tg.video)
		view.Mode = mode
		view.CellW, view.CellH = termCellPx()
		c := buildCanvas(s.Ev, s.W, s.Det, s.Game, now, max(t, minBoardW), h, *interval, tg, s.Series, view)
		if !*once && (t < minBoardW || h < minTermH) {
			c = tooSmallCanvas(t, h, minBoardW, minTermH)
		}
		if *once {
			fmt.Print(canvasString(c))
			if c.mm != nil {
				fmt.Print(kittyOneShot(c.mm, c.h))
			}
			return
		}
		out := renderDiff(prev, c)
		fmt.Print(out + kit.Frame(c.mm, prev == nil || prev.w != c.w || prev.h != c.h))
		prev = c
	}

	if *once {
		draw()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	keys, leave := enterFullscreen(func() { mm.Close(mmCloseWait); fmt.Print(kit.Delete()) })
	defer leave()
	defer func() { fmt.Print(kit.Delete()) }()
	defer mm.Close(mmCloseWait)

	draw()
	go func() {
		// A panic here would kill the process without the main goroutine's deferred
		// cleanup: stop the children and restore the terminal first, then crash as usual.
		defer func() {
			if r := recover(); r != nil {
				mm.Close(mmCloseWait)
				fmt.Print(kit.Delete())
				leave()
				panic(r)
			}
		}()
		fd.Run(ctx)
	}()
	cadence := mmTickFor(tg.video && tg.mm)
	tick := time.NewTicker(cadence)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			if nt, nh := termSize(); nt != t || nh != h {
				t, h = nt, nh
				prev = nil
			}
			draw()
		case k := <-keys:
			if k.Kind == keyCtrlC || (k.Kind == keyRune && (k.Rune == 'q' || k.Rune == 'Q')) {
				return
			}
			boardKey(&tg, k, true)
			prev = nil
			draw()
		}
		if d := mmTickFor(tg.video && tg.mm); d != cadence {
			cadence = d
			tick.Reset(d)
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
	mf := fs.Bool("m", false, "show broadcast minimap (synthetic markers)")
	icons := fs.String("icons", mmModeAuto, "minimap drawing: auto|kitty|blocks|text")
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
	tg := toggles{view: v, spark: *sf, omap: *of, mm: *mf}
	mode, err := mmResolveMode(*icons)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	view := demoMinimap()
	mmPreloadAssets(3*time.Second, view.Ver, view.Icons)
	view.Mode = mode
	view.CellW, view.CellH = termCellPx()
	c := demoCanvasAt(tw, th, time.Now(), 10*time.Second, detailsResp{}, tg, nil, view)
	fmt.Print(canvasString(c))
	if c.mm != nil {
		fmt.Print(kittyOneShot(c.mm, c.h))
	}
}

// demoMinimap places the demo champions at fixed spots in the lanes (blue starts bottom-left,
// red top-right). The icon version is the latest Data Dragon one (empty offline: plain discs).
func demoMinimap() mmView {
	champs := []string{"Yasuo", "Skarner", "Aurora", "Varus", "Renata", "Gnar", "LeeSin", "Sylas", "Xayah", "Rakan"}
	roles := []string{"top", "jungle", "mid", "bottom", "support"}
	pos := [][2]float64{{0.12, 0.30}, {0.30, 0.62}, {0.46, 0.55}, {0.78, 0.90}, {0.74, 0.88},
		{0.20, 0.12}, {0.62, 0.30}, {0.56, 0.44}, {0.90, 0.24}, {0.88, 0.20}}
	refs := make([]champRef, len(champs))
	for i, c := range champs {
		refs[i] = champRef{PID: i + 1, Team: 100 + 100*(i/5), Champ: c, Role: roles[i%5]}
	}
	labels := mmLabels(refs)
	v := mmView{Status: "demo · synthetic positions"}
	v.Ver, _ = ddLatestVersion(context.Background())
	for i, r := range refs {
		v.Markers = append(v.Markers, mmMarker{Team: r.Team, Label: labels[r.PID], X: pos[i][0], Y: pos[i][1]})
		v.Icons = append(v.Icons, mmIcon{Team: r.Team, Champ: r.Champ, Label: labels[r.PID], X: pos[i][0], Y: pos[i][1]})
	}
	return v
}

func demoCanvasAt(t, h int, now time.Time, interval time.Duration, det detailsResp, tg toggles, series []goldPt, mm mmView) *canvas {
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
	return buildCanvas(ev, w, det, game, now, t, h, interval, tg, series, mm)
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

// bench proves the double-buffer diff: full repaint vs 1-second diff byte cost.
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
	a := demoCanvasAt(t, h, now, 10*time.Second, detailsResp{}, toggles{}, nil, mmView{})
	b := demoCanvasAt(t, h, now.Add(1*time.Second), 10*time.Second, detailsResp{}, toggles{}, nil, mmView{}) // clock +1s

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
