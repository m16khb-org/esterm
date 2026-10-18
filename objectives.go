package main

import (
	"sort"
	"time"
)

// Objective timers: when the Dragon pit and the Baron pit spawn next, derived from the
// livestats feed (a team's dragons list and barons count grow when its team kills one).
//
// Rules, patch 26.20 (the Worlds 2026 patch; no objective rule changed between 26.1 and
// 26.20, checked in the 26.1, 26.19 and 26.20 notes and the wiki patch histories). All
// times are on the game clock (the HUD clock, paused while the game is paused):
//
//	Dragon, first spawn   5:00  wiki Dragon_pit "At 5 minutes into the game, an elemental
//	                            drake ... spawns"; 26.1 notes: "Epic Monster spawn times
//	                            are unchanged unless noted above"
//	Dragon, respawn       5:00 after the kill       wiki Dragon_pit (same text, V9.23)
//	Rift element          the 3rd drake's element is the only one that spawns afterwards
//	                      (the first three differ): wiki Dragon_pit V9.23
//	Dragon Soul           a team's 4th elemental drake kill (wiki Dragon_pit)
//	Elder Dragon          replaces the drake after the 4th drake kill of either team, 6:00
//	                      after that kill, and again 6:00 after every Elder kill: wiki
//	                      Elder_Dragon "Respawn 6:00", Dragon_pit V14.3 hotfix (the 5:00
//	                      first spawn of V14.2 was reverted to 6:00)
//	Baron Nashor          first spawn 20:00 (26.1: "Baron Nashor: 25:00 => 20:00"),
//	                      respawn 6:00 after the kill (wiki Baron_Nashor: Initial 20:00,
//	                      Respawn 6:00)
//	Atakhan               removed in 26.1 ("Atakhan has been removed from the game and will
//	                      no longer spawn"), so nothing moves Baron any more; in 25.S1 the
//	                      25:00 Baron spawn was its Atakhan-era timing and the old data of
//	                      those games is not covered here.
//
// Sources: https://www.leagueoflegends.com/en-us/news/game-updates/patch-26-1-notes,
// https://wiki.leagueoflegends.com/en-us/Dragon_pit, .../Elder_Dragon, .../Baron_Nashor.
// The feed lists the Elder Dragon in the killing team's dragons list as "elder", so it is
// told apart from the elemental drakes by its name. What the feed does not carry is the
// element of a drake before it dies: the next drake's type is known only from the 3rd drake
// on (the rift element), and Elder Dragon after the soul. See .issueops/adr.
const (
	objDragonFirst   = 5 * time.Minute
	objDragonRespawn = 5 * time.Minute
	objElderRespawn  = 6 * time.Minute
	objBaronFirst    = 20 * time.Minute
	objBaronRespawn  = 6 * time.Minute
	objSoulDrakes    = 4

	objElderName = "elder" // how the feed names the Elder Dragon in a dragons list
)

type objKind uint8

const (
	objDrake objKind = iota // an elemental drake
	objElder                // the Elder Dragon
	objBaron                // Baron Nashor
)

// objEvent is one objective kill seen in the feed: Team 0 blue, 1 red; N is the kill's
// place in the team's list (the index in dragons plus one, or the barons count), which
// identifies it for good. T is the first frame that shows it; the kill happened in
// (T-Gap, T], and a negative Gap means it happened at some unknown time before T (the
// frames before it are not known: the game was joined later, or its window is not fetched).
type objEvent struct {
	Kind  objKind
	Team  int
	N     int
	Drake string // the drake's type as the feed names it ("elder" for the Elder Dragon)
	T     time.Time
	Gap   time.Duration
}

// key identifies the kill: dragons list entry (elemental or Elder) or baron, team, place.
func (e objEvent) key() [3]int {
	class := 0
	if e.Kind == objBaron {
		class = 1
	}
	return [3]int{class, e.Team, e.N}
}

