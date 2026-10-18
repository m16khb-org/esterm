package main

import (
	"image"
	"image/color"
	"math"
	"math/rand"
	"testing"
)

// synthIcon draws a high-contrast, non-periodic 3×3-block pattern; kind seeds it so
// different kinds look different.
func synthIcon(kind int) *image.RGBA { return synthIconD(kind, mmTplSize) }

// synthIconD is synthIcon at a d px diameter.
func synthIconD(kind, d int) *image.RGBA {
	im := image.NewRGBA(image.Rect(0, 0, d, d))
	rng := rand.New(rand.NewSource(int64(100 + kind)))
	for by := 0; by < d; by += 3 {
		for bx := 0; bx < d; bx += 3 {
			v := uint8(20 + rng.Intn(220))
			for y := by; y < by+3; y++ {
				for x := bx; x < bx+3; x++ {
					im.Set(x, y, color.RGBA{v, v, v, 255})
				}
			}
		}
	}
	return im
}

type synthIconAt struct {
	kind, x, y int
}

// addRing paints the team ring (rgb ring) of a d px icon at top-left (x, y), the way
// broadcast minimaps draw a champion's team colour around its portrait.
func addRing(rgb []byte, x, y, d int, ring [3]byte) {
	c := float64(d-1) / 2
	for yy := 0; yy < d; yy++ {
		for xx := 0; xx < d; xx++ {
			if r := math.Hypot(float64(xx)-c, float64(yy)-c) / (float64(d) / 2); r >= 0.8 && r <= 1 {
				i := 3 * ((y+yy)*mmFrameSize + x + xx)
				rgb[i], rgb[i+1], rgb[i+2] = ring[0], ring[1], ring[2]
			}
		}
	}
}

var blueRing = [3]byte{20, 60, 200}

// synthFrame renders a textured background (fixed seed) with the given icons pasted in,
// each inside a blue team ring.
func synthFrame(icons ...synthIconAt) []byte {
	rgb := make([]byte, mmFrameSize*mmFrameSize*3)
	rng := rand.New(rand.NewSource(1))
	for by := 0; by < mmFrameSize; by += 6 {
		for bx := 0; bx < mmFrameSize; bx += 6 {
			v := byte(90 + rng.Intn(50))
			for y := by; y < by+6 && y < mmFrameSize; y++ {
				for x := bx; x < bx+6 && x < mmFrameSize; x++ {
					i := 3 * (y*mmFrameSize + x)
					rgb[i], rgb[i+1], rgb[i+2] = v, v, v
				}
			}
		}
	}
	for _, ic := range icons {
		src := synthIcon(ic.kind)
		for y := 0; y < mmTplSize; y++ {
			for x := 0; x < mmTplSize; x++ {
				r, g, b, _ := src.At(x, y).RGBA()
				i := 3 * ((ic.y+y)*mmFrameSize + ic.x + x)
				rgb[i], rgb[i+1], rgb[i+2] = byte(r>>8), byte(g>>8), byte(b>>8)
			}
		}
		addRing(rgb, ic.x, ic.y, mmTplSize, blueRing)
	}
	return rgb
}

func synthTracker(kinds ...int) *mmTracker {
	var tpls []champTpl
	for i, k := range kinds {
		tpls = append(tpls, newChampTpl(champRef{PID: i + 1, Team: 100, Champ: "C"}, synthIcon(k), mmTplSize))
	}
	return newMMTracker(tpls)
}

func expectAt(t *testing.T, label string, d mmDet, x, y int) {
	t.Helper()
	if !d.Visible {
		t.Fatalf("%s: not visible (score %.2f)", label, d.Score)
	}
	wx, wy := (float64(x)+mmTplSize/2)/mmFrameSize, (float64(y)+mmTplSize/2)/mmFrameSize
	if math.Abs(d.X-wx)*mmFrameSize > 1 || math.Abs(d.Y-wy)*mmFrameSize > 1 {
		t.Fatalf("%s: got (%.1f,%.1f) px, want (%d,%d)", label, d.X*mmFrameSize, d.Y*mmFrameSize, x+mmTplSize/2, y+mmTplSize/2)
	}
}

func TestTrackerFollowsMovingIcon(t *testing.T) {
	tr := synthTracker(0)
	for i := 0; i < 12; i++ {
		x, y := 40+6*i, 60+4*i
		expectAt(t, "step", tr.Step(i, synthFrame(synthIconAt{0, x, y}))[0], x, y)
	}
}

