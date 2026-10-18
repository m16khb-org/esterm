package main

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// objKill is one objective kill of a synthetic game: at feed seconds after the start, by
// team 0 (blue) or 1 (red); what is a drake type, objElderName or "baron".
type objKill struct {
	at   float64
	team int
	what string
}

// objGame builds the frames of a game that starts at replayT0: one every 0.5 s up to total,
// counts growing with the kills, and an optional pause (a paused frame at pauseAt, none
// for pauseFor).
func objGame(total float64, kills []objKill, pauseAt, pauseFor float64) []frame {
	var out []frame
	for k := 0; float64(k)*0.5 <= total; k++ {
		at := float64(k) * 0.5
		if pauseFor > 0 && at > pauseAt && at < pauseAt+pauseFor {
			continue
		}
		f := frame{RFC460Timestamp: replayT0.Add(sec(at)).Format(time.RFC3339Nano), GameState: "in_game"}
		if pauseFor > 0 && at == pauseAt {
			f.GameState = "paused"
		}
		for _, kl := range kills {
			if kl.at > at {
				continue
			}
			ts := &f.BlueTeam
			if kl.team == 1 {
				ts = &f.RedTeam
			}
			if kl.what == "baron" {
				ts.Barons++
			} else {
				ts.Dragons = append(ts.Dragons, kl.what)
			}
		}
		out = append(out, f)
	}
	return out
}

func stampAll(fr []frame) []stamped[frame] {
	out := make([]stamped[frame], len(fr))
	for i, f := range fr {
		out[i] = stamped[frame]{parseTime(f.RFC460Timestamp), f}
	}
	return out
}

// objShown is the objective view of a game's feed at feed second at, with the game clock
// of a replay (start replayT0, no skew, game pauses taken from the frames).
func objShown(t *testing.T, fr []frame, at float64) objView {
	t.Helper()
	st := stampAll(fr)
	pauses := findPauses(st)
	clock := func(ts time.Time) time.Duration { return ts.Sub(replayT0) - pauseShift(pauses, ts) }
	cur, ok := pickStamped(st, replayT0.Add(sec(at)))
	if !ok {
		t.Fatal("no frame")
	}
	return objAt(objDerive(st, replayKillGap), cur.v, cur.ts, clock, true)
}

func objNear(a, b time.Duration) bool {
	d := a - b
	return d > -sec(1) && d < sec(1)
}

func TestObjFirstSpawnsFollowTheGameClock(t *testing.T) {
	fr := objGame(1300, nil, 0, 0)
	v := objShown(t, fr, 240)
	if v.Dragon.State != objWaiting || !objNear(v.Dragon.Left, 60*time.Second) {
		t.Errorf("dragon at 4:00 = %+v, want waiting 1:00", v.Dragon)
	}
	if v.Baron.State != objWaiting || !objNear(v.Baron.Left, 16*time.Minute) {
		t.Errorf("baron at 4:00 = %+v, want waiting 16:00", v.Baron)
	}
	v = objShown(t, fr, 300)
	if v.Dragon.State != objUp || v.Baron.State != objWaiting {
		t.Errorf("at 5:00 dragon %+v baron %+v, want dragon up", v.Dragon, v.Baron)
	}
	if v = objShown(t, fr, 1200); v.Baron.State != objUp {
		t.Errorf("baron at 20:00 = %+v, want up", v.Baron)
	}
}

func TestObjDragonRespawnsFiveMinutesAfterTheKill(t *testing.T) {
	fr := objGame(900, []objKill{{360, 0, "mountain"}}, 0, 0)
	v := objShown(t, fr, 420)
	if v.Dragon.State != objWaiting || !objNear(v.Dragon.Left, 240*time.Second) {
		t.Fatalf("dragon 1:00 after the kill = %+v, want waiting 4:00", v.Dragon)
	}
	if v.Dragon.Err > time.Second {
		t.Errorf("uncertainty %v for a half-second frame gap", v.Dragon.Err)
	}
	if v.Dragon.Drake != "" {
		t.Errorf("drake type %q known after one kill", v.Dragon.Drake)
	}
	if v = objShown(t, fr, 660); v.Dragon.State != objUp {
		t.Errorf("dragon 5:00 after the kill = %+v, want up", v.Dragon)
	}
}

