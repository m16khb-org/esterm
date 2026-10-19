package main

// Main-view observations: where champions are on the broadcast's main camera, found from
// the health bars drawn above them. Pure functions on decoded images, no I/O:
//
//	mvFindBars      health bars (team, fill, level) in the main view image
//	mvFindViewport  the camera rectangle drawn on the minimap
//	mvToMap         bar -> champion position on the map
//	mvAssign        bars -> the feed's champions
//	mvPlates        nameplates above the bars, clustered by look and tied to champions (mainview_plate.go)
//
// The bar finder is a port of a spike measured on 130 labelled live frames of Worlds, LEC,
// LCK and LPL (precision 0.98, recall 0.91 at full scale): team-coloured run-length scan for
// the bar fill, then a learned luminance template of the level box and its frame rejects
// minions, objectives and ability bars.

import (
	"image"
	"math"
	"sort"
	"sync"
)

const (
	mvBarW  = 108.0 // health bar width in 1080p px
	mvTileH = 29    // template tile: rows from the bar top -5 ...
	mvTileW = 34    // ... columns from the bar left -26
	mvTileX = 26
	mvTileY = 5

	// The minimap and the objective panel above it cover the bottom right of the main view.
	mvExclX, mvExclY = 1640, 640

	mvScoreMax = 0.90 // template score above which a candidate is no health bar
	mvMinFill  = 20.0 // shortest accepted bar fill, 1080p px

	mvFrameW, mvFrameH = 1920.0, 1080.0

	mvLevelMinCorr = 0.5 // lowest window correlation accepted as a level
)

// mvBar is one health bar found in the main view.
type mvBar struct {
	Team   int     // 0 blue, 1 red
	X, Y   float64 // bar centre, source pixels of the 1920x1080 frame
	HPFrac float64 // filled share of the bar, 0..1
	Level  int     // level read from the box left of the bar, 0 unknown
	LevelC float64 // 0..1 confidence of Level: correlation lead over the next best level
	Conf   float64 // 0..1, from the template score
	Plate  []uint8 // the nameplate window above the bar (mvPlateOf), nil when it does not fit
}

// mvBarFactor is the size of a league's health bars relative to the reference (Worlds,
// LEC, LCK). LPL draws them at 0.88.
func mvBarFactor(layout string) float64 {
	if layout == "lpl" {
		return 0.88
	}
	return 1
}

func mvRound(v float64) int { return int(math.Floor(v + 0.5)) }

// ---- template ---------------------------------------------------------------------

// mvTpl is the bar template at one scale: the mean and 1/(std+8) of the luminance tile
// around the level box and the start of the bar, in two variants (normal frame, purple
// Baron-buff frame). The cells inside the level box (the digits) are left out.
type mvTpl struct {
	th, tw int
	ox, oy int // tile origin relative to the bar's left column and top row
	cells  []int32
	ta, ia []float32 // per cell, indexed like cells
	tb, ib []float32
}

var mvTplCache sync.Map // int(scale*1000) -> *mvTpl

func mvTemplate(s float64) *mvTpl {
	key := mvRound(s * 1000)
	if v, ok := mvTplCache.Load(key); ok {
		return v.(*mvTpl)
	}
	t := mvBuildTpl(s)
	v, _ := mvTplCache.LoadOrStore(key, t)
	return v.(*mvTpl)
}

