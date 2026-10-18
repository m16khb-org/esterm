package main

import (
	"image"
	"math"
	"sync"
)

// The camera rectangle the broadcast draws on the minimap: a thin white 16:9 frame,
// ~108x61 px on the 280 px Worlds/LEC map, ~78x42 px on LCK's. The map border clips it at
// the map's edge and champion icons or fight effects cover parts of it, so four clean
// edges are rare: the finder accepts three, and with a remembered size even two.

// mvView is the camera viewport in normalized map coordinates (0..1, origin top left, the
// coordinates of mmDet.X/Y). An edge clipped by the map border is extrapolated from the
// size, so X0 < 0 or X1 > 1 happen. With OK false nothing was found.
type mvView struct {
	X0, Y0, X1, Y1 float64
	OK             bool
}

func (v mvView) w() float64 { return v.X1 - v.X0 }
func (v mvView) h() float64 { return v.Y1 - v.Y0 }

const (
	mvLineMin    = 150 // lowest channel value of a viewport line pixel
	mvLineSpread = 45  // largest channel spread of a viewport line pixel
	mvLineGap    = 2   // gap along a line that is bridged
	mvAspectLo   = 1.5 // accepted viewport aspect range (w/h)
	mvAspectHi   = 2.15
	mvAspectMid  = 1.78
	mvViewWLo    = 0.18 // accepted viewport width as a share of the map
	mvViewWHi    = 0.62
)

type mvViewScratch struct {
	hp, vp     []int32 // prefix counts: row y (w+1 per row) / column x (h+1 per column)
	hm, vm     []uint8 // line pixels, bridged along x / along y
	hlen, vlen []int32 // longest bridged run in each row / column
	cx, cy     []int   // candidate line columns / rows
}

var mvViewPool = sync.Pool{New: func() any { return new(mvViewScratch) }}

// mvEdges is how much of each edge of a rectangle is line.
type mvEdges struct {
	cov [4]float64 // top, bottom, left, right
	n   [4]int     // visible length (inside the image), px
}

func (s *mvViewScratch) hcov(w, h, y, a, b int) (float64, int) {
	a, b = max(a, 0), min(b, w-1)
	if b < a || y < -1 || y > h {
		return 0, 0
	}
	best := int32(0)
	for dy := -1; dy <= 1; dy++ {
		if yy := y + dy; yy >= 0 && yy < h {
			if c := s.hp[yy*(w+1)+b+1] - s.hp[yy*(w+1)+a]; c > best {
				best = c
			}
		}
	}
	return float64(best) / float64(b-a+1), b - a + 1
}

func (s *mvViewScratch) vcov(w, h, x, a, b int) (float64, int) {
	a, b = max(a, 0), min(b, h-1)
	if b < a || x < -1 || x > w {
		return 0, 0
	}
	best := int32(0)
	for dx := -1; dx <= 1; dx++ {
		if xx := x + dx; xx >= 0 && xx < w {
			if c := s.vp[xx*(h+1)+b+1] - s.vp[xx*(h+1)+a]; c > best {
				best = c
			}
		}
	}
	return float64(best) / float64(b-a+1), b - a + 1
}

// edges measures the rectangle with left/top edge at (xl, yt) and size (rw, rh): lines at
// pixel columns xl, xl+rw and rows yt, yt+rh.
func (s *mvViewScratch) edges(w, h, xl, yt, rw, rh int) mvEdges {
	var e mvEdges
	e.cov[0], e.n[0] = s.hcov(w, h, yt, xl, xl+rw)
	e.cov[1], e.n[1] = s.hcov(w, h, yt+rh, xl, xl+rw)
	e.cov[2], e.n[2] = s.vcov(w, h, xl, yt, yt+rh)
	e.cov[3], e.n[3] = s.vcov(w, h, xl+rw, yt, yt+rh)
	return e
}

// support scores the rectangle: lengths of present edges; it reports the number of
// present edges and the share of the visible perimeter that is line.
func (e mvEdges) support(minLen int) (score float64, present int, share float64) {
	var vis, got float64
	for i := 0; i < 4; i++ {
		if e.n[i] < minLen {
			continue
		}
		vis += float64(e.n[i])
		if e.cov[i] >= 0.6 {
			present++
			got += e.cov[i] * float64(e.n[i])
		}
	}
	if vis == 0 {
		return 0, 0, 0
	}
	return got, present, got / vis
}

