package main

import (
	"context"
	"image"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// Terminal minimap: a braille Summoner's Rift with one-letter lane labels on top.

// mmMinRows is the fewest rows (title included) worth giving the minimap panel.
const mmMinRows = 6

// mmRoleLabel is the lane letter of a champion: top T, jungle J, mid M, bottom/adc B,
// support S. An unknown role falls back to the first letter of the champion id.
func mmRoleLabel(role, champ string) string {
	switch strings.ToLower(role) {
	case "top":
		return "T"
	case "jungle":
		return "J"
	case "mid", "middle":
		return "M"
	case "bottom", "bot", "adc":
		return "B"
	case "support", "sup", "utility":
		return "S"
	}
	if r := []rune(champ); len(r) > 0 {
		return string(unicode.ToUpper(r[0]))
	}
	return "?"
}

// mmLabels returns the lane letter per PID.
func mmLabels(refs []champRef) map[int]string {
	out := make(map[int]string, len(refs))
	for _, r := range refs {
		out[r.PID] = mmRoleLabel(r.Role, r.Champ)
	}
	return out
}

// ---------------- background ----------------

// mmDots is a boolean dot grid, row-major, w*h.
type mmDots struct {
	w, h int
	on   []bool
}

func (d mmDots) at(x, y int) bool { return x >= 0 && y >= 0 && x < d.w && y < d.h && d.on[y*d.w+x] }

// fallbackDots draws a frame, the mid lane (bottom-left to top-right) and the river.
func fallbackDots(w, h int) mmDots {
	d := mmDots{w, h, make([]bool, w*h)}
	set := func(x, y int) {
		if x >= 0 && y >= 0 && x < w && y < h {
			d.on[y*w+x] = true
		}
	}
	for x := 0; x < w; x++ {
		set(x, 0)
		set(x, h-1)
	}
	for y := 0; y < h; y++ {
		set(0, y)
		set(w-1, y)
	}
	for x := 0; x < w; x++ {
		set(x, (h-1)-x*(h-1)/max(1, w-1)) // mid lane
		set(x, x*(h-1)/max(1, w-1))       // river
	}
	return d
}

// mmWalkLum is the luminance (0..255) above which a map pixel counts as walkable terrain.
const mmWalkLum = 40

// mapDots downsamples the map image to w*h dots by area-averaging luminance and draws the
// outline of the walkable terrain (lanes, river, bases: bright; jungle walls: black).
func mapDots(img image.Image, w, h int) mmDots {
	b := img.Bounds()
	lum := make([]float64, w*h)
	for y := 0; y < h; y++ {
		y0, y1 := b.Min.Y+y*b.Dy()/h, b.Min.Y+(y+1)*b.Dy()/h
		for x := 0; x < w; x++ {
			x0, x1 := b.Min.X+x*b.Dx()/w, b.Min.X+(x+1)*b.Dx()/w
			var s float64
			n := 0
			for yy := y0; yy < max(y1, y0+1); yy++ {
				for xx := x0; xx < max(x1, x0+1); xx++ {
					r, g, bl, _ := img.At(xx, yy).RGBA()
					s += 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(bl)
					n++
				}
			}
			l := s / float64(n) / 257
			lum[y*w+x] = l
		}
	}
	walk := func(x, y int) bool { return x >= 0 && y >= 0 && x < w && y < h && lum[y*w+x] > mmWalkLum }
	d := mmDots{w, h, make([]bool, w*h)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			d.on[y*w+x] = walk(x, y) && !(walk(x-1, y) && walk(x+1, y) && walk(x, y-1) && walk(x, y+1))
		}
	}
	return d
}

var mmBg struct {
	sync.Mutex
	img   image.Image
	load  ddAsync
	cache map[[2]int]mmDots
	last  [2]int // size of the newest cache entry
}

// mmMapImg returns the map image, or nil while it is still loading (or offline). The image
// is fetched in the background (retried with backoff while offline) and never blocks the
// caller.
func mmMapImg() image.Image {
	mmBg.Lock()
	img := mmBg.img
	mmBg.Unlock()
	if img != nil {
		return img
	}
	img = mmBg.load.get(func(ctx context.Context) (image.Image, error) {
		v, err := ddLatestVersion(ctx)
		if err != nil {
			return nil, err
		}
		return ddImage(ctx, v, "img/map/map11.png")
	})
	if img != nil {
		mmBg.Lock()
		mmBg.img = img
		mmBg.Unlock()
	}
	return img
}