func (e objEvent) known() bool { return e.Gap >= 0 }

// objDerive lists the objective kills the sorted frames show. A frame gap longer than
// maxGap, and the counts of the first frame, give kills of unknown time.
func objDerive(fr []stamped[frame], maxGap time.Duration) []objEvent {
	if len(fr) == 0 {
		return nil
	}
	var out []objEvent
	teams := func(f frame) [2]teamStats { return [2]teamStats{f.BlueTeam, f.RedTeam} }
	add := func(team int, p, c teamStats, ts time.Time, gap time.Duration) {
		for j := len(p.Dragons); j < len(c.Dragons); j++ {
			k := objDrake
			if c.Dragons[j] == objElderName {
				k = objElder
			}
			out = append(out, objEvent{k, team, j + 1, c.Dragons[j], ts, gap})
		}
		for n := p.Barons + 1; n <= c.Barons; n++ {
			out = append(out, objEvent{objBaron, team, n, "", ts, gap})
		}
	}
	for team, c := range teams(fr[0].v) {
		add(team, teamStats{}, c, fr[0].ts, -1)
	}
	for i := 1; i < len(fr); i++ {
		gap := fr[i].ts.Sub(fr[i-1].ts)
		if fr[i-1].v.GameState == "paused" && gap > feedPauseGap {
			gap = 0 // the game stood still: the kill came right after the resume
		}
		if gap > maxGap {
			gap = -1
		}
		p, c := teams(fr[i-1].v), teams(fr[i].v)
		for team := range c {
			add(team, p[team], c[team], fr[i].ts, gap)
		}
	}
	return objMerge(nil, out)
}

// objMerge adds the events of add to old: an event is identified by its key, a known one
// replaces an unknown one, otherwise the first copy stays. The result is sorted by time.
func objMerge(old, add []objEvent) []objEvent {
	idx := make(map[[3]int]int, len(old)+len(add))
	out := make([]objEvent, 0, len(old)+len(add))
	for _, e := range append(append([]objEvent(nil), old...), add...) {
		i, seen := idx[e.key()]
		switch {
		case !seen:
			idx[e.key()] = len(out)
			out = append(out, e)
		case e.known() && !out[i].known():
			out[i] = e
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch {
		case !a.T.Equal(b.T):
			return a.T.Before(b.T)
		case a.Team != b.Team:
			return a.Team < b.Team
		}
		return a.N < b.N
	})
	return out
}

type objState uint8

const (
	objUnknown objState = iota // the kill that started the timer is not known
	objWaiting                 // spawns in Left
	objUp                      // alive in its pit
)

// objTimer is one pit: its state, the time to the spawn, the uncertainty of that time (the
// kill lies in a frame gap) and the drake in it or coming ("" unknown, objElderName).
type objTimer struct {
	State objState
	Left  time.Duration
	Err   time.Duration
	Drake string
}

// objView is what the board shows about the objectives at one instant; the zero value
// shows nothing. Soul is the team that took Dragon Soul (-1: none yet) with its type, and
// Point the team that is one drake short of it (-1: none).
type objView struct {
	Valid    bool
	Dragon   objTimer
	Baron    objTimer
	Soul     int
	SoulType string
	Point    int
}