func TestTrackerHidesAndReacquires(t *testing.T) {
	tr := synthTracker(0)
	expectAt(t, "seen", tr.Step(0, synthFrame(synthIconAt{0, 100, 100}))[0], 100, 100)
	// A short dropout keeps the icon at its last position (overlay or ping on top of it).
	for i := 1; i <= mmGraceFrames; i++ {
		expectAt(t, "grace", tr.Step(i, synthFrame())[0], 100, 100)
	}
	if d := tr.Step(mmGraceFrames+1, synthFrame())[0]; d.Visible {
		t.Fatalf("icon absent but visible at (%.2f,%.2f) score %.2f", d.X, d.Y, d.Score)
	}
	// Same spot shortly after: recovered near the last position.
	expectAt(t, "back", tr.Step(mmGraceFrames+2, synthFrame(synthIconAt{0, 104, 100}))[0], 104, 100)
}

func TestTrackerTeleport(t *testing.T) {
	tr := synthTracker(0)
	expectAt(t, "before", tr.Step(0, synthFrame(synthIconAt{0, 30, 30}))[0], 30, 30)
	expectAt(t, "after", tr.Step(1, synthFrame(synthIconAt{0, 220, 210}))[0], 220, 210)
}

func TestTrackerTwoChampionsStaySeparate(t *testing.T) {
	// Two champions share the same look; one icon on screen must not be claimed twice.
	tr := synthTracker(0, 0)
	dets := tr.Step(0, synthFrame(synthIconAt{0, 50, 50}))
	if dets[0].Visible == dets[1].Visible {
		t.Fatalf("one icon, visibility %v/%v: want exactly one champion", dets[0].Visible, dets[1].Visible)
	}
	// With both icons present each champion takes its own spot, and stays there.
	both := synthFrame(synthIconAt{0, 50, 50}, synthIconAt{0, 180, 120})
	dets = tr.Step(1, both)
	if !dets[0].Visible || !dets[1].Visible || math.Hypot(dets[0].X-dets[1].X, dets[0].Y-dets[1].Y)*mmFrameSize < 100 {
		t.Fatalf("both icons present: got %+v / %+v", dets[0], dets[1])
	}
}

func TestTrackerDistinguishesChampions(t *testing.T) {
	tr := synthTracker(0, 1)
	dets := tr.Step(0, synthFrame(synthIconAt{0, 40, 200}, synthIconAt{1, 200, 40}))
	expectAt(t, "stripes", dets[0], 40, 200)
	expectAt(t, "rings", dets[1], 200, 40)
}

func TestTrackerKeepsIconCoveredByAnother(t *testing.T) {
	tr := synthTracker(0, 1)
	tr.Step(0, synthFrame(synthIconAt{0, 100, 100}, synthIconAt{1, 140, 100}))
	// Icon 1 slides over icon 0, which is no longer visible: it stays at its last spot...
	covered := synthFrame(synthIconAt{1, 110, 100})
	for i := 1; i <= mmHoldFrames; i++ {
		dets := tr.Step(i, covered)
		expectAt(t, "covered", dets[0], 100, 100)
		expectAt(t, "cover", dets[1], 110, 100)
	}
	// ...but not forever: a champion that never re-emerges is dead or gone.
	if d := tr.Step(mmHoldFrames+1, covered)[0]; d.Visible {
		t.Fatalf("still held after %d frames at (%.2f,%.2f)", mmHoldFrames+1, d.X, d.Y)
	}
}

func TestTrackerHidesIconNotNearAnother(t *testing.T) {
	tr := synthTracker(0, 1)
	tr.Step(0, synthFrame(synthIconAt{0, 40, 40}, synthIconAt{1, 200, 200}))
	var dets []mmDet
	for i := 1; i <= mmGraceFrames+1; i++ {
		dets = tr.Step(i, synthFrame(synthIconAt{1, 200, 200}))
	}
	if dets[0].Visible {
		t.Fatalf("icon far from every other champion still visible at (%.2f,%.2f)", dets[0].X, dets[0].Y)
	}
	expectAt(t, "other", dets[1], 200, 200)
}

func TestTrackerToleratesOverlayOnOneQuadrant(t *testing.T) {
	overlay := func(rgb []byte, x, y int) {
		// bright block over the icon's top-right quadrant, like a ping arrow or ability
		for yy := y; yy < y+mmTplSize/2; yy++ {
			for xx := x + mmTplSize/2; xx < x+mmTplSize; xx++ {
				i := 3 * (yy*mmFrameSize + xx)
				rgb[i], rgb[i+1], rgb[i+2] = 255, 255, 255
			}
		}
	}
	tr := synthTracker(0)
	expectAt(t, "clean", tr.Step(0, synthFrame(synthIconAt{0, 100, 100}))[0], 100, 100)
	frame := synthFrame(synthIconAt{0, 104, 100})
	overlay(frame, 104, 100)
	tr.img.set(frame)
	if plain := tr.tpls[0].scan(&tr.img, mmWin{104, 104, 100, 100}, nil, -1, nil); plain.score >= mmSureScore {
		t.Fatalf("overlay too weak to need quadrant matching: plain score %.2f", plain.score)
	}
	expectAt(t, "overlaid", tr.Step(1, frame)[0], 104, 100)
}

