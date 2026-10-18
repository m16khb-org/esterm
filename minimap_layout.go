package main

import (
	"math"
	"strings"
	"sync"
)

// Broadcast layout profiles and HUD OCR.
//
// Every league's overlay places the minimap, the game clock and the two team kill
// counters at slightly different spots and draws the numbers in its own font. A layout
// carries the nominal boxes plus a glyph font per text element; mmReadHUD matches the
// font against the HUD region and mmDetectLayout tells the layouts apart.

// mmLayouts are the supported broadcast overlays; boxes are in 1920×1080 reference
// coordinates and measured on labelled broadcast frames.
var mmLayouts = []*mmLayout{
	{Name: "worlds", Map: mmBox{1640, 800, 280, 280}, Clock: mmBox{930, 76, 60, 28},
		KillsBlue: mmBox{900, 24, 45, 34}, KillsRed: mmBox{985, 24, 50, 34}},
	{Name: "lck", Map: mmBox{1637, 796, 271, 271}, Clock: mmBox{922, 78, 76, 24},
		KillsBlue: mmBox{858, 22, 56, 26}, KillsRed: mmBox{1004, 22, 56, 26}},
	{Name: "lpl", Map: mmBox{1641, 803, 271, 271}, Clock: mmBox{930, 76, 60, 28},
		KillsBlue: mmBox{896, 22, 40, 36}, KillsRed: mmBox{984, 22, 40, 36}},
	{Name: "lec", Map: mmBox{1647, 806, 261, 261}, Clock: mmBox{936, 76, 48, 28},
		KillsBlue: mmBox{906, 22, 32, 36}, KillsRed: mmBox{982, 22, 32, 36}},
}

const (
	mmTextPad = 3 // pixels searched around a text box (shifted overlays, vertical jitter)

	mmInkMinContrast = 90.0 // brightest minus darkest luminance (5th..99th percentile) in a text box
	mmSideInkMax     = 3    // bright pixels tolerated next to the text (a glyph that is not in the template)
	mmMaxKills       = 60

	// Decision thresholds in mean squared z-distance: the clock font has similar digits
	// (0/6/8/9) so it only demands a modest margin; kill digits are big and distinct.
	mmClockMaxCost   = 3.0
	mmClockMinMargin = 0.6
	mmKillMaxCost    = 2.0
	mmKillMinMargin  = 2.0
	mmKill2MaxCost   = 3.0  // two-digit counters: few samples per digit pair, a looser fit
	mmMinSigma       = 0.06 // per-pixel standard deviation floor of a template (levels 0..1)
	mmSigmaStep      = 0.03 // hex step of the stored standard deviation
)

// mmGlyph is one character model: per-pixel mean and inverse variance of the glyph's
// window (w columns, font height rows) over contrast-normalised luminance (0..1).
type mmGlyph struct {
	r   rune
	w   int // window width
	adv int // distance from this cell's window to the next cell's window
	mu  []float32
	inv []float32
}

// mmFont is the glyph set of one text element of one layout. A glyph's window starts
// at the cell's left edge minus a margin and the next cell's window starts adv (±mmShift)
// columns later.
type mmFont struct {
	h         int     // glyph rows
	top       int     // nominal glyph top row inside the padded text area
	maxCost   float32 // largest mean squared z-distance to a glyph that still reads
	minMargin float32 // required cost gap between the best glyph and every other class
	glyphs    []mmGlyph
}

const (
	mmShift  = 1 // per-glyph deviation of the cell pitch from the template advance
	mmLocalX = 2 // columns a glyph may move from its joint-decode anchor when judged alone
)

// mmFonts maps layout name to its clock and kill-counter fonts, mmBorders to the border
// profile of its minimap (minimap_layout_data.go). The two-digit counter font takes over
// the digit classes its samples never show from the one-digit font.
var (
	mmFonts   = parseFonts(mmFontData)
	mmBorders = parseBorders(mmFontData)
)

// mmLayoutFonts holds the fonts of one layout: the clock, and the kill counters as drawn
// with one digit (kills) and with two (kills2). A digit beside another is learned apart from
// a lone one: placement and neighbouring ink make them fit differently.
type mmLayoutFonts struct{ clock, kills, kills2 *mmFont }

