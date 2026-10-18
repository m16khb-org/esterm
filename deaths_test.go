package main

import (
	"math"
	"testing"
	"time"
)

// lifeState is what one frame says about one participant.
type lifeState struct{ level, deaths, hp int }

// lifeFrames builds frames every step seconds from 0 to end (inclusive) for participants
// 1-5 (blue) and 6-10 (red); state tells each participant's level, deaths and health.
// Frames for which skip is true are left out, like a window that was not fetched.
func lifeFrames(step, end float64, state func(sec float64, pid int) lifeState, skip func(sec float64) bool) []stamped[frame] {
	var out []stamped[frame]
	for k := 0; float64(k)*step <= end; k++ {
		sec := float64(k) * step
		if skip != nil && skip(sec) {
			continue
		}
		f := frame{GameState: "in_game"}
		for pid := 1; pid <= 10; pid++ {
			s := state(sec, pid)
			p := participant{ParticipantID: pid, Level: s.level, Deaths: s.deaths, CurrentHealth: s.hp, MaxHealth: 2000}
			if pid <= 5 {
				f.BlueTeam.Participants = append(f.BlueTeam.Participants, p)
			} else {
				f.RedTeam.Participants = append(f.RedTeam.Participants, p)
			}
		}
		out = append(out, stamped[frame]{at(sec), f})
	}
	return out
}

// at is the feed time sec seconds after t0.
func at(sec float64) time.Time { return t0.Add(time.Duration(sec * float64(time.Second))) }

// feedClock is a game clock that equals the feed time since t0 (no pauses).
func feedClock(ts time.Time) time.Duration { return ts.Sub(t0) }

