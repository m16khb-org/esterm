package main

import (
	"math/rand"
	"strconv"
	"testing"
)

// renderText draws text with font f into a mmRegionHUD buffer: the central adv columns of
// each glyph model (its window minus the margins that overlap the neighbours) become
// luminance, so consecutive cells tile exactly. The first window starts at area column
// x0 (the text box grown by mmTextPad).
func renderText(hud []byte, f *mmFont, box mmBox, text string, x0 int) {
	ax := box.X - mmTextPad - mmRegionHUD.X
	ay := box.Y - mmTextPad - mmRegionHUD.Y
	x := x0
	for _, r := range text {
		gl := f.glyphs[classOf(f, r)]
		margin := (gl.w - gl.adv) / 2
		for row := 0; row < f.h; row++ {
			for col := 0; col < gl.adv; col++ {
				v := byte(20 + gl.mu[row*gl.w+margin+col]*200)
				o := ((ay+f.top+row)*mmRegionHUD.W + ax + x + margin + col) * 3
				hud[o], hud[o+1], hud[o+2] = v, v, v
			}
		}
		x += gl.adv
	}
}

func classOf(f *mmFont, r rune) int {
	for i, g := range f.glyphs {
		if g.r == r {
			return i
		}
	}
	panic("glyph missing: " + string(r))
}

func darkHUD() []byte {
	hud := make([]byte, mmRegionHUD.W*mmRegionHUD.H*3)
	for i := range hud {
		hud[i] = 20
	}
	return hud
}

// renderHUD draws a full scoreboard of layout l: clock mm:ss and both counters.
func renderHUD(l *mmLayout, sec, blue, red int) []byte {
	f := mmFonts[l.Name]
	hud := darkHUD()
	clock := twoDigits(sec/60) + ":" + twoDigits(sec%60)
	renderText(hud, f.clock, l.Clock, clock, 8)
	renderCounter(hud, f, l.KillsBlue, blue)
	renderCounter(hud, f, l.KillsRed, red)
	return hud
}

// renderCounter draws a kill counter with the one-digit or the two-digit font.
func renderCounter(hud []byte, f mmLayoutFonts, box mmBox, n int) {
	if n < 10 {
		renderText(hud, f.kills, box, strconv.Itoa(n), 1)
	} else {
		renderText(hud, f.kills2, box, strconv.Itoa(n), 1)
	}
}