func TestTrackerReplacesWeakLocalLock(t *testing.T) {
	tr := synthTracker(0)
	expectAt(t, "seen", tr.Step(0, synthFrame(synthIconAt{0, 100, 100}))[0], 100, 100)
	// The icon turns up 30 px away, inside the jump distance, while a partly similar blob
	// (60% icon 0, 40% icon 1) stays at the old spot: the clearly better full-scan match
	// must win over the weak local one.
	frame := synthFrame(synthIconAt{0, 130, 100})
	a, b := synthIcon(0), synthIcon(1)
	for y := 0; y < mmTplSize; y++ {
		for x := 0; x < mmTplSize; x++ {
			ra, _, _, _ := a.At(x, y).RGBA()
			rb, _, _, _ := b.At(x, y).RGBA()
			i := 3 * ((100+y)*mmFrameSize + 100 + x)
			v := byte((3*int(ra>>8) + 2*int(rb>>8)) / 5)
			frame[i], frame[i+1], frame[i+2] = v, v, v
		}
	}
	addRing(frame, 100, 100, mmTplSize, blueRing)
	expectAt(t, "moved", tr.Step(1, frame)[0], 130, 100)
}

func TestTrackerUsesIconDiameter(t *testing.T) {
	const d = 30
	tr := newMMTracker([]champTpl{newChampTpl(champRef{PID: 1, Team: 100, Champ: "C"}, synthIconD(0, d), d)})
	rgb := synthFrame()
	src := synthIconD(0, d)
	for y := 0; y < d; y++ {
		for x := 0; x < d; x++ {
			r, g, b, _ := src.At(x, y).RGBA()
			i := 3 * ((60+y)*mmFrameSize + 100 + x)
			rgb[i], rgb[i+1], rgb[i+2] = byte(r>>8), byte(g>>8), byte(b>>8)
		}
	}
	addRing(rgb, 100, 60, d, blueRing)
	got := tr.Step(0, rgb)[0]
	if !got.Visible || math.Abs(got.X*mmFrameSize-115) > 1 || math.Abs(got.Y*mmFrameSize-75) > 1 {
		t.Fatalf("got visible=%v (%.1f,%.1f), want centre (115,75)", got.Visible, got.X*mmFrameSize, got.Y*mmFrameSize)
	}
}

// noisyIconFrame pastes the icon with noise that drops its match below mmSureScore, inside
// a ring of the given colour (none when zero), optionally with its colours turned orange.
func noisyIconFrame(ring [3]byte, orange bool, noise int) []byte {
	rgb := synthFrame(synthIconAt{0, 100, 100})
	rng := rand.New(rand.NewSource(9))
	c := float64(mmTplSize-1) / 2
	for y := 0; y < mmTplSize; y++ {
		for x := 0; x < mmTplSize; x++ {
			i := 3 * ((100+y)*mmFrameSize + 100 + x)
			r := math.Hypot(float64(x)-c, float64(y)-c) / (mmTplSize / 2)
			if r <= 0.72 {
				for k := 0; k < 3; k++ {
					rgb[i+k] = byte(clampInt(int(rgb[i+k])+rng.Intn(2*noise)-noise, 0, 255))
				}
				if orange {
					rgb[i+2] = rgb[i+2] / 3
					rgb[i] = byte(clampInt(int(rgb[i])*5/4, 0, 255))
				}
			}
			if r >= 0.8 && r <= 1 {
				if ring == [3]byte{} {
					v := byte(110)
					rgb[i], rgb[i+1], rgb[i+2] = v, v, v
				} else {
					rgb[i], rgb[i+1], rgb[i+2] = ring[0], ring[1], ring[2]
				}
			}
		}
	}
	return rgb
}

// weakSighting is a clean sighting, then the icon back smeared by an overlay: a weak match.
func weakSighting(frame []byte) mmDet {
	tr := synthTracker(0)
	tr.Step(0, synthFrame(synthIconAt{0, 100, 100}))
	return tr.Step(mmGraceFrames+1, frame)[0]
}

func TestTrackerWeakMatchNeedsTeamRing(t *testing.T) {
	if bare := weakSighting(noisyIconFrame([3]byte{}, false, 95)); bare.Visible {
		t.Errorf("weak match without a ring accepted (score %.2f)", bare.Score)
	}
	if red := weakSighting(noisyIconFrame([3]byte{200, 40, 40}, false, 95)); red.Visible {
		t.Errorf("weak match inside the other team's ring accepted (score %.2f)", red.Score)
	}
	ringed := weakSighting(noisyIconFrame(blueRing, false, 95))
	if !ringed.Visible {
		t.Fatalf("weak match with a team ring rejected")
	}
	if ringed.Score < mmAccept || ringed.Score >= mmSureScore {
		t.Fatalf("test frame scores %.2f, want a weak match within [%.2f, %.2f)", ringed.Score, mmAccept, mmSureScore)
	}
}

