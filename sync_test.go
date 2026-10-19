package main

import (
	"math"
	"math/rand"
	"strings"
	"testing"
	"time"
)

// simGame is a synthetic game: the feed runs on wall-clock time T (seconds since spawn),
// the HUD shows T − skew − Σpauses, and the video shows feed time t+o at video time t.
type simGame struct {
	spawn  time.Time
	skew   float64
	o      float64
	pauses []feedPause
	kills  []feedKill // T is the true flip time
}

func sec(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

func (g *simGame) at(s float64) time.Time { return g.spawn.Add(sec(s)) }

func (g *simGame) pause(start, dur float64) {
	g.pauses = append(g.pauses, feedPause{g.at(start), sec(dur)})
}

func (g *simGame) kill(team, count int, T float64) {
	g.kills = append(g.kills, feedKill{team, count, g.at(T)})
}

// hud is what the broadcast shows at video time t.
func (g *simGame) hud(t float64) mmHUD {
	T := g.at(t + g.o)
	game := T.Sub(g.spawn).Seconds() - g.skew - pauseShift(g.pauses, T).Seconds()
	h := mmHUD{ClockSec: int(math.Floor(game)), ClockOK: true, KillsOK: true}
	for _, k := range g.kills {
		if !k.T.After(T) {
			if k.Team == 0 {
				h.Blue = k.Count
			} else {
				h.Red = k.Count
			}
		}
	}
	return h
}

// feedKills are the kill events as the feed reports them: stamped with the first frame
// after the flip.
func (g *simGame) feedKills() []feedKill {
	out := append([]feedKill(nil), g.kills...)
	for i := range out {
		out[i].T = out[i].T.Add(250 * time.Millisecond)
	}
	return out
}

func (g *simGame) sync(fps float64) *mmSync {
	return newMMSync(g.spawn, fps, func() []feedPause { return g.pauses })
}

// run feeds video seconds [from,to) at fps to s, applying tweak to every reading.
func run(s *mmSync, g *simGame, from, to, fps float64, tweak func(t float64, h *mmHUD)) {
	for i := int(from * fps); float64(i) < to*fps; i++ {
		t := float64(i) / fps
		h := g.hud(t)
		if tweak != nil {
			tweak(t, &h)
		}
		s.ObserveHUD(i, h)
	}
}

func wantOffset(t *testing.T, s *mmSync, want, tol float64) {
	t.Helper()
	o, _, ok := s.Offset()
	if !ok || math.Abs(o-want) > tol {
		t.Fatalf("offset %.2f ok=%v, want %.2f ±%.2f", o, ok, want, tol)
	}
}

func killsEvery(g *simGame, from, every, until float64) {
	n := [2]int{}
	i := 0
	for T := from; T < until; T += every {
		team := i % 2
		n[team]++
		g.kill(team, n[team], T)
		i++
	}
}

func TestSyncSteadyStateLocksAndLearnsSkew(t *testing.T) {
	g := &simGame{spawn: t0, skew: 4.9, o: 100}
	killsEvery(g, 150, 37, 700)
	s := g.sync(2)
	s.ObserveFeedKills(g.feedKills())
	run(s, g, 0, 60, 2, nil)
	run(s, g, 60, 600, 2, nil)
	wantOffset(t, s, 100, 0.5)
	if d := s.Skew() - sec(4.9); d < -500*time.Millisecond || d > 500*time.Millisecond {
		t.Fatalf("skew %s, want 4.9s", s.Skew())
	}
	if !s.Locked() || !strings.HasPrefix(s.Status(), "sync ✓ ±") {
		t.Fatalf("status %q", s.Status())
	}
	if s.Relocks() != 0 {
		t.Fatalf("relocks %d", s.Relocks())
	}
}

func TestSyncClockOnlyWithCorrectSkewIsAccurate(t *testing.T) {
	g := &simGame{spawn: t0, skew: 3.5, o: -40.5}
	s := g.sync(2)
	run(s, g, 100, 700, 2, nil) // ten minutes, no kill flips at all
	wantOffset(t, s, -40.5, 1)
	if !s.Locked() {
		t.Fatalf("not locked: %s", s.Status())
	}
	if _, sigma, _ := s.Offset(); sigma < 1 {
		t.Fatalf("uncertainty %.2f claims more than the unlearned skew allows", sigma)
	}
}

func TestSyncSurvivesLongPauseWithoutRelock(t *testing.T) {
	g := &simGame{spawn: t0, skew: 3.5, o: 100}
	g.pause(500, 202)
	killsEvery(g, 60, 53, 1500)
	s := g.sync(2)
	s.ObserveFeedKills(g.feedKills())
	for _, w := range [][2]float64{{0, 400}, {400, 800}, {800, 1200}} {
		run(s, g, w[0], w[1], 2, nil)
		wantOffset(t, s, 100, 0.6)
	}
	if s.Relocks() != 0 {
		t.Fatalf("a pause caused %d relocks", s.Relocks())
	}
	// The game clock stands still over the pause and resumes where it stopped.
	game := func(T float64) float64 {
		return T - g.skew - pauseShift(g.pauses, g.at(T)).Seconds()
	}
	if a, b := game(501), game(700); a != b {
		t.Fatalf("clock moved in the pause: %.1f -> %.1f", a, b)
	}
	if d := game(703) - game(702); d != 1 {
		t.Fatalf("clock after resume advanced %.1f in 1 s", d)
	}
}

func TestSyncUnknownPauseRelocksOnce(t *testing.T) {
	// The feed buffer missed the pause: the clock suddenly lags by the pause length.
	g := &simGame{spawn: t0, skew: 3.5, o: 100}
	real := &simGame{spawn: t0, skew: 3.5, o: 100}
	real.pause(300, 120)
	s := g.sync(2)
	run(s, real, 0, 600, 2, nil)
	if s.Relocks() != 1 {
		t.Fatalf("relocks %d, want 1", s.Relocks())
	}
	wantOffset(t, s, 100-120, 0.6)
}

func TestSyncStreamRestartRelocks(t *testing.T) {
	g := &simGame{spawn: t0, skew: 3.5, o: 100}
	killsEvery(g, 40, 41, 900)
	s := g.sync(2)
	s.ObserveFeedKills(g.feedKills())
	run(s, g, 0, 300, 2, nil)
	wantOffset(t, s, 100, 0.6)
	// The pipeline restarts at a live edge 37 s further along: same game, new video timeline.
	g.o -= 37
	run(s, g, 300, 600, 2, nil)
	if s.Relocks() != 1 {
		t.Fatalf("relocks %d, want 1", s.Relocks())
	}
	wantOffset(t, s, 63, 0.6)
	if !s.Locked() {
		t.Fatalf("not locked after restart: %s", s.Status())
	}
}

func TestSyncIgnoresMisreadClockAndKillCounters(t *testing.T) {
	g := &simGame{spawn: t0, skew: 3.5, o: 100}
	killsEvery(g, 40, 41, 900)
	s := g.sync(2)
	s.ObserveFeedKills(g.feedKills())
	rng := rand.New(rand.NewSource(7))
	run(s, g, 0, 600, 2, func(t float64, h *mmHUD) {
		if rng.Intn(6) == 0 {
			h.ClockSec = rng.Intn(3600)
		}
		if rng.Intn(10) == 0 { // one-frame counter glitch
			h.Blue += 1 + rng.Intn(3)
		}
	})
	wantOffset(t, s, 100, 0.7)
	if s.Relocks() != 0 {
		t.Fatalf("misreads caused %d relocks", s.Relocks())
	}
}

func TestSyncKillFlipsMatchWhicheverSideArrivesLast(t *testing.T) {
	g := &simGame{spawn: t0, skew: 3.5, o: 50}
	g.kill(0, 1, 100)
	g.kill(1, 1, 130)
	g.kill(0, 2, 170)
	s := g.sync(2)
	s.ObserveFeedKills(g.feedKills()[:1]) // feed first for kill 1, video first for the rest
	run(s, g, 0, 200, 2, nil)
	if len(s.kills) != 1 {
		t.Fatalf("matched flips %d, want 1", len(s.kills))
	}
	s.ObserveFeedKills(g.feedKills())
	if len(s.kills) != 3 {
		t.Fatalf("matched flips %d, want 3", len(s.kills))
	}
	if len(s.skews) != 3 {
		t.Fatalf("skew samples %d, want 3", len(s.skews))
	}
}

func TestSyncKillGateRejectsDistantMatch(t *testing.T) {
	g := &simGame{spawn: t0, skew: 3.5, o: 100}
	g.kill(0, 1, 200)
	s := g.sync(2)
	run(s, g, 0, 260, 2, nil)
	// A feed event with the same team and count but 90 s elsewhere is another game or a
	// stale buffer: it must not become a sample.
	s.ObserveFeedKills([]feedKill{{0, 1, g.at(290)}})
	if len(s.kills) != 0 {
		t.Fatalf("distant kill matched: %+v", s.kills)
	}
	wantOffset(t, s, 100, 0.6)
}

func TestSyncDecaysToCalibrating(t *testing.T) {
	g := &simGame{spawn: t0, skew: 3.5, o: 10}
	s := g.sync(2)
	run(s, g, 0, 120, 2, nil)
	if !s.Locked() {
		t.Fatal("not locked")
	}
	for i := 240; i < 240+2*(syncStale+10); i++ {
		s.ObserveHUD(i, mmHUD{}) // HUD gone: replay or desk
	}
	if s.Locked() || !strings.Contains(s.Status(), "calibrating") {
		t.Fatalf("status %q after %.0f s without samples", s.Status(), syncStale+10)
	}
	run(s, g, 600, 700, 2, nil)
	if !s.Locked() {
		t.Fatalf("did not recover: %s", s.Status())
	}
	wantOffset(t, s, 10, 0.6)
}

func TestSyncTimeMapping(t *testing.T) {
	g := &simGame{spawn: t0, skew: 3.5, o: 25}
	s := g.sync(2)
	if _, ok := s.FeedTime(5); ok {
		t.Fatal("mapping offered before any sample")
	}
	run(s, g, 0, 30, 2, nil)
	T, _ := s.FeedTime(10)
	if d := T.Sub(t0.Add(35 * time.Second)); d < -time.Second || d > time.Second {
		t.Fatalf("FeedTime(10) = %s", T.Sub(t0))
	}
	if v, _ := s.VideoTime(T); math.Abs(v-10) > 1e-6 {
		t.Fatalf("VideoTime round trip %.3f", v)
	}
}

func TestPlayClockHoldsCatchesUpNeverRewinds(t *testing.T) {
	var c playClock
	now := t0.Add(time.Hour)
	edge := t0
	if got := c.Advance(now, edge); !got.Equal(edge.Add(-syncMargin)) {
		t.Fatalf("start %s", got.Sub(t0))
	}
	// Real-time advance while sources keep ahead.
	now, edge = now.Add(2*time.Second), edge.Add(2*time.Second)
	if got := c.Advance(now, edge); !got.Equal(edge.Add(-syncMargin)) {
		t.Fatalf("advance %s", got.Sub(t0))
	}
	// A slower source stalls: hold at its edge, never pass it.
	now = now.Add(20 * time.Second)
	got := c.Advance(now, edge)
	if !got.Equal(edge) {
		t.Fatalf("hold %s, want edge", got.Sub(t0))
	}
	// An edge that moves backwards (offset re-estimate) must not rewind the clock.
	if back := c.Advance(now.Add(time.Second), edge.Add(-5*time.Second)); back.Before(got) {
		t.Fatalf("rewound to %s", back.Sub(t0))
	}
	// Far behind the sources: jump forward to margin behind the edge.
	now, edge = now.Add(time.Second), edge.Add(5*time.Minute)
	if got := c.Advance(now, edge); !got.Equal(edge.Add(-syncMargin)) {
		t.Fatalf("catch-up %s", got.Sub(edge))
	}
	c.Reset()
	if c.on {
		t.Fatal("reset keeps state")
	}
}

func TestSyncLayoutPriorSkewIsUsedBeforeAnyFlip(t *testing.T) {
	g := &simGame{spawn: t0, skew: syncLayoutSkew["lck"], o: 100.1}
	s := g.sync(2)
	s.SetLayout("lck")
	run(s, g, 0, 60, 2, nil) // no kill flip anywhere: only the prior can place the clock
	wantOffset(t, s, 100.1, 0.3)
	if s.Skew() != sec(syncLayoutSkew["lck"]) {
		t.Fatalf("skew %s", s.Skew())
	}
	s2 := g.sync(2)
	s2.SetLayout("unknown")
	if s2.Skew() != sec(syncSkewPrior) {
		t.Fatalf("an unknown layout must keep the default prior, got %s", s2.Skew())
	}
}

func TestSyncPublishedOffsetDoesNotDither(t *testing.T) {
	g := &simGame{spawn: t0, skew: 3.5, o: 100.37}
	s := g.sync(2)
	changes, last := 0, math.NaN()
	for i := 0; i < 600; i++ {
		s.ObserveHUD(i, g.hud(float64(i)/2))
		if i < 120 {
			continue
		}
		if o, _, ok := s.Offset(); ok && o != last {
			changes++
			last = o
		}
	}
	if changes > 1 {
		t.Fatalf("published offset changed %d times in a steady state", changes)
	}
}

func TestSyncLockSurvivesPauseLongerThanStaleness(t *testing.T) {
	g := &simGame{spawn: t0, skew: 3.5, o: 100}
	g.pause(300, 400) // > syncStale, HUD clock frozen throughout
	s := g.sync(2)
	for from := 0.0; from < 900; from += 10 {
		run(s, g, from, from+10, 2, nil)
		if from >= 100 && !s.Locked() {
			t.Fatalf("lock lost at video %.0f s", from)
		}
	}
	if s.Relocks() != 0 {
		t.Fatalf("relocks %d", s.Relocks())
	}
	wantOffset(t, s, 100, 0.6)
}

func TestSyncHoldsLockWhileFeedHasNotReportedThePause(t *testing.T) {
	real := &simGame{spawn: t0, skew: 3.5, o: 100}
	real.pause(300, 202)
	reported := false
	s := newMMSync(t0, 2, func() []feedPause {
		if reported {
			return real.pauses
		}
		return nil
	})
	s.ObserveFeedLatest(real.at(300)) // the feed stopped at the paused frame
	run(s, real, 0, 460, 2, nil)      // the video is past the resume (video 402 s) already
	if s.Relocks() != 0 {
		t.Fatalf("relocked %d times on an unreported pause", s.Relocks())
	}
	wantOffset(t, s, 100, 0.6)
	reported = true
	s.ObserveFeedLatest(real.at(520))
	run(s, real, 460, 700, 2, nil)
	if s.Relocks() != 0 {
		t.Fatalf("relocked %d times once the pause was reported", s.Relocks())
	}
	wantOffset(t, s, 100, 0.6)
}

func TestSyncDisagreementWithCurrentFeedStillRelocks(t *testing.T) {
	g := &simGame{spawn: t0, skew: 3.5, o: 100}
	real := &simGame{spawn: t0, skew: 3.5, o: 100}
	real.pause(300, 120)
	s := g.sync(2)
	s.ObserveFeedLatest(g.at(10000)) // the feed is far past: nothing unreported can hide
	run(s, real, 0, 600, 2, nil)
	if s.Relocks() != 1 {
		t.Fatalf("relocks %d, want 1", s.Relocks())
	}
}

func TestSyncRestartDropsSamplesKeepsSkew(t *testing.T) {
	g := &simGame{spawn: t0, skew: 4.9, o: 100}
	killsEvery(g, 40, 41, 900)
	s := g.sync(2)
	s.ObserveFeedKills(g.feedKills())
	run(s, g, 0, 300, 2, nil)
	skew := s.Skew()
	s.ObserveRestart()
	if s.Locked() {
		t.Fatal("still locked right after a restart")
	}
	if s.Skew() != skew {
		t.Fatalf("skew %s forgotten, was %s", s.Skew(), skew)
	}
	g.o -= 37
	run(s, g, 300, 304, 2, nil)
	if !s.Locked() || s.Relocks() != 0 {
		t.Fatalf("not relocked quickly: %s relocks %d", s.Status(), s.Relocks())
	}
	wantOffset(t, s, 63, 0.6)
}

func TestPlayClockStartHoldsUntilSourcesAreMarginAhead(t *testing.T) {
	var c playClock
	now := t0.Add(time.Hour)
	pos := t0.Add(100 * time.Second)
	c.Start(now, pos)
	// The sources are only 3 s past pos: the clock stands still, never steps back.
	for i := 0; i < 4; i++ {
		now = now.Add(time.Second)
		if got := c.Advance(now, pos.Add(3*time.Second)); !got.Equal(pos) {
			t.Fatalf("tick %d moved to %s", i, got.Sub(pos))
		}
	}
	// Margin ahead: it plays on from where it stood.
	now = now.Add(time.Second)
	if got := c.Advance(now, pos.Add(syncMargin+time.Second)); got.Before(pos) || got.After(pos.Add(2*time.Second)) {
		t.Fatalf("release at %s", got.Sub(pos))
	}
}

// flipErrors runs the game through s and returns, for every kill flip, the offset error s
// held just before the flip showed on the video: what a viewer sees at that kill.
func flipErrors(s *mmSync, g *simGame, fps, until float64) []float64 {
	var errs []float64
	next := 0
	for i := 0; float64(i) < until*fps; i++ {
		t := float64(i) / fps
		for next < len(g.kills) && g.kills[next].T.Sub(g.spawn).Seconds()-g.o <= t {
			if o, _, ok := s.Offset(); ok {
				errs = append(errs, o-g.o)
			}
			next++
		}
		s.ObserveHUD(i, g.hud(t))
	}
	return errs
}

func TestSyncSkewFarFromPriorIsLearnedFromTheFirstFlips(t *testing.T) {
	// A game's skew strays from its layout's average: the first flip has only the prior to
	// go by. A prior a second off or more is dropped once two flips agree against it (the
	// third flip is placed by them); one that is merely off gives way as the flips pile up.
	for _, tc := range []struct {
		off   float64
		after int // flips from which the error stays within half a second
	}{{-0.6, 2}, {0.6, 2}, {-0.8, 3}, {2.5, 2}} {
		g := &simGame{spawn: t0, skew: syncLayoutSkew["lpl"] + tc.off, o: -47.7}
		killsEvery(g, 230, 53, 1200)
		s := g.sync(5)
		s.SetLayout("lpl")
		s.ObserveFeedKills(g.feedKills())
		errs := flipErrors(s, g, 5, 1200)
		if len(errs) < 10 {
			t.Fatalf("only %d flips measured", len(errs))
		}
		if math.Abs(errs[0]+tc.off) > 0.3 {
			t.Fatalf("skew off %+.1f: first flip error %+.2f, want the prior's %+.2f", tc.off, errs[0], -tc.off)
		}
		for n, e := range errs[tc.after:] {
			if math.Abs(e) > 0.5 {
				t.Fatalf("skew off %+.1f: flip %d error %+.2f s (all %v)", tc.off, n+tc.after+1, e, errs)
			}
		}
	}
}

func TestSyncSkewIgnoresAMismatchedKill(t *testing.T) {
	// One feed kill sits 3 s off (a late frame): it must not drag the flips after it.
	g := &simGame{spawn: t0, skew: 3.5, o: 100}
	killsEvery(g, 150, 37, 900)
	feed := g.feedKills()
	feed[3].T = feed[3].T.Add(3 * time.Second)
	s := g.sync(5)
	s.ObserveFeedKills(feed)
	errs := flipErrors(s, g, 5, 900)
	for n, e := range errs {
		if n != 3 && math.Abs(e) > 0.6 {
			t.Fatalf("flip %d error %+.2f s (all %v)", n+1, e, errs)
		}
	}
}

func TestSyncPriorOutweighedByConsistentFlips(t *testing.T) {
	s := newMMSync(t0, 2, nil)
	s.SetLayout("lck")
	prior := s.Skew()
	off := func(v float64, w float64) skewSample { return skewSample{prior.Seconds() + v, w} }
	s.skews = []skewSample{off(0.6, 0.2)}
	if d := s.Skew() - prior; d < 100*time.Millisecond || d > 400*time.Millisecond {
		t.Fatalf("one sharp flip 0.6 s off moved the skew by %s, want a part of it", d)
	}
	s.skews = []skewSample{off(0.6, 0.2), off(0.6, 0.2), off(0.6, 0.2), off(0.6, 0.2), off(0.6, 0.2)}
	if d := s.Skew() - prior; d < 400*time.Millisecond {
		t.Fatalf("five consistent flips 0.6 s off moved the skew by only %s", d)
	}
	s.skews = []skewSample{off(3, 0.2)}
	if d := s.Skew() - prior; d != 0 {
		t.Fatalf("one flip 3 s off must not beat the prior alone, moved %s", d)
	}
	s.skews = []skewSample{off(3, 0.2), off(3.1, 0.2)}
	if d := s.Skew() - prior; d < 2500*time.Millisecond {
		t.Fatalf("two flips 3 s off must beat the prior, moved %s", d)
	}
	s.skews = []skewSample{off(3, 0.2), off(0.1, 0.2), off(-0.1, 0.2)}
	if d := s.Skew() - prior; d < -300*time.Millisecond || d > 300*time.Millisecond {
		t.Fatalf("one stray flip among agreeing ones moved the skew by %s", d)
	}
	s.skews = []skewSample{off(1.5, 0.2)}
	sharp := s.Skew() - prior
	s.skews = []skewSample{off(1.5, 3)}
	if wide := s.Skew() - prior; wide >= sharp {
		t.Fatalf("a flip seen across a 3 s bracket moved the skew by %s, a sharp one by %s", wide, sharp)
	}
}

func TestSyncSurvivesBroadcastClockStallAndCatchUp(t *testing.T) {
	// Backtest KC-MVK g2: the broadcast clock stood still 6 s behind for several seconds
	// (the feed reports no pause), then ran fast until it had caught up. That is not a step
	// of the video timeline: the offset must not move to the stalled readings.
	g := &simGame{spawn: t0, skew: 3.1, o: -149}
	killsEvery(g, 30, 41, 2000)
	s := g.sync(2)
	s.ObserveFeedKills(g.feedKills())
	s.ObserveFeedLatest(g.at(100000))
	stall := map[float64]int{1696: 1536, 1696.5: 1540, 1697: 1543, 1697.5: 1544, 1698: 1545, 1698.5: 1545}
	run(s, g, 1500, 1686, 2, nil)
	run(s, g, 1686, 1710, 2, func(tt float64, h *mmHUD) {
		if v, ok := stall[tt]; ok {
			h.ClockSec = v
		} else if tt < 1696 {
			h.ClockSec = 1533
		}
	})
	wantOffset(t, s, -149, 0.6)
	if s.Relocks() != 0 {
		t.Fatalf("a stalled broadcast clock caused %d relocks", s.Relocks())
	}
}

func goldFrame(at float64, blue, red int) stamped[frame] {
	return stamped[frame]{t0.Add(sec(at)), frame{BlueTeam: teamStats{TotalGold: blue}, RedTeam: teamStats{TotalGold: red}}}
}

func TestGameStartIsTheFirstGoldAfterEmptyFrames(t *testing.T) {
	fr := []stamped[frame]{goldFrame(0, 0, 0), goldFrame(1.5, 0, 0), goldFrame(4.9, 2500, 0), goldFrame(5.0, 2500, 2500), goldFrame(6, 2500, 2500)}
	if got, ok := gameStart(fr); !ok || !got.Equal(t0.Add(sec(4.9))) {
		t.Fatalf("start %v ok=%v, want 4.9 s (a team's gold shows before the other's)", got.Sub(t0), ok)
	}
	if _, ok := gameStart(fr[3:]); ok {
		t.Fatal("a buffer that begins in the game has not seen its start")
	}
	glitch := []stamped[frame]{goldFrame(0, 9000, 9500), goldFrame(1, 0, 0), goldFrame(2, 9100, 9600)}
	if _, ok := gameStart(glitch); ok {
		t.Fatal("an empty frame in the middle of a game is not a start")
	}
}

func TestSyncGameStartPlacesTheSkewBeforeAnyFlip(t *testing.T) {
	// This game's skew is a second above its league's average. Its first gold shows
	// syncLayoutGap after the HUD clock's zero, so the start fixes the skew without a flip.
	lck := syncLayoutSkew["lck"] + 1
	g := &simGame{spawn: t0, skew: lck, o: 100.1}
	killsEvery(g, 150, 53, 1000)
	for _, withStart := range []bool{false, true} {
		s := g.sync(5)
		s.SetLayout("lck")
		if withStart {
			s.ObserveGameStart(t0.Add(sec(lck + syncLayoutGap["lck"])))
		}
		s.ObserveFeedKills(g.feedKills())
		errs := flipErrors(s, g, 5, 1000)
		if len(errs) < 10 {
			t.Fatalf("only %d flips measured", len(errs))
		}
		if withStart && math.Abs(errs[0]) > 0.3 {
			t.Fatalf("the first flip is %+.2f s off although the game start is known", errs[0])
		}
		if !withStart && math.Abs(errs[0]) < 0.8 {
			t.Fatalf("the first flip is only %+.2f s off without the game start: the test does not show the prior's miss", errs[0])
		}
	}
}

func TestSyncGameStartOfALayoutWithoutGapIsIgnored(t *testing.T) {
	s := newMMSync(t0, 5, nil)
	s.SetLayout("lpl")
	prior := s.Skew()
	s.ObserveGameStart(t0.Add(sec(4.8)))
	if s.Skew() != prior {
		t.Fatalf("skew %s moved from the prior %s by a game start", s.Skew(), prior)
	}
}

func TestSyncGameStartYieldsToFlipsThatDisagree(t *testing.T) {
	// A start that does not match the game (another game's, a feed that lagged) is only a
	// prior: the flips decide once they agree among themselves.
	g := &simGame{spawn: t0, skew: 3.0, o: 100}
	killsEvery(g, 100, 31, 900)
	s := g.sync(5)
	s.SetLayout("lck")
	s.ObserveGameStart(t0.Add(sec(3.0 + syncLayoutGap["lck"] + 2.5)))
	s.ObserveFeedKills(g.feedKills())
	errs := flipErrors(s, g, 5, 900)
	for n, e := range errs[5:] {
		if math.Abs(e) > 0.5 {
			t.Fatalf("flip %d error %+.2f s with a wrong game start (all %v)", n+6, e, errs)
		}
	}
}

func TestFeedRemembersTheGameStartAfterTheFramesAreTrimmed(t *testing.T) {
	f := newFeed("", time.Second)
	at := func(s float64, gold int) frame {
		return mkFrame(t0.Add(sec(s)), gold)
	}
	// A feed that joined mid-game has not seen the start...
	f.absorbLocked(windowResp{Frames: []frame{at(100, 9000), at(101, 9010)}}, detailsResp{})
	if _, ok := f.GameStart(); ok {
		t.Fatal("a buffer that begins with gold has no game start")
	}
	// ...one that saw the loading screen turn into the game has, and keeps it for good.
	f = newFeed("", time.Second)
	f.absorbLocked(windowResp{Frames: []frame{at(0, 0), at(1, 0), at(4.9, 2500), at(6, 2500)}}, detailsResp{})
	f.absorbLocked(windowResp{Frames: []frame{at(2000, 30000), at(2001, 30100)}}, detailsResp{})
	if got, ok := f.GameStart(); !ok || !got.Equal(t0.Add(sec(4.9))) {
		t.Fatalf("game start %v ok=%v, want 4.9 s", got.Sub(t0), ok)
	}
}