// mmBackground returns the dot grid for a w*h dot canvas without blocking; until the map
// image arrives (or if offline) the fallback is used. Only the two newest sizes are kept.
func mmBackground(w, h int) mmDots {
	img := mmMapImg()
	if img == nil {
		return fallbackDots(w, h)
	}
	mmBg.Lock()
	defer mmBg.Unlock()
	k := [2]int{w, h}
	if d, ok := mmBg.cache[k]; ok {
		return d
	}
	if len(mmBg.cache) >= 2 {
		prev := mmBg.cache[mmBg.last]
		mmBg.cache = map[[2]int]mmDots{mmBg.last: prev}
	}
	if mmBg.cache == nil {
		mmBg.cache = map[[2]int]mmDots{}
	}
	d := mapDots(img, w, h)
	mmBg.cache[k], mmBg.last = d, k
	return d
}

// ---------------- panel ----------------

func minimapPanel(markers []mmMarker, status string, t, h int) []line {
	cols, rows := mmGeometry(t, h)
	return renderMinimap(markers, status, t, h, mmBackground(cols*2, rows*4))
}

// mmGeometry sizes the map area in cells: terminal cells are ~1:2, so rows ≈ cols/2;
// one row is reserved for the title.
func mmGeometry(t, h int) (cols, rows int) {
	cols = max(8, t-4)
	rows = max(2, cols/2)
	if rows > h-1 {
		rows = max(2, h-1)
	}
	cols = min(cols, rows*2)
	return cols, rows
}

const brailleBits = "\x01\x08\x02\x10\x04\x20\x40\x80" // dot (dx,dy) bit, index dy*2+dx

func renderMinimap(markers []mmMarker, status string, t, h int, bg mmDots) []line {
	W := t - 4
	cols, rows := mmGeometry(t, h)
	if bg.w != cols*2 || bg.h != rows*4 {
		bg = fallbackDots(cols*2, rows*4)
	}
	cells := make([][]seg, rows)
	for y := range cells {
		cells[y] = make([]seg, cols)
		for x := range cells[y] {
			var bits rune
			for dy := 0; dy < 4; dy++ {
				for dx := 0; dx < 2; dx++ {
					if bg.at(x*2+dx, y*4+dy) {
						bits |= rune(brailleBits[dy*2+dx])
					}
				}
			}
			cells[y][x] = C(string(0x2800+bits), cGray+cDim)
		}
	}
	for _, m := range placeMarkers(markers, cols, rows) {
		letter := cBold + cCyan
		if m.team == 200 {
			letter = cBold + cRed
		}
		if m.state != mmIconLive {
			letter = cDim + letter[len(cBold):]
		}
		for i, r := range []rune(m.label) {
			style := letter
			if i > 0 {
				style = cDim // the countdown digits
			}
			cells[m.y][m.x+i] = C(string(r), style)
		}
	}

	out := []line{borderedL(line{C(trim(status, W), cDim)}, t)}
	pad := (W - cols) / 2
	for _, row := range cells {
		l := line{S(strings.Repeat(" ", pad))}
		l = append(l, mergeSegs(row)...)
		out = append(out, borderedL(l, t))
	}
	return out
}

func mergeSegs(row []seg) line {
	var l line
	for _, s := range row {
		if n := len(l); n > 0 && l[n-1].color == s.color {
			l[n-1].text += s.text
		} else {
			l = append(l, s)
		}
	}
	return l
}

type placed struct {
	team  int
	label string // the letter, then the countdown digits of a dead champion
	x, y  int
	state mmIconState
}

// mmCountText is the countdown of a marker: the seconds until a dead champion respawns
// (".." when it is due), the seconds since a stale one was last seen (nothing when it was
// just seen), nothing for a live one. At most kittyMaxDigits characters.
func mmCountText(state mmIconState, secs int) string {
	switch {
	case state == mmIconDead && secs <= 0:
		return ".."
	case state != mmIconLive && secs > 0:
		return strconv.Itoa(min(secs, 999))
	}
	return ""
}

