package main

import (
	"math"
	"testing"
)

// paintRing paints the annulus r0..r1 around (cx, cy) in colour c.
func paintRing(rgb []byte, cx, cy, r0, r1 int, c [3]byte) {
	for y := cy - r1; y <= cy+r1; y++ {
		for x := cx - r1; x <= cx+r1; x++ {
			if d := (x-cx)*(x-cx) + (y-cy)*(y-cy); d < r0*r0 || d > r1*r1 {
				continue
			}
			o := 3 * (y*mmFrameSize + x)
			rgb[o], rgb[o+1], rgb[o+2] = c[0], c[1], c[2]
		}
	}
}

var (
	tpRed   = [3]byte{255, 60, 90}
	tpGreen = [3]byte{60, 240, 90}
)

func TestMMTPClassTellsTheSwirlsFromOrange(t *testing.T) {
	for _, c := range []struct {
		name    string
		r, g, b uint8
		want    int8
	}{
		{"red swirl", 255, 60, 90, 1},
		{"red swirl, warm edge", 255, 50, 30, 1},
		{"orange broadcast overlay", 255, 120, 30, -1},
		{"green swirl", 60, 240, 90, 0},
		{"dim red", 120, 30, 40, -1},
	} {
		if got := mmTPClass(c.r, c.g, c.b); got != c.want {
			t.Errorf("%s: class %d, want %d", c.name, got, c.want)
		}
	}
}

// tpTracker is a tracker of one red-team champion (PID 6) with a portrait template.
func tpTracker() *mmTracker {
	return newMMTracker([]champTpl{newChampTpl(champRef{PID: 6, Team: 200, Champ: "C"}, synthIcon(0), mmTplSize)})
}

// tpStep runs the channel and Teleport readings of tracker t on frame idx: the champion
// measured at (cx, cy) when measured, the frame rgb.
func tpStep(t *mmTracker, idx int, rgb []byte, measured bool, cx, cy int) {
	tr := &t.trk[0]
	tr.measured, tr.lastMX, tr.lastMY = measured, float64(cx), float64(cy)
	t.rgb = rgb
	t.img.set(rgb)
	t.detectChannels(idx)
	t.detectTeleports(idx)
}

func TestMMRedTeleportIsToldApartByItsDestination(t *testing.T) {
	tr := tpTracker()
	// A red ring around the champion, and a red swirl on a minion across the map.
	frame := func() []byte {
		rgb := ringFrameAt(60, 144, tpRed[0], tpRed[1], tpRed[2])
		paintRing(rgb, 200, 100, 10, 15, tpRed)
		return rgb
	}
	last := mmTPConfirm - 1 // the frame the destination is seen for the mmTPConfirm-th time
	for i := 0; i < last; i++ {
		tpStep(tr, i, frame(), true, 60, 144)
	}
	if got := tr.teleports(last - 1); len(got) != 0 {
		t.Fatalf("frame %d: %+v, want no destination before mmTPConfirm frames", last-1, got)
	}
	if c := tr.trk[0].chanState; c != mmChanRed {
		t.Fatalf("frame %d: channel %v, want red until the destination counts", last-1, c)
	}
	tpStep(tr, last, frame(), true, 60, 144)
	got := tr.teleports(last)
	if len(got) != 1 || got[0].Team != 200 || got[0].PID != 6 ||
		math.Abs(got[0].X*mmFrameSize-200) > 1 || math.Abs(got[0].Y*mmFrameSize-100) > 1 {
		t.Fatalf("frame %d: %+v, want the red destination at (200, 100) cast by PID 6", last, got)
	}
	if c := tr.trk[0].chanState; c != mmChanTeleport {
		t.Fatalf("frame %d: channel %v, want teleport", last, c)
	}
	// The ring outlasts mmRecallSure (the swirl gone): still a Teleport, not a Recall.
	end := last + mmRecallSure
	for i := last + 1; i <= end; i++ {
		tpStep(tr, i, ringFrameAt(60, 144, tpRed[0], tpRed[1], tpRed[2]), true, 60, 144)
	}
	if c := tr.trk[0].chanState; c != mmChanTeleport {
		t.Fatalf("frame %d: channel %v, want teleport", end, c)
	}
	if got := tr.teleports(end); len(got) != 0 {
		t.Fatalf("swirl gone: %+v, want no destination", got)
	}
}

func TestMMSwirlBesideAChannelingAllyIsItsRing(t *testing.T) {
	// Two icons on top of each other: the ring of the channeling one is found a little
	// off its icon. It is that champion's channel, not a destination.
	tr := tpTracker()
	frame := ringFrameAt(180, 100, tpRed[0], tpRed[1], tpRed[2])
	paintRing(frame, 200, 100, 10, 15, tpRed)
	for i := 0; i < mmTPConfirm+1; i++ {
		tpStep(tr, i, frame, true, 180, 100)
		if got := tr.teleports(i); len(got) != 0 {
			t.Fatalf("frame %d: %+v, want none beside the channeling champion", i, got)
		}
	}
}

