package main

import (
	"math"
	"sort"
)

// Teleport destinations. From the start of a Teleport's channel until the dash, the
// broadcast minimap draws a swirl ring at the destination, around the allied minion, ward or
// turret the champion targets: green for the blue team, red for the red team (the friendly
// and enemy teleport highlights), shrinking as the channel runs. Around a red-team champion
// a Recall and a Teleport draw the same red ring (see mmChannel); the red swirl away from
// every champion is what tells the Teleport apart, and it shows where the champion lands.

const (
	mmTPRin      = 9           // px: inner radius of the annulus searched for a swirl
	mmTPRout     = 16          // px: outer radius; the swirl starts about this size and shrinks
	mmTPCore     = 5           // px: radius of the disc inside, which a swirl leaves uncoloured
	mmTPStep     = 2           // px between the centres tried
	mmTPStart    = 0.25        // share of the annulus in the team's bright teleport colour
	mmTPCoreMax  = 0.30        // share of the disc inside that may have it: a ring, not a coloured blob
	mmTPPortrait = 0.75        // a same-team portrait matching this well inside the ring: a champion's own ring (the unit a destination circles matches up to ~0.72)
	mmTPVisible  = 0.30        // share of the annulus that must lie outside the icons found on the frame
	mmTPCastPort = mmSureScore // ... this well: that champion's, which is then channeling there
	mmTPChanNear = 1.5         // icon diameters: a swirl this close to a channeling champion of its team is that champion's ring
	mmTPConfirm  = mmFPS       // frames a destination must be seen on before it counts: a lost champion's ring shows for moments
	mmTPMove     = 6.0         // px a destination's centre may move between frames
	mmTPMiss     = 3           // frames a destination survives without being seen
	mmTPLead     = 4 * mmFPS   // frames the caster's channel may start before its destination is first seen
	mmTPRingAge  = 2 * mmFPS   // frames a champion's own teleport-coloured ring counts after it was last seen
	mmTPRooted   = 8 * mmFPS   // frames after a channel started during which its champion may still stand in it: a Recall's 8 s
	mmTPRecast   = 30 * mmFPS  // frames after its destination went during which a caster casts no other: the cooldown is minutes
	mmTPSep      = 2 * mmTPRin // px between two swirls of one colour
)

// mmTPTeams is the team of each swirl colour class: green is the blue team's, red the red team's.
var mmTPTeams = [2]int{100, 200}

// mmTPClass is the swirl colour class (0 green, 1 red) of a pixel, -1 for neither: the
// bright threshold and green hues of mmReadRing, and a red that stops short of orange (the
// red swirls measured 335°..15°, mostly 345°..5°; the orange corner of the LEC overlay
// 15°..30°).
func mmTPClass(r, g, b uint8) int8 {
	h, s, v := rgbHSV(r, g, b)
	switch {
	case v < 0.72 || s < 0.22:
		return -1
	case h >= 80 && h <= 165:
		return 0
	case h < 12 || h >= 335:
		return 1
	}
	return -1
}

// mmTPDest is a destination swirl followed across frames.
type mmTPDest struct {
	team   int
	x, y   float64 // px in the mmFrameSize frame
	first  int     // frame it was first seen
	seen   int     // frame it was last seen
	n      int     // frames it was seen on
	caster int     // template index of the champion teleporting there, -1 while unknown
}

// mmTPScratch holds the per-frame buffers of the swirl search.
type mmTPScratch struct {
	cls  []int8     // swirl colour class per pixel
	sum  [2][]int32 // summed-area table of each class, (mmFrameSize+1)² entries
	cov  []bool     // pixels under the icons found on the frame
	ring [][2]int   // offsets of the annulus pixels
	core [][2]int   // offsets of the disc inside
}

