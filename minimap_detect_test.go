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
		x, y := 40+3*i, 60+2*i
		expectAt(t, "step", tr.Step(i, synthFrame(synthIconAt{0, x, y}), nil)[0], x, y)
	}
}

func TestTrackerKeepsLostIconWithItsAgeThenDropsIt(t *testing.T) {
	tr := synthTracker(0)
	expectAt(t, "seen", tr.Step(0, synthFrame(synthIconAt{0, 100, 100}), nil)[0], 100, 100)
	// the icon vanishes (overlay, ping, banner): the champion stays at its last position,
	// older by the frame, until mmHoldMax
	for _, i := range []int{1, 7, mmHoldMax} {
		d := tr.Step(i, synthFrame(), nil)[0]
		expectAt(t, "lost", d, 100, 100)
		if d.Age != i || d.Covered {
			t.Fatalf("frame %d: age %d covered %v, want age %d", i, d.Age, d.Covered, i)
		}
	}
	if d := tr.Step(mmHoldMax+1, synthFrame(), nil)[0]; d.Visible {
		t.Fatalf("still held after mmHoldMax at (%.2f,%.2f)", d.X, d.Y)
	}
	// and found again where it stands
	expectAt(t, "back", tr.Step(mmHoldMax+2, synthFrame(synthIconAt{0, 104, 100}), nil)[0], 104, 100)
}

func TestTrackerReacquiresAfterAHideWithAWiderGate(t *testing.T) {
	tr := synthTracker(0)
	expectAt(t, "before", tr.Step(0, synthFrame(synthIconAt{0, 30, 30}), nil)[0], 30, 30)
	// a replay shot hid the map for 20 s (no frames); the champion has walked on
	expectAt(t, "after", tr.Step(20*mmFPS, synthFrame(synthIconAt{0, 150, 100}), nil)[0], 150, 100)
}

func TestTrackerAcceptsATeleportOfAKnownChampionOnTheSecondFrame(t *testing.T) {
	tr := synthTracker(0)
	expectAt(t, "before", tr.Step(0, synthFrame(synthIconAt{0, 30, 30}), nil)[0], 30, 30)
	// a champion seen a moment ago turns up across the map: that may be a swap or a
	// look-alike, so it is taken when it is there on the next frame too
	if d := tr.Step(1, synthFrame(synthIconAt{0, 220, 210}), nil)[0]; d.Age == 0 {
		t.Fatalf("a jump across the map taken on its first frame: %+v", d)
	}
	expectAt(t, "after", tr.Step(2, synthFrame(synthIconAt{0, 220, 210}), nil)[0], 220, 210)
}

func TestTrackerDropsAFarSightingThatDoesNotRepeat(t *testing.T) {
	tr := synthTracker(0)
	tr.Step(0, synthFrame(synthIconAt{0, 30, 30}), nil)
	tr.Step(1, synthFrame(synthIconAt{0, 220, 210}), nil) // a one-frame look-alike
	expectAt(t, "back", tr.Step(2, synthFrame(synthIconAt{0, 31, 30}), nil)[0], 31, 30)
}

func TestTrackerTakesALongLostChampionAnywhereAtOnce(t *testing.T) {
	tr := synthTracker(0)
	tr.Step(0, synthFrame(synthIconAt{0, 30, 30}), nil)
	// seen again after mmJumpRecent frames away: it may well have walked there
	expectAt(t, "found", tr.Step(mmJumpRecent+3, synthFrame(synthIconAt{0, 220, 210}), nil)[0], 220, 210)
}

func TestTrackerDistinguishesChampions(t *testing.T) {
	tr := synthTracker(0, 1)
	dets := tr.Step(0, synthFrame(synthIconAt{0, 40, 200}, synthIconAt{1, 200, 40}), nil)
	expectAt(t, "stripes", dets[0], 40, 200)
	expectAt(t, "rings", dets[1], 200, 40)
	// they swap sides of the map in a frame (a champion cannot, but the assignment must
	// follow the portraits, not the old positions)
	swapped := synthFrame(synthIconAt{0, 200, 40}, synthIconAt{1, 40, 200})
	tr.Step(1, swapped, nil) // seen once: not yet believed
	dets = tr.Step(2, swapped, nil)
	expectAt(t, "stripes", dets[0], 200, 40)
	expectAt(t, "rings", dets[1], 40, 200)
}

func TestTrackerTwoChampionsOfOneLookStayApart(t *testing.T) {
	// two champions share one look; one icon on screen must not be claimed twice
	tr := synthTracker(0, 0)
	dets := tr.Step(0, synthFrame(synthIconAt{0, 50, 50}), nil)
	if dets[0].Visible == dets[1].Visible {
		t.Fatalf("one icon, visibility %v/%v: want exactly one champion", dets[0].Visible, dets[1].Visible)
	}
}

