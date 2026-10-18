package main

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"time"
)

// Continuous sync between the broadcast video and the livestats feed.
//
// Everything is placed on the feed-time axis T (frame wall-clock timestamps, in seconds
// since the game spawned). A video frame at t = Idx/fps shows feed time t+O; O is the
// offset estimated here from two kinds of observations:
//
//   - kill flips: a HUD kill counter reaching n at video time t is the feed's totalKills
//     reaching n at T (±0.5 s). They fix O and teach the skew (see below).
//   - clock reads: the HUD game clock G at t is T = G + skew + Σpauses. Dense and precise,
//     but skew (feed time since spawn minus HUD clock) differs per broadcast.
//
// Clock reads are stored as c = G + Σpauses − t = O − skew, so a skew update never
// invalidates them; O = median(c) + skew. Skew is the median of (kill-flip O − c) samples,
// or the layout's prior skew until a flip has been matched.

const (
	syncFPS         = 2.0             // default video sampling rate
	syncWindow      = 120.0           // seconds of clock samples kept
	syncStale       = 180.0           // seconds without a usable sample before sync decays to calibrating
	syncMinSamples  = 6               // clock samples needed to call the estimate locked
	syncKillGate    = 20.0            // a flip must match a feed kill this close to the estimate
	syncStep        = 2.0             // deviation that counts as disagreement
	syncStepCount   = 3               // consistent disagreeing samples that re-lock
	syncStepSpan    = 2.0             // ... spread over at least this many video seconds
	syncSkewPrior   = 3.5             // skew before the layout is known (= feedPriorSkew in seconds)
	syncSkewKeep    = 16              // learned skew samples kept
	syncPriorWeight = 3               // flips the prior skew is worth
	syncKillKeep    = 900.0           // seconds of kill-flip offsets kept
	syncFlipGap     = 3.0             // max video gap across which a counter change is trusted
	syncMargin      = 6 * time.Second // playout distance kept behind the slower source
	syncCatchUp     = 10 * time.Second
	syncOffsetHold  = 0.3   // published offset moves only when the estimate does by more than this
	syncHoldMax     = 600.0 // video seconds (a long pause plus slack) after which a held disagreement is believed anyway
	syncHoldSlack   = 1.0   // the feed must be this far past a sample before the disagreement is believed
)

type syncSample struct{ t, v float64 }

// mmSync estimates the video-to-feed offset. Safe for concurrent use.
type mmSync struct {
	mu     sync.Mutex
	fps    float64
	spawn  time.Time
	pauses func() []feedPause

	clk   []syncSample // t, c = O − skew
	pend  []syncSample // consecutive clock samples that disagree with clk
	kills []syncSample // t, O from a matched kill flip
	skews []float64

	feedKills map[[2]int]float64 // (team,count) → feed seconds since spawn
	hudKills  map[[2]int]float64 // (team,count) → video seconds
	matched   map[[2]int]bool

	conf     [2]int // debounced HUD kill counters
	confOK   bool
	cand     [2]int
	candN    int
	candT    float64 // video time of the first frame showing cand
	candPrev float64 // last video time conf was seen before cand appeared
	prevT    float64

	lastT   float64 // newest observation, usable or not
	lastUse float64 // newest usable sample
	used    bool
	relocks int

	prior      float64 // skew used until a kill flip taught one
	feedLatest float64 // newest feed frame, seconds since spawn
	haveFeed   bool
	disSince   float64 // video time of the first sample of the current disagreement
	pub        float64 // published offset (hysteresis over the raw estimate)
	pubOK      bool
}

// newMMSync returns an estimator for a game that spawned at spawn, fed frames at fps. pauses
// reports the feed's game pauses sorted by start; it may be nil.
func newMMSync(spawn time.Time, fps float64, pauses func() []feedPause) *mmSync {
	if fps <= 0 {
		fps = syncFPS
	}
	return &mmSync{
		fps: fps, spawn: spawn, pauses: pauses, prior: syncSkewPrior,
		feedKills: map[[2]int]float64{}, hudKills: map[[2]int]float64{}, matched: map[[2]int]bool{},
	}
}

// syncLayoutSkew is the feed-minus-HUD clock skew of each broadcast layout in seconds, the
// best guess before a kill flip has taught the real one. It is the median of the skews
// this code's own pipeline (HUD clock reads, feed kills, mmSync) learns over whole games:
// worlds 3.02/3.59/~2.6 (three games), lck 2.79/2.58, lpl 3.23 (one game), lec 4.73/4.66.
var syncLayoutSkew = map[string]float64{"worlds": 3.1, "lck": 2.7, "lpl": 3.3, "lec": 4.7}

