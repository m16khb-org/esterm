package main

import (
	"math"
)

// Multi-object champion tracker on the minimap: candidate icons come from team rings
// (minimap_ring.go), the identity of each ring from the champions' portraits by a global
// assignment per team (Hungarian, minimap_kalman.go) whose cost adds the portrait
// dissimilarity and the distance from where a constant-velocity Kalman filter expects the
// champion. Tracks live on through hidden stretches (replay shots, banners, another icon on
// top): they only coast, their search gate widening with the time since the last sighting.
// What the feed says is used both ways: a champion that is dead is not looked for, its
// track stays where it died; a champion that has respawned restarts at its fountain.

const (
	mmSimMin        = 0.50          // lowest trimmed portrait NCC of a match inside a track's gate
	mmSimAcquire    = 0.60          // ... and of one found by a search of the whole map
	mmSimConfirm    = 0.80          // a track whose gate match scores less is also searched over the whole map
	mmNearColourMin = 0.50          // plausibleNear: lowest of score + mmColourWeight*(uv - mmSatWeight*sat)
	mmRevalPeriod   = 10            // frames between looks at the whole map for a champion that is tracked well
	mmPeaks         = 3             // matches kept per champion from a search of the whole map
	mmSearchMin     = 6             // px: smallest half-width of the search window around the prediction
	mmSearchMax     = 12            // px: largest
	mmSimMasked     = 0.62          // lowest NCC over the visible part of a portrait that other icons partly cover
	mmMaskedShare   = 0.40          // ... which must be at least this share of the portrait
	mmCarryFrames   = 3 * mmFPS / 2 // frames a champion hidden under an ally must stay hidden before it counts as carried by it
	mmCarryMax      = mmHoldMax     // frames a champion is carried by an ally (a support latched onto it) at most
	mmMVConf        = 0.3           // lowest confidence of a main-view observation that counts
	mmMVStd         = 3.0           // px standard deviation of a main-view position (a bar's foot, mapped through the camera)
	mmCostSim       = 3.0           // weight of (1 - similarity) in the assignment cost
	mmCostMotion    = 0.35          // weight of the squared distance, in gates, from the prediction
	mmCostMotionM   = 3.0           // cap of the squared distance in gates
	mmCostMiss      = 2.0           // cost of leaving a champion without a match this frame
	mmCostMasked    = 1.0           // cost of a match scored on part of the portrait only, times the part that was left out
	mmGateBase      = 5.0           // px: gate radius of a champion just seen
	mmGateSpeed     = 15.0          // px/s the gate grows by while a champion is out of sight (a walk is up to ~14 px/s, a blink 8 px)
	mmGateSpawn     = 16.0          // px of extra gate for a track started from a spawn prior
	mmCoverDist     = 0.8           // icon diameters: a lost champion this close to a found icon counts as covered by it
	mmCarryDist     = 1.3           // ... and an ally this close, which may carry it
	mmCoverFrames   = 10 * mmFPS
	mmDeathGrace    = 3 * mmFPS / 2 // frames after the feed's death during which the champion is still tracked (sync error)
	mmJumpRecent    = mmFPS         // frames: a champion measured this recently cannot have been teleported (see assign)
	mmJumpConfirm   = 2             // frames within which a far sighting must be seen again, near the first, to be taken
	mmAttachLost    = 3 * mmFPS     // frames an attach-capable champion must be off the map, alive, to count as attached to an ally
)

// mmAttachers are the champions that can latch onto an ally (Yuumi's W) and then have no
// icon of their own on the minimap: they are where their host is.
var mmAttachers = map[string]bool{"Yuumi": true}

// spawnPrior is where a champion respawns (normalized): the team's fountain.
func spawnPrior(team int) (x, y float64) {
	if team == 200 {
		return 0.95, 0.05
	}
	return 0.05, 0.95
}