func TestTrackerMarksAChampionHiddenUnderAnotherIconCovered(t *testing.T) {
	tr := synthTracker(0, 1)
	tr.Step(0, synthFrame(synthIconAt{0, 100, 100}, synthIconAt{1, 140, 100}), nil)
	// icon 1 slides over icon 0, which is no longer visible: it stays under it, not lost
	idx := 1
	for x := 135; x >= 110; x -= 5 {
		tr.Step(idx, synthFrame(synthIconAt{1, x, 100}), nil)
		idx++
	}
	covered := synthFrame(synthIconAt{1, 110, 100})
	for i := idx; i < idx+2; i++ { // fewer frames than mmCarryFrames: not yet taken for carried
		dets := tr.Step(i, covered, nil)
		expectAt(t, "covered", dets[0], 100, 100)
		expectAt(t, "cover", dets[1], 110, 100)
		if !dets[0].Covered || dets[0].Age != 0 {
			t.Fatalf("frame %d: covered %v age %d, want covered with age 0", i, dets[0].Covered, dets[0].Age)
		}
	}
	// icon 1 leaves: the champion is not found where it was, so it is lost now, with an age
	dets := tr.Step(idx+2, synthFrame(synthIconAt{1, 200, 200}), nil)
	if dets[0].Covered || dets[0].Age == 0 {
		t.Fatalf("nothing covers it any more: covered %v age %d", dets[0].Covered, dets[0].Age)
	}
}

func TestTrackerFollowsTheAllyAChampionIsHiddenUnder(t *testing.T) {
	// a support latched onto an ally has no icon of its own: after it has been hidden near
	// the ally for a moment it goes where the ally goes
	tr := synthTracker(0, 1)
	tr.Step(0, synthFrame(synthIconAt{0, 100, 100}, synthIconAt{1, 125, 100}), nil)
	idx := 1
	for ; idx <= mmCarryFrames+2; idx++ {
		tr.Step(idx, synthFrame(synthIconAt{1, 125, 100}), nil)
	}
	for x := 125; x <= 185; x += 4 {
		dets := tr.Step(idx, synthFrame(synthIconAt{1, x, 100}), nil)
		idx++
		expectAt(t, "ally", dets[1], x, 100)
		if !dets[0].Visible || !dets[0].Covered || dets[0].Age != 0 || math.Abs(dets[0].X-dets[1].X)*mmFrameSize > 2 {
			t.Fatalf("ally at %d: hidden champion %+v, want it carried along with the ally at %.1f", x, dets[0], dets[1].X*mmFrameSize)
		}
	}
}

func TestTrackerDoesNotCoverAChampionFarFromEveryOtherIcon(t *testing.T) {
	tr := synthTracker(0, 1)
	tr.Step(0, synthFrame(synthIconAt{0, 40, 40}, synthIconAt{1, 200, 200}), nil)
	d := tr.Step(1, synthFrame(synthIconAt{1, 200, 200}), nil)[0]
	if d.Covered || d.Age != 1 {
		t.Fatalf("lost far from every icon: covered %v age %d, want not covered, age 1", d.Covered, d.Age)
	}
}

func TestTrackerIgnoresADeadChampionAndFreezesItWhereItDied(t *testing.T) {
	tr := synthTracker(0, 1)
	for i := 0; i < 5; i++ {
		tr.Step(i, synthFrame(synthIconAt{0, 100 + 2*i, 100}, synthIconAt{1, 200, 40}), nil)
	}
	dead := mmLives{1: {Dead: true, Secs: 30}, 2: {AliveFor: 60}}
	// the feed says champion 1 died; for the grace its icon is still followed (sync error),
	// afterwards a look-alike elsewhere must not pull it away
	idx := 5
	for ; idx <= 5+mmDeathGrace+2; idx++ {
		tr.Step(idx, synthFrame(synthIconAt{0, 108, 100}, synthIconAt{1, 200, 40}), dead)
	}
	for ; idx < 5+mmDeathGrace+30; idx++ {
		dets := tr.Step(idx, synthFrame(synthIconAt{0, 180, 180}, synthIconAt{1, 200, 40}), dead)
		expectAt(t, "frozen", dets[0], 108, 100)
		expectAt(t, "alive", dets[1], 200, 40)
	}
}

