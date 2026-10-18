package main

import (
	"math"
	"sort"
	"time"
)

// The death timer: a champion that died at level L, T game minutes into the game, is
// back after deathBase[L] · (1 + timeFactor(T)) seconds. These are the only numbers of
// the model, fitted on 713 deaths of 26 finished games of 10 leagues (median error 0.24 s,
// 100 % within 1.5 s); the livestats feed does not publish a respawn time, so a live game
// only has this estimate while a replay reads the true respawn from the frames after the
// death.
var (
	// deathBase is the base respawn time in seconds by champion level (index 0 unused;
	// levels beyond the table use its last entry).
	deathBase = [19]float64{0, 10, 10, 12, 12, 14, 16, 20, 25, 28, 32.5, 35, 37.5, 40, 42.5, 45, 47.5, 50, 52.5}

	// deathRamp is the time factor: from the minute From on, the respawn time grows by
	// PerMin (a fraction of the base) per game minute, up to the next entry's From.
	deathRamp = []struct{ From, PerMin float64 }{{15, 0.0085}, {30, 0.0060}, {45, 0.0290}}
)

// deathFactorCap is the largest time factor (+50 %).
const deathFactorCap = 0.5

const (
	// deathGap is the longest frame gap that still counts as consecutive frames. A longer
	// one (not a game pause) is a hole: a window the replay has not fetched yet.
	deathGap = 3 * time.Second
	// deathLookBack is how far back an alive champion's last respawn is looked for.
	deathLookBack = 5 * time.Minute
	// deathDropWithin is how long after its death counter ticked a champion's health may
	// stay above half: the counter leads the health by about half a second, and a tick
	// without a drop (a revive, not a death) is not a death.
	deathDropWithin = 8 * time.Second
	// deathTickLead is how far the counter tick leads the health reaching 0 (median 0.52 s):
	// the respawn time runs from the death itself, so the estimate starts that much later.
	deathTickLead = 500 * time.Millisecond
)

// deathTimer is the respawn time of a champion that died at the given level and game minute.
func deathTimer(level int, minutes float64) time.Duration {
	base := deathBase[min(max(level, 1), len(deathBase)-1)]
	factor := 0.0
	for i, r := range deathRamp {
		to := math.Inf(1)
		if i+1 < len(deathRamp) {
			to = deathRamp[i+1].From
		}
		if minutes > r.From {
			factor += r.PerMin * (math.Min(minutes, to) - r.From)
		}
	}
	return time.Duration(base * (1 + math.Min(factor, deathFactorCap)) * float64(time.Second))
}

// low reports whether the participant is below half health. A dead champion reads 0 or a
// few hundred points until it respawns, which comes back with (nearly) full health; the
// first point of health is no respawn, and neither is health at 0 for a revive.
func (p participant) low() bool { return 2*p.CurrentHealth < p.MaxHealth }

// participantIn finds participant pid in the frame.
func participantIn(f frame, pid int) (participant, bool) {
	for _, team := range [2][]participant{f.BlueTeam.Participants, f.RedTeam.Participants} {
		for i := range team {
			if team[i].ParticipantID == pid {
				return team[i], true
			}
		}
	}
	return participant{}, false
}

// consecutive reports whether frame b follows frame a without a hole in between: a gap
// after a paused frame is the game standing still, not missing data.
func consecutive(a, b stamped[frame]) bool {
	return b.ts.Sub(a.ts) <= deathGap || a.v.GameState == "paused"
}

