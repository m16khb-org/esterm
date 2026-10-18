package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func det(pid int) []mmDet { return []mmDet{{champRef: champRef{PID: pid}, Visible: true}} }

func TestMMPlayoutBurstyFrames(t *testing.T) {
	m := newMinimap(minimapCfg{})
	t0 := time.Unix(1000, 0)
	const burst = 5 * mmFPS // frames per 5 s of stream
	// A burst of frames (5 s of stream) arrives together 5 s after the stream started.
	for i := 0; i < burst; i++ {
		m.push(mmResult{idx: i, layout: "lck", visible: true, dets: det(i)}, t0.Add(5*time.Second))
	}
	// The next burst arrives 5 s later, so its base stays the earliest observed.
	for i := burst; i < 2*burst; i++ {
		m.push(mmResult{idx: i, layout: "lck", visible: true, dets: det(i)}, t0.Add(10*time.Second))
	}
	pid := func(at time.Duration) int {
		r, _, ok := mmPick(m.results, t0.Add(at))
		if !ok {
			return -1
		}
		return r.dets[0].PID
	}
	// The first burst shows its newest frame (all share base 5s-(burst-1)/fps, due at 11 s);
	// after that frame i is due at base + i/fps + 6 s.
	if got := pid(10900 * time.Millisecond); got != -1 {
		t.Fatalf("nothing is due before the buffer elapses, got frame %d", got)
	}
	if got := pid(11 * time.Second); got != burst-1 {
		t.Fatalf("burst shows its newest frame at 11s, got %d", got)
	}
	if got, want := pid(14*time.Second), int(float64(mmFPS)*(14-6-0.2)); got != want {
		t.Fatalf("frame %d at 14s, got %d", want, got)
	}
	if got := pid(time.Hour); got != 2*burst-1 {
		t.Fatalf("last frame once everything is due, got %d", got)
	}
}

func TestMMPickDropsOlderResults(t *testing.T) {
	now := time.Unix(100, 0)
	rs := []mmResult{{due: now.Add(-2 * time.Second)}, {due: now.Add(-time.Second)}, {due: now.Add(time.Second)}}
	_, rest, ok := mmPick(rs, now)
	if !ok || len(rest) != 2 || rest[0].due != rs[1].due {
		t.Fatalf("rest = %v ok=%v", rest, ok)
	}
}

func TestMMGameXY(t *testing.T) {
	if x, y := mmGameXY(0, 0); x != -120 || y != 14980 {
		t.Fatalf("top-left = %v,%v", x, y)
	}
	if x, y := mmGameXY(1, 1); x != 14870 || y != -120 {
		t.Fatalf("bottom-right = %v,%v", x, y)
	}
}

func chd(pid, team int, x, y float64, vis bool, age int) mmDet {
	return mmDet{champRef: champRef{PID: pid, Team: team, Champ: "C" + strconv.Itoa(pid)}, X: x, Y: y, Visible: vis, Age: age}
}

func TestMMChampsWithoutFeedShowOnlyRecentSightings(t *testing.T) {
	labels := map[int]string{1: "Ol", 2: "Vi", 3: "Ja"}
	r := mmResult{visible: true, dets: []mmDet{
		chd(1, 100, 0.25, 0.5, true, 0),
		chd(2, 100, 0.5, 0.5, true, mmHoldShort),    // lost for 1.5 s: still drawn
		chd(3, 100, 0.75, 0.5, true, mmHoldShort+1), // lost longer, nobody says it is alive: gone
	}}
	c := mmChampsOf(r, nil, 0, nil, labels)
	if len(c.Markers) != 2 || c.Markers[0] != (mmMarker{Team: 100, Label: "Ol", X: 0.25, Y: 0.5}) || c.Markers[1].Label != "Vi" {
		t.Fatalf("markers = %+v", c.Markers)
	}
	if len(c.Icons) != 2 || c.Icons[0].Champ != "C1" || c.Live != 2 || c.Stale != 0 || c.Alive != 3 {
		t.Fatalf("icons = %+v counts live=%d stale=%d alive=%d", c.Icons, c.Live, c.Stale, c.Alive)
	}
}