func fontsOf(l *mmLayout) (mmLayoutFonts, bool) {
	f, ok := mmFonts[l.Name]
	return f, ok
}

// mmReadHUD reads the game clock and both kill counters of layout l from a
// mmRegionHUD-sized rgb24 buffer. It reports ok=false for everything it cannot read
// unambiguously with l's fonts: another league's HUD, no HUD, an overlay on top.
func mmReadHUD(l *mmLayout, hud []byte) mmHUD {
	h, _ := readHUD(l, hud)
	return h
}

// readHUD is mmReadHUD plus the worse of the two counters' fit costs (0 when the
// counters are not read).
func readHUD(l *mmLayout, hud []byte) (out mmHUD, killCost float32) {
	f, ok := fontsOf(l)
	if !ok || len(hud) != mmRegionHUD.W*mmRegionHUD.H*3 {
		return out, 0
	}
	s := hudPool.Get().(*hudScratch)
	defer hudPool.Put(s)
	if txt, _, ok := readText(f.clock, hud, l.Clock, "dd:ad", s); ok {
		m := int(txt[0]-'0')*10 + int(txt[1]-'0')
		sec := int(txt[3]-'0')*10 + int(txt[4]-'0')
		if sec < 60 {
			out.ClockSec, out.ClockOK = m*60+sec, true
		}
	}
	b, bc, bok := readKills(f, hud, l.KillsBlue, s)
	r, rc, rok := readKills(f, hud, l.KillsRed, s)
	if bok && rok {
		out.Blue, out.Red, out.KillsOK = b, r, true
		killCost = max(bc, rc)
	}
	return out, killCost
}

// hudScratch holds the buffers one HUD reading works in, so a reading allocates only its
// result strings.
type hudScratch struct {
	lum, nv, costs []float32
	tab            []mmCell
	glyphs         [2][]mmPlaced
}

var hudPool = sync.Pool{New: func() any { return new(hudScratch) }}

// grow returns s with length n, reallocating only when its capacity is too small. The
// contents are unspecified.
func grow[T any](s []T, n int) []T {
	if cap(s) < n {
		return make([]T, n)
	}
	return s[:n]
}

// readKills reads a counter of one or two digits. The two-digit reading explains more of
// the ink, so it wins whenever it reads at all; a blank where its first digit would be
// makes it fail and leaves the single digit.
func readKills(f mmLayoutFonts, hud []byte, box mmBox, s *hudScratch) (n int, cost float32, ok bool) {
	txt, cost, ok := readText(f.kills2, hud, box, "dd", s)
	if !ok {
		if txt, cost, ok = readText(f.kills, hud, box, "d", s); !ok {
			return 0, 0, false
		}
	}
	for _, c := range txt {
		n = n*10 + int(c-'0')
	}
	return n, cost, n <= mmMaxKills
}

// textLum returns the luminance of box grown by mmTextPad, taken from the HUD region.
func textLum(hud []byte, box mmBox, buf []float32) (lum []float32, w, h int) {
	x0, y0 := box.X-mmTextPad-mmRegionHUD.X, box.Y-mmTextPad-mmRegionHUD.Y
	w, h = box.W+2*mmTextPad, box.H+2*mmTextPad
	lum = grow(buf, w*h)
	clear(lum)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			sx, sy := x0+x, y0+y
			if sx < 0 || sy < 0 || sx >= mmRegionHUD.W || sy >= mmRegionHUD.H {
				continue
			}
			i := 3 * (sy*mmRegionHUD.W + sx)
			lum[y*w+x] = 0.299*float32(hud[i]) + 0.587*float32(hud[i+1]) + 0.114*float32(hud[i+2])
		}
	}
	return lum, w, h
}

// readText reads a string of the given pattern ('d' digit, 'a' digit below 6, ':' colon) from
// box. cost is the worst glyph's mean squared z-distance to its model.
func readText(f *mmFont, hud []byte, box mmBox, pattern string, s *hudScratch) (txt string, cost float32, ok bool) {
	lum, w, h := textLum(hud, box, s.lum)
	s.lum = lum
	return f.read(lum, w, h, pattern, s)
}