func (s *mmTPScratch) init() {
	if s.cls != nil {
		return
	}
	s.cls = make([]int8, mmFrameSize*mmFrameSize)
	s.cov = make([]bool, mmFrameSize*mmFrameSize)
	for c := range s.sum {
		s.sum[c] = make([]int32, (mmFrameSize+1)*(mmFrameSize+1))
	}
	for dy := -mmTPRout; dy <= mmTPRout; dy++ {
		for dx := -mmTPRout; dx <= mmTPRout; dx++ {
			switch r2 := dx*dx + dy*dy; {
			case r2 >= mmTPRin*mmTPRin && r2 <= mmTPRout*mmTPRout:
				s.ring = append(s.ring, [2]int{dx, dy})
			case r2 <= mmTPCore*mmTPCore:
				s.core = append(s.core, [2]int{dx, dy})
			}
		}
	}
}

// boxSum is the count of class c in the square [x0, x1] x [y0, y1] (inclusive).
func (s *mmTPScratch) boxSum(c, x0, y0, x1, y1 int) int32 {
	const w = mmFrameSize + 1
	t := s.sum[c]
	return t[(y1+1)*w+x1+1] - t[y0*w+x1+1] - t[(y1+1)*w+x0] + t[y0*w+x0]
}

// mmTPSpot is a destination swirl found on one frame.
type mmTPSpot struct {
	team  int
	x, y  float64
	share float64
}

// findTPSpots returns the swirls of frame idx that no champion stands in (see tpOwner). A
// ring that is the own ring of a champion of its team, told by its portrait, marks that
// champion as channeling there (tpRing): the tracker often loses an icon inside its ring.
func (t *mmTracker) findTPSpots(idx int) []mmTPSpot {
	t.tpCoverIcons()
	var out []mmTPSpot
	for _, c := range t.tpCandidates() {
		own, k := t.tpOwner(c, idx)
		if !own {
			out = append(out, c)
			continue
		}
		if k >= 0 {
			tr := &t.trk[k]
			if idx-tr.tpRing > mmTPMiss {
				tr.tpRingFirst = idx
			}
			tr.tpRing = idx
		}
	}
	return out
}

// tpCoverIcons marks the pixels under the icons found on this frame (tp.cov).
func (t *mmTracker) tpCoverIcons() {
	s := &t.tp
	s.init()
	clear(s.cov)
	r := float64(t.d())/2 + 1
	for k := range t.trk {
		tr := &t.trk[k]
		x, y := tr.lastMX, tr.lastMY
		switch {
		case tr.measured:
		case tr.covered && tr.init:
			x, y = tr.kf.x.p, tr.kf.y.p
		default:
			continue
		}
		for py := max(int(y-r), 0); py <= min(int(y+r), mmFrameSize-1); py++ {
			for px := max(int(x-r), 0); px <= min(int(x+r), mmFrameSize-1); px++ {
				if math.Hypot(float64(px)-x, float64(py)-y) <= r {
					s.cov[py*mmFrameSize+px] = true
				}
			}
		}
	}
}