// SetLayout selects the prior skew of the named broadcast layout.
func (s *mmSync) SetLayout(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := syncLayoutSkew[name]; ok {
		s.prior = v
	}
}

func median(xs []float64) float64 {
	c := append([]float64(nil), xs...)
	sort.Float64s(c)
	n := len(c)
	if n%2 == 1 {
		return c[n/2]
	}
	return (c[n/2-1] + c[n/2]) / 2
}

// robustMean averages the values within a few MADs of the median. Clock samples sit on a
// 0.5 s lattice, so their median hops between lattice points; the mean does not.
func robustMean(xs []float64) float64 {
	med := median(xs)
	lim := math.Max(1, 3*madSigma(xs, med))
	sum, n := 0.0, 0
	for _, x := range xs {
		if math.Abs(x-med) <= lim {
			sum += x
			n++
		}
	}
	return sum / float64(n)
}

// madSigma is the robust standard deviation 1.4826·MAD.
func madSigma(xs []float64, med float64) float64 {
	d := make([]float64, len(xs))
	for i, x := range xs {
		d[i] = math.Abs(x - med)
	}
	return 1.4826 * median(d)
}

func values(ss []syncSample) []float64 {
	out := make([]float64, len(ss))
	for i, s := range ss {
		out[i] = s.v
	}
	return out
}

func (s *mmSync) pauseList() []feedPause {
	if s.pauses == nil {
		return nil
	}
	return s.pauses()
}

// skewLocked is the current feed-minus-HUD clock skew in seconds. The layout's prior
// counts as syncPriorWeight flips, so a single noisy flip cannot move it far.
func (s *mmSync) skewLocked() float64 {
	if len(s.skews) == 0 {
		return s.prior
	}
	xs := append(make([]float64, 0, len(s.skews)+syncPriorWeight), s.skews...)
	for i := 0; i < syncPriorWeight; i++ {
		xs = append(xs, s.prior)
	}
	return robustMean(xs)
}

// unpausedSpan is the video time between a and b not covered by a known game pause, at
// the current offset estimate.
func (s *mmSync) unpausedSpan(a, b float64) float64 {
	span := b - a
	o, _, ok := s.offsetLocked()
	if !ok {
		return span
	}
	for _, p := range s.pauseList() {
		st := p.Start.Sub(s.spawn).Seconds() - o
		span -= math.Max(0, math.Min(st+p.Dur.Seconds(), b)-math.Max(st, a))
	}
	return span
}

func (s *mmSync) skewUncLocked() float64 {
	switch n := len(s.skews); {
	case n == 0:
		return 1.5
	case n < 3:
		return 0.7
	default:
		return math.Max(0.2, madSigma(s.skews, median(s.skews))/math.Sqrt(float64(n)))
	}
}

// ObserveHUD ingests the HUD reading of the video frame with index idx.
func (s *mmSync) ObserveHUD(idx int, h mmHUD) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := float64(idx) / s.fps
	if t > s.lastT {
		s.lastT = t
	}
	if h.ClockOK {
		s.addClock(t, h.ClockSec)
	}
	if h.KillsOK {
		s.addKills(t, h.Blue, h.Red)
	}
}

// ObserveFeedLatest tells the estimator how far the feed has delivered; a disagreement
// that the feed has not yet reached may be a pause it has not reported.
func (s *mmSync) ObserveFeedLatest(t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.feedLatest, s.haveFeed = t.Sub(s.spawn).Seconds(), true
}

// ObserveRestart drops what was learned about the stream position (clock samples, flip
// offsets): the video restarted and its timeline jumped. The learned skew is kept.
func (s *mmSync) ObserveRestart() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clk, s.pend, s.kills = nil, nil, nil
	s.used, s.pubOK, s.confOK, s.candN = false, false, false, 0
}

// ObserveFeedKills ingests the feed's kill-count increases (repeats are fine).
func (s *mmSync) ObserveFeedKills(ks []feedKill) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range ks {
		key := [2]int{k.Team, k.Count}
		if _, ok := s.feedKills[key]; ok {
			continue
		}
		// The first frame showing the new count is up to one frame step after the kill.
		s.feedKills[key] = k.T.Sub(s.spawn).Seconds() - 0.25
		s.tryMatch(key)
	}
}

// feedOfGame inverts the pause accounting: the feed time (seconds since spawn) at which
// the HUD clock shows tg seconds of game time. ok is false when tg falls on a pause, where
// the frozen HUD clock says nothing about T.
func (s *mmSync) feedOfGame(tg float64) (shift float64, ok bool) {
	m := math.Max(1, 2.5*s.skewUncLocked()) // the frozen clock sits within the skew error of the pause start
	for _, p := range s.pauseList() {
		st := p.Start.Sub(s.spawn).Seconds()
		cur := tg + shift
		switch {
		case cur < st-m:
			return shift, true
		case cur <= st+m:
			return 0, false
		}
		shift += p.Dur.Seconds()
	}
	return shift, true
}

