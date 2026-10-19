package main

import "math"

// Camera mapping: a health bar's screen position to the champion's place on the map, and
// the assignment of bars to the feed's champions.

// The screen-to-viewport mapping was fitted on ~3900 bar/champion pairs of four broadcasts
// (Worlds, LEC, LCK, LPL): a bar whose level was that of exactly one champion of its team
// was paired with that champion's minimap position (tracker, score > 0). u, v are the bar
// centre's screen position minus 0.5; the champion's position relative to the viewport
// centre, in viewport widths/heights, is
//
//	nx = ax·(1, u, u·v, v, u²)    ny = ay·(1, v, v², u, u·v, v³)
//
// The vertical terms carry the camera tilt (ground compressed towards the top of the
// screen) and the bar-to-feet offset.
var (
	mvMapAX = [5]float64{0.0022, 0.6888, -0.3445, 0.0016, -0.0082}
	mvMapAY = [6]float64{0.2120, 0.7684, -0.3984, -0.0065, -0.0103, 0.3340}
)

// mvToMap returns the position (normalized map coordinates) of the champion a bar belongs
// to; ok is false when the viewport is unknown.
func mvToMap(b mvBar, v mvView) (x, y float64, ok bool) {
	if !v.OK {
		return 0, 0, false
	}
	u, w := b.X/mvFrameW-0.5, b.Y/mvFrameH-0.5
	nx := mvMapAX[0] + mvMapAX[1]*u + mvMapAX[2]*u*w + mvMapAX[3]*w + mvMapAX[4]*u*u
	ny := mvMapAY[0] + mvMapAY[1]*w + mvMapAY[2]*w*w + mvMapAY[3]*u + mvMapAY[4]*u*w + mvMapAY[5]*w*w*w
	return (v.X0+v.X1)/2 + nx*v.w(), (v.Y0+v.Y1)/2 + ny*v.h(), true
}

// mvChamp is what the feed (and the tracker's last sighting) says about one champion at the
// instant of the frame.
type mvChamp struct {
	PID     int
	Team    int     // 100 blue, 200 red
	Level   int     // feed level
	HPFrac  float64 // current health / max health
	Alive   bool
	HasPrev bool    // the tracker has seen it before
	PrevX   float64 // last tracked position, normalized map coordinates
	PrevY   float64
	PrevAge float64 // seconds since that sighting
}

// mvObs is a champion position seen in the main view.
type mvObs struct {
	PID  int
	X, Y float64
	Conf float64
}

const (
	mvAssignUnmatched = 3.0 // cost of leaving a bar without a champion
	mvAssignMax       = 3.0 // highest cost of an accepted match
	mvAssignBars      = 8   // bars per team considered
)

// mvPairCost is the cost of bar b being champion c at map position (x, y): level mismatch,
// health mismatch, and distance from where the champion was last seen.
func mvPairCost(b mvBar, c mvChamp, x, y float64) float64 {
	cost := 0.0
	switch {
	case b.Level == 0 || b.LevelC < 0.15:
		cost += 0.4
	case b.Level == c.Level:
	case b.Level-c.Level == 1 || c.Level-b.Level == 1:
		cost += 1.5
	default:
		return 99
	}
	cost += 0.5 * math.Abs(b.HPFrac-c.HPFrac)
	if c.HasPrev {
		sigma := 0.04 + 0.012*c.PrevAge
		d := math.Hypot(x-c.PrevX, y-c.PrevY) / sigma
		cost += math.Min(d*d/2, 8)
	}
	return cost
}

// mvAssign matches the bars to the champions of their team: the assignment of least total
// cost (team, level, health, proximity to the last sighting, and the nameplate when book
// knows the champions' plates) with unmatched bars allowed. A match made with confidence
// teaches the book that champion's plate (frame idx is the caller's clock). Only bars with
// a known viewport yield observations. book may be nil.
func mvAssign(bars []mvBar, v mvView, champs []mvChamp, idx int, book *mvPlates) []mvObs {
	if !v.OK || len(bars) == 0 {
		return nil
	}
	var out []mvObs
	for team := 0; team < 2; team++ {
		var bs []int
		var xs, ys []float64
		for i, b := range bars {
			if b.Team != team {
				continue
			}
			if x, y, ok := mvToMap(b, v); ok && len(bs) < mvAssignBars {
				bs, xs, ys = append(bs, i), append(xs, x), append(ys, y)
			}
		}
		var cs []mvChamp
		for _, c := range champs {
			if c.Alive && (c.Team == 100) == (team == 0) {
				cs = append(cs, c)
			}
		}
		if len(bs) == 0 || len(cs) == 0 || len(cs) > 10 {
			continue
		}
		cost := make([][]float64, len(bs))
		match := make([]mvPlateMatch, len(bs))
		for i, bi := range bs {
			cost[i] = make([]float64, len(cs))
			var pc []float64
			if book != nil && bars[bi].Plate != nil {
				match[i] = book.match(team, bars[bi].Plate)
				pc = book.cost(team, match[i], cs)
			}
			for j, c := range cs {
				if pc != nil && pc[j] == 0 {
					c.HasPrev = false // the plate names this champion: where it was last seen is no evidence
				}
				cost[i][j] = mvPairCost(bars[bi], c, xs[i], ys[i])
				if pc != nil {
					cost[i][j] += pc[j]
				}
			}
		}
		best, pick := mvSolve(cost, len(cs), -1, -1)
		for i, j := range pick {
			if j < 0 || cost[i][j] > mvAssignMax {
				continue
			}
			// how much worse is the best assignment that denies this match?
			alt, _ := mvSolve(cost, len(cs), i, j)
			conf := bars[bs[i]].Conf * math.Min(1, (alt-best)/2)
			out = append(out, mvObs{PID: cs[j].PID, X: xs[i], Y: ys[i], Conf: conf})
		}
		if book != nil {
			for i, bi := range bs {
				if bars[bi].Plate != nil {
					book.observe(idx, team, bars[bi], xs[i], ys[i], match[i], cs)
				}
			}
		}
	}
	return out
}

// mvSolve finds the cheapest assignment of rows to distinct columns (a row may stay
// unmatched at mvAssignUnmatched), optionally forbidding the pair (nr, nc). Columns are
// tracked as a bitmask, so it is exact for up to ~10 champions per team.
func mvSolve(cost [][]float64, cols, nr, nc int) (float64, []int) {
	rows := len(cost)
	memo := make([]float64, (rows+1)<<uint(cols))
	choice := make([]int8, (rows+1)<<uint(cols))
	for i := range memo {
		memo[i] = -1
	}
	var rec func(i int, used int) float64
	rec = func(i int, used int) float64 {
		if i == rows {
			return 0
		}
		k := i<<uint(cols) | used
		if memo[k] >= 0 {
			return memo[k]
		}
		best := mvAssignUnmatched + rec(i+1, used)
		choice[k] = -1
		for j := 0; j < cols; j++ {
			if used>>uint(j)&1 == 1 || (i == nr && j == nc) {
				continue
			}
			if c := cost[i][j] + rec(i+1, used|1<<uint(j)); c < best {
				best, choice[k] = c, int8(j)
			}
		}
		memo[k] = best
		return best
	}
	total := rec(0, 0)
	pick := make([]int, rows)
	used := 0
	for i := 0; i < rows; i++ {
		j := int(choice[i<<uint(cols)|used])
		pick[i] = j
		if j >= 0 {
			used |= 1 << uint(j)
		}
	}
	return total, pick
}