// tpCandidates returns every swirl-shaped ring of a teleport colour on this frame, best
// first, at least mmTPSep apart within a colour.
func (t *mmTracker) tpCandidates() []mmTPSpot {
	s := &t.tp
	s.init()
	const w = mmFrameSize + 1
	for y := 0; y < mmFrameSize; y++ {
		var row [2]int32
		for x := 0; x < mmFrameSize; x++ {
			o := 3 * (y*mmFrameSize + x)
			c := mmTPClass(t.rgb[o], t.rgb[o+1], t.rgb[o+2])
			s.cls[y*mmFrameSize+x] = c
			if c >= 0 {
				row[c]++
			}
			for k := range s.sum {
				s.sum[k][(y+1)*w+x+1] = s.sum[k][y*w+x+1] + row[k]
			}
		}
	}
	// The annulus lies between the square inside its inner circle and the square around its
	// outer circle, so the count between the two squares bounds the annulus' from above:
	// most centres are ruled out without visiting a pixel.
	need := int32(math.Ceil(mmTPStart * float64(len(s.ring))))
	in := int(math.Floor(mmTPRin / math.Sqrt2))
	var cands []mmTPSpot
	for cy := mmTPRout; cy < mmFrameSize-mmTPRout; cy += mmTPStep {
		for cx := mmTPRout; cx < mmFrameSize-mmTPRout; cx += mmTPStep {
			for c := range s.sum {
				box := s.boxSum(c, cx-mmTPRout, cy-mmTPRout, cx+mmTPRout, cy+mmTPRout) - s.boxSum(c, cx-in, cy-in, cx+in, cy+in)
				if box < need {
					continue
				}
				n := 0
				for _, p := range s.ring {
					if s.cls[(cy+p[1])*mmFrameSize+cx+p[0]] == int8(c) {
						n++
					}
				}
				if int32(n) < need {
					continue
				}
				core := 0
				for _, p := range s.core {
					if s.cls[(cy+p[1])*mmFrameSize+cx+p[0]] == int8(c) {
						core++
					}
				}
				if float64(core) > mmTPCoreMax*float64(len(s.core)) {
					continue
				}
				cands = append(cands, mmTPSpot{mmTPTeams[c], float64(cx), float64(cy), float64(n) / float64(len(s.ring))})
			}
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].share > cands[j].share })
	var out []mmTPSpot
	for _, c := range cands {
		near := false
		for _, o := range out {
			near = near || (o.team == c.team && math.Hypot(o.x-c.x, o.y-c.y) < mmTPSep)
		}
		if !near {
			out = append(out, c)
		}
	}
	return out
}

// tpOwner reports whether swirl c is a champion's own ring rather than a destination, and
// whose (-1 when it cannot tell, or the champion is of the other team): a ring mostly under
// the icons found on the frame (their team rings make it), at a champion, near a channeling
// champion of its team (around an icon the tracker places a little off when two icons
// overlap) or where one started a channel it may still be in (a channel roots the
// champion, and the tracker often loses an icon inside its ring), in its team's fountain,
// or around a portrait of its team.
func (t *mmTracker) tpOwner(c mmTPSpot, idx int) (own bool, k int) {
	vis := 0
	for _, p := range t.tp.ring {
		if !t.tp.cov[(int(c.y)+p[1])*mmFrameSize+int(c.x)+p[0]] {
			vis++
		}
	}
	if float64(vis) < mmTPVisible*float64(len(t.tp.ring)) {
		return true, -1
	}
	d := t.d()
	near := mmTPChanNear * float64(d)
	for k := range t.trk {
		tr := &t.trk[k]
		same := t.tpls[k].ref.Team == c.team
		if x, y, ok := t.chanSpot(k, idx); same && ok && math.Hypot(x-c.x, y-c.y) < near {
			return true, k
		}
		x, y := tr.lastMX, tr.lastMY
		switch {
		case tr.measured:
		case tr.covered && tr.init:
			x, y = tr.kf.x.p, tr.kf.y.p
		default:
			continue
		}
		r := float64(d) / 2
		if tr.chanState != mmChanNone && same {
			r = near
		}
		if math.Hypot(x-c.x, y-c.y) < r {
			if !same {
				return true, -1
			}
			return true, k
		}
	}
	fx, fy := spawnPrior(c.team)
	own = math.Hypot(c.x/mmFrameSize-fx, c.y/mmFrameSize-fy) < mmFountainR
	x0, y0 := int(c.x)-d/2, int(c.y)-d/2
	win := mmWin{clampInt(x0-3, 0, mmFrameSize-d), clampInt(x0+3, 0, mmFrameSize-d), clampInt(y0-3, 0, mmFrameSize-d), clampInt(y0+3, 0, mmFrameSize-d)}
	best, bestK := float32(mmTPPortrait), -1
	for j := range t.tpls {
		tp := &t.tpls[j]
		if tp.ref.Team != c.team {
			continue
		}
		if h := tp.scan(&t.img, win, nil, best, nil); h.score >= best {
			best, bestK = h.score, j
		}
	}
	if bestK < 0 {
		return own, -1
	}
	if best < mmTPCastPort {
		return true, -1
	}
	return true, bestK
}