// mmTrk is the state of one champion.
type mmTrk struct {
	kf       mmKF
	init     bool    // kf holds a position (seen, or spawned)
	lastIdx  int     // frame the filter was last advanced to
	measIdx  int     // frame of the last measurement or of the track's start; the age of the position counts from there
	covered  bool    // not found this frame, hidden under another found icon
	sim      float32 // portrait similarity of the last measurement
	spawn    bool    // the track started from a spawn prior and has not been measured since
	dead     bool    // the feed says dead
	deadIdx  int     // frame the feed's death was first seen
	frozen   bool    // dead for longer than mmDeathGrace: the position stays where it was
	deadX    float64 // position at the death frame, used when no measurement is near it
	deadY    float64
	lastMX   float64 // last measured position
	lastMY   float64
	measured bool // measured on the current frame
	viaMV    bool // ... by the main view, not by the portrait
	coverIdx int  // frame the champion was first seen covered in the current stretch, -1 when not covered
	carrier  int  // template index of the ally it is covered by, -1 for none
	carrying bool // follows the carrier: it is hidden under it, whereever it goes
	pend     bool // a sighting far from where the champion can be waits for its confirmation
	pendX    float64
	pendY    float64
	pendIdx  int // frame of that sighting
	lastHost int // template index of the ally it was last attached to or carried by, -1 for none
}

// mmTracker follows every champion of a game across the frames of one stream.
type mmTracker struct {
	tpls []champTpl
	trk  []mmTrk
	img  mmFrameGray
	rgb  []byte
	mask []bool // pixels covered by found icons (peel)
	line mmLineScratch
}

func newMMTracker(tpls []champTpl) *mmTracker {
	t := &mmTracker{tpls: tpls, trk: make([]mmTrk, len(tpls))}
	t.Reset()
	return t
}

// Reset forgets every position (the stream jumped).
func (t *mmTracker) Reset() {
	for i := range t.trk {
		t.trk[i] = mmTrk{coverIdx: -1, carrier: -1, lastHost: -1}
	}
}

func (t *mmTracker) d() int {
	if len(t.tpls) == 0 {
		return mmTplSize
	}
	return t.tpls[0].d
}

// advance moves every track to frame idx: dead and respawn bookkeeping from the feed's
// lives (nil: nothing is known, everyone is treated as alive), then the filters' predict.
func (t *mmTracker) advance(idx int, lives mmLives) {
	for k := range t.trk {
		tr := &t.trk[k]
		tr.measured, tr.viaMV = false, false
		ref := t.tpls[k].ref
		lf, known := lives[ref.PID]
		isDead := lives != nil && known && lf.Dead
		switch {
		case isDead && !tr.dead:
			tr.dead, tr.deadIdx, tr.frozen = true, idx, false
			if tr.init {
				tr.deadX, tr.deadY = tr.kf.x.p, tr.kf.y.p
			}
		case !isDead && tr.dead:
			// respawned: it stands at its fountain
			tr.dead, tr.frozen = false, false
			fx, fy := spawnPrior(ref.Team)
			tr.kf.init(fx*mmFrameSize, fy*mmFrameSize)
			// a respawn seen late (the map was hidden) started AliveFor seconds ago
			back := 0
			if !math.IsInf(lf.AliveFor, 1) {
				back = min(int(lf.AliveFor*mmFPS), idx-tr.lastIdx)
			}
			tr.init, tr.spawn, tr.lastIdx, tr.measIdx, tr.covered = true, true, idx, idx-max(back, 0), false
		}
		if tr.dead && !tr.frozen && idx-tr.deadIdx > mmDeathGrace {
			tr.frozen, tr.covered = true, false
			// the icon vanishes at the death: the last sighting at or just before it is where
			// the champion died, else the estimate at that moment
			if tr.init && tr.measIdx >= tr.deadIdx-2*mmFPS && !tr.spawn {
				tr.kf.x.p, tr.kf.y.p = tr.lastMX, tr.lastMY
			} else if tr.init {
				tr.kf.x.p, tr.kf.y.p = tr.deadX, tr.deadY
			}
		}
		if tr.init && !tr.frozen {
			tr.kf.predict(float64(idx-tr.lastIdx) / mmFPS)
		}
		tr.lastIdx = idx
	}
}

func (t *mmTracker) active(k int) bool { return !t.trk[k].frozen }

// gate is the search radius in px around the prediction of champion k at frame idx.
func (t *mmTracker) gate(k, idx int) float64 {
	tr := &t.trk[k]
	g := mmGateBase + mmGateSpeed*float64(idx-tr.measIdx)/mmFPS
	if tr.spawn {
		g += mmGateSpawn
	}
	return g
}

// Step processes one frame (mmFrameSize² rgb24) and returns one detection per template, in
// template order. lives is the feed's word on who is dead at this frame (nil: unknown).
func (t *mmTracker) Step(idx int, rgb []byte, lives mmLives) []mmDet {
	return t.StepWith(idx, rgb, lives, nil)
}

