package main

import (
	"image"
	"image/color"
	"math/rand"
	"testing"
)

// gateRig drives a pipeline of one champion on synthetic frames: live is a minimap that
// fits the map, covered one an objective banner hides (no terrain), clock a HUD whose
// clock reads, gap the same plate with unreadable digits, replay a different picture.
type gateRig struct {
	t       *testing.T
	p       *mmPipeline
	idx     int
	live    []byte
	covered []byte
	clock   []byte
	gap     []byte
	replay  []byte
}

// withPlate paints the static logo blocks of a scoreboard on a copy of hud: the part of
// the HUD that stays when only the digits change.
func withPlate(hud []byte) []byte {
	out := append([]byte(nil), hud...)
	for _, r := range []mmBox{{4, 4, 30, 12}, {4, 90, 40, 8}, {200, 4, 24, 24}} {
		for y := r.Y; y < r.Y+r.H; y++ {
			for x := r.X; x < r.X+r.W; x++ {
				i := 3 * (y*mmRegionHUD.W + x)
				out[i], out[i+1], out[i+2] = 230, 230, 230
			}
		}
	}
	return out
}

func newGateRig(t *testing.T) *gateRig {
	mmSetMapRef(synthMap(512))
	l := mmLayouts[0]
	icon := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			icon.SetRGBA(x, y, color.RGBA{uint8(x * 4), uint8(y * 4), 90, 255})
		}
	}
	g := &gateRig{t: t, live: synthRegion(synthMap(512), l.Map), clock: withPlate(renderHUD(l, 754, 3, 5)), gap: withPlate(darkHUD())}
	g.covered = make([]byte, len(g.live))
	g.replay = make([]byte, len(g.clock))
	rand.New(rand.NewSource(1)).Read(g.replay)
	g.p = newMMPipeline([]champIcon{{ref: champRef{PID: 1, Team: 100, Champ: "X"}, img: icon}}, nil)
	g.p.lock.cur = l
	if r := g.step(g.live, g.clock); !r.visible || r.layout != l.Name {
		t.Fatalf("a live map with a readable clock is not shown: %+v", r)
	}
	// the champion was found on that frame
	g.p.marks[0] = mmMark{x: 0.3, y: 0.4, idx: 0, ok: true}
	return g
}

func (g *gateRig) step(mm, hud []byte) mmResult {
	r := g.p.Process(mmFrame{Idx: g.idx, MM: mm, HUD: hud})
	g.idx++
	return r
}

func TestProcessKeepsChampionsThroughAnObjectiveBanner(t *testing.T) {
	g := newGateRig(t)
	for i := 1; i <= mmCoverMax; i++ {
		r := g.step(g.covered, g.clock)
		if !r.visible || len(r.dets) != 1 || !r.dets[0].Visible || r.dets[0].X != 0.3 || r.dets[0].Y != 0.4 || r.dets[0].Age != i {
			t.Fatalf("banner frame %d: %+v", i, r)
		}
	}
	if r := g.step(g.covered, g.clock); r.visible {
		t.Fatal("a map that stays covered with the HUD up must end up hidden")
	}
	if r := g.step(g.live, g.clock); !r.visible {
		t.Fatal("the map is shown again as soon as it is back")
	}
}

func TestProcessKeepsTheMapThroughAnUnreadableClock(t *testing.T) {
	g := newGateRig(t)
	for i := 1; i <= mmClockLong; i++ {
		if r := g.step(g.live, g.gap); !r.visible {
			t.Fatalf("frame %d without a readable clock but with the plate on screen: hidden", i)
		}
	}
	if r := g.step(g.live, g.gap); r.visible {
		t.Fatal("a clock unreadable for longer than mmClockLong must hide the map")
	}
	if r := g.step(g.live, g.clock); !r.visible {
		t.Fatal("a clock read brings the map back at once")
	}
}

