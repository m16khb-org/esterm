package main

// The broadcast draws the camera's view as a thin white rectangle on the minimap, on top
// of the champion icons. A line across an icon (2 px of white on a 25 px portrait) is
// enough to drop its match below what the tracker accepts, and the rectangle follows the
// camera, which follows the fight. unline paints such lines out before the portraits are
// matched: the pixels of the line are filled from their neighbours on either side.

const (
	mmLineRun    = 20  // px: shortest straight stretch that is a line, not a bright icon detail or a lane
	mmLineMin    = 150 // lowest channel value of a line pixel
	mmLineSpread = 45  // largest channel spread of a line pixel (it is white, the map is not)
	mmLineRidge  = 22  // how much brighter than both sides, 3 px away, the line is
	mmLineGap    = 2   // px: gap along a line that is bridged (an icon's own pixels are not white)
	mmLineFill   = 3   // passes of filling: lines are up to ~4 px wide once dilated
)

// mmLineScratch holds the buffers of unline.
type mmLineScratch struct {
	h, v []bool // ridge pixels of horizontal / vertical lines
	mask []bool // pixels to repaint
	next []bool
	out  []byte
}

// mmRidge reports whether pixel (x, y) of rgb is a bright neutral pixel standing out from
// the pixels (dx, dy)*3 away on both sides (dx, dy: the direction across the line).
func mmRidge(rgb []byte, x, y, dx, dy int) bool {
	o := 3 * (y*mmFrameSize + x)
	r, g, b := int(rgb[o]), int(rgb[o+1]), int(rgb[o+2])
	lo, hi := min(r, g, b), max(r, g, b)
	if lo < mmLineMin || hi-lo > mmLineSpread {
		return false
	}
	side := func(sx, sy int) int {
		if sx < 0 || sy < 0 || sx >= mmFrameSize || sy >= mmFrameSize {
			return 0
		}
		so := 3 * (sy*mmFrameSize + sx)
		return min(int(rgb[so]), int(rgb[so+1]), int(rgb[so+2]))
	}
	return lo-max(side(x-3*dx, y-3*dy), side(x+3*dx, y+3*dy)) >= mmLineRidge
}

// unline returns rgb (an mmFrameSize² rgb24 frame) with the straight white lines of the
// camera rectangle painted out, and whether there were any; with none it returns rgb itself.
// The result is valid until the next call.
func (s *mmLineScratch) unline(rgb []byte) ([]byte, bool) {
	n := mmFrameSize * mmFrameSize
	if len(s.h) != n {
		s.h, s.v, s.mask, s.next = make([]bool, n), make([]bool, n), make([]bool, n), make([]bool, n)
	}
	clear(s.h)
	clear(s.v)
	found := false
	// horizontal lines: runs along x of pixels that stand out from the rows above and below
	for y := 0; y < mmFrameSize; y++ {
		run, gap := 0, 0
		for x := 0; x <= mmFrameSize; x++ {
			hit := x < mmFrameSize && mmRidge(rgb, x, y, 0, 1)
			switch {
			case hit:
				run, gap = run+gap+1, 0
			case run > 0 && gap < mmLineGap:
				gap++
				continue
			default:
				if run >= mmLineRun {
					for i := x - gap - run; i < x-gap; i++ {
						s.h[y*mmFrameSize+i] = true
					}
					found = true
				}
				run, gap = 0, 0
			}
		}
	}
	for x := 0; x < mmFrameSize; x++ {
		run, gap := 0, 0
		for y := 0; y <= mmFrameSize; y++ {
			hit := y < mmFrameSize && mmRidge(rgb, x, y, 1, 0)
			switch {
			case hit:
				run, gap = run+gap+1, 0
			case run > 0 && gap < mmLineGap:
				gap++
				continue
			default:
				if run >= mmLineRun {
					for i := y - gap - run; i < y-gap; i++ {
						s.v[i*mmFrameSize+x] = true
					}
					found = true
				}
				run, gap = 0, 0
			}
		}
	}
	if !found {
		return rgb, false
	}
	// the pixels to repaint: the lines and one pixel on either side (their soft edges)
	clear(s.mask)
	for y := 0; y < mmFrameSize; y++ {
		for x := 0; x < mmFrameSize; x++ {
			i := y*mmFrameSize + x
			if s.h[i] {
				for dy := -1; dy <= 1; dy++ {
					if yy := y + dy; yy >= 0 && yy < mmFrameSize {
						s.mask[yy*mmFrameSize+x] = true
					}
				}
			}
			if s.v[i] {
				for dx := -1; dx <= 1; dx++ {
					if xx := x + dx; xx >= 0 && xx < mmFrameSize {
						s.mask[y*mmFrameSize+xx] = true
					}
				}
			}
		}
	}
	s.out = append(s.out[:0], rgb...)
	for pass := 0; pass < mmLineFill; pass++ {
		copy(s.next, s.mask)
		left := false
		for y := 0; y < mmFrameSize; y++ {
			for x := 0; x < mmFrameSize; x++ {
				i := y*mmFrameSize + x
				if !s.mask[i] {
					continue
				}
				var sum [3]int
				cnt := 0
				for _, d := range [4][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}} {
					xx, yy := x+d[0], y+d[1]
					if xx < 0 || yy < 0 || xx >= mmFrameSize || yy >= mmFrameSize || s.mask[yy*mmFrameSize+xx] {
						continue
					}
					o := 3 * (yy*mmFrameSize + xx)
					sum[0], sum[1], sum[2] = sum[0]+int(s.out[o]), sum[1]+int(s.out[o+1]), sum[2]+int(s.out[o+2])
					cnt++
				}
				if cnt == 0 {
					left = true
					continue
				}
				o := 3 * i
				s.out[o], s.out[o+1], s.out[o+2] = byte(sum[0]/cnt), byte(sum[1]/cnt), byte(sum[2]/cnt)
				s.next[i] = false
			}
		}
		s.mask, s.next = s.next, s.mask
		if !left {
			break
		}
	}
	return s.out, true
}