// StepWith is Step with positions read from the main view: they stand in for champions
// whose portrait was not found on the map.
func (t *mmTracker) StepWith(idx int, rgb []byte, lives mmLives, obs []mvObs) []mmDet {
	rgb, _ = t.line.unline(rgb)
	t.img.set(rgb)
	t.rgb = rgb
	t.advance(idx, lives)
	t.match(idx)
	t.fuse(idx, obs)
	t.markCovered(idx)
	return t.dets(idx)
}

// Prev is where champion pid was last measured (normalized) and how many seconds ago, for
// the main-view reader; ok is false for a champion that was never measured.
func (t *mmTracker) Prev(pid, idx int) (x, y, age float64, ok bool) {
	for k := range t.trk {
		if t.tpls[k].ref.PID == pid {
			tr := &t.trk[k]
			if !tr.init || tr.spawn {
				return 0, 0, 0, false
			}
			return tr.lastMX / mmFrameSize, tr.lastMY / mmFrameSize, float64(idx-tr.measIdx) / mmFPS, true
		}
	}
	return 0, 0, 0, false
}

// fuse takes the main-view observations as measurements of the champions the portraits did
// not find. A champion the map search found keeps its position; an observation that lands
// on another champion's icon, which the map search found with a good match, is dropped.
func (t *mmTracker) fuse(idx int, obs []mvObs) {
	d := float64(t.d())
	for _, o := range obs {
		if o.Conf < mmMVConf {
			continue
		}
		k := -1
		for i := range t.tpls {
			if t.tpls[i].ref.PID == o.PID {
				k = i
			}
		}
		if k < 0 || !t.active(k) || t.trk[k].measured {
			continue
		}
		x, y := o.X*mmFrameSize, o.Y*mmFrameSize
		clash := false
		for m := range t.trk {
			if o2 := &t.trk[m]; m != k && o2.measured && o2.sim >= mmSimConfirm && math.Hypot(o2.kf.x.p-x, o2.kf.y.p-y) < d*0.4 {
				clash = true
			}
		}
		if clash {
			continue
		}
		tr := &t.trk[k]
		if !tr.init || tr.spawn || math.Hypot(x-tr.kf.x.p, y-tr.kf.y.p) > 1.5*t.gate(k, idx) {
			tr.kf.init(x, y)
		} else {
			tr.kf.update(x, y, mmMVStd)
		}
		tr.init, tr.spawn, tr.measured, tr.covered, tr.viaMV = true, false, true, false, true
		tr.measIdx, tr.lastMX, tr.lastMY = idx, x, y
		tr.coverIdx, tr.carrier, tr.carrying = -1, -1, false
	}
}

// Coast advances the tracks over a frame that shows no usable map (an objective banner).
func (t *mmTracker) Coast(idx int, lives mmLives) []mmDet {
	t.advance(idx, lives)
	t.markCovered(idx)
	return t.dets(idx)
}

// mmHyp is one place champion k may be: the match of its portrait there.
type mmHyp struct {
	k       int
	h       mmHit
	cx, cy  float64 // centre of the match in px
	penalty float64 // added to the assignment cost
}

// hypotheses finds where champion k may be on this frame: its portrait is searched in the
// gate around the filter's prediction on every frame, and over the whole map when it is not
// tracked, was not found there, or is due for a look at the whole map (a lock on a
// look-alike must not last).
func (t *mmTracker) hypotheses(k, idx int, out []mmHyp) []mmHyp {
	tp, tr := &t.tpls[k], &t.trk[k]
	d := tp.d
	maxXY := mmFrameSize - d
	gate := t.gate(k, idx)
	best := float32(-1)
	add := func(h mmHit) {
		cx, cy := float64(h.x)+float64(d)/2, float64(h.y)+float64(d)/2
		out = append(out, mmHyp{k: k, h: h, cx: cx, cy: cy})
		best = max(best, h.score)
	}
	if tr.init {
		g := clampInt(int(math.Ceil(gate)), mmSearchMin, mmSearchMax)
		tx, ty := int(math.Round(tr.kf.x.p))-d/2, int(math.Round(tr.kf.y.p))-d/2
		h := tp.scanTrim(&t.img, mmWin{
			clampInt(tx-g, 0, maxXY), clampInt(tx+g, 0, maxXY),
			clampInt(ty-g, 0, maxXY), clampInt(ty+g, 0, maxXY),
		}, nil, nil)
		if h.score >= mmSimMin && t.plausibleNear(tp, h.x, h.y, h.score) {
			add(h)
		}
	}
	if !tr.init || best < mmSimConfirm || (idx+k)%mmRevalPeriod == 0 {
		for _, h := range tp.scanPeaks(&t.img, mmWin{0, maxXY, 0, maxXY}, mmPeaks, mmSimAcquire) {
			dup := false
			for _, o := range out {
				if math.Hypot(float64(h.x-o.h.x), float64(h.y-o.h.y)) < float64(d)*0.3 {
					dup = true
					break
				}
			}
			if !dup && t.plausible(tp, h.x, h.y, h.score) {
				add(h)
			}
		}
	}
	return out
}

