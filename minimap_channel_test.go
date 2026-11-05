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
			got := mmRingChannel(ringFrame(c.r, c.g, c.b), cx, cy, c.team)
			if got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}