func TestObjRiftElementIsTheThirdDrakeAndSoulComesWithTheFourth(t *testing.T) {
	kills := []objKill{{360, 0, "mountain"}, {700, 1, "cloud"}, {1000, 1, "ocean"}, {1330, 0, "ocean"}, {1700, 0, "ocean"}, {2050, 0, "ocean"}}
	fr := objGame(2600, kills, 0, 0)
	if v := objShown(t, fr, 800); v.Dragon.Drake != "" || v.Soul != -1 {
		t.Errorf("after two drakes: type %q soul %d, want both unknown", v.Dragon.Drake, v.Soul)
	}
	v := objShown(t, fr, 1100)
	if v.Dragon.Drake != "ocean" {
		t.Errorf("after the third drake the next type is %q, want ocean", v.Dragon.Drake)
	}
	// Blue has mountain, ocean, ocean (3 kills at 1700 = ocean #3): soul point before its 4th.
	v = objShown(t, fr, 1800)
	if v.Point != 0 || v.Soul != -1 || v.Dragon.Drake != "ocean" {
		t.Errorf("blue on three drakes: point %d soul %d type %q", v.Point, v.Soul, v.Dragon.Drake)
	}
	v = objShown(t, fr, 2100) // blue's 4th drake at 2050 gives the soul; Elder in 6:00
	if v.Soul != 0 || v.SoulType != "ocean" || v.Dragon.Drake != objElderName {
		t.Fatalf("after the 4th drake: soul %d %q, pit %q", v.Soul, v.SoulType, v.Dragon.Drake)
	}
	if v.Dragon.State != objWaiting || !objNear(v.Dragon.Left, 310*time.Second) {
		t.Errorf("Elder = %+v, want waiting about 5:10 (6:00 minus 50 s)", v.Dragon)
	}
	if v = objShown(t, fr, 2420); v.Dragon.State != objUp {
		t.Errorf("Elder 6:10 after the soul = %+v, want up", v.Dragon)
	}
}

func TestObjElderKillStartsAnotherSixMinutes(t *testing.T) {
	kills := []objKill{{360, 0, "ocean"}, {700, 0, "cloud"}, {1000, 0, "ocean"}, {1330, 0, "ocean"}, {1800, 1, objElderName}}
	fr := objGame(2400, kills, 0, 0)
	v := objShown(t, fr, 1900)
	if v.Soul != 0 || v.Dragon.Drake != objElderName || v.Dragon.State != objWaiting || !objNear(v.Dragon.Left, 260*time.Second) {
		t.Errorf("100 s after the Elder = soul %d pit %+v, want waiting 4:20", v.Soul, v.Dragon)
	}
	if v = objShown(t, fr, 2200); v.Dragon.State != objUp {
		t.Errorf("Elder respawn = %+v, want up", v.Dragon)
	}
}

func TestObjBaronFirstSpawnThenSixMinutesAfterEveryKill(t *testing.T) {
	fr := objGame(2400, []objKill{{1530, 1, "baron"}}, 0, 0)
	if v := objShown(t, fr, 1500); v.Baron.State != objUp {
		t.Errorf("baron at 25:00 = %+v, want up", v.Baron)
	}
	v := objShown(t, fr, 1680)
	if v.Baron.State != objWaiting || !objNear(v.Baron.Left, 210*time.Second) {
		t.Errorf("baron 2:30 after the kill = %+v, want waiting 3:30", v.Baron)
	}
	if v = objShown(t, fr, 1900); v.Baron.State != objUp {
		t.Errorf("baron 6:10 after the kill = %+v, want up", v.Baron)
	}
}