func TestTrackerRespawnsAChampionAtItsFountain(t *testing.T) {
	tr := synthTracker(0)
	tr.Step(0, synthFrame(synthIconAt{0, 100, 100}), nil)
	dead := mmLives{1: {Dead: true, Secs: 30}}
	for i := 1; i < 3*mmFPS; i++ {
		tr.Step(i, synthFrame(), dead)
	}
	alive := mmLives{1: {AliveFor: 0}}
	d := tr.Step(3*mmFPS, synthFrame(), alive)[0]
	fx, fy := spawnPrior(100)
	if !d.Visible || math.Abs(d.X-fx) > 0.01 || math.Abs(d.Y-fy) > 0.01 || d.Age != 0 {
		t.Fatalf("after the respawn: %+v, want it at the fountain (%.2f,%.2f) with age 0", d, fx, fy)
	}
	// it walks out and is picked up there
	expectAt(t, "walking", tr.Step(3*mmFPS+10, synthFrame(synthIconAt{0, 60, 220}), alive)[0], 60, 220)
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
	expectAt(t, "clean", tr.Step(0, synthFrame(synthIconAt{0, 100, 100}), nil)[0], 100, 100)
	frame := synthFrame(synthIconAt{0, 104, 100})
	overlay(frame, 104, 100)
	tr.img.set(frame)
	if plain := tr.tpls[0].scan(&tr.img, mmWin{104, 104, 100, 100}, nil, -1, nil); plain.score >= mmSureScore {
		t.Fatalf("overlay too weak to need quadrant matching: plain score %.2f", plain.score)
	}
	expectAt(t, "overlaid", tr.Step(1, frame, nil)[0], 104, 100)
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
	got := tr.Step(0, rgb, nil)[0]
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

// weakNoise is the noise that drops the match of a synthetic icon below mmSureScore.
const weakNoise = 150

// weakSighting is a clean sighting, then the icon back smeared by an overlay: a weak match.
func weakSighting(frame []byte) mmDet {
	tr := synthTracker(0)
	tr.Step(0, synthFrame(synthIconAt{0, 100, 100}), nil)
	return tr.Step(10, frame, nil)[0]
}

func TestTrackerWeakMatchNeedsTeamRing(t *testing.T) {
	// the sighting is old enough that "still there, not found" would show an age: a fresh
	// match is one with age 0
	fresh := func(d mmDet) bool { return d.Visible && d.Age == 0 }
	if bare := weakSighting(noisyIconFrame([3]byte{}, false, weakNoise)); fresh(bare) {
		t.Errorf("weak match without a ring accepted (score %.2f)", bare.Score)
	}
	if red := weakSighting(noisyIconFrame([3]byte{200, 40, 40}, false, weakNoise)); fresh(red) {
		t.Errorf("weak match inside the other team's ring accepted (score %.2f)", red.Score)
	}
	ringed := weakSighting(noisyIconFrame(blueRing, false, weakNoise))
	if !fresh(ringed) {
		t.Fatalf("weak match with a team ring rejected")
	}
	if ringed.Score < mmSimMin || ringed.Score >= mmSureScore {
		t.Fatalf("test frame scores %.2f, want a weak match within [%.2f, %.2f)", ringed.Score, mmSimMin, mmSureScore)
	}
}

func TestTrackerWeakMatchNeedsPortraitColours(t *testing.T) {
	// the same luminance pattern in the wrong colours (terrain that looks like the icon in
	// gray) is not the champion, even inside a team ring
	if orange := weakSighting(noisyIconFrame(blueRing, true, weakNoise)); orange.Visible && orange.Age == 0 && orange.Score < mmSureScore {
		t.Errorf("weak match in the wrong colours accepted (score %.2f)", orange.Score)
	}
}

func TestTrackerAcquiresWeakMatchWithRing(t *testing.T) {
	// a champion never seen before is acquired by a weak match when ring and colours agree
	tr := synthTracker(0)
	got := tr.Step(0, noisyIconFrame(blueRing, false, 135), nil)[0]
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
		for _, floor := range []float32{-1, mmSimAcquire} {
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

func TestScanPeaksFindsOddPositionsBestFirstAndApart(t *testing.T) {
	// scanPeaks samples every second pixel, then refines: it must land on odd offsets too
	frame := blur3(synthFrame(synthIconAt{0, 101, 57}, synthIconAt{0, 183, 211}, synthIconAt{1, 30, 30}))
	var m mmFrameGray
	m.set(frame)
	tr := synthTracker(0, 1)
	full := mmWin{0, mmFrameSize - mmTplSize, 0, mmFrameSize - mmTplSize}
	got := tr.tpls[0].scanPeaks(&m, full, 3, mmSimAcquire)
	if len(got) < 2 {
		t.Fatalf("two copies of the icon, got %+v", got)
	}
	for i, want := range [][2]int{{101, 57}, {183, 211}} {
		found := false
		for _, h := range got {
			found = found || (h.x == want[0] && h.y == want[1])
		}
		if !found {
			t.Fatalf("icon %d at (%d,%d) not among %+v", i, want[0], want[1], got)
		}
	}
	for i := 1; i < len(got); i++ {
		if got[i].score > got[i-1].score {
			t.Fatalf("not best first: %+v", got)
		}
	}
	if got := tr.tpls[1].scanPeaks(&m, full, 1, mmSimAcquire); len(got) != 1 || got[0].x != 30 || got[0].y != 30 {
		t.Fatalf("the other icon: %+v", got)
	}
}

func TestScanMaskedMatchesAnIconPartlyCovered(t *testing.T) {
	frame := synthFrame(synthIconAt{0, 100, 100})
	// another icon sits over the left half of it
	for y := 100; y < 100+mmTplSize; y++ {
		for x := 100; x < 100+13; x++ {
			i := 3 * (y*mmFrameSize + x)
			frame[i], frame[i+1], frame[i+2] = 250, 20, 20
		}
	}
	var m mmFrameGray
	m.set(frame)
	tp := synthTracker(0).tpls[0]
	mask := make([]bool, mmFrameSize*mmFrameSize)
	for y := 100; y < 100+mmTplSize; y++ {
		for x := 100; x < 113; x++ {
			mask[y*mmFrameSize+x] = true
		}
	}
	win := mmWin{96, 104, 96, 104}
	if plain := tp.scan(&m, win, nil, -1, nil); plain.score >= 0.9 {
		t.Fatalf("the cover is too weak to need masking: plain score %.2f", plain.score)
	}
	h, share := tp.scanMasked(&m, win, mask, mmMaskedShare)
	if h.x != 100 || h.y != 100 || h.score < 0.9 || share > 0.8 || share < mmMaskedShare {
		t.Fatalf("masked match %+v on %.2f of the portrait, want the icon at (100,100) scoring >= 0.9", h, share)
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

func TestTrackerTakesMainViewPositionsForChampionsItDidNotFind(t *testing.T) {
	tr := synthTracker(0, 1)
	px := func(x, y int) (float64, float64) {
		return (float64(x) + mmTplSize/2) / mmFrameSize, (float64(y) + mmTplSize/2) / mmFrameSize
	}
	// champion 0 is on the map, champion 1 is not (stacked under it, say)
	frame := synthFrame(synthIconAt{0, 60, 60})
	ox, oy := px(70, 66)
	dets := tr.StepWith(0, frame, nil, []mvObs{{PID: 2, X: ox, Y: oy, Conf: 0.9}})
	expectAt(t, "found", dets[0], 60, 60)
	expectAt(t, "main view", dets[1], 70, 66)
	if !dets[1].MV || dets[1].Age != 0 || dets[0].MV {
		t.Fatalf("sources: %+v / %+v", dets[0], dets[1])
	}
	// a weak observation is not taken; one for a champion the map found is not needed
	tr = synthTracker(0, 1)
	dets = tr.StepWith(0, frame, nil, []mvObs{{PID: 2, X: ox, Y: oy, Conf: 0.1}, {PID: 1, X: 0.9, Y: 0.9, Conf: 0.9}})
	if dets[1].Visible {
		t.Fatalf("a confidence-0.1 observation must be ignored: %+v", dets[1])
	}
	expectAt(t, "map wins", dets[0], 60, 60)
	// an observation that lands on the icon of another champion the map found well is a
	// mix-up of bars, not a second champion there
	tr = synthTracker(0, 1)
	cx, cy := px(62, 61)
	if dets = tr.StepWith(0, frame, nil, []mvObs{{PID: 2, X: cx, Y: cy, Conf: 0.9}}); dets[1].Visible {
		t.Fatalf("an observation on another champion's icon was taken: %+v", dets[1])
	}
}

func TestTrackerTakesASureMatchWithoutARing(t *testing.T) {
	// some broadcasts draw a thin dark ring that hardly leans toward the team colour: a
	// match that sure stands on the portrait
	tr := synthTracker(0)
	got := tr.Step(0, noisyIconFrame([3]byte{}, false, 60), nil)[0]
	if got.Score < mmSureScore {
		t.Fatalf("test frame scores %.2f, want a sure match (at least %.2f)", got.Score, mmSureScore)
	}
	if !got.Visible || got.Age != 0 {
		t.Fatalf("a sure match without a ring rejected: %+v", got)
	}
	expectAt(t, "sure", got, 100, 100)
}

func TestTrackerRejectsASureMatchInTheWrongColours(t *testing.T) {
	tr := synthTracker(0)
	if got := tr.Step(0, noisyIconFrame([3]byte{}, true, 60), nil)[0]; got.Visible && got.Age == 0 {
		t.Fatalf("a sure luminance match in the wrong colours accepted (score %.2f)", got.Score)
	}
}

// attachTracker follows a Yuumi (support) and the bottom laner she latches onto.
func attachTracker() *mmTracker {
	return newMMTracker([]champTpl{
		newChampTpl(champRef{PID: 1, Team: 100, Champ: "Yuumi", Role: "support"}, synthIcon(0), mmTplSize),
		newChampTpl(champRef{PID: 2, Team: 100, Champ: "Jhin", Role: "bottom"}, synthIcon(1), mmTplSize),
	})
}

func TestTrackerAttachesASupportThatIsNotOnTheMap(t *testing.T) {
	tr := attachTracker()
	// Yuumi was never seen (she latched on before the stream began): after a while off the
	// map she is with the bottom laner, as fresh as his position, and goes where he goes
	idx := 0
	for ; idx < mmAttachLost-1; idx++ {
		if d := tr.Step(idx, synthFrame(synthIconAt{1, 100, 100}), nil)[0]; d.Attached {
			t.Fatalf("frame %d: attached before mmAttachLost", idx)
		}
	}
	for x := 100; x < 160; x += 3 {
		dets := tr.Step(idx, synthFrame(synthIconAt{1, x, 100}), nil)
		idx++
		if idx < mmAttachLost+2 {
			continue
		}
		y, j := dets[0], dets[1]
		if !y.Visible || !y.Attached || !y.Covered || y.Age != 0 || math.Hypot(y.X-j.X, y.Y-j.Y)*mmFrameSize > 2 {
			t.Fatalf("host at %d: Yuumi %+v, want attached at the host (%.1f,%.1f) with age 0", x, y, j.X*mmFrameSize, j.Y*mmFrameSize)
		}
	}
}

func TestTrackerKeepsASupportWithAHostThatIsHiddenForAWhile(t *testing.T) {
	tr := attachTracker()
	idx := 0
	for ; idx < 2*mmAttachLost; idx++ {
		tr.Step(idx, synthFrame(synthIconAt{1, 100, 100}), nil)
	}
	// the host's icon is gone for 2 s (a replay shot, a banner, a crowd): she stays with
	// him, and her age is his
	for i := 0; i < 2*mmFPS; i++ {
		dets := tr.Step(idx, synthFrame(), nil)
		idx++
		y, j := dets[0], dets[1]
		if !y.Attached || y.Age != j.Age || y.Age == 0 && i > 0 || math.Hypot(y.X-j.X, y.Y-j.Y)*mmFrameSize > 2 {
			t.Fatalf("frame %d without the host: Yuumi %+v, host %+v", i, y, j)
		}
	}
}

func TestTrackerDetachesASupportThatIsFoundOrWhoseHostDies(t *testing.T) {
	tr := attachTracker()
	idx := 0
	for ; idx < 2*mmAttachLost; idx++ {
		tr.Step(idx, synthFrame(synthIconAt{1, 100, 100}), nil)
	}
	// her own icon appears elsewhere (she jumped off): found there, no longer attached
	var dets []mmDet
	for i := 0; i < 6; i++ {
		dets = tr.Step(idx, synthFrame(synthIconAt{1, 100, 100}, synthIconAt{0, 200, 200}), nil)
		idx++
	}
	expectAt(t, "jumped off", dets[0], 200, 200)
	if dets[0].Attached {
		t.Fatal("found on her own icon but still attached")
	}
	// lost again, attached again; then the host dies: she is left where he died
	for i := 0; i < 2*mmAttachLost; i++ {
		tr.Step(idx, synthFrame(synthIconAt{1, 100, 100}), nil)
		idx++
	}
	if !tr.Step(idx, synthFrame(synthIconAt{1, 100, 100}), nil)[0].Attached {
		t.Fatal("not attached again after being lost")
	}
	idx++
	dead := mmLives{1: {AliveFor: 100}, 2: {Dead: true, Secs: 30}}
	for i := 0; i < mmDeathGrace+3; i++ {
		dets = tr.Step(idx, synthFrame(), dead)
		idx++
	}
	if y := dets[0]; y.Attached || math.Abs(y.X-dets[1].X)*mmFrameSize > 2 {
		t.Fatalf("host dead: Yuumi %+v, host %+v, want her detached where he died", y, dets[1])
	}
}
