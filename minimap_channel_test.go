package main

import "testing"

func ringFrame(r, g, b uint8) []byte {
	rgb := make([]byte, mmFrameSize*mmFrameSize*3)
	for i := 0; i < len(rgb); i += 3 {
		rgb[i], rgb[i+1], rgb[i+2] = 18, 26, 18
	}
	cx, cy := mmFrameSize/2, mmFrameSize/2
	for y := 0; y < mmFrameSize; y++ {
		for x := 0; x < mmFrameSize; x++ {
			dx, dy := x-cx, y-cy
			d := dx*dx + dy*dy
			if d < 14*14 || d > 18*18 {
				continue
			}
			o := 3 * (y*mmFrameSize + x)
			rgb[o], rgb[o+1], rgb[o+2] = r, g, b
		}
	}
	return rgb
}

func TestMMRingChannelReadsTheSwirlColours(t *testing.T) {
	cx, cy := float64(mmFrameSize/2), float64(mmFrameSize/2)
	cases := []struct {
		name    string
		r, g, b uint8
		team    int
		want    mmChannel
	}{
		{"blue recall", 40, 220, 255, 100, mmChanRecall},
		{"blue teleport", 60, 240, 90, 100, mmChanTeleport},
		{"purple teleport", 165, 90, 255, 100, mmChanTeleport},
		{"purple teleport red team", 165, 90, 255, 200, mmChanTeleport},
		{"red team channel", 255, 60, 90, 200, mmChanRed},
		{"red on blue is ignored", 255, 60, 90, 100, mmChanNone},
		{"cyan on red is ignored", 40, 220, 255, 200, mmChanNone},
		{"plain terrain", 120, 130, 90, 100, mmChanNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mmReadRing(ringFrame(c.r, c.g, c.b), cx, cy, c.team).channel()
			if got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

// ringFrameAt is ringFrame with the ring centred at (x, y).
func ringFrameAt(x, y int, r, g, b uint8) []byte {
	rgb := ringFrame(18, 26, 18)
	for yy := y - 18; yy <= y+18; yy++ {
		for xx := x - 18; xx <= x+18; xx++ {
			dx, dy := xx-x, yy-y
			if d := dx*dx + dy*dy; d < 14*14 || d > 18*18 || xx < 0 || yy < 0 || xx >= mmFrameSize || yy >= mmFrameSize {
				continue
			}
			o := 3 * (yy*mmFrameSize + xx)
			rgb[o], rgb[o+1], rgb[o+2] = r, g, b
		}
	}
	return rgb
}

// chanStep is one frame of a champion: where it stands and the ring colour drawn around it.
type chanStep struct {
	x, y    int
	r, g, b uint8
}

// channelRun feeds the steps of one champion of team to a tracker and returns the channel
// shown after each.
func channelRun(team int, steps []chanStep) []mmChannel {
	t := newMMTracker([]champTpl{{ref: champRef{PID: 6, Team: team}, d: 24}})
	var out []mmChannel
	for i, s := range steps {
		tr := &t.trk[0]
		tr.measured, tr.lastMX, tr.lastMY = true, float64(s.x), float64(s.y)
		t.rgb = ringFrameAt(s.x, s.y, s.r, s.g, s.b)
		t.detectChannels(i)
		out = append(out, tr.chanState)
	}
	return out
}

// redRing is a red-team champion standing at column x of the middle row: nBright frames of
// a bright red ring, nDim of a faded one, then nNone without a ring.
func redRing(x, nBright, nDim, nNone int) []chanStep {
	var s []chanStep
	y := mmFrameSize / 2
	for i := 0; i < nBright; i++ {
		s = append(s, chanStep{x, y, 255, 60, 90})
	}
	for i := 0; i < nDim; i++ {
		s = append(s, chanStep{x, y, 150, 35, 55})
	}
	for i := 0; i < nNone; i++ {
		s = append(s, chanStep{x, y, 18, 26, 18})
	}
	return s
}

func TestMMRedRingLongEnoughIsARecall(t *testing.T) {
	c := mmFrameSize / 2

	// An 8 s Recall: bright for half of it, then fading. "?" until mmRecallSure, then R.
	got := channelRun(200, redRing(c, 20, 20, 5))
	if got[0] != mmChanNone || got[1] != mmChanRed {
		t.Fatalf("start: %v %v, want none then red after two frames", got[0], got[1])
	}
	if got[mmRecallSure-1] != mmChanRed || got[mmRecallSure] != mmChanRecall {
		t.Fatalf("frames %d/%d: %v %v, want red then recall", mmRecallSure-1, mmRecallSure, got[mmRecallSure-1], got[mmRecallSure])
	}
	if got[39] != mmChanRecall || got[40] != mmChanNone {
		t.Fatalf("end: %v %v, want the recall kept through the faded ring and dropped after it", got[39], got[40])
	}

	// A 4 s Teleport (or a cancelled Recall) never becomes R.
	for i, ch := range channelRun(200, redRing(c, 10, 10, 5)) {
		if ch == mmChanRecall {
			t.Fatalf("frame %d: a 4 s red ring was taken for a recall", i)
		}
	}

	// A faded ring alone starts nothing: red structures near an icon are dim red too.
	for i, ch := range channelRun(200, redRing(c, 0, 40, 0)) {
		if ch != mmChanNone {
			t.Fatalf("frame %d: a dim ring started %v", i, ch)
		}
	}
}

func TestMMChannelEndsWhenTheChampionWalksOff(t *testing.T) {
	c := mmFrameSize / 2
	steps := redRing(c, 2, 2, 0)
	steps = append(steps, chanStep{c + 13, c, 150, 35, 55})
	if got := channelRun(200, steps); got[3] != mmChanRed || got[4] != mmChanNone {
		t.Fatalf("got %v, want the channel dropped once the champion moved half an icon away", got)
	}
}

func TestMMNoChannelStartsOnTheRecallArrival(t *testing.T) {
	c := mmFrameSize / 2
	steps := redRing(c, 20, 20, 0)
	// Recalled: the champion is in the fountain, whose colour reads as a bright ring.
	arrival := redRing(c+60, 5, 0, 0)
	got := channelRun(200, append(steps, arrival...))
	for i := len(steps); i < len(got); i++ {
		if got[i] != mmChanNone {
			t.Fatalf("frame %d: the fountain arrival started %v", i, got[i])
		}
	}
	// Later in the fountain the same ring is a channel (a Teleport back to lane).
	later := append(append(steps, redRing(c+60, 0, 0, mmArriveQuiet)...), redRing(c+60, 3, 0, 0)...)
	if got := channelRun(200, later); got[len(got)-1] != mmChanRed {
		t.Fatalf("a ring %d frames after the arrival: %v, want red", mmArriveQuiet, got[len(got)-1])
	}
}

func TestMMNoRecallInTheFountain(t *testing.T) {
	fx, fy := spawnPrior(100)
	cyan := func(x, y int) []chanStep {
		return []chanStep{{x, y, 40, 220, 255}, {x, y, 40, 220, 255}, {x, y, 40, 220, 255}}
	}
	if got := channelRun(100, cyan(int(fx*mmFrameSize), int(fy*mmFrameSize))); got[2] != mmChanNone {
		t.Fatalf("a cyan ring in the blue fountain read as %v", got[2])
	}
	if got := channelRun(100, cyan(mmFrameSize/2, mmFrameSize/2)); got[2] != mmChanRecall {
		t.Fatalf("a cyan ring on the map read as %v, want recall", got[2])
	}
}