func TestObjTimersStandStillWhileTheGameIsPaused(t *testing.T) {
	// Dragon dies at 360 s, the game is paused from 400 s for 90 s.
	fr := objGame(900, []objKill{{360, 0, "mountain"}}, 400, 90)
	before := objShown(t, fr, 400)
	during := objShown(t, fr, 450)
	if before.Dragon.State != objWaiting || before.Dragon.Left != during.Dragon.Left {
		t.Errorf("timer moved while paused: %v then %v", before.Dragon.Left, during.Dragon.Left)
	}
	after := objShown(t, fr, 500) // 10 s after the resume at 490
	if got := before.Dragon.Left - after.Dragon.Left; !objNear(got, 10*time.Second) {
		t.Errorf("10 s after the resume the timer moved %v, want 10 s", got)
	}
	if v := objShown(t, fr, 360+300+90+1); v.Dragon.State != objUp {
		t.Errorf("dragon 5:00 of game time after the kill = %+v, want up", v.Dragon)
	}
}

func TestObjSeekingBackAndForthShowsTheStateOfThatMoment(t *testing.T) {
	sw := &synthWindow{frames: objGame(190, []objKill{{20, 0, "mountain"}, {80, 1, "baron"}}, 0, 0)}
	st := newSynthStore(sw)
	for b := st.first; b <= st.last; b++ {
		w, _ := sw.win("g1", startingTime(bucketStart(b)))
		st.absorb(w, detailsResp{})
	}
	at := func(s float64) objView { return st.snapshot(replayT0.Add(sec(s)), 0, 0).Obj }
	for _, s := range []float64{10, 150, 10, 100} { // forward, back before the kill, forward again
		if v := at(s); v.Dragon.State != objWaiting || v.Baron.State != objWaiting {
			t.Errorf("at %v s: dragon %v baron %v, want both waiting", s, v.Dragon.State, v.Baron.State)
		}
	}
	if a, b := at(10).Dragon.Left, at(10).Dragon.Left; a != b || !objNear(a, 290*time.Second) {
		t.Errorf("dragon at 10 s = %v, want 4:50 on every visit", a)
	}
	if got := at(150).Dragon.Left; !objNear(got, 170*time.Second) { // killed at 20, up at 320
		t.Errorf("dragon at 150 s = %v, want 2:50", got)
	}
}

func TestObjJoiningLateKnowsNothingUntilTheTimerMustHaveRunOut(t *testing.T) {
	// The game is joined at 800 s with two drakes down and one baron: when they died is lost.
	fr := objGame(2000, []objKill{{360, 0, "mountain"}, {700, 1, "cloud"}, {790, 1, "baron"}}, 0, 0)
	late := stampAll(fr)
	late = late[1600:] // frames from 800 s on
	clock := func(ts time.Time) time.Duration { return ts.Sub(replayT0) }
	evs := objDerive(late, replayKillGap)
	for _, e := range evs {
		if e.known() {
			t.Fatalf("event %+v known without its frames", e)
		}
	}
	show := func(s float64) objView {
		cur, _ := pickStamped(late, replayT0.Add(sec(s)))
		return objAt(evs, cur.v, cur.ts, clock, true)
	}
	if v := show(900); v.Dragon.State != objUnknown || v.Baron.State != objUnknown {
		t.Errorf("at 900 s: dragon %+v baron %+v, want both unknown", v.Dragon, v.Baron)
	}
	// 5:00 after the first frame at least one drake kill is behind: the pit is surely up.
	if v := show(1100); v.Dragon.State != objUp {
		t.Errorf("dragon at 1100 s = %+v, want up", v.Dragon)
	}
	if v := show(1160); v.Baron.State != objUp {
		t.Errorf("baron at 1160 s = %+v, want up", v.Baron)
	}
	// A kill seen live is exact again.
	more := objGame(2000, []objKill{{360, 0, "mountain"}, {700, 1, "cloud"}, {790, 1, "baron"}, {1200, 0, "ocean"}}, 0, 0)
	late = stampAll(more)[1600:]
	evs = objDerive(late, replayKillGap)
	if v := show(1300); v.Dragon.State != objWaiting || !objNear(v.Dragon.Left, 200*time.Second) {
		t.Errorf("dragon 100 s after a live kill = %+v, want waiting 3:20", v.Dragon)
	}
}