func (s *mmSync) addClock(t float64, sec int) {
	g := float64(sec) + 0.5 // the HUD shows the floor of the running clock
	shift, ok := s.feedOfGame(g + s.skewLocked())
	if !ok {
		return
	}
	c := g + shift - t
	if s.used && s.unpausedSpan(s.lastUse, t) > syncStale {
		s.clk, s.pend, s.pubOK = nil, nil, false // too long unobserved: start over
	}
	s.markUsed(t)
	if n := len(s.clk); n >= 5 {
		vs := values(s.clk)
		med := median(vs)
		if math.Abs(c-med) > math.Max(syncStep, 4*madSigma(vs, med)) {
			if s.pend = append(s.pend, syncSample{t, c}); len(s.pend) == 1 {
				s.disSince = t
			}
			if s.looksUnreportedPause(t, c, med) {
				if len(s.pend) > 8*syncStepCount {
					s.pend = s.pend[len(s.pend)-4*syncStepCount:]
				}
				return
			}
			s.maybeRelock()
			return
		}
	}
	s.pend = nil
	s.clk = append(s.clk, syncSample{t, c})
	for len(s.clk) > 0 && s.clk[0].t < t-syncWindow {
		s.clk = s.clk[1:]
	}
}

func (s *mmSync) markUsed(t float64) {
	if !s.used || t > s.lastUse {
		s.lastUse, s.used = t, true
	}
}

// looksUnreportedPause reports whether a clock sample that reads earlier than the lock
// predicts may be a game pause the feed has not reported yet: the HUD clock stood still
// while the feed has not delivered up to the sample's position. Such samples are held
// back instead of relocking; the feed's report makes later samples agree again.
func (s *mmSync) looksUnreportedPause(t, c, med float64) bool {
	if !s.haveFeed || c >= med || t-s.disSince >= syncHoldMax {
		return false
	}
	return s.feedLatest < t+med+s.skewLocked()+syncHoldSlack
}

// maybeRelock adopts the pending samples as the new truth once enough of them agree:
// the stream restarted or the pause accounting changed.
func (s *mmSync) maybeRelock() {
	ref := s.pend[len(s.pend)-1].v
	var agree []syncSample
	for _, p := range s.pend {
		if math.Abs(p.v-ref) <= 1 {
			agree = append(agree, p)
		}
	}
	if len(agree) < syncStepCount || agree[len(agree)-1].t-agree[0].t < syncStepSpan {
		if len(s.pend) > 8*syncStepCount {
			s.pend = s.pend[len(s.pend)-4*syncStepCount:]
		}
		return
	}
	s.clk, s.pend, s.kills, s.pubOK = agree, nil, nil, false
	s.relocks++
}

func (s *mmSync) addKills(t float64, blue, red int) {
	v := [2]int{blue, red}
	if !s.confOK {
		if s.candN > 0 && s.cand == v {
			s.candN++
		} else {
			s.cand, s.candN = v, 1
		}
		if s.candN >= 2 {
			s.conf, s.confOK, s.candN, s.prevT = v, true, 0, t
		}
		return
	}
	if v == s.conf {
		s.candN, s.prevT = 0, t
		return
	}
	if s.candN > 0 && v == s.cand {
		s.candN++
	} else {
		s.cand, s.candN, s.candT, s.candPrev = v, 1, t, s.prevT
	}
	if s.candN < 2 {
		return
	}
	// Debounced change. Only a single +1 on one team seen across a short gap is a flip.
	if s.candT-s.candPrev <= syncFlipGap {
		for team := 0; team < 2; team++ {
			if v[team] == s.conf[team]+1 && v[1-team] == s.conf[1-team] {
				key := [2]int{team, v[team]}
				s.hudKills[key] = (s.candT + s.candPrev) / 2
				s.tryMatch(key)
			}
		}
	}
	s.conf, s.candN, s.prevT = v, 0, t
}

// tryMatch turns a flip seen on both sides into an offset sample.
func (s *mmSync) tryMatch(key [2]int) {
	tv, okV := s.hudKills[key]
	tf, okF := s.feedKills[key]
	if !okV || !okF || s.matched[key] {
		return
	}
	s.matched[key] = true
	o := tf - tv
	if est, _, ok := s.offsetLocked(); ok && math.Abs(o-est) > syncKillGate {
		return
	}
	s.markUsed(tv)
	s.kills = append(s.kills, syncSample{tv, o})
	for len(s.kills) > 0 && s.kills[0].t < tv-syncKillKeep {
		s.kills = s.kills[1:]
	}
	var near []float64
	for _, c := range s.clk {
		if math.Abs(c.t-tv) <= 15 {
			near = append(near, c.v)
		}
	}
	if len(near) >= 3 {
		s.skews = append(s.skews, o-robustMean(near))
		if len(s.skews) > syncSkewKeep {
			s.skews = s.skews[1:]
		}
	}
}

