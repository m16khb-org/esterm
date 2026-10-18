package main

import (
	"math"
	"sync"
)

// The level box left of a health bar shows the champion's level in a small bold font. It is
// read by template matching against windows learned from labelled broadcast frames
// (mainview_levels.go), one per level and digit style (the broadcasts differ: Worlds and
// LEC, LCK, LPL), resampled to the scale the bar is drawn at.

const (
	mvLevelMax = 20 // highest level with a template
	mvFonts    = 3  // digit styles: Worlds/LEC, LCK, LPL
	mvWinW     = 17 // digit window at the reference bar size: columns bar-21 .. bar-4
	mvWinH     = 13 // rows bar+1 .. bar+14
	mvWinX     = 21 // window left edge, columns left of the bar
	mvWinY     = 1  // window top edge, rows below the bar's top
)

// mvLevelWin is the digit window set of one scale: zero-mean unit-norm windows.
type mvLevelWin struct {
	w, h   int
	ox, oy int
	tpl    [mvFonts][mvLevelMax][]float32
}

var mvLevelCache sync.Map // int(scale*1000) -> *mvLevelWin

func mvLevelTemplates(sb float64) *mvLevelWin {
	key := mvRound(sb * 1000)
	if v, ok := mvLevelCache.Load(key); ok {
		return v.(*mvLevelWin)
	}
	t := &mvLevelWin{w: max(3, mvRound(mvWinW*sb)), h: max(3, mvRound(mvWinH*sb)), ox: mvRound(mvWinX * sb), oy: mvRound(mvWinY * sb)}
	for f := 0; f < mvFonts; f++ {
		for l := 0; l < mvLevelMax; l++ {
			t.tpl[f][l] = mvResampleWin(&mvLevelTpl[f][l], sb, t.w, t.h)
		}
	}
	v, _ := mvLevelCache.LoadOrStore(key, t)
	return v.(*mvLevelWin)
}

// mvResampleWin area-averages a reference-size window onto the grid of scale sb and
// normalizes it.
func mvResampleWin(src *[mvWinW * mvWinH]uint8, sb float64, tw, th int) []float32 {
	v := make([]float32, tw*th)
	for y := 0; y < th; y++ {
		ya, yb := float64(y)/sb, float64(y+1)/sb
		for x := 0; x < tw; x++ {
			xa, xb := float64(x)/sb, float64(x+1)/sb
			var sum, wt float64
			for yy := int(ya); float64(yy) < yb && yy < mvWinH; yy++ {
				wy := math.Min(yb, float64(yy+1)) - math.Max(ya, float64(yy))
				for xx := int(xa); float64(xx) < xb && xx < mvWinW; xx++ {
					w := wy * (math.Min(xb, float64(xx+1)) - math.Max(xa, float64(xx)))
					sum += w * float64(src[yy*mvWinW+xx])
					wt += w
				}
			}
			if wt > 0 {
				v[y*tw+x] = float32(sum / wt)
			}
		}
	}
	mvNormalize(v)
	return v
}

// mvNormalize makes v zero-mean and unit-norm; a flat window becomes all zero.
func mvNormalize(v []float32) {
	var m float32
	for _, x := range v {
		m += x
	}
	m /= float32(len(v))
	var n float32
	for i := range v {
		v[i] -= m
		n += v[i] * v[i]
	}
	if n < 1e-6 {
		for i := range v {
			v[i] = 0
		}
		return
	}
	n = float32(math.Sqrt(float64(n)))
	for i := range v {
		v[i] /= n
	}
}

// mvReadLevel reads the level of the bar whose template-aligned position is (bx, by) on the
// luminance plane lum (w px wide, h rows), the bar drawn at sb times the reference size. It
// tries the window one pixel to either side and reports the best level with its
// correlation and the correlation lead over the best other level; level 0: no window.
func mvReadLevel(lum []uint8, w, h, bx, by int, sb float64) (level int, corr, lead float32) {
	t := mvLevelTemplates(sb)
	y0 := by + t.oy
	var buf [1024]float32
	win := buf[:0]
	if n := t.w * t.h; n <= len(buf) {
		win = buf[:n]
	} else {
		win = make([]float32, n)
	}
	var score [mvLevelMax]float32
	for l := range score {
		score[l] = -2
	}
	seen := false
	for dx := -1; dx <= 1; dx++ {
		x0 := bx - t.ox + dx
		if x0 < 0 || y0 < 0 || x0+t.w > w || y0+t.h > h {
			continue
		}
		seen = true
		for y := 0; y < t.h; y++ {
			for x := 0; x < t.w; x++ {
				win[y*t.w+x] = float32(lum[(y0+y)*w+x0+x])
			}
		}
		mvNormalize(win)
		for f := 0; f < mvFonts; f++ {
			for l := 0; l < mvLevelMax; l++ {
				var c float32
				for i, v := range t.tpl[f][l] {
					c += v * win[i]
				}
				if c > score[l] {
					score[l] = c
				}
			}
		}
	}
	if !seen {
		return 0, 0, 0
	}
	best, second := float32(-2), float32(-2)
	for l, c := range score {
		if c > best {
			second, best, level = best, c, l+1
		} else if c > second {
			second = c
		}
	}
	return level, best, best - second
}