func TestMMChampsAliveChampionStaysUntilFound(t *testing.T) {
	r := mmResult{visible: true, dets: []mmDet{
		chd(1, 100, 0.2, 0.3, true, 0),
		chd(2, 100, 0.4, 0.3, true, mmStaleAfter), // 2 s: drawn like a tracked one
		chd(3, 100, 0.6, 0.3, true, 2*mmFPS*10),   // 20 s: dimmed with its age
		chd(4, 100, 0.8, 0.3, false, 0),           // never seen: nothing to draw
		chd(5, 100, 0.9, 0.3, true, 2*mmFPS*10),   // lost 20 s ago ...
	}}
	lives := mmLives{1: {AliveFor: 600}, 2: {AliveFor: 600}, 3: {AliveFor: 600}, 4: {AliveFor: 600},
		5: {AliveFor: 3}} // ... but respawned 3 s ago: that sighting is from before its death
	c := mmChampsOf(r, nil, 0, lives, nil)
	if len(c.Icons) != 3 {
		t.Fatalf("icons = %+v", c.Icons)
	}
	if c.Icons[1].State != mmIconLive || c.Icons[2].State != mmIconStale || c.Icons[2].Secs != 20 || c.Icons[2].X != 0.6 {
		t.Fatalf("states = %+v", c.Icons)
	}
	if c.Live != 2 || c.Stale != 1 || c.Alive != 5 || c.Dead != 0 {
		t.Fatalf("live=%d stale=%d alive=%d dead=%d", c.Live, c.Stale, c.Alive, c.Dead)
	}
}

func TestMMChampsDeadStayWhereTheyDied(t *testing.T) {
	r := mmResult{visible: true, dets: []mmDet{
		chd(2, 100, 0.5, 0.5, true, 0), // the tracker's position of the champion when it died
		chd(7, 200, 0, 0, false, 0),    // never tracked
		chd(8, 200, 0.3, 0.3, true, 0),
	}}
	lives := mmLives{2: {Dead: true, Secs: 22.3}, 7: {Dead: true, Secs: 0}, 8: {AliveFor: 100}}
	c := mmChampsOf(r, nil, 0, lives, nil)
	if len(c.Icons) != 3 || c.Dead != 2 || c.Live != 1 || c.Alive != 1 {
		t.Fatalf("icons=%+v dead=%d live=%d alive=%d", c.Icons, c.Dead, c.Live, c.Alive)
	}
	// a champion the tracker has a position for dies where it was last tracked ...
	if d := c.Icons[0]; d.State != mmIconDead || d.Secs != 23 || d.X != 0.5 || d.Y != 0.5 {
		t.Fatalf("blue dead icon = %+v, want a dead marker at its last tracked position (0.5,0.5)", d)
	}
	// ... one it never tracked waits at its team's base
	rx, ry := mmBasePos(200, 7)
	if d := c.Icons[1]; d.State != mmIconDead || d.Secs != 0 || d.X != rx || d.Y != ry || rx <= 0.5 || ry >= 0.5 {
		t.Fatalf("red dead icon = %+v (base %v,%v)", d, rx, ry)
	}
	if a, b := mmChampsOf(r, nil, 0, mmLives{2: {Dead: true}, 7: {Dead: true}, 3: {Dead: true}}, nil).Icons, c.Icons; len(a) < 2 || a[0].X == a[1].X && a[0].Y == a[1].Y {
		t.Fatalf("dead icons overlap: %+v / %+v", a, b)
	}
}