// read decodes pattern from a w×h luminance area (a text box grown by mmTextPad).
func (f *mmFont) read(lum []float32, w, h int, pattern string, s *hudScratch) (txt string, worst float32, ok bool) {
	nv, ok := normalizeText(lum, s.nv)
	if !ok {
		return "", 0, false
	}
	s.nv = nv
	sc := f.scores(nv, w, h, s.costs)
	s.costs = sc.sc
	best := sc.decode(pattern, s)
	if best.glyph == nil {
		return "", 0, false
	}
	var sb strings.Builder
	for k, g := range best.glyph {
		// The joint decode anchors the glyphs; each glyph is then judged on its own
		// over a small neighbourhood, and must beat every other class clearly.
		c, cost, margin := sc.local(pattern[k], g.x, best.dy)
		if cost > f.maxCost || margin < f.minMargin {
			return "", 0, false
		}
		worst = max(worst, cost)
		sb.WriteRune(f.glyphs[c].r)
	}
	// A counter's digit count is not fixed: bright ink beside the decoded digits is another
	// digit (or something else) that the pattern did not cover.
	if !strings.Contains(pattern, ":") && f.sideInk(nv, w, best) {
		return "", 0, false
	}
	return sb.String(), worst, true
}

// normalizeText maps luminance to 0..1 between the area's dark (5th percentile) and
// bright (99th percentile) levels; it fails when the area has no text-like contrast.
func normalizeText(lum, out []float32) ([]float32, bool) {
	var hist [256]int
	for _, v := range lum {
		hist[int(min(max(v, 0), 255))]++
	}
	pct := func(p float64) float32 {
		target, sum := int(p*float64(len(lum))), 0
		for i, c := range hist {
			if sum += c; sum > target {
				return float32(i)
			}
		}
		return 255
	}
	lo, hi := pct(0.05), pct(0.99)
	if hi-lo < mmInkMinContrast {
		return nil, false
	}
	out = grow(out, len(lum))
	for i, v := range lum {
		out[i] = min(max((v-lo)/(hi-lo), 0), 1.2)
	}
	return out, true
}

// sideInk reports bright pixels hugging the decoded text on either side: part of the
// text that the pattern did not account for.
func (f *mmFont) sideInk(nv []float32, w int, p decoded) bool {
	first, last := p.glyph[0], p.glyph[len(p.glyph)-1]
	x0, x1 := first.x, last.x+f.glyphs[last.class].w
	n := 0
	for y := p.top; y < p.top+f.h; y++ {
		for dx := 1; dx <= 3; dx++ {
			if x := x0 - dx; x >= 0 && nv[y*w+x] >= 0.5 {
				n++
			}
			if x := x1 - 1 + dx; x < w && nv[y*w+x] >= 0.5 {
				n++
			}
		}
	}
	return n > mmSideInkMax
}

// glyphCosts holds, for every row offset dy, glyph class and column x, the mean squared
// z-distance of the class at column x to the area, with the glyph top at f.top+dy-mmTextPad
// (see at).
type glyphCosts struct {
	f    *mmFont
	w    int
	rows int
	sc   []float32
}

const mmNoCost = float32(1e9)

func (g *glyphCosts) at(dy, c, x int) float32 { return g.sc[(dy*len(g.f.glyphs)+c)*g.w+x] }

// scores fills buf (grown when too small) with the costs of every glyph placement.
func (f *mmFont) scores(nv []float32, w, h int, buf []float32) glyphCosts {
	nc, rows := len(f.glyphs), 2*mmTextPad+1
	g := glyphCosts{f: f, w: w, rows: rows, sc: grow(buf, rows*nc*w)}
	for dy := 0; dy < rows; dy++ {
		top := f.top + dy - mmTextPad
		for c, gl := range f.glyphs {
			row := g.sc[(dy*nc+c)*w : (dy*nc+c+1)*w]
			for x := range row {
				row[x] = mmNoCost
				if top >= 0 && top+f.h <= h && x+gl.w <= w {
					row[x] = cost(nv, w, x, top, gl)
				}
			}
		}
	}
	return g
}