// mvResample returns the canonical tile (value/8) area-averaged onto the tile grid of
// scale s, with the share of each cell that lies inside the canonical tile.
func mvResample(src *[mvTileH * mvTileW]uint16, s float64, th, tw, oy, ox int) (out, cover []float32) {
	out = make([]float32, th*tw)
	cover = make([]float32, th*tw)
	for r := 0; r < th; r++ {
		ya := (float64(r-oy))/s + mvTileY
		yb := (float64(r-oy+1))/s + mvTileY
		for c := 0; c < tw; c++ {
			xa := (float64(c-ox))/s + mvTileX
			xb := (float64(c-ox+1))/s + mvTileX
			var sum, wt float64
			for yy := int(math.Floor(ya)); float64(yy) < yb; yy++ {
				if yy < 0 || yy >= mvTileH {
					continue
				}
				wy := math.Min(yb, float64(yy+1)) - math.Max(ya, float64(yy))
				for xx := int(math.Floor(xa)); float64(xx) < xb; xx++ {
					if xx < 0 || xx >= mvTileW {
						continue
					}
					w := wy * (math.Min(xb, float64(xx+1)) - math.Max(xa, float64(xx)))
					sum += w * float64(src[yy*mvTileW+xx]) / 8
					wt += w
				}
			}
			area := (yb - ya) * (xb - xa)
			if wt > 0 {
				out[r*tw+c] = float32(sum / wt)
				cover[r*tw+c] = float32(wt / area)
			}
		}
	}
	return out, cover
}

func mvBuildTpl(s float64) *mvTpl {
	t := &mvTpl{ox: mvRound(mvTileX * s), oy: mvRound(mvTileY * s)}
	t.tw, t.th = mvRound(mvTileW*s), mvRound(mvTileH*s)
	ma, cov := mvResample(&mvTplMeanA, s, t.th, t.tw, t.oy, t.ox)
	sa, _ := mvResample(&mvTplStdA, s, t.th, t.tw, t.oy, t.ox)
	mb, _ := mvResample(&mvTplMeanB, s, t.th, t.tw, t.oy, t.ox)
	sb, _ := mvResample(&mvTplStdB, s, t.th, t.tw, t.oy, t.ox)
	for r := 0; r < t.th; r++ {
		for c := 0; c < t.tw; c++ {
			i := r*t.tw + c
			if cov[i] < 0.5 {
				continue
			}
			// canonical coordinates of the cell centre; the level digits are masked out
			cy := (float64(r-t.oy)+0.5)/s + mvTileY
			cx := (float64(c-t.ox)+0.5)/s + mvTileX
			if cy >= 8 && cy < 18 && cx >= 6 && cx < 21 {
				continue
			}
			t.cells = append(t.cells, int32(i))
			t.ta = append(t.ta, ma[i])
			t.ia = append(t.ia, 1/(sa[i]+8))
			t.tb = append(t.tb, mb[i])
			t.ib = append(t.ib, 1/(sb[i]+8))
		}
	}
	return t
}

// ---- scan -------------------------------------------------------------------------

type mvRun struct{ y, x0, x1, parent int32 }

type mvCand struct {
	team   int
	x, y   int // aligned bar left column and top row
	fill   int // filled columns, from the candidate's own left edge
	score  float32
	mx, my int // unaligned candidate position (fill is measured from it)
}

type mvScratch struct {
	lum      []uint8
	mask     [2][]uint8
	vr       []uint8
	runs     []mvRun
	rowStart []int32
	cands    []mvCand
	out      []mvCand
}

var mvPool = sync.Pool{New: func() any { return new(mvScratch) }}

func (s *mvScratch) size(n int) {
	if cap(s.lum) < n {
		s.lum = make([]uint8, n)
		s.mask[0] = make([]uint8, n)
		s.mask[1] = make([]uint8, n)
		s.vr = make([]uint8, n)
	}
	s.lum, s.mask[0], s.mask[1], s.vr = s.lum[:n], s.mask[0][:n], s.mask[1][:n], s.vr[:n]
}

// mvFindBars finds the health bars in img, the main view at scale of the 1080p frame
// (origin at the frame's top left; rows below the scoreboard may be left out). Bars sit
// at the reference size; see mvFindBarsF for leagues that draw them smaller.
func mvFindBars(img *image.RGBA, scale float64) []mvBar { return mvFindBarsF(img, scale, 1) }

// mvFindBarsF is mvFindBars for bars drawn at factor times the reference size (mvBarFactor).
// Smaller bars are found on the image enlarged to the reference bar size: the template
// was learned there, and shrinking the template instead loses much of its selectivity.
func mvFindBarsF(img *image.RGBA, scale, factor float64) []mvBar {
	if scale <= 0 || factor <= 0 {
		return nil
	}
	if factor < 1 {
		up := mvEnlarge(img, 1/factor)
		return mvDetect(up, scale/factor, scale)
	}
	return mvDetect(img, scale, scale*factor)
}