// plausibleNear is plausible for a match inside the gate of a track: a sure match goes
// through, a weaker one needs a team ring and a portrait that is more than a hint, the
// position being already evidence.
func (t *mmTracker) plausibleNear(tp *champTpl, x, y int, s float32) bool {
	if s >= mmSureScore {
		return t.colourScore(tp, x, y, s) >= mmSureColour
	}
	if ringLean(t.rgb, x, y, tp.d, tp.ref.Team) < mmSureLean {
		return false
	}
	return t.colourScore(tp, x, y, s) >= mmNearColourMin
}

// match finds every active champion on the frame: first by the hypotheses of all of them
// at once, then, for those still missing, among the pixels the found icons leave free.
func (t *mmTracker) match(idx int) {
	var rows []int
	var hyps []mmHyp
	for k := range t.tpls {
		if t.active(k) {
			rows = append(rows, k)
			hyps = t.hypotheses(k, idx, hyps)
		}
	}
	t.assign(idx, rows, hyps)
	t.peel(idx, rows)
}

// assign gives every row champion at most one of the places the hypotheses found, so that
// no icon is claimed twice and the total cost (portrait dissimilarity plus distance from
// the prediction) is least, and feeds the filters of those that got one.
func (t *mmTracker) assign(idx int, rows []int, hyps []mmHyp) {
	if len(hyps) == 0 {
		return
	}
	d := float64(t.d())
	// places: hypotheses of different champions on one icon share a column
	var cols []mmHyp
	col := make([]int, len(hyps))
	for i, hy := range hyps {
		col[i] = -1
		for j, c := range cols {
			if math.Hypot(hy.cx-c.cx, hy.cy-c.cy) < d*0.5 {
				col[i] = j
				break
			}
		}
		if col[i] < 0 {
			col[i] = len(cols)
			cols = append(cols, hy)
		}
	}
	row := make(map[int]int, len(rows))
	cost := make([][]float64, len(rows))
	for i, k := range rows {
		row[k] = i
		cost[i] = make([]float64, len(cols)+len(rows))
		for j := range cols {
			cost[i][j] = 1e6
		}
		for j := len(cols); j < len(cols)+len(rows); j++ {
			cost[i][j] = mmCostMiss
		}
	}
	best := make([]int, len(rows)*len(cols)) // hypothesis index behind cost[i][j]
	for i := range best {
		best[i] = -1
	}
	for i, hy := range hyps {
		r := row[hy.k]
		tr := &t.trk[hy.k]
		motion := 0.0
		if tr.init {
			g := t.gate(hy.k, idx)
			dist := math.Hypot(hy.cx-tr.kf.x.p, hy.cy-tr.kf.y.p)
			motion = math.Min(dist*dist/(g*g), mmCostMotionM)
		}
		c := mmCostSim*float64(1-hy.h.score) + mmCostMotion*motion + hy.penalty
		if c < cost[r][col[i]] {
			cost[r][col[i]] = c
			best[r*len(cols)+col[i]] = i
		}
	}
	for r, c := range mmAssign(cost) {
		if c >= len(cols) || cost[r][c] >= mmCostMiss {
			continue
		}
		hy := hyps[best[r*len(cols)+c]]
		k := rows[r]
		tr := &t.trk[k]
		gate := t.gate(k, idx)
		far := math.Hypot(hy.cx-tr.kf.x.p, hy.cy-tr.kf.y.p) > 1.5*gate
		if tr.init && !tr.spawn && far && idx-tr.measIdx <= mmJumpRecent {
			// a champion seen a moment ago turns up far from where it can be: a swap with
			// another icon or a look-alike on the terrain, unless it is there on the next
			// frame too (a teleport costs one frame)
			if !tr.pend || idx-tr.pendIdx > mmJumpConfirm || math.Hypot(hy.cx-tr.pendX, hy.cy-tr.pendY) > 0.5*float64(t.d()) {
				tr.pend, tr.pendX, tr.pendY, tr.pendIdx = true, hy.cx, hy.cy, idx
				continue
			}
		}
		tr.pend = false
		if !tr.init || tr.spawn || far {
			// first sighting, a respawn, or a jump the filter cannot have predicted (a
			// teleport, a recall): start over rather than smooth towards it
			tr.kf.init(hy.cx, hy.cy)
		} else {
			tr.kf.update(hy.cx, hy.cy, mmKFMeasStd)
		}
		tr.init, tr.spawn, tr.measured, tr.covered = true, false, true, false
		tr.measIdx, tr.sim, tr.lastMX, tr.lastMY = idx, hy.h.score, hy.cx, hy.cy
		tr.coverIdx, tr.carrier, tr.carrying = -1, -1, false
	}
}

