package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"os/signal"
	"strings"
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
	w, h := 80, 24
	if errno == 0 {
		if ws.Col >= 40 {
			w = int(ws.Col)
		}
		if ws.Row >= 8 {
			h = int(ws.Row)
		}
	}
	return w, h
}

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

// renderDiff emits ONLY the cells that changed since prev.
// prev==nil or size change => full repaint (clear + draw). Otherwise a
// cell-by-cell diff with cursor addressing (the "SPA" minimal-update path).
func renderDiff(prev, cur *canvas) string {
	var b strings.Builder
	ls := "\x00" // sentinel: forces first style emit
	emit := func(ce cell) {
		eff := ce.sty
		if eff == "" {
			eff = cReset
		}
		if eff != ls {
			b.WriteString(eff)
			ls = eff
		}
		b.WriteRune(ce.ch)
	}

	// full repaint
	if prev == nil || prev.w != cur.w {
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
			eff := ce.sty
			if eff == "" {
				eff = cReset
			}
			if eff != ls {
				b.WriteString(eff)
				ls = eff
			}
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

func playerRow(p participant, m pmeta, c cols, tc string) line {
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
		label    string
		lv, rv   int
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
func buildCanvas(ev event, w windowResp, det detailsResp, spawn, now time.Time, t, h int, interval time.Duration, tg toggles, series []goldPt) *canvas {
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
		rows = append(rows, borderedL(joinLR(line{}, line{C("try: esterm lol schedule", cDim)}, W), t))
		rows = append(rows, blankL(t))
		return finishCanvas(rows, t, now, compact, interval, tg)
	}

	dot := C("●", cDim)
	if pulse {
		dot = C("●", cBold+cRed)
	}
	clk := "—:—"
	if !spawn.IsZero() {
		d := now.Sub(spawn)
		if d < 0 {
			d = 0
		}
		clk = fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
	}
	hdL := line{dot, S(" "), C("LIVE", cBold+cRed), S("  "),
		C(strings.ToUpper(ev.League.Slug), cBold),
		S(" · " + or(ev.BlockName, "Match"))}
	hdR := line{C("◷", cGray), S(" " + clk)}
	if t >= 88 {
		hdR = append(hdR, C("  patch "+shortPatch(w.GameMetadata.PatchVersion), cDim))
	}
	rows = append(rows, borderedL(joinLR(hdL, hdR, W), t))
	bl()

	var lname, rname string = "TBD", "TBD"
	var lwins, rwins int
	if ev.Match != nil && len(ev.Match.Teams) >= 2 {
		lname = or(ev.Match.Teams[0].Name, or(ev.Match.Teams[0].Code, "TBD"))
		rname = or(ev.Match.Teams[1].Name, or(ev.Match.Teams[1].Code, "TBD"))
		if ev.Match.Teams[0].Result != nil {
			lwins = ev.Match.Teams[0].Result.GameWins
		}
		if ev.Match.Teams[1].Result != nil {
			rwins = ev.Match.Teams[1].Result.GameWins
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
	bL := line{C(trim(lname, nameW), cBold+cCyan), S(" "), C(fmt.Sprintf("[%d]", lwins), cBold)}
	bR := line{C(fmt.Sprintf("[%d]", rwins), cBold), S(" "), C(trim(rname, nameW), cBold+cRed)}
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
		if tg.omap {
			for _, pl := range objMapPanel(b, r, t) {
				rows = append(rows, pl)
			}
			bl()
		}
		if tg.spark {
			rows = append(rows, borderedL(sparkline(series, W), t))
			bl()
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
					lp = playerRow(b.Participants[i], bm[b.Participants[i].ParticipantID], cL, cCyan)
				}
				if i < len(r.Participants) {
					rp = playerRow(r.Participants[i], rm[r.Participants[i].ParticipantID], cR, cRed)
				}
				rows = append(rows, sideLine(lp, rp, side, t))
			}
		} else {
			c := pickCols(W)
			rows = append(rows, borderedL(playerHeader(c), t))
			for i := range b.Participants {
				rows = append(rows, borderedL(playerRow(b.Participants[i], bm[b.Participants[i].ParticipantID], c, cCyan), t))
			}
			if !compact {
				rows = append(rows, borderedL(line{C(strings.Repeat("─", W), cGray)}, t))
			}
			for i := range r.Participants {
				rows = append(rows, borderedL(playerRow(r.Participants[i], rm[r.Participants[i].ParticipantID], c, cRed), t))
			}
		}
	} else {
		rows = append(rows, borderedL(joinLR(line{}, line{C("◷  waiting for in-game data…", cYel)}, W), t))
	}

	return finishCanvas(rows, t, now, compact, interval, tg)
}

func finishCanvas(rows []line, t int, now time.Time, compact bool, interval time.Duration, tg toggles) *canvas {
	W := t - 4
	if !compact {
		rows = append(rows, blankL(t))
	}
	clock := now.In(kst).Format("15:04:05")
	fL := line{C("◷", cGreen), S(" live feed · " + clock + " KST")}
	mode, next := "stats", "items"
	if tg.view == 1 {
		mode, next = "items", "runes"
	} else if tg.view == 2 {
		mode, next = "runes", "stats"
	}
	hint := fmt.Sprintf("b:%s→%s · s spark · o map · q quit", mode, next)
	fR := line{C(fmt.Sprintf("↻ %s", interval), cDim), S("  "), C(hint, cDim)}
	rows = append(rows, borderedL(joinLR(fL, fR, W), t))
	rows = append(rows, botBorderL(t))
	return linesToCanvas(rows, t)
}

// ---------------- data fetch ----------------
func fetchLiveEvent(lid string) (event, string, bool) {
	url := gwBase + "/getLive?hl=en-US"
	if lid != "" {
		url += "&leagueId=" + lid
	}
	var lr scheduleResp
	if err := getJSON(url, &lr); err != nil {
		return event{}, "", false
	}
	for _, ev := range lr.Data.Schedule.Events {
		if ev.State != "inProgress" || ev.Match == nil {
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
func cmdRelay(args []string) {
	fs := flag.NewFlagSet("lol relay", flag.ExitOnError)
	league := fs.String("l", "", "league slug")
	once := fs.Bool("1", false, "render a single frame and exit (for screenshots/scripts)")
	interval := fs.Duration("i", 10*time.Second, "poll/render interval (feed updates ~10s)")
	fs.Parse(args)

	lid, ok := leagueID(*league)
	if !ok {
		fmt.Fprintln(os.Stderr, "unknown league:", *league)
		return
	}

	t, h := termSize()
	if t < 50 {
		t = 50
	}

	ev, gid, found := fetchLiveEvent(lid)
	var spawn time.Time
	if found && gid != "" {
		spawn = fetchSpawn(gid)
	}

	tg := toggles{}
	var series []goldPt
	var lastW windowResp
	var lastDet detailsResp
	fetchData := func() {
		lastW = windowResp{}
		lastDet = detailsResp{}
		if gid == "" {
			return
		}
		getJSON(feedBase+"/window/"+gid+"?startingTime="+latestStartingTime(), &lastW)
		getJSON(feedBase+"/details/"+gid+"?startingTime="+latestStartingTime(), &lastDet)
		if len(lastW.Frames) > 0 {
			f := lastW.Frames[len(lastW.Frames)-1]
			gm := 0
			if !spawn.IsZero() {
				gm = int(time.Since(spawn).Minutes())
			}
			diff := f.BlueTeam.TotalGold - f.RedTeam.TotalGold
			if n := len(series); n == 0 || series[n-1].gameMin != gm {
				series = append(series, goldPt{gm, diff})
				if len(series) > 240 {
					series = series[len(series)-240:]
				}
			} else {
				series[n-1] = goldPt{gm, diff}
			}
		}
	}
	var prev *canvas
	draw := func() {
		now := time.Now()
		c := buildCanvas(ev, lastW, lastDet, spawn, now, t, h, *interval, tg, series)
		if *once {
			fmt.Print(canvasString(c))
			return
		}
		fmt.Print(renderDiff(prev, c))
		prev = c
	}
	fetchData()

	if *once {
		draw()
		return
	}

	fmt.Print("\033[?1049h\033[?25l")
	cleanup := func() { fmt.Print("\033[?25h\033[?1049l") }
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		cleanup()
		os.Exit(0)
	}()
	defer cleanup()

	// raw mode + key reader for toggles (darwin; unavailable elsewhere -> ignored)
	oldTerm, _ := makeRaw(int(os.Stdin.Fd()))
	if oldTerm != nil {
		defer restoreTerm(int(os.Stdin.Fd()), oldTerm)
	}
	keys := make(chan []byte, 8)
	go func() {
		buf := make([]byte, 16)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				return
			}
			if n > 0 {
				cp := make([]byte, n)
				copy(cp, buf[:n])
				select {
				case keys <- cp:
				default:
				}
			}
		}
	}()

	draw()
	tick := time.NewTicker(1 * time.Second) // render every 1s: smooth clock + LIVE pulse
	defer tick.Stop()
	fetchEvery := int(*interval / time.Second)
	if fetchEvery < 1 {
		fetchEvery = 1
	}
	rcount := 0
	fcount := 0
	for {
		select {
		case <-tick.C:
			rcount++
			if nt, nh := termSize(); nt != t || nh != h {
				t, h = nt, nh
				if t < 50 {
					t = 50
				}
				prev = nil
			}
			if rcount%fetchEvery == 0 {
				fcount++
				if fcount%3 == 0 {
					if nev, ngid, nf := fetchLiveEvent(lid); nf {
						if ngid != gid {
							gid = ngid
							series = nil
							if gid != "" {
								spawn = fetchSpawn(gid)
							} else {
								spawn = time.Time{}
							}
						}
						ev = nev
					} else {
						ev = event{}
						gid = ""
						spawn = time.Time{}
						series = nil
					}
				}
				fetchData()
			}
			draw()
		case b := <-keys:
			quit := false
			for _, c := range b {
				switch {
				case c == 'b' || c == 'B' || c == '\t':
					tg.view = (tg.view + 1) % 3
				case c == 's' || c == 'S':
					tg.spark = !tg.spark
				case c == 'o' || c == 'O':
					tg.omap = !tg.omap
				case c == 'q' || c == 'Q' || c == 0x03:
					quit = true
				}
			}
			if quit {
				return
			}
			prev = nil
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
	fmt.Print(canvasString(demoCanvasAt(tw, th, time.Now(), 10*time.Second, detailsResp{}, tg, nil)))
}

func demoCanvasAt(t, h int, now time.Time, interval time.Duration, det detailsResp, tg toggles, series []goldPt) *canvas {
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
	f.BlueTeam = teamStats{TotalKills: 18, TotalGold: 57900, Barons: 0, Towers: 4, Inhibitors: 0, Dragons: []string{"mountain","ocean"}}
	f.RedTeam = teamStats{TotalKills: 27, TotalGold: 64700, Barons: 1, Towers: 7, Inhibitors: 1, Dragons: []string{"infernal","hextech","mountain","ocean"}}
	for i, p := range blue {
		f.BlueTeam.Participants = append(f.BlueTeam.Participants, participant{ParticipantID: i + 1, Level: 17, Kills: p.k, Deaths: p.d, Assists: p.a, CreepScore: p.cs, TotalGold: p.g, CurrentHealth: p.hp, MaxHealth: p.mx})
	}
	for i, p := range red {
		f.RedTeam.Participants = append(f.RedTeam.Participants, participant{ParticipantID: i + 6, Level: 17, Kills: p.k, Deaths: p.d, Assists: p.a, CreepScore: p.cs, TotalGold: p.g, CurrentHealth: p.hp, MaxHealth: p.mx})
	}
	w.Frames = []frame{f}
	spawn := time.Now().Add(-31 * time.Minute).Add(-42 * time.Second)
	if det.Frames == nil {
		det = syntheticDetails()
	}
	if tg.spark && len(series) == 0 {
		series = syntheticSeries()
	}
	return buildCanvas(ev, w, det, spawn, now, t, h, interval, tg, series)
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
	if *wf > 0 { t = *wf }
	if *hf > 0 { h = *hf }
	if t < 50 { t = 50 }
	now := time.Now()
	a := demoCanvasAt(t, h, now, 10*time.Second, detailsResp{}, toggles{}, nil)
	b := demoCanvasAt(t, h, now.Add(1*time.Second), 10*time.Second, detailsResp{}, toggles{}, nil) // clock +1s

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