// placeMarkers maps normalized positions to cells (one-rune labels). Markers are processed
// in (team, label, position) order; each takes the nearest free spot within ±1 row and
// ±1 column of the cell its position falls in, so placement is deterministic and rarely
// overlaps. A dead champion's label is its lower-case letter, followed by the countdown in
// the cells to its right when they are all still free after every letter has been placed.
func placeMarkers(markers []mmMarker, cols, rows int) []placed {
	ms := append([]mmMarker(nil), markers...)
	sort.SliceStable(ms, func(i, j int) bool {
		a, b := ms[i], ms[j]
		switch {
		case a.Team != b.Team:
			return a.Team < b.Team
		case a.Label != b.Label:
			return a.Label < b.Label
		case a.X != b.X:
			return a.X < b.X
		}
		return a.Y < b.Y
	})
	taken := map[[2]int]bool{}
	free := func(x, y int) bool {
		return x >= 0 && y >= 0 && x < cols && y < rows && !taken[[2]int{x, y}]
	}
	out := make([]placed, 0, len(ms))
	for _, m := range ms {
		x := min(max(int(m.X*float64(cols)), 0), cols-1)
		y := min(max(int(m.Y*float64(rows)), 0), rows-1)
		bx, by := x, y
		best := -1
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				if !free(x+dx, y+dy) {
					continue
				}
				if d := abs(dx) + abs(dy); best < 0 || d < best {
					best, bx, by = d, x+dx, y+dy
				}
			}
		}
		taken[[2]int{bx, by}] = true
		_, n := utf8.DecodeRuneInString(m.Label)
		label := m.Label[:n]
		if m.State == mmIconDead {
			label = strings.ToLower(label)
		}
		out = append(out, placed{m.Team, label, bx, by, m.State})
	}
	for i, m := range ms {
		if m.State != mmIconDead {
			continue
		}
		text := mmCountText(m.State, m.Secs)
		fits := true
		for j := range text {
			fits = fits && free(out[i].x+1+j, out[i].y)
		}
		if !fits {
			continue
		}
		for j := range text {
			taken[[2]int{out[i].x + 1 + j, out[i].y}] = true
		}
		out[i].label += text
	}
	return out
}

// mmPanelView builds the panel for the view's mode. The slot is non-nil only in kitty mode
// and its Row is relative to the first returned line.
func mmPanelView(v mmView, t, h int) ([]line, *mmSlot) {
	switch v.Mode {
	case mmModeKitty:
		cols, rows := mmGeometry(t, h)
		W := t - 4
		pad := (W - cols) / 2
		out := []line{borderedL(line{C(trim(v.Status, W), cDim)}, t)}
		for i := 0; i < rows; i++ {
			out = append(out, borderedL(line{S(strings.Repeat(" ", W))}, t))
		}
		return out, &mmSlot{Row: 1, Col: 2 + pad, Cols: cols, Rows: rows, CellW: v.CellW, CellH: v.CellH, Icons: v.Icons, Ver: v.Ver}
	case mmModeBlocks:
		return renderMinimapBlocks(v.Markers, v.Status, t, h), nil
	}
	return minimapPanel(v.Markers, v.Status, t, h), nil
}

// mmBlocks caches the marker-free half-block map of the newest geometry: the map pixels
// only change with the map image or the panel size, so each tick reuses them.
var mmBlocks struct {
	sync.Mutex
	bg            *image.RGBA // scaled map the entry was built from (nil: dark fill)
	cols, rows, t int
	img           *image.RGBA
	cells         [][]seg
	lines         []line // bordered rows without markers
}

// mmBlockBase returns the marker-free map pixels, cells and bordered rows, building them
// only when the map image or the geometry changed. The result is shared: callers must not
// modify it.
func mmBlockBase(bg *image.RGBA, cols, rows, t int) (*image.RGBA, [][]seg, []line) {
	mmBlocks.Lock()
	defer mmBlocks.Unlock()
	b := &mmBlocks
	if b.img == nil || b.bg != bg || b.cols != cols || b.rows != rows || b.t != t {
		img := mmBackdrop(cols, rows*2, bg)
		cells := mmBlockCells(img)
		pad := S(strings.Repeat(" ", (t-4-cols)/2))
		lines := make([]line, len(cells))
		for i, row := range cells {
			l := borderedL(append(line{pad}, mergeSegs(row)...), t)
			lines[i] = l[:len(l):len(l)]
		}
		b.bg, b.cols, b.rows, b.t, b.img, b.cells, b.lines = bg, cols, rows, t, img, cells, lines
	}
	return b.img, b.cells, b.lines
}

// renderMinimapBlocks draws the map as truecolor half-block pixels with role letters on top.
func renderMinimapBlocks(markers []mmMarker, status string, t, h int) []line {
	W := t - 4
	cols, rows := mmGeometry(t, h)
	img, cells, base := mmBlockBase(mmScaledMap(mmMapImg(), cols, rows*2), cols, rows, t)
	labelled := map[int][]seg{}
	for _, m := range placeMarkers(markers, cols, rows) {
		row, ok := labelled[m.y]
		if !ok {
			row = append([]seg(nil), cells[m.y]...)
			labelled[m.y] = row
		}
		i := 0
		for _, r := range m.label {
			row[m.x+i] = C(string(r), mmBlockLabel(img, m.x+i, m.y, m.team, m.state, i > 0))
			i++
		}
	}
	out := make([]line, 1, rows+1)
	out[0] = borderedL(line{C(trim(status, W), cDim)}, t)
	pad := S(strings.Repeat(" ", (W-cols)/2))
	for y := 0; y < rows; y++ {
		if row, ok := labelled[y]; ok {
			out = append(out, borderedL(append(line{pad}, mergeSegs(row)...), t))
		} else {
			out = append(out, base[y])
		}
	}
	return out
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
