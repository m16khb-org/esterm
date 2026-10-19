package main

import "math"

// The nameplate above a health bar ("TEAM Name" in the broadcast's font, a status text such
// as CHARMED while the champion is under an effect) tells champions apart that level and
// health cannot: two same-level champions with alike health in one fight, a support latched
// onto the carry. The fonts differ per broadcast and the names are only known as strings,
// so the plates are not read as text. They are clustered by look instead, and each cluster
// is tied to a champion by the votes of the bars it collects:
//
//   - a bar's plate is correlated with the clusters of its team's colour (small shifts
//     allowed); a plate close to one joins it, any other founds a new cluster (status
//     texts and fades end up in clusters of their own);
//   - every bar that joins a cluster votes for the champions whose level, health and last
//     seen position it agrees with, one vote per mvPlateVoteGap frames. The votes do not
//     depend on who the assignment gave the bar to, so a wrong assignment cannot confirm
//     itself, and champions that stand together at the same level are told apart by the
//     stretches where they differ;
//   - a cluster whose votes clearly favour one champion is that champion's plate, and a
//     bar that joins it cannot be another champion.

const (
	mvPlateW  = 152 // window width at the reference bar size
	mvPlateH  = 22  // window height
	mvPlateX  = 32  // columns left of the bar's left edge
	mvPlateY  = 28  // rows above the bar's top
	mvPlateIX = 5   // columns trimmed at both sides of the compared region
	mvPlateIY = 2   // rows trimmed at both sides
	mvPlateDX = 3   // horizontal shifts tried either way
	mvPlateDY = 2   // vertical shifts tried either way

	mvPlateSame     = 0.88 // correlation at which a plate joins a cluster
	mvPlateClusters = 14   // clusters kept per team colour
	mvPlateVoteGap  = 10   // frames between two votes of one cluster
	mvPlateVoteN    = 30   // votes a cluster needs before it names a champion
	mvPlateVoteLead = 0.5  // and its favourite's lead over the runner-up, per vote
	mvPlateWeight   = 3.0  // cost of a bar whose plate is another champion's
	mvPlateRamp0    = 0.6  // correlation at which that cost starts
	mvPlateRamp1    = 0.8  // and where it is reached
	mvPlateHP       = 0.04 // health agreement that counts as a vote
	mvPlateNear     = 2.5  // seconds a last sighting may be old to vote
)

// mvPlateOf cuts the nameplate window of a bar whose fill starts at (bx, by) out of the
// luminance plane; pixels outside the plane are 0. It returns nil when the window does not
// fit at least half inside.
func mvPlateOf(lum []uint8, w, h, bx, by int) []uint8 {
	x0, y0 := bx-mvPlateX, by-mvPlateY
	if x0+mvPlateW/2 < 0 || x0+mvPlateW/2 >= w || y0 < 0 || y0+mvPlateH > h {
		return nil
	}
	out := make([]uint8, mvPlateW*mvPlateH)
	for r := 0; r < mvPlateH; r++ {
		for c := 0; c < mvPlateW; c++ {
			if x := x0 + c; x >= 0 && x < w {
				out[r*mvPlateW+c] = lum[(y0+r)*w+x]
			}
		}
	}
	return out
}

// mvCluster is one look of plate seen in a game.
type mvCluster struct {
	mean  []float32 // mean of the aligned plates
	inner []float32 // zero-mean unit-norm compared region of mean
	n     int       // plates averaged in
	voted int       // frame of the last vote
	nv    int       // votes cast
	votes [11]float64
}

// identity is the participant id the cluster's votes clearly favour, 0 when they do not.
func (c *mvCluster) identity() int {
	best, second, pid := 0.0, 0.0, 0
	for p := 1; p <= 10; p++ {
		if v := c.votes[p]; v > best {
			best, second, pid = v, best, p
		} else if v > second {
			second = v
		}
	}
	if c.nv < mvPlateVoteN || best-second < mvPlateVoteLead*float64(c.nv) {
		return 0
	}
	return pid
}