func TestMMChampsInterpolateBetweenResults(t *testing.T) {
	cur := mmResult{visible: true, dets: []mmDet{
		chd(1, 100, 0.20, 0.20, true, 0),
		chd(2, 100, 0.20, 0.20, true, 0),
		chd(3, 100, 0.20, 0.20, true, 0),
		chd(4, 100, 0.20, 0.20, true, 2), // held at the last position
	}}
	next := mmResult{visible: true, dets: []mmDet{
		chd(1, 100, 0.30, 0.20, true, 0),
		chd(2, 100, 0.90, 0.90, true, 0), // a teleport: far more than a quarter of the map
		chd(3, 100, 0, 0, false, 0),      // lost on the next frame
		chd(4, 100, 0.30, 0.20, true, 1), // still only held: not a new sighting
	}}
	got := mmChampsOf(cur, &next, 0.5, nil, nil).Icons
	if x := got[0].X; x < 0.2499 || x > 0.2501 || got[0].Y != 0.2 {
		t.Fatalf("halfway = %+v", got[0])
	}
	for _, i := range []int{1, 2, 3} {
		if got[i].X != 0.2 || got[i].Y != 0.2 {
			t.Fatalf("champion %d moved: %+v", i+1, got[i])
		}
	}
	if got := mmChampsOf(cur, &next, 0, nil, nil).Icons; got[0].X != 0.2 {
		t.Fatalf("at the first result the marker is where it was found: %+v", got[0])
	}
	next.visible = false // the next frame is a replay: nothing to move towards
	if got := mmChampsOf(cur, &next, 0.5, nil, nil).Icons; got[0].X != 0.2 {
		t.Fatalf("interpolated towards a hidden map: %+v", got[0])
	}
}

func TestMMStatusCountsAgainstAliveChampions(t *testing.T) {
	m := newMinimap(minimapCfg{})
	m.labels = map[int]string{}
	r := mmResult{layout: "worlds", visible: true, dets: []mmDet{
		chd(1, 100, 0.2, 0.2, true, 0),
		chd(2, 100, 0.3, 0.2, true, 0),
		chd(3, 100, 0.4, 0.2, true, 40), // lost, alive: drawn dimmed
		chd(4, 100, 0.5, 0.2, false, 0), // never seen yet
		chd(5, 100, 0, 0, false, 0),     // dead
	}}
	lives := mmLives{1: {AliveFor: 99}, 2: {AliveFor: 99}, 3: {AliveFor: 99}, 4: {AliveFor: 99}, 5: {Dead: true, Secs: 9}}
	if v := m.viewLocked(r, nil, 0, lives); v.Status != "WORLDS · tracking 2/4 (1 est.) · 1 dead" {
		t.Fatalf("status = %q", v.Status)
	}
	if v := m.viewLocked(r, nil, 0, nil); v.Status != "WORLDS · tracking 2/5" {
		t.Fatalf("status without a feed = %q", v.Status)
	}
}

func TestMMChampRefsTeams(t *testing.T) {
	var w windowResp
	w.GameMetadata.BlueTeamMetadata.Participants = append(w.GameMetadata.BlueTeamMetadata.Participants, struct {
		ParticipantID int    `json:"participantId"`
		SummonerName  string `json:"summonerName"`
		ChampionID    string `json:"championId"`
		Role          string `json:"role"`
	}{ParticipantID: 1, ChampionID: "Olaf", Role: "top"})
	w.GameMetadata.RedTeamMetadata.Participants = append(w.GameMetadata.RedTeamMetadata.Participants, struct {
		ParticipantID int    `json:"participantId"`
		SummonerName  string `json:"summonerName"`
		ChampionID    string `json:"championId"`
		Role          string `json:"role"`
	}{ParticipantID: 6, ChampionID: "Jax"})
	refs := mmChampRefs(w)
	if len(refs) != 2 || refs[0] != (champRef{PID: 1, Team: 100, Champ: "Olaf", Role: "top"}) || refs[1] != (champRef{PID: 6, Team: 200, Champ: "Jax"}) {
		t.Fatalf("refs = %+v", refs)
	}
}