func TestObjUnfetchedWindowGivesAnUnknownKillUntilItArrives(t *testing.T) {
	fr := stampAll(objGame(600, []objKill{{360, 0, "mountain"}}, 0, 0))
	var gapped []stamped[frame] // the 10 s window around the kill was not fetched yet
	for _, f := range fr {
		if s := f.ts.Sub(replayT0); s < 350*time.Second || s >= 370*time.Second {
			gapped = append(gapped, f)
		}
	}
	evs := objDerive(gapped, replayKillGap)
	if len(evs) != 1 || evs[0].known() {
		t.Fatalf("events across a gap: %+v, want one unknown kill", evs)
	}
	clock := func(ts time.Time) time.Duration { return ts.Sub(replayT0) }
	cur, _ := pickStamped(gapped, replayT0.Add(400*time.Second))
	if v := objAt(evs, cur.v, cur.ts, clock, true); v.Dragon.State != objUnknown {
		t.Errorf("dragon with an unknown kill = %+v, want unknown", v.Dragon)
	}
	evs = objDerive(fr, replayKillGap)
	if v := objAt(evs, cur.v, cur.ts, clock, true); v.Dragon.State != objWaiting || !objNear(v.Dragon.Left, 260*time.Second) {
		t.Errorf("dragon once the window arrived = %+v, want waiting 4:20", v.Dragon)
	}
}

func TestObjMergeKeepsKnownKillsAndOrdersByTime(t *testing.T) {
	a := replayT0.Add(10 * time.Second)
	unknown := objEvent{objDrake, 0, 1, "ocean", a, -1}
	known := objEvent{objDrake, 0, 1, "ocean", a.Add(-time.Second), 500 * time.Millisecond}
	other := objEvent{objBaron, 1, 1, "", a.Add(-5 * time.Second), 0}
	got := objMerge([]objEvent{unknown}, []objEvent{known, other})
	if len(got) != 2 || got[0] != other || got[1] != known {
		t.Fatalf("merge = %+v", got)
	}
	if got = objMerge(got, []objEvent{unknown}); len(got) != 2 || !got[1].known() {
		t.Errorf("an unknown copy replaced a known kill: %+v", got)
	}
}

func TestObjNothingIsShownForAFinishedGame(t *testing.T) {
	fr := objGame(500, nil, 0, 0)
	fr[len(fr)-1].GameState = "finished"
	if v := objShown(t, fr, 500); v.Valid {
		t.Errorf("finished frame shows %+v", v)
	}
}

func TestObjWithoutTheGameClockOnlyKillTimersAreKnown(t *testing.T) {
	st := stampAll(objGame(500, []objKill{{100, 0, "ocean"}}, 0, 0))
	clock := func(ts time.Time) time.Duration { return ts.Sub(replayT0) }
	cur, _ := pickStamped(st, replayT0.Add(150*time.Second))
	v := objAt(objDerive(st, replayKillGap), cur.v, cur.ts, clock, false)
	if v.Dragon.State != objWaiting || !objNear(v.Dragon.Left, 250*time.Second) {
		t.Errorf("dragon = %+v, want waiting 4:10", v.Dragon)
	}
	if v.Baron.State != objUnknown {
		t.Errorf("baron = %+v, want unknown without the absolute clock", v.Baron)
	}
}

func TestFeedSnapshotCarriesObjectives(t *testing.T) {
	f := newFeed("", time.Second)
	f.spawn = replayT0
	f.SetSkew(time.Second)
	fr := objGame(400, []objKill{{100, 1, "infernal"}}, 0, 0)
	for i := 0; i < len(fr); i += 100 { // a poll's worth of frames at a time
		f.absorbLocked(windowResp{Frames: fr[i:min(i+100, len(fr))]}, detailsResp{})
	}
	s := f.SnapshotAt(replayT0, replayT0.Add(150*time.Second))
	// the drake died at 100 s feed, 50 s before the shown frame (the skew cancels): up in 4:10.
	if !s.Obj.Valid || s.Obj.Dragon.State != objWaiting || !objNear(s.Obj.Dragon.Left, 250*time.Second) {
		t.Errorf("snapshot dragon = %+v, want waiting about 4:10", s.Obj.Dragon)
	}
	if s.Obj.Baron.State != objWaiting || !objNear(s.Obj.Baron.Left, 1200*time.Second-149*time.Second) {
		t.Errorf("snapshot baron = %+v", s.Obj.Baron)
	}
}