// objAt derives the objective timers shown with frame cur (feed time curTS) from the
// kills in evs. clock converts a feed time to the game clock; absolute says it is the HUD
// clock, which the first spawns need (the timers that follow a kill only use differences).
func objAt(evs []objEvent, cur frame, curTS time.Time, clock func(time.Time) time.Duration, absolute bool) objView {
	v := objView{Soul: -1, Point: -1}
	if cur.GameState == "finished" {
		return v
	}
	v.Valid = true
	now := clock(curTS)

	// The frame's own counts are the truth about how many kills happened; the events say
	// when. A mismatch (an event the log lacks) leaves the timer unknown.
	var drakes [2]int // elemental drakes per team
	var elders, barons int
	var types []string // elemental drake types of both teams
	for team, ts := range [2]teamStats{cur.BlueTeam, cur.RedTeam} {
		for _, d := range ts.Dragons {
			if d == objElderName {
				elders++
				continue
			}
			drakes[team]++
			types = append(types, d)
		}
		barons += ts.Barons
	}

	var seq []objEvent // elemental drake kills in order
	var pit, baron *objEvent
	var pitKills, baronKills int
	var counts [2]int
	for i := range evs {
		e := &evs[i]
		if e.T.After(curTS) {
			break
		}
		switch e.Kind {
		case objDrake:
			if counts[e.Team]++; counts[e.Team] == objSoulDrakes && v.Soul < 0 {
				v.Soul = e.Team
			}
			seq = append(seq, *e)
			pit, pitKills = e, pitKills+1
		case objElder:
			pit, pitKills = e, pitKills+1
		case objBaron:
			baron, baronKills = e, baronKills+1
		}
	}
	if v.Soul < 0 { // the frame knows a soul the log misses (kills before it was joined)
		for team, n := range drakes {
			if n >= objSoulDrakes {
				v.Soul = team
			}
		}
	}

	rift := objRift(seq, types)
	if v.Soul >= 0 {
		v.SoulType = rift
		v.Dragon.Drake = objElderName
	} else if rift != "" {
		v.Dragon.Drake = rift
		for team, n := range drakes {
			if n == objSoulDrakes-1 {
				v.Point = team
			}
		}
	}

	// After a drake kill the next drake comes in 5:00, unless that kill gave the soul (the
	// last drake kill of a game with a soul is that kill): then the Elder Dragon, in 6:00.
	respawn := objDragonRespawn
	if v.Soul >= 0 {
		respawn = objElderRespawn
	}
	v.Dragon.State, v.Dragon.Left, v.Dragon.Err = objTime(pit, pitKills == drakes[0]+drakes[1]+elders, now, clock, absolute, objDragonFirst, respawn)
	v.Baron.State, v.Baron.Left, v.Baron.Err = objTime(baron, baronKills == barons, now, clock, absolute, objBaronFirst, objBaronRespawn)
	return v
}

// objRift is the element every drake from the third on has: the type of the third drake
// killed. Without a trustworthy order it is the type seen at least twice (the first
// three differ, so a repeated type is the rift's); "" while it cannot be told.
func objRift(seq []objEvent, types []string) string {
	if len(seq) >= 3 && seq[0].known() && seq[1].known() && seq[2].known() {
		return seq[2].Drake
	}
	n := map[string]int{}
	for _, t := range types {
		if n[t]++; n[t] == 2 {
			return t
		}
	}
	return ""
}

// objTime is the state of one pit: the state, the time left to the spawn when waiting and
// its uncertainty. last is the newest kill there (nil: none yet, the pit spawns first at
// first on the game clock); complete says the log holds every kill the frame counts;
// respawn is the delay after that kill.
func objTime(last *objEvent, complete bool, now time.Duration, clock func(time.Time) time.Duration, absolute bool, first, respawn time.Duration) (objState, time.Duration, time.Duration) {
	state := func(left time.Duration) objState {
		if left <= 0 {
			return objUp
		}
		return objWaiting
	}
	switch {
	case !complete:
		return objUnknown, 0, 0
	case last == nil:
		if !absolute {
			return objUnknown, 0, 0
		}
		left := first - now
		return state(left), left, 0
	}
	since := now - clock(last.T) // time since the first frame that showed the kill
	if !last.known() {
		// The kill was at or before that frame: the spawn is at most that far away.
		if respawn-since <= 0 {
			return objUp, 0, 0
		}
		return objUnknown, 0, 0
	}
	// The kill lies in (T-Gap, T]: assume the middle.
	left := respawn - since - last.Gap/2
	return state(left), left, last.Gap / 2
}