// livesAt derives the life of every participant at feed time t from the frames fr (sorted
// by time); nil when there is nothing to derive it from. game maps a feed time to the game
// clock, which stands still while the game is paused.
//
// A champion dies in the frame where its deaths counter ticks and is back in the first
// frame with at least half health after its health had fallen. With lookahead the frames
// after t are used as well (a replay holds the whole game): the respawn of a dead champion
// is then that frame, exact unless a hole hides it. Without it (live) the respawn is the
// death timer's estimate from the tick, and a frame with health ends it at once.
func livesAt(fr []stamped[frame], t time.Time, game func(time.Time) time.Duration, lookahead bool) mmLives {
	if len(fr) == 0 {
		return nil
	}
	i := max(sort.Search(len(fr), func(i int) bool { return fr[i].ts.After(t) })-1, 0)
	at := t
	if at.Before(fr[0].ts) {
		at = fr[0].ts
	}
	out := mmLives{}
	for _, team := range [2][]participant{fr[i].v.BlueTeam.Participants, fr[i].v.RedTeam.Participants} {
		for _, p := range team {
			out[p.ParticipantID] = lifeOf(fr, i, p, at, game, lookahead)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// lifeOf is the life of participant cur, as frame fr[i] shows it, at feed time at.
func lifeOf(fr []stamped[frame], i int, cur participant, at time.Time, game func(time.Time) time.Duration, lookahead bool) mmLife {
	lf := lifeNow(fr, i, cur, at, game, lookahead)
	lf.Level = cur.Level
	if cur.MaxHealth > 0 {
		lf.HP = float64(cur.CurrentHealth) / float64(cur.MaxHealth)
	}
	return lf
}

// lifeNow is lifeOf without the level and health.
func lifeNow(fr []stamped[frame], i int, cur participant, at time.Time, game func(time.Time) time.Duration, lookahead bool) mmLife {
	pid := cur.ParticipantID
	if cur.Deaths == 0 { // the counter never ticked: alive since the first frame
		return mmLife{AliveFor: at.Sub(fr[0].ts).Seconds()}
	}
	c, found := lastTick(fr, i, pid, at)
	if !found { // no death in the stretch looked at; c is the oldest frame looked at
		return mmLife{AliveFor: at.Sub(fr[c].ts).Seconds()}
	}
	end, j := followDeath(fr, c, i, pid, lookahead)
	switch {
	case end == deathFalse:
		return mmLife{AliveFor: at.Sub(fr[0].ts).Seconds()}
	case end == deathRespawned && j <= i:
		return mmLife{AliveFor: at.Sub(fr[j].ts).Seconds()}
	}
	now := game(at)
	if end == deathRespawned {
		return mmLife{Dead: true, Secs: max(0, (game(fr[j].ts) - now).Seconds()), Exact: true}
	}
	first, _ := participantIn(fr[c].v, pid)
	start := game(fr[c].ts) + deathTickLead
	due := start + deathTimer(first.Level, max(start, 0).Minutes())
	return mmLife{Dead: true, Secs: max(0, (due - now).Seconds())}
}

// lastTick finds the latest frame at or before fr[i] where the deaths counter of
// participant pid ticked, looking back at most deathLookBack from at. A first frame that
// already shows it low after deaths counts too (the buffer began during the death). When
// there is none, it returns the oldest frame looked at and false.
func lastTick(fr []stamped[frame], i, pid int, at time.Time) (int, bool) {
	j := i
	cur, _ := participantIn(fr[j].v, pid)
	for j > 0 && at.Sub(fr[j-1].ts) <= deathLookBack {
		prev, _ := participantIn(fr[j-1].v, pid)
		if cur.Deaths > prev.Deaths {
			return j, true
		}
		cur = prev
		j--
	}
	return j, j == 0 && cur.Deaths > 0 && cur.low()
}

// deathEnd is how the death that began at a counter tick turned out.
type deathEnd int

const (
	deathOpen      deathEnd = iota // no end seen: the champion is dead as far as the frames show
	deathRespawned                 // back with health at the returned frame
	deathFalse                     // health never fell: the tick was no death
)

// followDeath walks participant pid's frames from the counter tick at fr[c] on, up to the
// shown frame fr[i], and beyond it while lookahead is set and the frames have no hole.
// The champion is first waiting for its health to fall, then for it to come back.
func followDeath(fr []stamped[frame], c, i, pid int, lookahead bool) (deathEnd, int) {
	down := false
	for j := c; j < len(fr); j++ {
		if j > i && (!lookahead || !consecutive(fr[j-1], fr[j])) {
			break
		}
		p, _ := participantIn(fr[j].v, pid)
		switch {
		case !down && p.low():
			down = true
		case !down && fr[j].ts.Sub(fr[c].ts) > deathDropWithin:
			return deathFalse, 0
		case down && !p.low():
			return deathRespawned, j
		}
	}
	return deathOpen, 0
}