// cost is the mean squared z-distance of the window at (x, y) to glyph gl.
func cost(nv []float32, stride, x, y int, gl mmGlyph) float32 {
	h := len(gl.mu) / gl.w
	var sum float32
	for r := 0; r < h; r++ {
		row := nv[(y+r)*stride+x:]
		mu := gl.mu[r*gl.w:]
		inv := gl.inv[r*gl.w:]
		for c := 0; c < gl.w; c++ {
			d := row[c] - mu[c]
			sum += d * d * inv[c]
		}
	}
	return sum / float32(len(gl.mu))
}

// local returns the best class (colon or digit) for a glyph anchored at column x and row
// offset dy, searching ±mmLocalX columns and ±1 row, with its cost and the cost gap to
// the runner-up class.
func (g *glyphCosts) local(sym byte, x, dy int) (class int, cost, margin float32) {
	best, second := mmNoCost, mmNoCost
	for c, gl := range g.f.glyphs {
		if !mmAllowed(sym, gl.r) {
			continue
		}
		v := mmNoCost
		for r := max(dy-1, 0); r <= min(dy+1, g.rows-1); r++ {
			for cx := max(x-mmLocalX, 0); cx <= min(x+mmLocalX, g.w-1); cx++ {
				v = min(v, g.at(r, c, cx))
			}
		}
		switch {
		case v < best:
			class, best, second = c, v, best
		case v < second:
			second = v
		}
	}
	return class, best, second - best
}

// mmAllowed reports whether a pattern symbol accepts glyph r: 'd' any digit, 'a' a digit
// below 6 (tens of seconds), ':' the colon.
func mmAllowed(sym byte, r rune) bool {
	switch sym {
	case ':':
		return r == ':'
	case 'a':
		return r >= '0' && r <= '5'
	}
	return r >= '0' && r <= '9'
}

type mmPlaced struct {
	class int
	x     int
	cost  float32
}

type decoded struct {
	total float32 // summed glyph costs
	dy    int     // row offset index of the best fit
	top   int     // window top row in the area
	glyph []mmPlaced
}

// decode finds the cheapest placement of the pattern's glyphs: consecutive cells advance
// by the glyph's adv ±mmShift. glyph is nil when the pattern does not fit.
func (g *glyphCosts) decode(pattern string, s *hudScratch) decoded {
	best := decoded{total: mmNoCost}
	cur := 0
	for dy := 0; dy < g.rows; dy++ {
		s.glyphs[cur] = grow(s.glyphs[cur], len(pattern))
		if d := g.decodeRow(dy, pattern, s, s.glyphs[cur]); d.glyph != nil && d.total < best.total {
			best = d
			cur ^= 1 // keep the best row's glyphs, write the next row into the other buffer
		}
	}
	if best.glyph == nil {
		return decoded{}
	}
	return best
}

type mmCell struct {
	val  float32
	prev int // class*w + x of the previous cell, -1 at the first position
	ok   bool
}

// decodeRow decodes row offset dy; the glyphs of a successful decode are written to out.
func (g *glyphCosts) decodeRow(dy int, pattern string, s *hudScratch, out []mmPlaced) decoded {
	nc := len(g.f.glyphs)
	stride := nc * g.w
	s.tab = grow(s.tab, len(pattern)*stride)
	clear(s.tab)
	tab := s.tab
	for k, p := range pattern {
		cur := tab[k*stride : (k+1)*stride]
		for c, gl := range g.f.glyphs {
			if !mmAllowed(byte(p), gl.r) {
				continue
			}
			for x := 0; x+gl.w <= g.w; x++ {
				sc := g.at(dy, c, x)
				if sc >= mmNoCost {
					continue
				}
				cl := mmCell{val: sc, prev: -1, ok: k == 0}
				if k > 0 {
					prev := tab[(k-1)*stride : k*stride]
					for pc, pg := range g.f.glyphs {
						for d := -mmShift; d <= mmShift; d++ {
							px := x - pg.adv - d
							if px < 0 || px >= g.w {
								continue
							}
							if pv := prev[pc*g.w+px]; pv.ok && (!cl.ok || pv.val+sc < cl.val) {
								cl.val, cl.prev, cl.ok = pv.val+sc, pc*g.w+px, true
							}
						}
					}
				}
				cur[c*g.w+x] = cl
			}
		}
	}
	last := len(pattern) - 1
	bi, bv := -1, mmNoCost
	for i, cl := range tab[last*stride : (last+1)*stride] {
		if cl.ok && cl.val < bv {
			bi, bv = i, cl.val
		}
	}
	if bi < 0 {
		return decoded{}
	}
	res := decoded{total: bv, dy: dy, top: g.f.top + dy - mmTextPad, glyph: out[:len(pattern)]}
	for k, i := last, bi; k >= 0; k-- {
		c, x := i/g.w, i%g.w
		res.glyph[k] = mmPlaced{c, x, g.at(dy, c, x)}
		i = tab[k*stride+i].prev
	}
	return res
}