// ---- text ----

// objClock formats a countdown as m:ss, rounded up to whole seconds.
func objClock(d time.Duration) string {
	s := int((d + time.Second - 1) / time.Second)
	if s < 0 {
		s = 0
	}
	return itoa(s/60) + ":" + pad2(s%60)
}

func pad2(n int) string {
	if n < 10 {
		return "0" + itoa(n)
	}
	return itoa(n)
}

var objDrakeNames = map[string]string{
	"chemtech": "Chemtech", "cloud": "Cloud", "hextech": "Hextech", "infernal": "Infernal",
	"mountain": "Mountain", "ocean": "Ocean", objElderName: "Elder",
}

var objDrakeShort = map[string]string{
	"chemtech": "CHE", "cloud": "CLD", "hextech": "HEX", "infernal": "INF",
	"mountain": "MTN", "ocean": "OCN", objElderName: "ELD",
}

func objTeamName(team int) string {
	if team == 1 {
		return "red"
	}
	return "blue"
}

// objStatus is the status word of a pit: m:ss until the spawn, UP, or -- when unknown.
func objStatus(t objTimer) string {
	switch t.State {
	case objWaiting:
		return objClock(t.Left)
	case objUp:
		return "UP"
	}
	return "--"
}

func objStatusColor(t objTimer) string {
	switch t.State {
	case objWaiting:
		return cYel
	case objUp:
		return cBold + cGreen
	}
	return cDim
}

// objTimerRow is the board's timer line for a panel t cells wide, in the longest of four
// forms that fits: "Dragon 3:12 Infernal · Baron UP · Soul point blue", the same without
// the soul note, "DRG 3:12 INF · BRN UP" and "D 3:12 · B UP". The elder pit is named
// "Elder" in the long forms. It is empty without a shown frame.
func objTimerRow(o objView, t int) line {
	if !o.Valid {
		return nil
	}
	W := t - 4
	dName, dShort, dLetter := "Dragon", "DRG", "D"
	if o.Dragon.Drake == objElderName {
		dName, dShort, dLetter = "Elder", "ELD", "E"
	}
	pit := func(label, drake string, tm objTimer, names map[string]string) line {
		l := line{C(label, cDim), S(" "), C(objStatus(tm), objStatusColor(tm))}
		if n := names[drake]; n != "" && drake != objElderName {
			l = append(l, S(" "+n))
		}
		return l
	}
	soul := ""
	switch {
	case o.Soul >= 0:
		soul = "Soul " + objTeamName(o.Soul)
		if n := objDrakeNames[o.SoulType]; n != "" {
			soul += " " + n
		}
	case o.Point >= 0:
		soul = "Soul point " + objTeamName(o.Point)
	}
	sep := line{C(" · ", cGray)}
	join := func(parts ...line) line {
		var out line
		for i, p := range parts {
			if i > 0 {
				out = append(out, sep...)
			}
			out = append(out, p...)
		}
		return out
	}
	forms := []func() line{
		func() line {
			parts := []line{pit(dName, o.Dragon.Drake, o.Dragon, objDrakeNames), pit("Baron", "", o.Baron, objDrakeNames)}
			if soul != "" {
				parts = append(parts, line{C(soul, cDim)})
			}
			return join(parts...)
		},
		func() line {
			return join(pit(dName, o.Dragon.Drake, o.Dragon, objDrakeNames), pit("Baron", "", o.Baron, objDrakeNames))
		},
		func() line {
			return join(pit(dShort, o.Dragon.Drake, o.Dragon, objDrakeShort), pit("BRN", "", o.Baron, objDrakeShort))
		},
		func() line { return join(pit(dLetter, "", o.Dragon, nil), pit("B", "", o.Baron, nil)) },
	}
	for _, f := range forms {
		if l := f(); l.vis() <= W {
			return l
		}
	}
	return nil
}