func TestProcessHidesReplaysAtOnce(t *testing.T) {
	g := newGateRig(t)
	// a replay shows the minimap but replaces the scoreboard: gone on the first frame,
	// although the clock was read one frame ago
	for i := 0; i < 3*mmAbsentReset; i++ {
		if r := g.step(g.live, g.replay); r.visible || r.dets != nil {
			t.Fatalf("replay frame %d shown: %+v", i, r)
		}
	}
	if g.p.marks != nil {
		t.Fatal("champion positions from before the replay must be forgotten")
	}
	if r := g.step(g.live, g.clock); !r.visible || (r.dets[0].Visible && r.dets[0].Age != 0) {
		t.Fatalf("after the replay only a fresh sighting may be shown, never the old position: %+v", r.dets)
	}
}

func TestHUDLiveNeedsPlateAndClockAge(t *testing.T) {
	p := newMMPipeline(nil, nil)
	hud := withPlate(renderHUD(mmLayouts[0], 754, 3, 5))
	if p.hudLive(10, hud, false) {
		t.Fatal("no clock read yet: no proof the HUD is up")
	}
	p.clockAt = 100
	p.setPlate(hud)
	if !p.plateOK {
		t.Fatal("a HUD with logos must serve as the plate")
	}
	rng := rand.New(rand.NewSource(2))
	elsewhere := make([]byte, len(hud))
	rng.Read(elsewhere)
	for _, c := range []struct {
		name string
		idx  int
		hud  []byte
		want bool
	}{
		{"plate intact, one frame after the read", 101, withPlate(darkHUD()), true},
		{"only the digits changed", 101, withPlate(renderHUD(mmLayouts[0], 755, 3, 6)), true},
		{"a dark replay shot without the plate", 101, darkHUD(), false},
		{"plate intact, almost mmClockLong later", 100 + mmClockLong, withPlate(darkHUD()), true},
		{"plate intact, longer than mmClockLong", 101 + mmClockLong, withPlate(darkHUD()), false},
		{"plate gone one frame after the read", 101, elsewhere, false},
		{"plate gone later", 110, elsewhere, false},
	} {
		if got := p.hudLive(c.idx, c.hud, false); got != c.want {
			t.Errorf("%s: hudLive = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestHoldMarksKeepsLostChampionsAtTheLastPosition(t *testing.T) {
	icons := []champIcon{{ref: champRef{PID: 1}}, {ref: champRef{PID: 2}}, {ref: champRef{PID: 3}}}
	p := newMMPipeline(icons, nil)
	found := func(x, y float64) mmDet { return mmDet{X: x, Y: y, Visible: true} }
	// frame 10: champions 1 and 2 found, 3 not yet
	got := p.holdMarks(10, []mmDet{found(0.1, 0.2), found(0.5, 0.5), {}})
	if got[0].Age != 0 || got[1].Age != 0 || got[2].Visible {
		t.Fatalf("frame 10: %+v", got)
	}
	// frame 13: champion 1 moved, 2 is lost, 3 was never seen
	got = p.holdMarks(13, []mmDet{found(0.15, 0.25), {}, {}})
	if got[0].Age != 0 || got[0].X != 0.15 || !got[1].Visible || got[1].X != 0.5 || got[1].Y != 0.5 || got[1].Age != 3 || got[2].Visible {
		t.Fatalf("frame 13: %+v", got)
	}
	// covered map: the tracker did not run, everybody seen before is held
	got = p.holdMarks(20, nil)
	if len(got) != 3 || got[0].PID != 1 || got[0].Age != 7 || got[1].Age != 10 || got[2].Visible {
		t.Fatalf("frame 20 (covered): %+v", got)
	}
	// a champion lost for longer than mmHoldMax is dropped
	if got := p.holdMarks(10+mmHoldMax+1, nil); got[1].Visible {
		t.Fatalf("held beyond mmHoldMax: %+v", got[1])
	}
	if got := p.holdMarks(13+mmHoldMax, nil); !got[0].Visible || got[0].Age != mmHoldMax {
		t.Fatalf("held up to mmHoldMax: %+v", got[0])
	}
}