// mvPlates holds the plate clusters of one game, per team colour. The zero value is ready.
type mvPlates struct {
	teams [2][]*mvCluster
	sum   []float64 // scratch: integral images of the plate being compared
	sq    []float64
}

// prep fills the integral images of plate p.
func (b *mvPlates) prep(p []uint8) {
	const sw = mvPlateW + 1
	n := sw * (mvPlateH + 1)
	if len(b.sum) != n {
		b.sum, b.sq = make([]float64, n), make([]float64, n)
	}
	for r := 0; r < mvPlateH; r++ {
		rs, rq := 0.0, 0.0
		for c := 0; c < mvPlateW; c++ {
			v := float64(p[r*mvPlateW+c])
			rs += v
			rq += v * v
			b.sum[(r+1)*sw+c+1] = b.sum[r*sw+c+1] + rs
			b.sq[(r+1)*sw+c+1] = b.sq[r*sw+c+1] + rq
		}
	}
}

// corr is the best correlation of the cluster's compared region with the plate p (whose
// integral images prep built), over small shifts of the plate; it also returns the shift.
func (b *mvPlates) corr(c *mvCluster, p []uint8) (best float32, bdx, bdy int) {
	const sw = mvPlateW + 1
	iw, ih := mvPlateW-2*mvPlateIX, mvPlateH-2*mvPlateIY
	n := float64(iw * ih)
	best = -1
	for dy := -mvPlateDY; dy <= mvPlateDY; dy++ {
		for dx := -mvPlateDX; dx <= mvPlateDX; dx++ {
			x0, y0 := mvPlateIX+dx, mvPlateIY+dy
			x1, y1 := x0+iw, y0+ih
			s := b.sum[y1*sw+x1] - b.sum[y0*sw+x1] - b.sum[y1*sw+x0] + b.sum[y0*sw+x0]
			q := b.sq[y1*sw+x1] - b.sq[y0*sw+x1] - b.sq[y1*sw+x0] + b.sq[y0*sw+x0]
			v := q - s*s/n
			if v < 1 {
				continue // a flat window correlates with nothing
			}
			dot := float32(0)
			for rr := 0; rr < ih; rr++ {
				ref := c.inner[rr*iw : (rr+1)*iw]
				row := p[(y0+rr)*mvPlateW+x0 : (y0+rr)*mvPlateW+x0+iw]
				for k, rv := range ref {
					dot += rv * float32(row[k])
				}
			}
			if v := dot / float32(math.Sqrt(v)); v > best {
				best, bdx, bdy = v, dx, dy
			}
		}
	}
	return best, bdx, bdy
}

// mvPlateInner returns the zero-mean unit-norm compared region of a mean plate, nil when
// it is flat.
func mvPlateInner(mean []float32) []float32 {
	iw, ih := mvPlateW-2*mvPlateIX, mvPlateH-2*mvPlateIY
	out := make([]float32, iw*ih)
	var s float64
	for r := 0; r < ih; r++ {
		for c := 0; c < iw; c++ {
			v := mean[(r+mvPlateIY)*mvPlateW+c+mvPlateIX]
			out[r*iw+c] = v
			s += float64(v)
		}
	}
	m := float32(s / float64(iw*ih))
	var q float64
	for i := range out {
		out[i] -= m
		q += float64(out[i]) * float64(out[i])
	}
	if q < 1 {
		return nil
	}
	k := float32(1 / math.Sqrt(q))
	for i := range out {
		out[i] *= k
	}
	return out
}

// mvPlateMatch is where a plate stands among the clusters of its team: the best one, the
// correlation with it and the shift that aligns them; no cluster when there is none.
type mvPlateMatch struct {
	cluster *mvCluster
	corr    float32
	dx, dy  int
}

// match finds the cluster plate p looks most like.
func (b *mvPlates) match(team int, p []uint8) mvPlateMatch {
	m := mvPlateMatch{corr: -1}
	b.prep(p)
	for _, c := range b.teams[team] {
		if s, dx, dy := b.corr(c, p); s > m.corr {
			m = mvPlateMatch{c, s, dx, dy}
		}
	}
	return m
}

