package main

import (
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

func mkFrame(ts time.Time, blueGold int) frame {
	return frame{RFC460Timestamp: ts.Format("2006-01-02T15:04:05.000Z"), BlueTeam: teamStats{TotalGold: blueGold}}
}

// fakeFeed serves frames spaced step (default 0.5 s) from every requested startingTime for
// 10 s, but only those older than clock-lagMin, mimicking the real feed's publication lag.
type fakeFeed struct {
	mu    sync.Mutex
	clock time.Time
	// shape, when set, edits each served frame; false drops the frame.
	shape  func(fr *frame, ts time.Time) bool
	lagMin time.Duration // frames newer than clock-lagMin are not published yet
	step   time.Duration
	starts []string
	gid    string
}

// frameTimes lists the frame timestamps of the window starting at start.
func (ff *fakeFeed) frameTimes(start string) []time.Time {
	st, _ := time.Parse("2006-01-02T15:04:05.000Z", start)
	step := ff.step
	if step == 0 {
		step = 500 * time.Millisecond
	}
	var out []time.Time
	for ts := st; ts.Before(st.Add(feedProbeStep)); ts = ts.Add(step) {
		out = append(out, ts)
	}
	return out
}

func (ff *fakeFeed) now() time.Time {
	ff.mu.Lock()
	defer ff.mu.Unlock()
	return ff.clock
}

func (ff *fakeFeed) advance(d time.Duration) {
	ff.mu.Lock()
	ff.clock = ff.clock.Add(d)
	ff.mu.Unlock()
}

func (ff *fakeFeed) newFeed(poll time.Duration) *feed {
	f := newFeed("", poll)
	f.now = ff.now
	f.liveEvent = func(string, string) (event, string, bool) {
		ff.mu.Lock()
		defer ff.mu.Unlock()
		return event{ID: "e"}, ff.gid, true
	}
	f.spawnAt = func(string) time.Time { return t0 }
	f.window = func(gid, start string) (windowResp, error) {
		ff.mu.Lock()
		defer ff.mu.Unlock()
		ff.starts = append(ff.starts, start)
		var w windowResp
		w.GameMetadata.PatchVersion = "16.1"
		for _, ts := range ff.frameTimes(start) {
			if ts.After(ff.clock.Add(-ff.lagMin)) {
				break
			}
			fr := mkFrame(ts, int(ts.Sub(t0)/time.Millisecond))
			if ff.shape != nil && !ff.shape(&fr, ts) {
				continue
			}
			w.Frames = append(w.Frames, fr)
		}
		return w, nil
	}
	f.details = func(gid, start string) (detailsResp, error) {
		ff.mu.Lock()
		defer ff.mu.Unlock()
		var d detailsResp
		for _, ts := range ff.frameTimes(start) {
			d.Frames = append(d.Frames, detailsFrame{RFC460Timestamp: ts.Format("2006-01-02T15:04:05.000Z")})
		}
		return d, nil
	}
	return f
}

func TestMergeStampedSortsDedupesAndTrims(t *testing.T) {
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	buf := []stamped[int]{{at(10), 1}, {at(20), 2}}
	buf = mergeStamped(buf, []stamped[int]{{at(15), 3}, {at(20), 9}, {at(5), 4}}, time.Hour)
	want := []int{4, 1, 3, 9}
	if len(buf) != len(want) {
		t.Fatalf("got %v", buf)
	}
	for i, v := range want {
		if buf[i].v != v {
			t.Fatalf("pos %d = %d, want %d", i, buf[i].v, v)
		}
	}
	buf = mergeStamped(buf, []stamped[int]{{at(400), 7}}, 5*time.Minute)
	if len(buf) != 1 || buf[0].v != 7 {
		t.Fatalf("trim: %v", buf)
	}
}

func TestStartingTimeAligned(t *testing.T) {
	got := startingTime(t0.Add(37*time.Second + 400*time.Millisecond))
	if got != "2026-10-17T12:00:30.000Z" {
		t.Fatal(got)
	}
}

func TestProbeStepsBackUntilFramesThenFollowsNewest(t *testing.T) {
	ff := &fakeFeed{clock: t0.Add(20 * time.Minute), lagMin: 30 * time.Second, gid: "g"}
	f := ff.newFeed(2 * time.Second)
	f.FetchOnce()
	// now-10s = 12:19:50 (empty: frames must be older than 12:19:30), then 12:19:40, then 12:19:30...
	want := []string{"2026-10-17T12:19:50.000Z", "2026-10-17T12:19:40.000Z", "2026-10-17T12:19:30.000Z"}
	for i, w := range want {
		if ff.starts[i] != w {
			t.Fatalf("probe %d = %s, want %s (all %v)", i, ff.starts[i], w, ff.starts)
		}
	}
	// Frames exist up to 12:19:30.000: the follow-up polls that window only, and the next
	// one joins it once the newest frame is within a poll of the window's end.
	ff.starts = nil
	ff.advance(2 * time.Second)
	f.FetchOnce()
	if len(ff.starts) != 1 || ff.starts[0] != "2026-10-17T12:19:30.000Z" {
		t.Fatalf("early follow-up starts %v", ff.starts)
	}
	ff.advance(6 * time.Second)
	f.FetchOnce()
	ff.starts = nil
	ff.advance(time.Second)
	f.FetchOnce()
	got := map[string]bool{}
	for _, s := range ff.starts {
		got[s] = true
	}
	if len(ff.starts) != 2 || !got["2026-10-17T12:19:30.000Z"] || !got["2026-10-17T12:19:40.000Z"] {
		t.Fatalf("late follow-up starts %v", ff.starts)
	}
}

// Real windows end anywhere in their last second (frame spacing is irregular), so the
// follower must cross into the next window instead of re-requesting the current one.
func TestFollowCrossesWindowWhenLastFrameEndsEarly(t *testing.T) {
	ff := &fakeFeed{clock: t0.Add(20 * time.Minute), lagMin: 3 * time.Second, step: 700 * time.Millisecond, gid: "g"}
	f := ff.newFeed(2 * time.Second)
	f.FetchOnce()
	first := parseTime(f.Snapshot(ff.now()).W.Frames[0].RFC460Timestamp)
	for i := 0; i < 80; i++ { // 40 s of render ticks, polling every 2 s
		ff.advance(500 * time.Millisecond)
		if i%4 == 3 {
			f.FetchOnce()
		}
		f.Snapshot(ff.now())
	}
	last := parseTime(f.Snapshot(ff.now()).W.Frames[0].RFC460Timestamp)
	if last.Sub(first) < 30*time.Second {
		t.Fatalf("playout advanced only %s over 40 s", last.Sub(first))
	}
}

func TestFallingFarBehindReprobesLiveEdge(t *testing.T) {
	ff := &fakeFeed{clock: t0.Add(20 * time.Minute), lagMin: 3 * time.Second, gid: "g"}
	f := ff.newFeed(2 * time.Second)
	f.FetchOnce()
	ff.advance(30 * time.Minute) // laptop sleep
	f.FetchOnce()
	if s := f.Snapshot(ff.now()); s.Lag > time.Minute {
		t.Fatalf("still %s behind after wake", s.Lag)
	}
}

func TestPlayoutAdvancesHalfSecondWithAdaptiveDelay(t *testing.T) {
	ff := &fakeFeed{clock: t0.Add(20 * time.Minute), lagMin: 3 * time.Second, gid: "g"}
	f := ff.newFeed(2 * time.Second)
	f.FetchOnce()
	var last time.Time
	stalls := 0
	for i := 0; i < 40; i++ { // 20 s of render ticks, polling every 2 s
		ff.advance(500 * time.Millisecond)
		if i%4 == 3 {
			f.FetchOnce()
		}
		s := f.Snapshot(ff.now())
		if len(s.W.Frames) != 1 || len(s.Det.Frames) != 1 {
			t.Fatalf("tick %d: want exactly one frame, got %d/%d", i, len(s.W.Frames), len(s.Det.Frames))
		}
		ts := parseTime(s.W.Frames[0].RFC460Timestamp)
		if ts.Before(last) {
			t.Fatalf("tick %d: playout rewound %s -> %s", i, last, ts)
		}
		if i >= 8 && !ts.After(last) {
			stalls++
		}
		if d := parseTime(s.Det.Frames[0].RFC460Timestamp); d.After(ts) {
			t.Fatalf("tick %d: details %s after window frame %s", i, d, ts)
		}
		if !last.IsZero() && ts.Sub(last) > 500*time.Millisecond {
			t.Fatalf("tick %d: jumped %s", i, ts.Sub(last))
		}
		if s.W.GameMetadata.PatchVersion != "16.1" {
			t.Fatal("metadata missing")
		}
		last = ts
	}
	if stalls > 4 {
		t.Fatalf("playout stalled on %d of 32 ticks", stalls)
	}
	if s := f.Snapshot(ff.now()); s.Lag < 3*time.Second {
		t.Fatalf("lag %s smaller than feed publication lag", s.Lag)
	}
}

func TestGameChangeResetsBuffers(t *testing.T) {
	ff := &fakeFeed{clock: t0.Add(20 * time.Minute), lagMin: 0, gid: "g1"}
	f := ff.newFeed(2 * time.Second)
	f.FetchOnce()
	if s := f.Snapshot(ff.now()); len(s.W.Frames) == 0 || len(s.Series) == 0 {
		t.Fatal("expected frames and series for g1")
	}
	ff.mu.Lock()
	ff.gid = "g2"
	ff.mu.Unlock()
	ff.advance(feedDiscoverEvery)
	ff.mu.Lock()
	ff.lagMin = time.Hour // g2 has published nothing yet
	ff.mu.Unlock()
	f.FetchOnce()
	s := f.Snapshot(ff.now())
	if s.GID != "g2" || len(s.W.Frames) != 0 || len(s.Series) != 0 {
		t.Fatalf("game change did not reset: gid=%s frames=%d series=%d", s.GID, len(s.W.Frames), len(s.Series))
	}
	if !s.Spawn.Equal(t0) {
		t.Fatalf("spawn %v", s.Spawn)
	}
}

func TestDiscoveryIsWallClockPaced(t *testing.T) {
	ff := &fakeFeed{clock: t0, gid: "g"}
	calls := 0
	f := ff.newFeed(2 * time.Second)
	inner := f.liveEvent
	f.liveEvent = func(l, m string) (event, string, bool) { calls++; return inner(l, m) }
	for i := 0; i < 20; i++ { // 40 s
		f.FetchOnce()
		ff.advance(2 * time.Second)
	}
	if calls != 2 {
		t.Fatalf("discovery calls = %d, want 2 over 40 s", calls)
	}
}

func TestSnapshotAtPlaysFrameOfFeedTime(t *testing.T) {
	ff := &fakeFeed{clock: t0.Add(20 * time.Minute), lagMin: 3 * time.Second, gid: "g"}
	f := ff.newFeed(2 * time.Second)
	f.FetchOnce()
	latest, ok := f.Latest()
	if !ok {
		t.Fatal("no frames")
	}
	// The newest frame is published lagMin behind the clock.
	if want := t0.Add(20*time.Minute - 3*time.Second); latest.Before(want.Add(-time.Second)) || latest.After(want) {
		t.Fatalf("latest %s, want about %s", latest, want)
	}
	want := latest.Add(-3 * time.Second)
	s := f.SnapshotAt(ff.now(), want)
	if len(s.W.Frames) != 1 {
		t.Fatalf("frames %d", len(s.W.Frames))
	}
	ts := parseTime(s.W.Frames[0].RFC460Timestamp)
	if d := want.Sub(ts); d < 0 || d > 500*time.Millisecond {
		t.Fatalf("played frame %s for requested %s", ts, want)
	}
	// Without a learned skew the header clock uses the prior.
	if got := ts.Sub(t0) - feedPriorSkew; got != s.Game {
		t.Fatalf("Game %s does not match frame time %s", s.Game, got)
	}
	f.SetSkew(2 * time.Second)
	if s2 := f.SnapshotAt(ff.now(), want); s2.Game != ts.Sub(t0)-2*time.Second {
		t.Fatalf("learned skew not applied: %s", s2.Game)
	}
	if s2 := f.SnapshotAt(ff.now(), latest); s2.Game-s.Game < 2*time.Second {
		t.Fatalf("game time did not follow: %s -> %s", s.Game, s2.Game)
	}
}

func TestSnapshotCarriesLivesFromTheBufferedFrames(t *testing.T) {
	ff := &fakeFeed{clock: t0.Add(110 * time.Second), gid: "g", shape: func(fr *frame, ts time.Time) bool {
		p := participant{ParticipantID: 1, Level: 6, CurrentHealth: 1500, MaxHealth: 2000}
		if ts.Sub(t0) >= 100*time.Second { // dead from the first buffered frame on
			p.Deaths, p.CurrentHealth = 1, 0
		}
		fr.BlueTeam.Participants = []participant{p}
		return true
	}}
	f := ff.newFeed(2 * time.Second)
	f.FetchOnce()
	// Level 6 at game minute 1.6: 16 s of death timer from half a second after the first dead frame, 11.5 s of it left at 105 s.
	l := f.SnapshotAt(ff.now(), t0.Add(105*time.Second)).Lives[1]
	if !l.Dead || l.Exact || math.Abs(l.Secs-11.5) > 0.01 {
		t.Fatalf("lives at 105 s: %+v, want dead with an 11.5 s estimate", l)
	}
	if l := f.Snapshot(ff.now()).Lives[1]; !l.Dead {
		t.Fatalf("the playout snapshot lost the death: %+v", l)
	}
	f.mu.Lock()
	f.spawn = time.Time{} // the game's start is not known: only differences count
	f.mu.Unlock()
	if l := f.SnapshotAt(ff.now(), t0.Add(105*time.Second)).Lives[1]; math.Abs(l.Secs-11.5) > 0.01 {
		t.Fatalf("without a spawn time: %+v, want the same 11.5 s", l)
	}
}

func TestSnapshotGameBeforeClockStartsIsZero(t *testing.T) {
	ff := &fakeFeed{clock: t0.Add(10 * time.Second), lagMin: 0, gid: "g"}
	f := ff.newFeed(2 * time.Second)
	f.FetchOnce()
	if s := f.SnapshotAt(ff.now(), t0); s.Game < 0 {
		t.Fatalf("a frame is playing, game %s", s.Game)
	}
	if s := f.Meta(); len(s.W.Frames) != 0 || s.Game != -1 || s.GID != "g" {
		t.Fatalf("meta must carry the game but no frame: %+v", s)
	}
}

// pausedFeed serves a game that pauses for dur at t0+at: one paused frame, then nothing
// until play resumes. Blue's kill count rises by one every 30 s from t0+10 s.
func pausedFeed(clock time.Time, at, dur time.Duration) *fakeFeed {
	ff := &fakeFeed{clock: clock, gid: "g"}
	ff.shape = func(fr *frame, ts time.Time) bool {
		d := ts.Sub(t0)
		if d > at && d < at+dur {
			return false
		}
		if d == at {
			fr.GameState = "paused"
		}
		if d >= 10*time.Second {
			fr.BlueTeam.TotalKills = 1 + int((d-10*time.Second)/(30*time.Second))
		}
		return true
	}
	return ff
}

func TestPauseFreezesGameClockAndShiftsLater(t *testing.T) {
	at, dur := 60*time.Second, 202*time.Second
	ff := pausedFeed(t0.Add(70*time.Second), at, dur)
	f := ff.newFeed(2 * time.Second)
	f.FetchOnce()
	for ff.now().Before(t0.Add(300 * time.Second)) {
		ff.advance(2 * time.Second)
		f.FetchOnce()
	}
	ps := f.Pauses()
	if len(ps) != 1 || !ps[0].Start.Equal(t0.Add(at)) || ps[0].Dur < dur-time.Second || ps[0].Dur > dur+time.Second {
		t.Fatalf("pauses %+v", ps)
	}
	game := func(off time.Duration) time.Duration { return f.SnapshotAt(ff.now(), t0.Add(off)).Game }
	before := game(at)
	if before != at-feedPriorSkew {
		t.Fatalf("game at pause start %s", before)
	}
	if g := game(at + dur - time.Second); g != before {
		t.Fatalf("game clock moved during the pause: %s -> %s", before, g)
	}
	if g, want := game(at+dur+30*time.Second), before+30*time.Second; g < want-time.Second || g > want+time.Second {
		t.Fatalf("game after the pause %s, want about %s", g, want)
	}
}

func TestKillsListsSingleIncreases(t *testing.T) {
	ff := pausedFeed(t0.Add(35*time.Second), time.Hour, 0)
	f := ff.newFeed(2 * time.Second)
	f.FetchOnce()
	for ff.now().Before(t0.Add(150 * time.Second)) {
		ff.advance(2 * time.Second)
		f.FetchOnce()
	}
	ks := f.Kills()
	if len(ks) < 3 {
		t.Fatalf("kills %+v", ks)
	}
	for i, k := range ks {
		if k.Team != 0 || k.Count != i+2 || !k.T.Equal(t0.Add(time.Duration(10+30*(i+1))*time.Second)) {
			t.Fatalf("kill %d: %+v", i, k)
		}
	}
}

func TestNeedBackfillsOlderWindowsAndNeverShowsALaterFrame(t *testing.T) {
	ff := &fakeFeed{clock: t0.Add(100 * time.Second), gid: "g"}
	f := ff.newFeed(2 * time.Second)
	f.FetchOnce()
	want := t0.Add(52 * time.Second)
	if s := f.SnapshotAt(ff.now(), want); len(s.W.Frames) != 0 || s.Game != -1 || s.GID != "g" {
		t.Fatalf("before the buffer a later frame was shown: %d frames", len(s.W.Frames))
	}
	f.Need(want)
	deadline := time.Now().Add(5 * time.Second)
	for {
		s := f.SnapshotAt(ff.now(), want)
		if len(s.W.Frames) == 1 {
			if ts := parseTime(s.W.Frames[0].RFC460Timestamp); ts.After(want) || want.Sub(ts) > time.Second {
				t.Fatalf("frame %s for %s", ts.Sub(t0), want.Sub(t0))
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("backfill did not deliver")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestKillsSinceIsIncremental(t *testing.T) {
	ff := pausedFeed(t0.Add(35*time.Second), time.Hour, 0)
	f := ff.newFeed(2 * time.Second)
	f.FetchOnce()
	n := 0
	var got []feedKill
	for ff.now().Before(t0.Add(150 * time.Second)) {
		ff.advance(2 * time.Second)
		f.FetchOnce()
		var ks []feedKill
		ks, n = f.KillsSince(n)
		got = append(got, ks...)
	}
	if all := f.Kills(); len(all) != len(got) || len(got) < 3 {
		t.Fatalf("incremental %d kills, total %d", len(got), len(all))
	}
	if ks, m := f.KillsSince(n); len(ks) != 0 || m != n {
		t.Fatalf("repeat returned %d new kills", len(ks))
	}
}

func TestFinishedGameKeepsItsLastFrame(t *testing.T) {
	end := t0.Add(20 * time.Minute)
	ff := &fakeFeed{clock: end.Add(10 * time.Second), lagMin: 3 * time.Second, gid: "g",
		shape: func(fr *frame, ts time.Time) bool {
			if ts.After(end) {
				return false // the game is over: nothing newer is ever published
			}
			if !ts.Before(end.Add(-time.Second)) {
				fr.GameState = "finished"
			}
			return true
		}}
	f := ff.newFeed(2 * time.Second)
	f.FetchOnce()
	last, ok := f.Latest()
	if !ok {
		t.Fatal("no frames before the game ended")
	}
	for i := 0; i < 5; i++ {
		ff.advance(feedResync + time.Minute) // far beyond the resync horizon
		f.FetchOnce()
		if got, ok := f.Latest(); !ok || !got.Equal(last) {
			t.Fatalf("round %d: latest %v ok=%v, want the final frame %v", i, got, ok, last)
		}
	}
}

// waitBackfill blocks until no background backfill is running.
func waitBackfill(f *feed) {
	for {
		f.mu.Lock()
		busy := f.fetching
		f.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func requestsOver(t *testing.T, need func(f *feed) time.Time, shape func(*frame, time.Time) bool) int {
	t.Helper()
	ff := &fakeFeed{clock: t0.Add(20 * time.Minute), lagMin: 3 * time.Second, gid: "g", shape: shape}
	f := ff.newFeed(2 * time.Second)
	f.FetchOnce()
	ff.starts = nil
	for i := 0; i < 240; i++ { // 2 min of render ticks
		ff.advance(500 * time.Millisecond)
		if i%4 == 3 {
			f.FetchOnce()
		}
		f.Need(need(f))
		waitBackfill(f)
	}
	return len(ff.starts)
}

func TestNeedDoesNotStormWhenNothingOlderExists(t *testing.T) {
	// The game's first frame is the one the buffer starts with, so no window holds older data.
	g0 := t0.Add(20*time.Minute - 15*time.Second)
	live := requestsOver(t, func(f *feed) time.Time { return t0 }, nil)
	n := requestsOver(t, func(f *feed) time.Time {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.frames[0].ts.Add(-5 * time.Second)
	}, func(fr *frame, ts time.Time) bool { return !ts.Before(g0) })
	if n > live+8 { // the live-edge polls plus a handful of backed-off backfills
		t.Fatalf("%d window requests in 2 min, %d without backfill", n, live)
	}
}

func TestNeedSkipsTimeOlderThanTheBufferKeeps(t *testing.T) {
	live := requestsOver(t, func(f *feed) time.Time { return t0 }, nil)
	n := requestsOver(t, func(f *feed) time.Time {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.frames[len(f.frames)-1].ts.Add(-feedKeep - time.Minute)
	}, nil)
	if n != live {
		t.Fatalf("%d window requests in 2 min, want the %d live-edge polls", n, live)
	}
}

func TestSpawnLookupRetriesUntilItSucceeds(t *testing.T) {
	ff := &fakeFeed{clock: t0.Add(20 * time.Minute), gid: "g"}
	f := ff.newFeed(2 * time.Second)
	calls := 0
	f.spawnAt = func(string) time.Time {
		calls++
		if calls == 1 {
			return time.Time{}
		}
		return t0
	}
	f.FetchOnce()
	if !f.spawn.IsZero() {
		t.Fatal("first lookup should have failed")
	}
	ff.advance(feedDiscoverEvery)
	f.FetchOnce()
	if f.spawn.IsZero() || calls != 2 {
		t.Fatalf("spawn %v after %d lookups", f.spawn, calls)
	}
	ff.advance(feedDiscoverEvery)
	f.FetchOnce()
	if calls != 2 {
		t.Fatalf("spawn looked up again after success: %d", calls)
	}
}

func TestDetailsFetchedOncePerWindowUnlessViewNeedsThem(t *testing.T) {
	count := func(view bool) int {
		ff := &fakeFeed{clock: t0.Add(20 * time.Minute), lagMin: 3 * time.Second, gid: "g"}
		f := ff.newFeed(2 * time.Second)
		f.SetDetailsView(view)
		var n atomic.Int32
		inner := f.details
		f.details = func(g, s string) (detailsResp, error) { n.Add(1); return inner(g, s) }
		f.FetchOnce()
		for i := 0; i < 60; i++ { // 2 min of polls
			ff.advance(2 * time.Second)
			f.FetchOnce()
		}
		return int(n.Load())
	}
	if off, on := count(false), count(true); off > 14 || on < 60 {
		t.Fatalf("details requests: view off %d (want <= 14), view on %d (want >= 60)", off, on)
	}
}

func TestAbsorbFindsKillsAndPausesInTheNewTail(t *testing.T) {
	f := newFeed("", time.Second)
	mk := func(sec float64, kills int, state string) frame {
		fr := mkFrame(t0.Add(time.Duration(sec*float64(time.Second))), 0)
		fr.BlueTeam.TotalKills, fr.GameState = kills, state
		return fr
	}
	f.absorbLocked(windowResp{Frames: []frame{mk(0, 0, "in_game"), mk(1, 0, "in_game")}}, detailsResp{})
	f.absorbLocked(windowResp{Frames: []frame{mk(1, 0, "in_game"), mk(2, 1, "in_game"), mk(3, 1, "paused")}}, detailsResp{})
	f.absorbLocked(windowResp{Frames: []frame{mk(8, 1, "in_game")}}, detailsResp{})
	if len(f.kills) != 1 || f.kills[0].Count != 1 {
		t.Fatalf("kills %v", f.kills)
	}
	if len(f.pauses) != 1 || f.pauses[0].Dur != 5*time.Second {
		t.Fatalf("pauses %v", f.pauses)
	}
	if len(f.frames) != 5 {
		t.Fatalf("frames %d", len(f.frames))
	}
}