func TestObjTimerRowFitsEveryWidth(t *testing.T) {
	v := objView{Valid: true, Soul: -1, Point: -1,
		Dragon: objTimer{State: objWaiting, Left: 192 * time.Second, Drake: "infernal"},
		Baron:  objTimer{State: objUp}}
	for _, tc := range []struct {
		w    int
		want string
	}{
		{80, "Dragon 3:12 Infernal · Baron UP"},
		{30, "DRG 3:12 INF · BRN UP"},
		{20, "D 3:12 · B UP"},
	} {
		l := objTimerRow(v, tc.w+4)
		got := ""
		for _, s := range l {
			got += s.text
		}
		if got != tc.want || l.vis() > tc.w {
			t.Errorf("width %d: %q, want %q", tc.w, got, tc.want)
		}
	}
	v.Soul, v.SoulType = 1, "infernal"
	v.Dragon = objTimer{State: objWaiting, Left: 100 * time.Second, Drake: objElderName}
	if l := objTimerRow(v, 100); !strings.Contains(lineText(l), "Elder 1:40") || !strings.Contains(lineText(l), "Soul red Infernal") {
		t.Errorf("elder row %q", lineText(l))
	}
	if objTimerRow(objView{}, 80) != nil {
		t.Error("a row for an empty view")
	}
}

func lineText(l line) string {
	var b strings.Builder
	for _, s := range l {
		b.WriteString(s.text)
	}
	return b.String()
}

var ansiOff = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func TestBoardShowsTheTimerRowAndStillFitsTheTerminal(t *testing.T) {
	offlineMap(t)
	obj := objView{Valid: true, Soul: -1, Point: -1,
		Dragon: objTimer{State: objWaiting, Left: 192 * time.Second, Drake: "infernal"},
		Baron:  objTimer{State: objUp}}
	now := time.Now()
	for _, mode := range []string{mmModeText, mmModeBlocks, mmModeKitty} {
		view := mmView{Mode: mode, Status: "x", CellW: 8, CellH: 16, Obj: obj}
		for _, w := range []int{50, 80, 104, 200} {
			for h := 12; h <= 60; h++ {
				for flags := 0; flags < 8; flags++ {
					tg := toggles{view: flags & 1, spark: flags&2 != 0, omap: flags&4 != 0, mm: true}
					if c := demoCanvasAt(w, h, now, 2*time.Second, detailsResp{}, tg, nil, view); c.h > h {
						t.Fatalf("mode %s w=%d h=%d tg=%+v: canvas has %d rows", mode, w, h, tg, c.h)
					}
				}
			}
		}
	}
	for w, want := range map[int]string{104: "Dragon 3:12 Infernal · Baron UP", 32: "DRG 3:12 INF · BRN UP", 24: "D 3:12 · B UP"} {
		c := demoCanvasAt(w, 60, now, 2*time.Second, detailsResp{}, toggles{}, nil, mmView{Obj: obj})
		if got := ansiOff.ReplaceAllString(canvasString(c), ""); !strings.Contains(got, want) {
			t.Errorf("width %d: no %q in\n%s", w, want, got)
		}
	}
	if c := demoCanvasAt(104, 60, now, 2*time.Second, detailsResp{}, toggles{}, nil, mmView{}); strings.Contains(ansiOff.ReplaceAllString(canvasString(c), ""), "Baron") {
		t.Error("a timer row without a view")
	}
}

func TestFeedGameChangeForgetsTheObjectiveLog(t *testing.T) {
	ff := &fakeFeed{clock: t0.Add(20 * time.Minute), gid: "g1", shape: func(fr *frame, ts time.Time) bool {
		fr.BlueTeam.Dragons = []string{"ocean"}
		return true
	}}
	f := ff.newFeed(2 * time.Second)
	f.FetchOnce()
	if len(f.objs) == 0 {
		t.Fatal("no objective kill logged from frames that already show a drake")
	}
	ff.mu.Lock()
	ff.gid, ff.lagMin = "g2", time.Hour
	ff.mu.Unlock()
	ff.advance(feedDiscoverEvery)
	f.FetchOnce()
	if len(f.objs) != 0 {
		t.Errorf("objective kills of the previous game survive: %+v", f.objs)
	}
}