// peel looks again for the champions that are on a track but were not found, among the
// pixels the found icons leave free: an icon that another one overlaps shows only a
// crescent, which the portrait still matches when the covered part is left out.
func (t *mmTracker) peel(idx int, rows []int) {
	var miss []int
	for _, k := range rows {
		if tr := &t.trk[k]; tr.init && !tr.measured && !tr.spawn {
			miss = append(miss, k)
		}
	}
	if len(miss) == 0 {
		return
	}
	d := t.d()
	if t.mask == nil {
		t.mask = make([]bool, mmFrameSize*mmFrameSize)
	}
	clear(t.mask)
	r := float64(d) / 2
	found := false
	for k := range t.trk {
		tr := &t.trk[k]
		if !tr.measured {
			continue
		}
		found = true
		cx, cy := tr.kf.x.p, tr.kf.y.p
		for y := max(int(cy-r), 0); y <= min(int(cy+r), mmFrameSize-1); y++ {
			for x := max(int(cx-r), 0); x <= min(int(cx+r), mmFrameSize-1); x++ {
				if math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy) <= r {
					t.mask[y*mmFrameSize+x] = true
				}
			}
		}
	}
	if !found {
		return
	}
	var hyps []mmHyp
	maxXY := mmFrameSize - d
	for _, k := range miss {
		tp, tr := &t.tpls[k], &t.trk[k]
		g := clampInt(int(math.Ceil(t.gate(k, idx))), mmSearchMin, mmSearchMax)
		tx, ty := int(math.Round(tr.kf.x.p))-d/2, int(math.Round(tr.kf.y.p))-d/2
		h, share := tp.scanMasked(&t.img, mmWin{
			clampInt(tx-g, 0, maxXY), clampInt(tx+g, 0, maxXY),
			clampInt(ty-g, 0, maxXY), clampInt(ty+g, 0, maxXY),
		}, t.mask, mmMaskedShare)
		if h.score < mmSimMasked {
			continue
		}
		hyps = append(hyps, mmHyp{k: k, h: h, cx: float64(h.x) + float64(d)/2, cy: float64(h.y) + float64(d)/2, penalty: mmCostMasked * float64(1-share)})
	}
	t.assign(idx, miss, hyps)
}