// mvEnlarge resamples img by f (>1) with bilinear interpolation.
func mvEnlarge(img *image.RGBA, f float64) *image.RGBA {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	nw, nh := int(float64(w)*f), int(float64(h)*f)
	out := image.NewRGBA(image.Rect(0, 0, nw, nh))
	xi := make([]int, nw)
	xw := make([]int, nw) // weight of the right neighbour, 0..256
	for x := 0; x < nw; x++ {
		sx := (float64(x)+0.5)/f - 0.5
		if sx < 0 {
			sx = 0
		}
		i := int(sx)
		if i >= w-1 {
			i, sx = w-1, float64(w-1)
		}
		xi[x], xw[x] = i, int((sx-float64(i))*256)
	}
	for y := 0; y < nh; y++ {
		sy := (float64(y)+0.5)/f - 0.5
		if sy < 0 {
			sy = 0
		}
		j := int(sy)
		if j >= h-1 {
			j, sy = h-1, float64(h-1)
		}
		wy := int((sy - float64(j)) * 256)
		r0 := img.Pix[j*img.Stride:]
		r1 := r0
		if j+1 < h {
			r1 = img.Pix[(j+1)*img.Stride:]
		}
		dst := out.Pix[y*out.Stride:]
		for x := 0; x < nw; x++ {
			i, wx := xi[x]*4, xw[x]
			r2 := i
			if xi[x]+1 < w {
				r2 = i + 4
			}
			for c := 0; c < 3; c++ {
				top := int(r0[i+c])*(256-wx) + int(r0[r2+c])*wx
				bot := int(r1[i+c])*(256-wx) + int(r1[r2+c])*wx
				dst[x*4+c] = uint8((top*(256-wy) + bot*wy) >> 16)
			}
			dst[x*4+3] = 255
		}
	}
	return out
}

// mvDetect finds the bars of an image at scale of the 1080p frame, whose bars are drawn at
// sb times the reference size.
func mvDetect(img *image.RGBA, scale, sb float64) []mvBar {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 64 || h < 64 || scale <= 0 {
		return nil
	}
	tpl := mvTemplate(sb)
	sc := mvPool.Get().(*mvScratch)
	defer mvPool.Put(sc)
	sc.size(w * h)
	exX, exY := int(mvExclX*scale), int(mvExclY*scale)
	for y := 0; y < h; y++ {
		row := img.Pix[y*img.Stride : y*img.Stride+w*4]
		lm := sc.lum[y*w : (y+1)*w]
		mb := sc.mask[0][y*w : (y+1)*w]
		mr := sc.mask[1][y*w : (y+1)*w]
		ex := y >= exY
		for x := 0; x < w; x++ {
			r, g, bl := int(row[x*4]), int(row[x*4+1]), int(row[x*4+2])
			lm[x] = uint8((77*r + 150*g + 29*bl) >> 8)
			var red, blue uint8
			if r >= 100 && r-g >= 45 && r-bl >= 35 {
				red = 1
			}
			if bl >= 110 && bl-r >= 40 && bl-g >= 10 && g >= r {
				blue = 1
			}
			if ex && x >= exX {
				red, blue = 0, 0
			}
			mb[x], mr[x] = blue, red
		}
	}
	sc.cands = sc.cands[:0]
	for team := 0; team < 2; team++ {
		mvTeamCands(sc, team, w, h, sb, tpl)
	}
	sort.Slice(sc.cands, func(i, j int) bool { return sc.cands[i].score < sc.cands[j].score })
	sc.out = sc.out[:0]
	dupY, dupX := int(math.Ceil(8*sb)), int(math.Ceil(60*sb))
	for _, c := range sc.cands {
		dup := false
		for _, k := range sc.out {
			if absInt(k.y-c.y) <= dupY && absInt(k.x-c.x) < dupX {
				dup = true
				break
			}
		}
		if !dup {
			sc.out = append(sc.out, c)
		}
	}
	bars := make([]mvBar, 0, len(sc.out))
	for _, c := range sc.out {
		lvl, corr, lead := mvReadLevel(sc.lum, w, h, c.x, c.y, sb)
		if corr < mvLevelMinCorr {
			lvl, lead = 0, 0
		}
		bars = append(bars, mvBar{
			Team:   c.team,
			X:      (float64(c.x) + mvBarW/2*sb) / scale,
			Y:      (float64(c.y) + mvTileY*sb) / scale,
			HPFrac: math.Min(1, float64(c.fill)/(mvBarW*sb)),
			Level:  lvl,
			LevelC: math.Max(0, math.Min(1, float64(lead)/0.3)),
			Conf:   math.Max(0, math.Min(1, (mvScoreMax-float64(c.score))/0.6)),
			Plate:  mvPlateOf(sc.lum, w, h, c.x, c.y),
		})
	}
	return bars
}