func twoDigits(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

func TestReadHUDRoundTrip(t *testing.T) {
	cases := []struct{ sec, blue, red int }{
		{0, 0, 0}, {75, 1, 2}, {754, 3, 5}, {1799, 10, 4}, {2312, 0, 12}, {3599, 11, 5},
		// a tens digit of 2 or more: wider than a lone digit and clipped by too narrow a box
		{1999, 20, 8}, {2400, 23, 5}, {1500, 9, 26}, {2100, 29, 21},
	}
	for _, l := range mmLayouts {
		for _, c := range cases {
			got := mmReadHUD(l, renderHUD(l, c.sec, c.blue, c.red))
			if !got.ClockOK || got.ClockSec != c.sec {
				t.Errorf("%s clock %d: got %+v", l.Name, c.sec, got)
			}
			if !got.KillsOK || got.Blue != c.blue || got.Red != c.red {
				t.Errorf("%s kills %d-%d: got %+v", l.Name, c.blue, c.red, got)
			}
		}
	}
}

func TestReadHUDRejectsForeignAndBroken(t *testing.T) {
	for _, l := range mmLayouts {
		hud := renderHUD(l, 754, 3, 5)
		for _, other := range mmLayouts {
			if other == l {
				continue
			}
			// The clock fonts differ between leagues. Worlds and LPL draw the kill counters in
			// the same font, so those may read - but never as different numbers.
			got := mmReadHUD(other, hud)
			if got.ClockOK || (got.KillsOK && (got.Blue != 3 || got.Red != 5)) {
				t.Errorf("%s HUD read with %s fonts: %+v", l.Name, other.Name, got)
			}
		}
		if got := mmReadHUD(l, darkHUD()); got.ClockOK || got.KillsOK {
			t.Errorf("%s: blank HUD read: %+v", l.Name, got)
		}
		rng := rand.New(rand.NewSource(1))
		noise := make([]byte, len(hud))
		for i := range noise {
			noise[i] = byte(rng.Intn(256))
		}
		if got := mmReadHUD(l, noise); got.ClockOK || got.KillsOK {
			t.Errorf("%s: noise read: %+v", l.Name, got)
		}
		if got := mmReadHUD(l, hud[:100]); got.ClockOK || got.KillsOK {
			t.Errorf("%s: truncated HUD read: %+v", l.Name, got)
		}
		// seconds above 59 and implausible kill totals are not a clock reading
		if got := mmReadHUD(l, renderHUD(l, 12*60+59, 61, 3)); got.KillsOK {
			t.Errorf("%s: 61 kills accepted: %+v", l.Name, got)
		}
	}
}

func TestReadHUDHalfCovered(t *testing.T) {
	// An overlay hiding part of the clock must not produce a different time.
	for _, l := range mmLayouts {
		hud := renderHUD(l, 754, 3, 5)
		for y := l.Clock.Y - mmRegionHUD.Y; y < l.Clock.Y-mmRegionHUD.Y+l.Clock.H; y++ {
			for x := l.Clock.X - mmRegionHUD.X + l.Clock.W/2; x < l.Clock.X-mmRegionHUD.X+l.Clock.W; x++ {
				o := (y*mmRegionHUD.W + x) * 3
				hud[o], hud[o+1], hud[o+2] = 20, 20, 20
			}
		}
		if got := mmReadHUD(l, hud); got.ClockOK {
			t.Errorf("%s: half-covered clock read as %d", l.Name, got.ClockSec)
		}
	}
}

// renderBorder paints l's stored border profile around its nominal map box into a
// mmRegionMM buffer.
func renderBorder(l *mmLayout) []byte {
	mm := make([]byte, mmRegionMM.W*mmRegionMM.H*3)
	ref := mmBorders[l.Name]
	bx, by := l.Map.X-mmRegionMM.X, l.Map.Y-mmRegionMM.Y
	set := func(x, y int, c [3]float32) {
		if x < 0 || y < 0 || x >= mmRegionMM.W || y >= mmRegionMM.H {
			return
		}
		o := (y*mmRegionMM.W + x) * 3
		mm[o], mm[o+1], mm[o+2] = byte(c[0]), byte(c[1]), byte(c[2])
	}
	for d := 1; d <= mmBorderDepth; d++ {
		for i := 0; i < l.Map.W; i++ {
			set(bx+i, by-d, ref[0][d-1])
			set(bx+i, by+l.Map.H-1+d, ref[2][d-1])
		}
		for i := 0; i < l.Map.H; i++ {
			set(bx+l.Map.W-1+d, by+i, ref[1][d-1])
			set(bx-d, by+i, ref[3][d-1])
		}
	}
	return mm
}

func TestDetectLayout(t *testing.T) {
	for _, l := range mmLayouts {
		hud, mm := renderHUD(l, 1234, 5, 6), renderBorder(l)
		got, conf := mmDetectLayout(hud, mm)
		if got != l || conf < 0.95 {
			t.Errorf("%s: HUD+border detected as %v conf %.2f", l.Name, name(got), conf)
		}
		got, conf = mmDetectLayout(hud, nil)
		if got != l || conf < 0.75 {
			t.Errorf("%s: HUD only detected as %v conf %.2f", l.Name, name(got), conf)
		}
		// Worlds and LPL share the counter font: without the clock a frame may stay
		// undecided but is never attributed to the other league.
		noClock := renderHUD(l, 1234, 5, 6)
		for y := l.Clock.Y - mmRegionHUD.Y; y < l.Clock.Y-mmRegionHUD.Y+l.Clock.H; y++ {
			for x := l.Clock.X - mmRegionHUD.X; x < l.Clock.X-mmRegionHUD.X+l.Clock.W; x++ {
				o := (y*mmRegionHUD.W + x) * 3
				noClock[o], noClock[o+1], noClock[o+2] = 20, 20, 20
			}
		}
		if got, _ = mmDetectLayout(noClock, nil); got != nil && got != l {
			t.Errorf("%s: HUD without clock detected as %v", l.Name, name(got))
		}
		got, conf = mmDetectLayout(darkHUD(), mm)
		if got != l || conf < mmDetectMinConf {
			t.Errorf("%s: border only detected as %v conf %.2f", l.Name, name(got), conf)
		}
	}
	if got, conf := mmDetectLayout(darkHUD(), make([]byte, mmRegionMM.W*mmRegionMM.H*3)); got != nil || conf != 0 {
		t.Errorf("blank frame detected as %v conf %.2f", name(got), conf)
	}
	if got, _ := mmDetectLayout(nil, nil); got != nil {
		t.Errorf("no input detected as %v", name(got))
	}
}

func name(l *mmLayout) string {
	if l == nil {
		return "none"
	}
	return l.Name
}

func TestPipelineReadHUDFollowsChangesAndLayout(t *testing.T) {
	p := newMMPipeline(nil, nil)
	a, b := renderHUD(mmLayouts[0], 754, 3, 5), renderHUD(mmLayouts[0], 755, 3, 5)
	for i, c := range []struct {
		hud []byte
		sec int
	}{{a, 754}, {a, 754}, {b, 755}, {a, 754}, {b, 755}, {b, 755}} {
		if got := p.readHUD(mmLayouts[0], c.hud); !got.ClockOK || got.ClockSec != c.sec || got.Blue != 3 || got.Red != 5 {
			t.Fatalf("read %d: got %+v, want clock %d", i, got, c.sec)
		}
	}
	// the same bytes under another layout's fonts are another reading
	if got := p.readHUD(mmLayouts[1], b); got.ClockOK && got.ClockSec == 755 {
		t.Fatalf("a cached reading leaked across layouts: %+v", got)
	}
}