// lineCands returns the positions of local maxima of the run-length profile at least
// min long.
func lineCands(prof []int32, min int32, dst []int) []int {
	dst = dst[:0]
	for i, v := range prof {
		if v < min {
			continue
		}
		if i > 0 && prof[i-1] > v {
			continue
		}
		if i+1 < len(prof) && prof[i+1] >= v {
			continue
		}
		dst = append(dst, i)
	}
	return dst
}

// mvFindViewport finds the camera rectangle on mapCrop, the minimap square (any size).
// prev is the last view found or the zero value; its size narrows the search and lets a
// rectangle with only two edges visible (the other two clipped or covered) be placed.
func mvFindViewport(mapCrop *image.RGBA, prev mvView) mvView {
	b := mapCrop.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 32 || h < 32 {
		return mvView{}
	}
	s := mvViewPool.Get().(*mvViewScratch)
	defer mvViewPool.Put(s)
	if cap(s.hp) < h*(w+1) {
		s.hp = make([]int32, h*(w+1))
	}
	if cap(s.vp) < w*(h+1) {
		s.vp = make([]int32, w*(h+1))
	}
	if cap(s.hm) < w*h {
		s.hm = make([]uint8, w*h)
		s.vm = make([]uint8, w*h)
	}
	s.hp, s.vp, s.hm, s.vm = s.hp[:h*(w+1)], s.vp[:w*(h+1)], s.hm[:w*h], s.vm[:w*h]
	if cap(s.hlen) < h {
		s.hlen = make([]int32, h)
	}
	if cap(s.vlen) < w {
		s.vlen = make([]int32, w)
	}
	s.hlen, s.vlen = s.hlen[:h], s.vlen[:w]
	for y := 0; y < h; y++ {
		row := mapCrop.Pix[y*mapCrop.Stride:]
		m := s.hm[y*w : (y+1)*w]
		for x := 0; x < w; x++ {
			r, g, bl := int(row[4*x]), int(row[4*x+1]), int(row[4*x+2])
			lo, hi := min(r, g, bl), max(r, g, bl)
			if lo >= mvLineMin && hi-lo <= mvLineSpread {
				m[x] = 1
			} else {
				m[x] = 0
			}
		}
	}
	copy(s.vm, s.hm)
	// bridge gaps along x in hm and along y in vm; prefix sums and longest runs
	for y := 0; y < h; y++ {
		m := s.hm[y*w : (y+1)*w]
		last := -100
		for x := 0; x < w; x++ {
			if m[x] != 0 {
				if last >= 0 && x-last > 1 && x-last <= mvLineGap+1 {
					for k := last + 1; k < x; k++ {
						m[k] = 1
					}
				}
				last = x
			}
		}
		var run, longest int32
		base := y * (w + 1)
		s.hp[base] = 0
		for x := 0; x < w; x++ {
			s.hp[base+x+1] = s.hp[base+x] + int32(m[x])
			if m[x] != 0 {
				if run++; run > longest {
					longest = run
				}
			} else {
				run = 0
			}
		}
		s.hlen[y] = longest
	}
	for x := 0; x < w; x++ {
		last := -100
		for y := 0; y < h; y++ {
			if s.vm[y*w+x] != 0 {
				if last >= 0 && y-last > 1 && y-last <= mvLineGap+1 {
					for k := last + 1; k < y; k++ {
						s.vm[k*w+x] = 1
					}
				}
				last = y
			}
		}
		var run, longest int32
		base := x * (h + 1)
		s.vp[base] = 0
		for y := 0; y < h; y++ {
			v := s.vm[y*w+x]
			s.vp[base+y+1] = s.vp[base+y] + int32(v)
			if v != 0 {
				if run++; run > longest {
					longest = run
				}
			} else {
				run = 0
			}
		}
		s.vlen[x] = longest
	}

	fw := float64(w)
	wLo, wHi := int(mvViewWLo*fw), int(mvViewWHi*fw)
	pw, ph := 0, 0
	if prev.OK {
		pw, ph = mvRound(prev.w()*fw), mvRound(prev.h()*float64(h))
	}
	minLine := int32(math.Max(10, 0.12*fw))
	s.cx = lineCands(s.vlen, minLine, s.cx)
	s.cy = lineCands(s.hlen, minLine, s.cy)

	type cand struct {
		xl, yt, rw, rh int
		score          float64
	}
	best := cand{score: -1}
	try := func(xl, yt, rw, rh int) {
		if rw < wLo || rw > wHi || rh <= 0 {
			return
		}
		if asp := float64(rw) / float64(rh); asp < mvAspectLo || asp > mvAspectHi {
			return
		}
		e := s.edges(w, h, xl, yt, rw, rh)
		score, present, share := e.support(8)
		need := 3
		sizeOK := prev.OK && absInt(rw-pw) <= max(4, pw/8) && absInt(rh-ph) <= max(4, ph/8)
		if sizeOK {
			need = 2
			score += 20 - float64(absInt(rw-pw)+absInt(rh-ph))
		}
		if present < need || share < 0.6 {
			return
		}
		score += float64(present)*6 - 10*math.Abs(float64(rw)/float64(rh)-mvAspectMid)
		if score > best.score {
			best = cand{xl, yt, rw, rh, score}
		}
	}
	// widths from vertical line pairs, heights from horizontal line pairs
	for ia, xa := range s.cx {
		for _, xb := range s.cx[ia+1:] {
			rw := xb - xa
			if rw < wLo || rw > wHi {
				continue
			}
			for jb, ya := range s.cy {
				for _, yb := range s.cy[jb+1:] {
					try(xa, ya, rw, yb-ya)
				}
				// one horizontal line: the height follows from the aspect or the last view
				for _, rh := range heightsFor(rw, pw, ph) {
					try(xa, ya, rw, rh)
					try(xa, ya-rh, rw, rh)
				}
			}
		}
	}
	// one vertical line, two horizontal: the width follows from the aspect or the last view
	for jb, ya := range s.cy {
		for _, yb := range s.cy[jb+1:] {
			rh := yb - ya
			for _, rw := range widthsFor(rh, pw, ph) {
				for _, xv := range s.cx {
					try(xv, ya, rw, rh)
					try(xv-rw, ya, rw, rh)
				}
			}
		}
	}
	// two edges only (one horizontal, one vertical) with the remembered size
	if prev.OK {
		for _, ya := range s.cy {
			for _, xv := range s.cx {
				for _, dx := range [2]int{0, -pw} {
					for _, dy := range [2]int{0, -ph} {
						try(xv+dx, ya+dy, pw, ph)
					}
				}
			}
		}
	}
	if best.score < 0 {
		return mvView{}
	}
	fh := float64(h)
	return mvView{
		X0: float64(best.xl) / fw, X1: float64(best.xl+best.rw) / fw,
		Y0: float64(best.yt) / fh, Y1: float64(best.yt+best.rh) / fh,
		OK: true,
	}
}

// heightsFor lists the heights a rectangle of width rw may have: around the last view's
// aspect when there is one, else every height of the accepted aspect range.
func heightsFor(rw, pw, ph int) []int {
	lo, hi := mvRound(float64(rw)/mvAspectHi), mvRound(float64(rw)/mvAspectLo)
	if ph > 0 && pw > 0 {
		c := mvRound(float64(rw) * float64(ph) / float64(pw))
		lo, hi = max(lo, c-3), min(hi, c+3)
	}
	var hs []int
	for h := lo; h <= hi; h++ {
		hs = append(hs, h)
	}
	return hs
}

func widthsFor(rh, pw, ph int) []int {
	lo, hi := mvRound(float64(rh)*mvAspectLo), mvRound(float64(rh)*mvAspectHi)
	if ph > 0 && pw > 0 {
		c := mvRound(float64(rh) * float64(pw) / float64(ph))
		lo, hi = max(lo, c-3), min(hi, c+3)
	}
	var ws []int
	for w := lo; w <= hi; w++ {
		ws = append(ws, w)
	}
	return ws
}