// parseFonts reads the glyph data: "F <layout> <clock|kills|kills2> <rows> <top>" starts a
// font, "G <char> <width> <advance>" a glyph followed by <rows> lines of hex pairs (mean level, standard deviation step) per pixel.
func parseFonts(data string) map[string]mmLayoutFonts {
	out := map[string]mmLayoutFonts{}
	lines := strings.Split(strings.TrimSpace(data), "\n")
	var cur *mmFont
	for i := 0; i < len(lines); i++ {
		fs := strings.Fields(lines[i])
		if len(fs) == 0 {
			continue
		}
		switch fs[0] {
		case "F":
			cur = &mmFont{h: atoi(fs[3]), top: atoi(fs[4])}
			lf := out[fs[1]]
			switch fs[2] {
			case "clock":
				cur.maxCost, cur.minMargin = mmClockMaxCost, mmClockMinMargin
				lf.clock = cur
			case "kills2":
				cur.maxCost, cur.minMargin = mmKill2MaxCost, mmKillMinMargin
				lf.kills2 = cur
			default:
				cur.maxCost, cur.minMargin = mmKillMaxCost, mmKillMinMargin
				lf.kills = cur
			}
			out[fs[1]] = lf
		case "G":
			gl := mmGlyph{r: rune(fs[1][0]), w: atoi(fs[2]), adv: atoi(fs[3])}
			n := gl.w * cur.h
			gl.mu, gl.inv = make([]float32, 0, n), make([]float32, 0, n)
			for _, row := range lines[i+1 : i+1+cur.h] {
				for j := 0; j+1 < len(row); j += 2 {
					mu := strings.IndexByte("0123456789abcdef", row[j])
					sg := mmMinSigma + mmSigmaStep*float32(strings.IndexByte("0123456789abcdef", row[j+1]))
					gl.mu = append(gl.mu, float32(mu)/15)
					gl.inv = append(gl.inv, 1/(sg*sg))
				}
			}
			i += cur.h
			cur.glyphs = append(cur.glyphs, gl)
		}
	}
	return out
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

// mmBorderDepth is how many pixel rings outside a layout's nominal map box make up its
// border profile.
const mmBorderDepth = 12

// mmBorder is the mean colour of each ring outside the nominal map box, per edge (top,
// right, bottom, left), averaged along the edge without its corners.
type mmBorder [4][mmBorderDepth][3]float32

// parseBorders reads the "B <layout>" records: four lines (top, right, bottom, left) of
// mmBorderDepth rgb triples as hex digits (levels of 16).
func parseBorders(data string) map[string]*mmBorder {
	out := map[string]*mmBorder{}
	lines := strings.Split(strings.TrimSpace(data), "\n")
	for i := 0; i < len(lines); i++ {
		fs := strings.Fields(lines[i])
		if len(fs) != 2 || fs[0] != "B" {
			continue
		}
		b := &mmBorder{}
		for e := range b {
			row := strings.TrimSpace(lines[i+1+e])
			for d := 0; d < mmBorderDepth; d++ {
				for c := 0; c < 3; c++ {
					b[e][d][c] = float32(strings.IndexByte("0123456789abcdef", row[3*d+c])) * 16
				}
			}
		}
		out[fs[1]] = b
		i += 4
	}
	return out
}

// measureBorder samples the ring profile around box from a mmRegionMM-sized rgb24 buffer.
// Rings or edges outside the region read as black, as they do in the stored profiles.
func measureBorder(mm []byte, box mmBox) *mmBorder {
	bx, by := box.X-mmRegionMM.X, box.Y-mmRegionMM.Y
	const margin = 20 // skip the corners
	var out mmBorder
	for d := 1; d <= mmBorderDepth; d++ {
		out[0][d-1] = lineMean(mm, bx+margin, by-d, box.W-2*margin, 1)
		out[1][d-1] = lineMean(mm, bx+box.W-1+d, by+margin, 1, box.H-2*margin)
		out[2][d-1] = lineMean(mm, bx+margin, by+box.H-1+d, box.W-2*margin, 1)
		out[3][d-1] = lineMean(mm, bx-d, by+margin, 1, box.H-2*margin)
	}
	return &out
}

func lineMean(mm []byte, x, y, w, h int) [3]float32 {
	var sum [3]float32
	n := 0
	for yy := y; yy < y+h; yy++ {
		for xx := x; xx < x+w; xx++ {
			if xx < 0 || yy < 0 || xx >= mmRegionMM.W || yy >= mmRegionMM.H {
				continue
			}
			o := (yy*mmRegionMM.W + xx) * 3
			sum[0] += float32(mm[o])
			sum[1] += float32(mm[o+1])
			sum[2] += float32(mm[o+2])
			n++
		}
	}
	if n > 0 {
		for c := range sum {
			sum[c] /= float32(n)
		}
	}
	return sum
}

// borderScore is 1 when the ring profile around l's nominal map box equals its stored
// profile and falls to 0 at a mean channel deviation of mmBorderTolerance levels.
func borderScore(l *mmLayout, mm []byte) float64 {
	ref := mmBorders[l.Name]
	if ref == nil || len(mm) != mmRegionMM.W*mmRegionMM.H*3 {
		return 0
	}
	got := measureBorder(mm, l.Map)
	var sum float64
	for e := range ref {
		for d := range ref[e] {
			for c := range ref[e][d] {
				sum += math.Abs(float64(got[e][d][c] - ref[e][d][c]))
			}
		}
	}
	dev := sum / float64(len(ref)*mmBorderDepth*3)
	return math.Max(0, 1-dev/mmBorderTolerance)
}

const (
	mmBorderTolerance = 30.0 // mean channel deviation (levels) at which a border no longer matches
	mmDetectMinConf   = 0.15 // below this no layout is reported
	mmDetectMinGap    = 0.04 // without a readable clock the winner must lead the runner-up by this much

	// The confidence adds up what the frame shows: the clock in the layout's font is the
	// strongest evidence (fonts differ between leagues; Worlds and LPL share the kill font,
	// and their borders look alike), then the counters, then the border colours. The
	// counters count as much as they fit: a shared font reads in both layouts but fits its
	// own better.
	mmConfClock  = 0.5
	mmConfKills  = 0.3
	mmConfBorder = 0.2
	mmKillFitEnd = 1.5 // counter fit cost at which a reading earns nothing
)

// mmDetectLayout picks the layout whose HUD fonts read the clock and kill counters of hud
// (mmRegionHUD rgb24) and whose border profile matches the ring around its map box in mm
// (mmRegionMM rgb24; either buffer may be nil). The confidence is 0..1. No layout is
// reported when none reaches mmDetectMinConf, or when the evidence cannot tell the two best
// apart: a clock read is decisive, counters and borders alone may be shared.
func mmDetectLayout(hud, mm []byte) (*mmLayout, float64) {
	var best *mmLayout
	bestConf, secondConf, bestClock := 0.0, 0.0, false
	for _, l := range mmLayouts {
		conf := mmConfBorder * borderScore(l, mm)
		h, killCost := readHUD(l, hud)
		if h.ClockOK {
			conf += mmConfClock
		}
		if h.KillsOK {
			conf += mmConfKills * math.Max(0, 1-float64(killCost)/mmKillFitEnd)
		}
		switch {
		case conf > bestConf:
			best, bestConf, secondConf, bestClock = l, conf, bestConf, h.ClockOK
		case conf > secondConf:
			secondConf = conf
		}
	}
	if bestConf < mmDetectMinConf || (!bestClock && bestConf-secondConf < mmDetectMinGap) {
		return nil, 0
	}
	return best, bestConf
}