// chanSpot is where champion k stands channeling at frame idx, as far as the tracker knows:
// where its last channel started, while that channel may still run (mmTPRooted) and the
// champion was not found anywhere else since its ring was last read.
func (t *mmTracker) chanSpot(k, idx int) (x, y float64, ok bool) {
	tr := &t.trk[k]
	if tr.dead || tr.chanSeen < tr.chanIdx || idx-tr.chanIdx > mmTPRooted {
		return 0, 0, false
	}
	if tr.measIdx > tr.chanSeen && math.Hypot(tr.lastMX-tr.chanX, tr.lastMY-tr.chanY) > float64(t.d())/2 {
		return 0, 0, false
	}
	return tr.chanX, tr.chanY, true
}

// detectTeleports follows the destination swirls and ties each to the champion teleporting
// there: the one of its team whose channel started nearest in time, at most mmTPLead
// before the destination, whether the tracker read that channel or the destination search
// found the champion inside a teleport-coloured ring (tpRing; also its dash, which a
// caster in its stacked fountain shows first). Until a caster is found this is tried on
// every frame. A destination counts once seen on mmTPConfirm frames; its caster's red ring
// is then a Teleport, never a Recall.
func (t *mmTracker) detectTeleports(idx int) {
	spots := t.findTPSpots(idx)
	used := make([]bool, len(spots))
	for i := range t.tps {
		d := &t.tps[i]
		best, bestD := -1, mmTPMove
		for j, s := range spots {
			if !used[j] && s.team == d.team {
				if dd := math.Hypot(s.x-d.x, s.y-d.y); dd <= bestD {
					best, bestD = j, dd
				}
			}
		}
		if best >= 0 {
			used[best] = true
			d.x, d.y, d.seen = spots[best].x, spots[best].y, idx
			d.n++
		}
	}
	kept := t.tps[:0]
	for _, d := range t.tps {
		if idx-d.seen <= mmTPMiss {
			kept = append(kept, d)
		}
	}
	t.tps = kept
	for j, s := range spots {
		if !used[j] {
			t.tps = append(t.tps, mmTPDest{team: s.team, x: s.x, y: s.y, first: idx, seen: idx, n: 1, caster: -1})
		}
	}
	for _, d := range t.tps {
		if d.caster >= 0 {
			t.trk[d.caster].tpCast = idx
		}
	}
	for i := range t.tps {
		d := &t.tps[i]
		if d.n < mmTPConfirm || d.caster >= 0 {
			continue
		}
		best, bestDt := -1, 0
		pick := func(k, start int) {
			if dt := absInt(start - d.first); start >= d.first-mmTPLead && (best < 0 || dt < bestDt) {
				best, bestDt = k, dt
			}
		}
		for k := range t.trk {
			tr := &t.trk[k]
			if t.tpls[k].ref.Team != d.team || tr.dead || idx-tr.tpCast <= mmTPRecast {
				continue
			}
			if tr.chanState == mmChanTeleport || tr.chanState == mmChanRed {
				pick(k, tr.chanIdx)
			}
			if idx-tr.tpRing <= mmTPRingAge {
				pick(k, tr.tpRingFirst)
			}
		}
		if best >= 0 {
			d.caster = best
			t.trk[best].tpCast = idx
			if c := &t.trk[best].chanState; *c == mmChanRed {
				*c = mmChanTeleport
			}
		}
	}
}

// tpCasting reports whether champion k is already the caster of a destination.
func (t *mmTracker) tpCasting(k int) bool {
	for _, d := range t.tps {
		if d.caster == k {
			return true
		}
	}
	return false
}

// teleports reports the destinations shown at frame idx.
func (t *mmTracker) teleports(idx int) []mmTP {
	var out []mmTP
	for _, d := range t.tps {
		if d.n < mmTPConfirm || idx-d.seen > mmTPMiss {
			continue
		}
		tp := mmTP{Team: d.team, X: d.x / mmFrameSize, Y: d.y / mmFrameSize}
		if d.caster >= 0 {
			tp.PID = t.tpls[d.caster].ref.PID
		}
		out = append(out, tp)
	}
	return out
}