func TestMMJSONLine(t *testing.T) {
	line := mmJSONLine(mmResult{
		idx: 7, layout: "lec", visible: true,
		hud: mmHUD{ClockSec: 754, ClockOK: true, Blue: 3, Red: 5, KillsOK: true},
		dets: []mmDet{
			{champRef: champRef{PID: 1, Team: 100, Champ: "Olaf"}, X: 0.5, Y: 0.5, Score: 0.9, Visible: true},
			{champRef: champRef{PID: 2, Team: 100, Champ: "Vi"}, Score: 0.1},
		},
	})
	var f mmJSONFrame
	if err := json.Unmarshal(line, &f); err != nil {
		t.Fatal(err)
	}
	if f.Idx != 7 || !f.Present || !f.Visible || f.Layout != "lec" || len(f.Champions) != 2 {
		t.Fatalf("frame = %+v", f)
	}
	if f.Clock == nil || *f.Clock != 754 || f.Kills == nil || *f.Kills != [2]int{3, 5} {
		t.Fatalf("hud = %v %v", f.Clock, f.Kills)
	}
	if f.Champions[0].X == nil || *f.Champions[0].X != 7375 || f.Champions[1].X != nil {
		t.Fatalf("positions = %+v", f.Champions)
	}
	var none mmJSONFrame
	if err := json.Unmarshal(mmJSONLine(mmResult{}), &none); err != nil || none.Clock != nil || none.Kills != nil || none.Present {
		t.Fatalf("unread HUD must be null: %+v %v", none, err)
	}
}

func TestMinimapCloseIsTerminal(t *testing.T) {
	m := newMinimap(minimapCfg{})
	m.Close(0)
	m.Start("g", "m", windowResp{}, time.Time{}, nil)
	if m.Done() != nil {
		t.Fatal("Start after Close must not launch a pipeline")
	}
}

func TestMinimapSupersededAndEndedPipelines(t *testing.T) {
	m := newMinimap(minimapCfg{})
	t0 := time.Now()
	m.gen = 2
	if m.pushGen(1, t0, mmResult{}) {
		t.Fatal("a superseded pipeline must not push results")
	}
	m.setStatusGen(1, "stale")
	if m.Status() == "stale" {
		t.Fatal("a superseded pipeline must not set the status")
	}
	m.push(mmResult{layout: "lck", visible: true, dets: det(1)}, t0)
	m.setStatusGen(2, "streaming")
	m.finish(context.Background(), 2)
	v := m.Snapshot(t0.Add(time.Minute), nil)
	if len(v.Markers) != 0 || v.Status != "stream ended" {
		t.Fatalf("after the stream ended: markers=%d status=%q", len(v.Markers), v.Status)
	}
}

func TestMMSnapshotAtFollowsStreamTime(t *testing.T) {
	m := newMinimap(minimapCfg{})
	m.cancel = func() {}
	if _, ok := m.VideoLatest(); ok {
		t.Fatal("no frames yet")
	}
	t0 := time.Unix(1000, 0)
	// 30 s of frames; the frame at 15 s is a replay (map not visible).
	const n = 30 * mmFPS
	for i := 0; i < n; i++ {
		m.push(mmResult{idx: i, layout: "lck", visible: i != n/2, dets: det(i)}, t0)
	}
	if latest, ok := m.VideoLatest(); !ok || latest != float64(n-1)/mmFPS {
		t.Fatalf("latest = %v %v", latest, ok)
	}
	if v := m.SnapshotAt(-1, nil); len(v.Markers) != 0 || !strings.HasPrefix(v.Status, "buffering") {
		t.Fatalf("before the first frame: %+v", v)
	}
	if v := m.SnapshotAt(10, nil); len(v.Markers) != 1 || v.Status != "LCK · tracking 1/1" {
		t.Fatalf("at 10 s: %+v", v)
	}
	if v := m.SnapshotAt(15, nil); len(v.Markers) != 0 || v.Status != "LCK · minimap hidden on broadcast" {
		t.Fatalf("the replay frame must show hidden: %q", v.Status)
	}
	if got := len(m.results); got > n/2+mmKeepBack {
		t.Fatalf("old frames must be dropped, %d left", got)
	}
}