// deadBetween makes participant 3 (level 6) die at tick and be back at respawn: the deaths
// counter ticks first, the health falls to 0 half a second later, and it is full again at
// the respawn. Participant 5 has had one death since the first frame and shows no health
// from 30 s to 34 s (a revive blip: the counter does not tick). Everyone else never died.
func deadBetween(tick, respawn float64) func(float64, int) lifeState {
	return func(sec float64, pid int) lifeState {
		switch {
		case pid == 3 && sec < tick:
			return lifeState{6, 0, 1500}
		case pid == 3 && sec < tick+0.5:
			return lifeState{6, 1, 1500}
		case pid == 3 && sec < respawn:
			return lifeState{6, 1, 0}
		case pid == 3:
			return lifeState{6, 1, 1500}
		case pid == 5 && sec >= 30 && sec < 34:
			return lifeState{6, 1, 0}
		case pid == 5:
			return lifeState{6, 1, 1500}
		}
		return lifeState{6, 0, 1500}
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.01 }

func TestDeathTimerFollowsLevelAndGameMinute(t *testing.T) {
	sec := func(d time.Duration) float64 { return d.Seconds() }
	for _, c := range []struct {
		name    string
		level   int
		minutes float64
		want    float64
	}{
		{"level 1 early", 1, 3, 10},
		{"level 6 early", 6, 5, 16},
		{"level 0 counts as level 1", 0, 5, 10},
		{"level 18", 18, 5, 52.5},
		{"levels beyond the table use the last one", 25, 5, 52.5},
		{"just before the factor starts", 10, 15, 32.5},
		{"15-30 min: +0.85 %/min", 10, 20, 32.5 * (1 + 0.0425)},
		{"30 min", 10, 30, 32.5 * (1 + 0.1275)},
		{"30-45 min: +0.6 %/min", 10, 40, 32.5 * (1 + 0.1275 + 0.06)},
		{"45 min", 10, 45, 32.5 * (1 + 0.2175)},
		{"beyond 45 min: +2.9 %/min", 10, 50, 32.5 * (1 + 0.2175 + 0.145)},
		{"the factor is capped", 18, 200, 52.5 * 1.5},
	} {
		if got := sec(deathTimer(c.level, c.minutes)); !near(got, c.want) {
			t.Errorf("%s: deathTimer(%d, %v) = %.3f s, want %.3f s", c.name, c.level, c.minutes, got, c.want)
		}
	}
}

func TestReplayLivesAreExact(t *testing.T) {
	// Participant 3 dies at 100 s and is back at 118 s; the death timer would say 16 s.
	fr := lifeFrames(0.5, 200, deadBetween(100, 118), nil)

	if l := livesAt(fr, at(100.25), feedClock, true)[3]; !l.Dead || !l.Exact || !near(l.Secs, 17.75) {
		t.Fatalf("the counter ticked, health still full: %+v, want dead, exact, 17.75 s", l)
	}
	l := livesAt(fr, at(105.25), feedClock, true)[3]
	if !l.Dead || !l.Exact || !near(l.Secs, 12.75) || l.AliveFor != 0 {
		t.Fatalf("mid-death: %+v, want dead, exact, 12.75 s", l)
	}
	if l := livesAt(fr, at(117.75), feedClock, true)[3]; !l.Dead || !near(l.Secs, 0.25) {
		t.Fatalf("a quarter second before the respawn: %+v", l)
	}
	if l := livesAt(fr, at(118), feedClock, true)[3]; l.Dead || l.AliveFor != 0 {
		t.Fatalf("at the first frame with health: %+v, want alive for 0 s", l)
	}
	if l := livesAt(fr, at(130), feedClock, true)[3]; l.Dead || !near(l.AliveFor, 12) {
		t.Fatalf("12 s after the respawn: %+v", l)
	}
}

func TestLivesIgnoreRevivesAndChampionsThatNeverDied(t *testing.T) {
	fr := lifeFrames(0.5, 200, deadBetween(100, 118), nil)
	l := livesAt(fr, at(32), feedClock, true)
	if l[5].Dead {
		t.Fatalf("health 0 without the counter ticking is a revive, not a death: %+v", l[5])
	}
	if l[3].Dead || l[3].AliveFor != 32 || l[1].AliveFor != 32 {
		t.Fatalf("a champion that never died has been alive since the first frame: p3 %+v p1 %+v", l[3], l[1])
	}
	if l := livesAt(fr, at(32), feedClock, false); l[5].Dead {
		t.Fatalf("live: a revive is no death either: %+v", l[5])
	}
}

func TestRespawnNeedsHalfHealthNotTheFirstPoint(t *testing.T) {
	// Dead from 100 s: 0 health for 2 s, then 1, 2, 3 ... up to 80 while still dead, and full at 118 s.
	state := func(sec float64, pid int) lifeState {
		switch {
		case pid != 3 || sec < 100:
			return lifeState{6, 0, 1500}
		case sec < 100.5:
			return lifeState{6, 1, 1500}
		case sec < 102:
			return lifeState{6, 1, 0}
		case sec < 118:
			return lifeState{6, 1, 1 + int(sec-102)*5}
		}
		return lifeState{6, 1, 1990}
	}
	fr := lifeFrames(0.5, 200, state, nil)
	if l := livesAt(fr, at(110), feedClock, true)[3]; !l.Dead || !l.Exact || !near(l.Secs, 8) {
		t.Fatalf("health 3 is not a respawn: %+v, want dead with exactly 8 s", l)
	}
	if l := livesAt(fr, at(110), feedClock, false)[3]; !l.Dead {
		t.Fatalf("live: health 3 is not a respawn: %+v", l)
	}
}

func TestACounterTickWithoutAHealthDropIsNoDeath(t *testing.T) {
	state := func(sec float64, pid int) lifeState {
		if pid == 3 && sec >= 100 {
			return lifeState{6, 1, 1500}
		}
		return lifeState{6, 0, 1500}
	}
	fr := lifeFrames(0.5, 200, state, nil)
	if l := livesAt(fr, at(105), feedClock, false)[3]; !l.Dead {
		t.Fatalf("live, within the grace for the health to fall: %+v, want dead", l)
	}
	if l := livesAt(fr, at(109), feedClock, false)[3]; l.Dead {
		t.Fatalf("live, health never fell: %+v, want alive", l)
	}
	if l := livesAt(fr, at(105), feedClock, true)[3]; l.Dead {
		t.Fatalf("replay knows the health never falls: %+v, want alive", l)
	}
}

func TestReplayLivesFallBackToTheEstimateAcrossAHole(t *testing.T) {
	// The window with the respawn (110-125 s) is not loaded.
	fr := lifeFrames(0.5, 200, deadBetween(100, 118), func(sec float64) bool { return sec >= 110 && sec < 125 })
	l := livesAt(fr, at(105), feedClock, true)[3]
	// Died at 100 s as level 6 at game minute 1.67: 16 s from half a second after the
	// counter ticked, so 11.5 s are left at 105 s.
	if !l.Dead || l.Exact || !near(l.Secs, 11.5) {
		t.Fatalf("with the respawn not loaded: %+v, want the 11.5 s estimate", l)
	}
	if l := livesAt(fr, at(109.5), feedClock, true)[3]; !l.Dead || l.Exact || !near(l.Secs, 7) {
		t.Fatalf("at the edge of the hole: %+v", l)
	}
}

func TestLiveLivesEstimateAndSnapToTheFeed(t *testing.T) {
	fr := lifeFrames(0.5, 200, deadBetween(100, 118), nil)
	// Live never looks at frames after the shown one, though the buffer holds them.
	l := livesAt(fr, at(105), feedClock, false)[3]
	if !l.Dead || l.Exact || !near(l.Secs, 11.5) {
		t.Fatalf("live estimate: %+v, want 11.5 s", l)
	}
	if l := livesAt(fr, at(107.25), feedClock, false)[3]; !near(l.Secs, 9.25) {
		t.Fatalf("the countdown must run between frames: %+v", l)
	}
	// The estimate (16 s) ran out but the feed still shows the champion dead.
	if l := livesAt(fr, at(117), feedClock, false)[3]; !l.Dead || l.Secs != 0 {
		t.Fatalf("overdue: %+v, want dead with 0 s", l)
	}
	// Health in the feed ends it at once, whatever the estimate said.
	early := lifeFrames(0.5, 200, deadBetween(100, 108), nil)
	if l := livesAt(early, at(108), feedClock, false)[3]; l.Dead || l.AliveFor != 0 {
		t.Fatalf("full health must snap to alive: %+v", l)
	}
}

func TestLiveEstimateUsesLevelAndGameMinuteAtTheTick(t *testing.T) {
	state := func(sec float64, pid int) lifeState {
		switch {
		case pid != 3 || sec < 1200:
			return lifeState{12, 3, 1500}
		case sec < 1200.5:
			return lifeState{13, 4, 1500} // the level-up comes with the tick
		case sec < 1250:
			return lifeState{13, 4, 0}
		}
		return lifeState{13, 4, 1500}
	}
	fr := lifeFrames(0.5, 1300, state, nil)
	want := deathTimer(13, 1200.5/60).Seconds() - 9.5 // died at minute 20, the timer runs from 1200.5 s
	if l := livesAt(fr, at(1210), feedClock, false)[3]; !near(l.Secs, want) {
		t.Fatalf("Secs = %.3f, want %.3f", l.Secs, want)
	}
}

func TestLivesStartedMidDeathCountFromTheFirstFrame(t *testing.T) {
	state := func(sec float64, pid int) lifeState {
		if pid == 3 && sec < 20 {
			return lifeState{6, 2, 0}
		}
		return lifeState{6, 2, 1500}
	}
	fr := lifeFrames(0.5, 100, state, nil)
	if l := livesAt(fr, at(5), feedClock, false)[3]; !l.Dead || l.Exact || !near(l.Secs, 11.5) {
		t.Fatalf("live: %+v, want dead, 11.5 s left of 16 counted from the first frame", l)
	}
	if l := livesAt(fr, at(5), feedClock, true)[3]; !l.Dead || !l.Exact || !near(l.Secs, 15) {
		t.Fatalf("replay: %+v, want exactly 15 s", l)
	}
	if l := livesAt(fr, at(60), feedClock, false)[3]; l.Dead || !near(l.AliveFor, 40) {
		t.Fatalf("after the respawn: %+v", l)
	}
}

func TestLivesCountDownOnTheGameClockAcrossAPause(t *testing.T) {
	// Dies at 100 s; the game is paused from 105 s to 125 s (a paused frame, then silence).
	skip := func(sec float64) bool { return sec > 105 && sec < 125 }
	fr := lifeFrames(0.5, 200, deadBetween(100, 140), skip)
	for i := range fr {
		if fr[i].ts.Equal(at(105)) {
			fr[i].v.GameState = "paused"
		}
	}
	pauses := findPauses(fr)
	if len(pauses) != 1 {
		t.Fatalf("pauses = %+v", pauses)
	}
	clock := func(ts time.Time) time.Duration { return ts.Sub(t0) - pauseShift(pauses, ts) }
	// 16 s of game from game second 100.5 end at game second 116.5 = feed 136.5 s.
	if l := livesAt(fr, at(130), clock, false)[3]; !l.Dead || !near(l.Secs, 6.5) {
		t.Fatalf("live across the pause: %+v, want 6.5 s", l)
	}
	if l := livesAt(fr, at(130), clock, true)[3]; !l.Exact || !near(l.Secs, 10) { // respawns at feed 140 s = game 120 s
		t.Fatalf("replay across the pause: %+v, want exactly 10 s", l)
	}
}

func TestLivesAliveForSinceTheLastRespawn(t *testing.T) {
	state := func(sec float64, pid int) lifeState {
		switch {
		case pid != 3 || sec < 40:
			return lifeState{6, 0, 1500}
		case sec < 40.5:
			return lifeState{6, 1, 1500}
		case sec < 55:
			return lifeState{6, 1, 0}
		}
		return lifeState{6, 1, 1500}
	}
	fr := lifeFrames(0.5, 700, state, nil)
	if l := livesAt(fr, at(100), feedClock, false)[3]; !near(l.AliveFor, 45) {
		t.Fatalf("alive for %.2f s, want 45 s since the respawn at 55 s", l.AliveFor)
	}
	// Far from the death the answer is only a lower bound: at least deathLookBack.
	if l := livesAt(fr, at(650), feedClock, false)[3]; l.AliveFor < deathLookBack.Seconds() {
		t.Fatalf("alive for %.2f s, want at least %v", l.AliveFor, deathLookBack)
	}
	if l := livesAt(fr, at(-5), feedClock, false)[3]; l.AliveFor != 0 {
		t.Fatalf("before the first frame: %+v", l)
	}
}

func TestLivesWithoutFramesAreUnknown(t *testing.T) {
	if l := livesAt(nil, at(1), feedClock, true); l != nil {
		t.Fatalf("no frames: %+v", l)
	}
	empty := []stamped[frame]{{at(0), frame{}}}
	if l := livesAt(empty, at(1), feedClock, true); l != nil {
		t.Fatalf("frames without participants: %+v", l)
	}
}