func TestMMRedRingWithoutDestinationStaysUntold(t *testing.T) {
	tr := tpTracker()
	for i := 0; i < 4; i++ {
		tpStep(tr, i, ringFrameAt(60, 144, tpRed[0], tpRed[1], tpRed[2]), true, 60, 144)
	}
	if c := tr.trk[0].chanState; c != mmChanRed {
		t.Fatalf("channel %v, want red: neither a Recall yet nor a Teleport", c)
	}
}

// pastePortrait draws the portrait of tpTracker's champion centred at (cx, cy).
func pastePortrait(frame []byte, cx, cy int) {
	src := synthIcon(0)
	x0, y0 := cx-mmTplSize/2, cy-mmTplSize/2
	for y := 0; y < mmTplSize; y++ {
		for x := 0; x < mmTplSize; x++ {
			r, g, b, _ := src.At(x, y).RGBA()
			o := 3 * ((y0+y)*mmFrameSize + x0 + x)
			frame[o], frame[o+1], frame[o+2] = byte(r>>8), byte(g>>8), byte(b>>8)
		}
	}
}

func TestMMSwirlAroundALostChampionIsNoDestination(t *testing.T) {
	// The tracker lost the champion while it channels: its own ring around its portrait
	// must not read as a destination.
	tr := tpTracker()
	frame := ringFrame(18, 26, 18)
	pastePortrait(frame, 200, 100)
	paintRing(frame, 200, 100, 13, 16, tpRed)
	for i := 0; i < mmTPConfirm+1; i++ {
		tpStep(tr, i, frame, false, 0, 0)
		if got := tr.teleports(i); len(got) != 0 {
			t.Fatalf("frame %d: %+v, want none around the portrait", i, got)
		}
	}
}

func TestMMRingWhereALostChampionChannelsIsNoDestination(t *testing.T) {
	// The champion starts a channel, then the tracker loses its icon (no portrait match
	// either, as when two icons overlap); its ring, found a little off where it stood, is
	// still its channel: a channel roots the champion.
	tr := tpTracker()
	for i := 0; i < 2; i++ {
		tpStep(tr, i, ringFrameAt(100, 144, tpRed[0], tpRed[1], tpRed[2]), true, 100, 144)
	}
	if c := tr.trk[0].chanState; c != mmChanRed {
		t.Fatalf("channel %v, want red", c)
	}
	frame := ringFrame(18, 26, 18)
	paintRing(frame, 108, 144, 10, 15, tpRed)
	for i := 2; i < 2+mmTPConfirm+1; i++ {
		tpStep(tr, i, frame, false, 0, 0)
		if got := tr.teleports(i); len(got) != 0 {
			t.Fatalf("frame %d: %+v, want none where the lost champion channels", i, got)
		}
	}
}

func TestMMCasterFoundInsideItsRingIsShownTeleporting(t *testing.T) {
	// The tracker does not have the caster, but its portrait sits inside a red ring while
	// a red destination shows: that champion is the caster, drawn as teleporting.
	tr := tpTracker()
	tr.trk[0].init = true
	frame := ringFrame(18, 26, 18)
	pastePortrait(frame, 60, 144)
	paintRing(frame, 60, 144, 13, 16, tpRed)
	paintRing(frame, 200, 100, 10, 15, tpRed)
	last := mmTPConfirm - 1
	for i := 0; i <= last; i++ {
		tpStep(tr, i, frame, false, 0, 0)
	}
	if got := tr.teleports(last); len(got) != 1 || got[0].PID != 6 {
		t.Fatalf("%+v, want the red destination cast by PID 6", got)
	}
	if c := tr.dets(last)[0].Chan; c != mmChanTeleport {
		t.Fatalf("caster drawn with %v, want teleport", c)
	}
}

func TestMMTeleportFromAnUnseenCasterKeepsItsDestination(t *testing.T) {
	// A blue Teleport cast from the fountain: nobody is seen channeling, the green swirl
	// still shows where the champion lands; it goes once the swirl has been gone mmTPMiss
	// frames.
	tr := tpTracker()
	frame := ringFrame(18, 26, 18)
	paintRing(frame, 120, 170, 10, 15, tpGreen)
	last := mmTPConfirm - 1
	for i := 0; i <= last; i++ {
		tpStep(tr, i, frame, false, 0, 0)
	}
	if got := tr.teleports(last); len(got) != 1 || got[0].Team != 100 || got[0].PID != 0 {
		t.Fatalf("%+v, want one blue destination without a caster", got)
	}
	empty := ringFrame(18, 26, 18)
	for i := last + 1; i <= last+mmTPMiss; i++ {
		tpStep(tr, i, empty, false, 0, 0)
	}
	if got := tr.teleports(last + mmTPMiss); len(got) != 1 {
		t.Fatalf("missed for %d frames: %+v, want it kept", mmTPMiss, got)
	}
	tpStep(tr, last+mmTPMiss+1, empty, false, 0, 0)
	if got := tr.teleports(last + mmTPMiss + 1); len(got) != 0 {
		t.Fatalf("missed for %d frames: %+v, want it gone", mmTPMiss+1, got)
	}
}