// mvTeamCands appends the validated bar candidates of one team colour to sc.cands.
func mvTeamCands(sc *mvScratch, team, w, h int, sb float64, tpl *mvTpl) {
	m := sc.mask[team]
	gapMax := max(2, mvRound(7*sb))
	// close horizontal gaps
	for y := 0; y < h; y++ {
		row := m[y*w : (y+1)*w]
		last := -100
		for x := 0; x < w; x++ {
			if row[x] != 0 {
				if last >= 0 && x-last > 1 && x-last <= gapMax {
					for k := last + 1; k < x; k++ {
						row[k] = 1
					}
				}
				last = x
			}
		}
	}
	// vertical run length going down from each pixel
	for x := 0; x < w; x++ {
		run := uint8(0)
		for y := h - 1; y >= 0; y-- {
			if m[y*w+x] != 0 {
				if run < 255 {
					run++
				}
			} else {
				run = 0
			}
			sc.vr[y*w+x] = run
		}
	}
	lo, hi := uint8(max(2, int(7*sb))), uint8(math.Ceil(15*sb))
	// candidate pixels: the top of a mask run of bar height; row runs of them
	sc.runs = sc.runs[:0]
	if cap(sc.rowStart) < h+1 {
		sc.rowStart = make([]int32, h+1)
	}
	sc.rowStart = sc.rowStart[:h+1]
	for y := 0; y < h; y++ {
		sc.rowStart[y] = int32(len(sc.runs))
		x := 0
		for x < w {
			i := y*w + x
			if v := sc.vr[i]; v >= lo && v <= hi && (y == 0 || m[i-w] == 0) {
				s := x
				for x < w {
					j := y*w + x
					if v2 := sc.vr[j]; v2 >= lo && v2 <= hi && (y == 0 || m[j-w] == 0) {
						x++
					} else {
						break
					}
				}
				sc.runs = append(sc.runs, mvRun{int32(y), int32(s), int32(x - 1), int32(len(sc.runs))})
			} else {
				x++
			}
		}
	}
	sc.rowStart[h] = int32(len(sc.runs))
	find := func(i int32) int32 {
		for sc.runs[i].parent != i {
			sc.runs[i].parent = sc.runs[sc.runs[i].parent].parent
			i = sc.runs[i].parent
		}
		return i
	}
	dyU, dxU := max(1, mvRound(4*sb)), max(1, mvRound(2*sb))
	for y := 0; y < h; y++ {
		for a := sc.rowStart[y]; a < sc.rowStart[y+1]; a++ {
			for yy := max(0, y-dyU); yy < y; yy++ {
				for b := sc.rowStart[yy]; b < sc.rowStart[yy+1]; b++ {
					if int(sc.runs[b].x1)+dxU >= int(sc.runs[a].x0) && int(sc.runs[a].x1)+dxU >= int(sc.runs[b].x0) {
						ra, rb := find(a), find(b)
						if ra != rb {
							sc.runs[ra].parent = rb
						}
					}
				}
			}
		}
	}
	// aggregate each component: left edge, row extent, median row
	type agg struct {
		minx, maxx, y0, y1 int
		hist               [24]uint8
	}
	var comps map[int32]*agg
	for i := range sc.runs {
		if comps == nil {
			comps = make(map[int32]*agg, 16)
		}
		r := find(int32(i))
		a := comps[r]
		if a == nil {
			a = &agg{minx: 1 << 30, y0: 1 << 30}
			comps[r] = a
		}
		ru := sc.runs[i]
		a.minx = min(a.minx, int(ru.x0))
		a.maxx = max(a.maxx, int(ru.x1))
		a.y0 = min(a.y0, int(ru.y))
		a.y1 = max(a.y1, int(ru.y))
	}
	for i := range sc.runs {
		ru := sc.runs[i]
		a := comps[find(int32(i))]
		if d := int(ru.y) - a.y0; d < len(a.hist) {
			a.hist[d]++
		}
	}
	minW, maxH := max(3, mvRound(8*sb)), int(math.Ceil(14*sb))
	minX, minY := mvRound(30*sb), mvRound(5*sb)
	adx, ady0, ady1 := int(math.Ceil(3*sb)), int(math.Ceil(4*sb)), int(math.Ceil(2*sb))
	for _, a := range comps {
		ww := a.maxx - a.minx + 1 + 4
		hh := a.y1 - a.y0 + 1 + 4
		if ww < minW || hh > maxH || a.y1-a.y0 >= len(a.hist) {
			continue
		}
		var tot, acc int
		for _, n := range a.hist[:a.y1-a.y0+1] {
			tot += int(n)
		}
		med := 0
		for d, n := range a.hist[:a.y1-a.y0+1] {
			acc += int(n)
			if 2*acc > tot {
				med = d
				break
			}
		}
		x0, y0 := a.minx, a.y0+med
		if x0 < minX || y0 < minY || y0+int(math.Ceil(24*sb)) >= h {
			continue
		}
		best := float32(9)
		bx, by := x0, y0
		for dy := -ady0; dy <= ady1; dy++ {
			for dx := -adx; dx <= adx; dx++ {
				if v := tpl.score(sc.lum, w, h, x0+dx, y0+dy, best); v < best {
					best, bx, by = v, x0+dx, y0+dy
				}
			}
		}
		if best > mvScoreMax {
			continue
		}
		// fill: mask run along the bar's middle row, bridging gaps up to 5 px
		row := min(h-1, y0+mvRound(6*sb))
		x, gap, last := x0, 0, x0
		gapFill := max(1, mvRound(5*sb))
		for x < w && x < x0+int(mvBarW*sb)+4 {
			if m[row*w+x] != 0 {
				last, gap = x, 0
			} else if gap++; gap > gapFill {
				break
			}
			x++
		}
		fill := last - x0 + 1
		if float64(fill) < mvMinFill*sb {
			continue
		}
		sc.cands = append(sc.cands, mvCand{team: team, x: bx, y: by, fill: fill, score: best, mx: x0, my: y0})
	}
}

// score is the mean normalised luminance deviation of the tile at bar position (x, y)
// from the template, the better of the two variants; best lets it stop early.
func (t *mvTpl) score(lum []uint8, w, h, x, y int, best float32) float32 {
	r0, c0 := y-t.oy, x-t.ox
	if r0 < 0 || c0 < 0 || r0+t.th > h || c0+t.tw > w {
		return 9
	}
	n := float32(len(t.cells))
	limit := best * n
	var sa, sb float32
	for k, ci := range t.cells {
		i := int(ci)
		p := float32(lum[(r0+i/t.tw)*w+c0+i%t.tw])
		da, db := p-t.ta[k], p-t.tb[k]
		if da < 0 {
			da = -da
		}
		if db < 0 {
			db = -db
		}
		sa += da * t.ia[k]
		sb += db * t.ib[k]
		if k&63 == 63 && sa > limit && sb > limit {
			return 9
		}
	}
	if sb < sa {
		sa = sb
	}
	return sa / n
}