// markCovered flags the champions that were not found but whose estimate lies under a found
// icon: they are hidden by it, not gone, so they keep their live look for a while. One that
// stays hidden under an ally for mmCarryFrames is taken to be carried by it (a support
// latched onto a champion has no icon of its own) and follows it. A champion that is known
// to latch onto allies (mmAttachers) and is not on the map at all for mmAttachLost frames,
// alive, is attached whether or not it was seen next to its host: its host is the one it
// had last, else the team's bottom laner, else the ally nearest to where it was. An
// attached champion stays with its host through the host's own hidden stretches (the host's
// estimate is its position, the host's age its age) until it is found, or the host dies.
func (t *mmTracker) markCovered(idx int) {
	d := float64(t.d())
	for k := range t.trk {
		tr := &t.trk[k]
		attacher := mmAttachers[t.tpls[k].ref.Champ]
		if tr.measured || tr.frozen || (!tr.init && !attacher) {
			continue
		}
		if tr.carrying {
			o := &t.trk[tr.carrier]
			if (o.measured || (attacher && o.init && !o.frozen)) && (attacher || idx-tr.coverIdx <= mmCarryMax) {
				tr.kf.init(o.kf.x.p, o.kf.y.p)
				tr.covered = true
				continue
			}
			tr.carrying, tr.carrier, tr.coverIdx = false, -1, -1
		}
		tr.covered = false
		if tr.init && idx-tr.measIdx > mmCoverFrames && !attacher { // hidden under an icon for this long: lost, unless an ally carries it
			tr.coverIdx, tr.carrier = -1, -1
			continue
		}
		near, nearD := -1, mmCoverDist*d
		for m := range t.trk {
			o := &t.trk[m]
			if m == k || !o.measured || !tr.init {
				continue
			}
			lim := mmCoverDist * d
			if t.tpls[m].ref.Team == t.tpls[k].ref.Team {
				lim = mmCarryDist * d
			}
			if dd := math.Hypot(o.kf.x.p-tr.kf.x.p, o.kf.y.p-tr.kf.y.p); dd <= lim && (near < 0 || dd < nearD) {
				near, nearD = m, dd
			}
		}
		if near < 0 {
			tr.coverIdx, tr.carrier = -1, -1
			if attacher && idx-tr.measIdx >= mmAttachLost {
				if h := t.attachHost(k); h >= 0 {
					tr.carrying, tr.carrier, tr.coverIdx, tr.lastHost = true, h, idx, h
					tr.kf.init(t.trk[h].kf.x.p, t.trk[h].kf.y.p)
					tr.init, tr.spawn, tr.covered = true, false, true
				}
			}
			continue
		}
		tr.covered = true
		if tr.coverIdx < 0 || tr.carrier != near {
			tr.carrier, tr.coverIdx = -1, idx
			if t.tpls[near].ref.Team == t.tpls[k].ref.Team {
				tr.carrier = near
			}
		}
		if tr.carrier >= 0 && idx-tr.coverIdx >= mmCarryFrames {
			tr.carrying, tr.lastHost = true, tr.carrier
		}
	}
}

// attachHost is the ally champion k is attached to when it is missing from the map: the
// host it had last, else the team's bottom laner (a support latches onto its carry), else
// the ally nearest to where k was. Only allies found on the frame count; -1 for none.
func (t *mmTracker) attachHost(k int) int {
	tr := &t.trk[k]
	if h := tr.lastHost; h >= 0 && t.trk[h].measured {
		return h
	}
	team := t.tpls[k].ref.Team
	bottom, near, nearD := -1, -1, math.Inf(1)
	for m := range t.trk {
		if m == k || !t.trk[m].measured || t.tpls[m].ref.Team != team {
			continue
		}
		if t.tpls[m].ref.Role == "bottom" {
			bottom = m
		}
		if dd := math.Hypot(t.trk[m].kf.x.p-tr.kf.x.p, t.trk[m].kf.y.p-tr.kf.y.p); tr.init && dd < nearD {
			near, nearD = m, dd
		}
	}
	if bottom >= 0 {
		return bottom
	}
	return near
}

// dets reports every champion at frame idx.
func (t *mmTracker) dets(idx int) []mmDet {
	out := make([]mmDet, len(t.tpls))
	for k := range t.tpls {
		tr := &t.trk[k]
		d := mmDet{champRef: t.tpls[k].ref, Score: tr.sim}
		if tr.init {
			age := idx - tr.measIdx
			if tr.covered && !tr.dead {
				age = 0
			}
			if tr.carrying && !tr.dead {
				// it is where its host is, as fresh as the host's own position
				if h := &t.trk[tr.carrier]; h.measured || h.covered {
					age = 0
				} else {
					age = idx - h.measIdx
				}
			}
			switch {
			case tr.dead:
				d.Visible = true
			default:
				d.Visible = age <= mmHoldMax
			}
			d.X = math.Min(math.Max(tr.kf.x.p/mmFrameSize, 0), 1)
			d.Y = math.Min(math.Max(tr.kf.y.p/mmFrameSize, 0), 1)
			d.Age, d.Covered, d.MV, d.Attached = age, tr.covered, tr.viaMV, tr.carrying
		}
		out[k] = d
	}
	return out
}