// cost is the extra cost of each champion of cs for the bar whose plate matched m: when
// the plate is that of an identified cluster, every other champion pays mvPlateWeight
// (scaled by how sure the correlation is); otherwise the plate says nothing and nil is
// returned.
func (b *mvPlates) cost(team int, m mvPlateMatch, cs []mvChamp) []float64 {
	if m.cluster == nil || m.corr <= mvPlateRamp0 {
		return nil
	}
	pid := m.cluster.identity()
	if pid == 0 {
		return nil
	}
	pen := mvPlateWeight * math.Min(1, float64(m.corr-mvPlateRamp0)/(mvPlateRamp1-mvPlateRamp0))
	out := make([]float64, len(cs))
	for j, c := range cs {
		if c.PID != pid {
			out[j] = pen
		}
	}
	return out
}

// observe files bar bar's plate (frame idx, the plate matched m) under a cluster, founding
// one when it looks like none, and lets the cluster vote for the champions of cs that the
// bar's level, health and map position (x, y) agree with.
func (b *mvPlates) observe(idx, team int, bar mvBar, x, y float64, m mvPlateMatch, cs []mvChamp) {
	c := m.cluster
	if c != nil && m.corr >= mvPlateSame {
		a := float32(math.Max(1/float64(c.n+1), 0.05))
		for r := 0; r < mvPlateH; r++ {
			sr := r + m.dy
			if sr < 0 || sr >= mvPlateH {
				continue
			}
			for k := 0; k < mvPlateW; k++ {
				sk := k + m.dx
				if sk < 0 || sk >= mvPlateW {
					continue
				}
				c.mean[r*mvPlateW+k] += a * (float32(bar.Plate[sr*mvPlateW+sk]) - c.mean[r*mvPlateW+k])
			}
		}
		c.n++
		c.inner = mvPlateInner(c.mean)
		if c.inner == nil { // the average went flat: forget it
			b.drop(team, c)
			return
		}
	} else {
		f := make([]float32, len(bar.Plate))
		for i, v := range bar.Plate {
			f[i] = float32(v)
		}
		inner := mvPlateInner(f)
		if inner == nil {
			return
		}
		c = &mvCluster{mean: f, inner: inner, n: 1, voted: idx - mvPlateVoteGap}
		if len(b.teams[team]) >= mvPlateClusters {
			b.drop(team, b.weakest(team))
		}
		b.teams[team] = append(b.teams[team], c)
	}
	if idx-c.voted < mvPlateVoteGap {
		return
	}
	c.voted = idx
	c.nv++
	for _, ch := range cs {
		v := 0.0
		if bar.Level > 0 && bar.LevelC >= 0.3 {
			switch d := bar.Level - ch.Level; {
			case d == 0:
				v++
			case d == 1 || d == -1:
				v += 0.25
			}
		}
		if math.Abs(bar.HPFrac-ch.HPFrac) <= mvPlateHP {
			v++
		}
		if ch.HasPrev && ch.PrevAge <= mvPlateNear && math.Hypot(x-ch.PrevX, y-ch.PrevY) <= 1.5*(0.04+0.012*ch.PrevAge) {
			v++
		}
		if ch.PID >= 1 && ch.PID <= 10 {
			c.votes[ch.PID] += v
		}
	}
}

// weakest is the cluster of a team that has averaged the fewest plates.
func (b *mvPlates) weakest(team int) *mvCluster {
	w := b.teams[team][0]
	for _, c := range b.teams[team] {
		if c.n < w.n {
			w = c
		}
	}
	return w
}

func (b *mvPlates) drop(team int, c *mvCluster) {
	cl := b.teams[team]
	for k := range cl {
		if cl[k] == c {
			b.teams[team] = append(cl[:k], cl[k+1:]...)
			return
		}
	}
}