// offsetLocked returns O and its uncertainty. Clock reads track it; with none, matched
// kill flips alone fix it.
func (s *mmSync) offsetLocked() (o, sigma float64, ok bool) {
	switch {
	case len(s.clk) > 0:
		vs := values(s.clk)
		med := median(vs)
		spread := math.Max(0.3, madSigma(vs, med))
		unc := s.skewUncLocked()
		return robustMean(vs) + s.skewLocked(), math.Sqrt(spread*spread + unc*unc), true
	case len(s.kills) > 0:
		vs := values(s.kills)
		med := median(vs)
		return med, math.Max(0.5, madSigma(vs, med)), true
	}
	return 0, 0, false
}

// Offset returns O = T − t in seconds with its uncertainty; ok is false before any sample.
func (s *mmSync) Offset() (o, sigma float64, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, sigma, ok = s.offsetLocked()
	if !ok {
		s.pubOK = false
		return 0, 0, false
	}
	// The raw estimate flickers by a frame step (median over an even window, 0.5 s frames).
	if !s.pubOK || math.Abs(o-s.pub) > syncOffsetHold {
		s.pub, s.pubOK = o, true
	}
	return s.pub, sigma, true
}

// Locked reports a trustworthy, recently confirmed estimate.
func (s *mmSync) Locked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lockedLocked()
}

func (s *mmSync) lockedLocked() bool {
	if _, _, ok := s.offsetLocked(); !ok || !s.used || s.unpausedSpan(s.lastUse, s.lastT) > syncStale {
		return false
	}
	return len(s.clk) >= syncMinSamples || len(s.kills) >= 2
}

// Status is the one-line state for the panel.
func (s *mmSync) Status() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.lockedLocked() {
		return "sync: calibrating"
	}
	_, sigma, _ := s.offsetLocked()
	return fmt.Sprintf("sync ✓ ±%.1fs", math.Ceil(sigma*10)/10)
}

// Skew is the learned feed-minus-HUD clock skew, the layout's prior until a kill flip taught it.
func (s *mmSync) Skew() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Duration(s.skewLocked() * float64(time.Second))
}

// Relocks counts step changes (stream restarts, pause misaccounting) adopted so far.
func (s *mmSync) Relocks() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.relocks
}

// FeedTime maps video time t (seconds) to feed time; ok is false before the first estimate.
func (s *mmSync) FeedTime(t float64) (time.Time, bool) {
	o, _, ok := s.Offset()
	return s.spawn.Add(time.Duration((t + o) * float64(time.Second))), ok
}

// VideoTime maps feed time T to video seconds; ok is false before the first estimate.
func (s *mmSync) VideoTime(T time.Time) (float64, bool) {
	o, _, ok := s.Offset()
	return T.Sub(s.spawn).Seconds() - o, ok
}

// playClock is the shared playout position on the feed-time axis: it advances in real
// time, never rewinds, holds when a source has not delivered that far, and catches up
// when it falls too far behind.
type playClock struct {
	t    time.Time
	at   time.Time
	on   bool
	hold bool // started at a given position: stand still until the sources are margin ahead of it
}

// Start puts the clock at feed time t, the position the viewer already sees, and holds it
// there until the sources have delivered syncMargin beyond it. The playout never moves
// back at a handover from another clock; it stands still instead.
func (c *playClock) Start(now, t time.Time) {
	*c = playClock{t: t, at: now, on: true, hold: true}
}

// Advance moves the clock to now and returns its feed time. edge is the newest feed time
// both sources can show: the earlier of the feed's newest frame and the video's newest
// frame mapped through the offset.
func (c *playClock) Advance(now, edge time.Time) time.Time {
	if !c.on {
		c.t, c.at, c.on = edge.Add(-syncMargin), now, true
		return c.t
	}
	if c.hold {
		if edge.Add(-syncMargin).Before(c.t) {
			c.at = now
			return c.t
		}
		c.hold = false
	}
	next := c.t.Add(now.Sub(c.at))
	c.at = now
	switch {
	case next.After(edge):
		next = edge
		if next.Before(c.t) {
			next = c.t
		}
	case edge.Sub(next) > syncMargin+syncCatchUp:
		next = edge.Add(-syncMargin)
	}
	c.t = next
	return c.t
}

// Reset forgets the position; the next Advance starts afresh from the sources.
func (c *playClock) Reset() { *c = playClock{} }