func TestTrackerWeakMatchNeedsPortraitColours(t *testing.T) {
	// the same luminance pattern in the wrong colours (terrain that looks like the icon in
	// gray) is not the champion, even inside a team ring
	if orange := weakSighting(noisyIconFrame(blueRing, true, 95)); orange.Visible && orange.Score < mmSureScore {
		t.Errorf("weak match in the wrong colours accepted (score %.2f)", orange.Score)
	}
}

func TestTrackerAcquiresWeakMatchWithRing(t *testing.T) {
	// a champion never seen before is acquired by a weak match (below the old 0.83 bar)
	// when ring and colours agree
	tr := synthTracker(0)
	got := tr.Step(0, noisyIconFrame(blueRing, false, 125))[0]
	if !got.Visible || got.Score >= 0.83 {
		t.Fatalf("got visible=%v score %.2f, want a weak acquisition", got.Visible, got.Score)
	}
	expectAt(t, "acquired", got, 100, 100)
}

// exhaustiveScan is the plain NCC search scan must agree with, whatever it prunes.
func exhaustiveScan(t *champTpl, g []float32, w mmWin) mmHit {
	best := mmNoHit
	for y := w.y0; y <= w.y1; y++ {
		for x := w.x0; x <= w.x1; x++ {
			var s, ss, sx float64
			for _, sp := range t.spans {
				for j := 0; j < sp.b-sp.a; j++ {
					p := float64(g[(y+sp.ty)*mmFrameSize+x+sp.a+j])
					s += p
					ss += p * p
					sx += p * float64(t.tv[sp.off+j])
				}
			}
			v := ss - s*s/float64(t.n)
			if float32(v) < t.n*mmVarFloor {
				continue
			}
			if sc := float32(sx / (math.Sqrt(v) * float64(t.norm))); sc > best.score {
				best = mmHit{x, y, sc}
			}
		}
	}
	return best
}

func TestScanAgreesWithExhaustiveSearch(t *testing.T) {
	frame := synthFrame(synthIconAt{0, 100, 100}, synthIconAt{1, 180, 60}, synthIconAt{0, 30, 200})
	var m mmFrameGray
	m.set(frame)
	tr := synthTracker(0, 1, 2)
	full := mmWin{0, mmFrameSize - mmTplSize, 0, mmFrameSize - mmTplSize}
	for k := range tr.tpls {
		tp := &tr.tpls[k]
		want := exhaustiveScan(tp, m.g, full)
		for _, floor := range []float32{-1, mmAccept} {
			got := tp.scan(&m, full, nil, floor, nil)
			if want.score < floor {
				if got.score >= floor {
					t.Fatalf("template %d floor %.2f: got %+v, nothing should reach it (best %+v)", k, floor, got, want)
				}
				continue
			}
			if got.x != want.x || got.y != want.y || math.Abs(float64(got.score-want.score)) > 1e-4 {
				t.Fatalf("template %d floor %.2f: got %+v, want %+v", k, floor, got, want)
			}
		}
	}
}

func TestScanFullFindsOddPositions(t *testing.T) {
	// scanFull samples every second pixel, then refines: it must land on odd offsets too
	frame := blur3(synthFrame(synthIconAt{0, 101, 57}, synthIconAt{1, 183, 211}))
	var m mmFrameGray
	m.set(frame)
	tr := synthTracker(0, 1)
	full := mmWin{0, mmFrameSize - mmTplSize, 0, mmFrameSize - mmTplSize}
	for k, want := range []mmHit{{101, 57, 1}, {183, 211, 1}} {
		got := tr.tpls[k].scanFull(&m, full, nil, mmAccept, nil)
		if got.x != want.x || got.y != want.y {
			t.Fatalf("template %d: got %+v, want icon at (%d,%d)", k, got, want.x, want.y)
		}
	}
}

// blur3 is a 3×3 box blur, so icons correlate over a few pixels as they do in broadcast video.
func blur3(rgb []byte) []byte {
	out := make([]byte, len(rgb))
	for y := 0; y < mmFrameSize; y++ {
		for x := 0; x < mmFrameSize; x++ {
			for c := 0; c < 3; c++ {
				var sum, n int
				for dy := -1; dy <= 1; dy++ {
					for dx := -1; dx <= 1; dx++ {
						if xx, yy := x+dx, y+dy; xx >= 0 && yy >= 0 && xx < mmFrameSize && yy < mmFrameSize {
							sum += int(rgb[3*(yy*mmFrameSize+xx)+c])
							n++
						}
					}
				}
				out[3*(y*mmFrameSize+x)+c] = byte(sum / n)
			}
		}
	}
	return out
}