func TestLayoutLockNeedsRepeatedClockReads(t *testing.T) {
	a, b := mmLayouts[0], mmLayouts[1]
	var k layoutLock
	for i := 0; i < mmLayoutVotes-1; i++ {
		k.vote(a)
	}
	k.vote(nil) // a frame without a clock read neither counts nor resets
	if k.cur != nil {
		t.Fatal("locked too early")
	}
	k.vote(b) // another layout reading the clock restarts the count
	k.vote(a)
	if k.cur != nil {
		t.Fatal("a vote for another layout must restart the count")
	}
	changed := false
	for i := 0; i < mmLayoutVotes; i++ {
		changed = k.vote(a) || changed
	}
	if k.cur != a || !changed {
		t.Fatal("not locked after enough clock reads")
	}
}

func TestLayoutLockSurvivesHiddenHUDAndSwitchesOnlyOnConsistentOtherClock(t *testing.T) {
	a, b := mmLayouts[0], mmLayouts[1]
	k := layoutLock{cur: a}
	for i := 0; i < 10*mmFPS*60; i++ { // minutes of replays: no clock reads at all
		k.vote(nil)
	}
	if k.cur != a {
		t.Fatal("a hidden HUD must not drop the lock")
	}
	for i := 0; i < mmLayoutVotes-1; i++ {
		k.vote(b)
	}
	k.vote(a) // the locked layout reading again resets the challenger
	for i := 0; i < mmLayoutVotes-1; i++ {
		k.vote(b)
	}
	if k.cur != a {
		t.Fatal("switched on interrupted votes")
	}
	if !k.vote(b) || k.cur != b {
		t.Fatal("did not switch after consistent reads of another layout")
	}
}

func TestMapVisibleFollowsHUD(t *testing.T) {
	mmSetMapRef(synthMap(512))
	l := mmLayouts[0]
	box := l.Map
	live := synthRegion(synthMap(512), box) // no broadcast border painted: the gate must not need one
	p := newMMPipeline(nil, nil)
	if !p.mapVisible(live, l, true) {
		t.Fatal("map with a live HUD not visible")
	}
	// a replay can show the same minimap, but the HUD is hidden
	if p.mapVisible(live, l, false) {
		t.Fatal("map without HUD (replay) reported visible")
	}
	if !p.mapVisible(live, l, true) {
		t.Fatal("map not visible again once the HUD is back")
	}
	// a crowd or desk shot under a still-readable HUD has no terrain
	if p.mapVisible(make([]byte, len(live)), l, true) {
		t.Fatal("terrain-less region reported visible")
	}
}

func TestRunSourceGivesUpOnUnreadableStream(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ESTERM_FFMPEG", fake)
	m := newMinimap(minimapCfg{})
	m.lockTimeout = 100 * time.Millisecond
	m.gen = 1
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	if !m.runSource(ctx, 1, "/nonexistent/a.mp4", nil, nil, true) {
		t.Fatal("did not give up on a stream without a layout")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("gave up too late")
	}
	if !strings.Contains(m.srcNote, "trying next stream") {
		t.Fatalf("note %q", m.srcNote)
	}
	last, stop := context.WithTimeout(ctx, 500*time.Millisecond)
	defer stop()
	if m.runSource(last, 1, "/nonexistent/a.mp4", nil, nil, false) {
		t.Fatal("the last stream must not report a fallback")
	}
	if m.srcNote != "HUD unreadable" {
		t.Fatalf("note %q", m.srcNote)
	}
}

func TestMMResultsStayBounded(t *testing.T) {
	m := newMinimap(minimapCfg{})
	t0 := time.Unix(1000, 0)
	for i := 0; i < mmResultsKeep*3+7; i++ {
		m.push(mmResult{idx: i}, t0)
	}
	n := len(m.results)
	if n != mmResultsKeep || m.results[0].idx != mmResultsKeep*2+7 || m.results[n-1].idx != mmResultsKeep*3+6 {
		t.Fatalf("kept %d results, idx %d..%d", n, m.results[0].idx, m.results[n-1].idx)
	}
}
