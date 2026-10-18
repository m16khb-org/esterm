package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

var replayT0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) // 10 s aligned

// synthFrames builds the feed of a 200 s game: a frame every 0.5 s, a blue kill every 30 s,
// and a game pause (paused frame at 60 s, nothing until 80 s).
func synthFrames() []frame {
	var out []frame
	for k := 0; k <= 400; k++ {
		at := time.Duration(k) * 500 * time.Millisecond
		if at > 60*time.Second && at < 80*time.Second {
			continue
		}
		f := frame{RFC460Timestamp: replayT0.Add(at).Format(time.RFC3339Nano), GameState: "in_game"}
		if at == 60*time.Second {
			f.GameState = "paused"
		}
		if at == 200*time.Second {
			f.GameState = "finished"
		}
		f.BlueTeam.TotalKills = int(at / (30 * time.Second))
		f.BlueTeam.TotalGold = 1000 * int(at/time.Second)
		out = append(out, f)
	}
	return out
}

// synthWindow serves synthFrames in 10 s windows and records the requests.
type synthWindow struct {
	mu     sync.Mutex
	starts []string
	fail   map[string]int // start → remaining failures
	frames []frame
}

func (s *synthWindow) win(gid, start string) (windowResp, error) {
	s.mu.Lock()
	s.starts = append(s.starts, start)
	if s.fail[start] > 0 {
		s.fail[start]--
		s.mu.Unlock()
		return windowResp{}, fmt.Errorf("boom")
	}
	s.mu.Unlock()
	from, _ := time.Parse("2006-01-02T15:04:05.000Z", start)
	var w windowResp
	w.GameMetadata.PatchVersion = "16.1"
	for _, f := range s.frames {
		if ts := parseTime(f.RFC460Timestamp); !ts.Before(from) && ts.Before(from.Add(10*time.Second)) {
			w.Frames = append(w.Frames, f)
		}
	}
	return w, nil
}

func newSynthStore(sw *synthWindow) *replayStore {
	st := newReplayStore("g1", replayT0, replayT0.Add(200*time.Second), 20*time.Millisecond)
	st.pace = 0
	st.win = sw.win
	st.det = func(gid, start string) (detailsResp, error) { return detailsResp{}, nil }
	return st
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestReplayStoreFetchesEveryWindowOnceAroundTheFocusFirst(t *testing.T) {
	sw := &synthWindow{frames: synthFrames()}
	st := newSynthStore(sw)
	focus := bucketOf(replayT0.Add(150 * time.Second))
	st.setFocus(replayT0.Add(150*time.Second), false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go st.run(ctx)
	waitFor(t, "all windows", func() bool { return st.progress() == 1 })
	for i := 0; i < 50; i++ { // seeking around changes nothing: every window is already here
		st.setFocus(replayT0.Add(time.Duration(i*4)*time.Second), false)
	}
	time.Sleep(50 * time.Millisecond)

	n := int(st.last-st.first) + 1
	if got := st.requestCount(); got != n {
		t.Errorf("requests = %d, want one per window (%d)", got, n)
	}
	seen := map[string]bool{}
	for _, s := range sw.starts {
		if seen[s] {
			t.Errorf("window %s fetched twice", s)
		}
		seen[s] = true
	}
	// The pick order is the store's decision; requests run concurrently, so check the picks.
	ord := newSynthStore(&synthWindow{frames: synthFrames()})
	ord.setFocus(replayT0.Add(150*time.Second), false)
	for i := int64(0); i <= replayNear+1; i++ { // the window before the focus, the focus, replayNear after it
		b, win, _, _, ok := ord.next()
		if !ok || !win || b < focus-1 || b > focus+replayNear {
			t.Fatalf("pick %d = window %d (ok %v), want one next to the focus window %d", i, b, ok, focus)
		}
	}
	ps := st.pausesList()
	if len(ps) != 1 || !ps[0].Start.Equal(replayT0.Add(60*time.Second)) || ps[0].Dur != 20*time.Second {
		t.Errorf("pauses = %v, want one of 20 s at 60 s", ps)
	}
	if e := st.edge(); !e.Equal(st.end) {
		t.Errorf("edge = %v, want the end %v", e, st.end)
	}
}

func TestReplayStoreRetriesFailedWindowAfterCooldown(t *testing.T) {
	sw := &synthWindow{frames: synthFrames(), fail: map[string]int{startingTime(replayT0.Add(100 * time.Second)): 2}}
	st := newSynthStore(sw)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go st.run(ctx)
	waitFor(t, "all windows despite failures", func() bool { return st.progress() == 1 })
	if want := int(st.last-st.first) + 1 + 2; st.requestCount() != want {
		t.Errorf("requests = %d, want %d (two retries)", st.requestCount(), want)
	}
}

func TestReplayKillsSkipUnknownGaps(t *testing.T) {
	mk := func(sec float64, kills int) stamped[frame] {
		var f frame
		f.BlueTeam.TotalKills = kills
		return stamped[frame]{replayT0.Add(time.Duration(sec * float64(time.Second))), f}
	}
	fr := []stamped[frame]{mk(0, 0), mk(1, 1), mk(8, 2), mk(8.5, 3), mk(9, 5)}
	ks := replayKills(fr)
	if len(ks) != 2 || ks[0].Count != 1 || ks[1].Count != 3 {
		t.Errorf("kills = %+v, want counts 1 and 3 (2 is across a 7 s gap, 5 skips a count)", ks)
	}
}

func TestReplayStoreSnapshot(t *testing.T) {
	sw := &synthWindow{frames: synthFrames()}
	st := newSynthStore(sw)
	for b := st.first; b <= st.last; b++ {
		w, _ := sw.win("g1", startingTime(bucketStart(b)))
		st.absorb(w, detailsResp{})
	}
	snap := st.snapshot(replayT0.Add(95*time.Second), 90*time.Second, 3500*time.Millisecond)
	if len(snap.W.Frames) != 1 || snap.W.Frames[0].BlueTeam.TotalKills != 3 {
		t.Fatalf("frame at 95 s = %+v", snap.W.Frames)
	}
	if snap.Game != 90*time.Second {
		t.Errorf("Game = %v, want the position passed in", snap.Game)
	}
	last := snap.Series[len(snap.Series)-1]
	if last.gameMin != 1 { // 95 s − 3.5 s skew − 20 s pause = 71.5 s of game
		t.Errorf("series ends at game minute %d, want 1", last.gameMin)
	}
	early := st.snapshot(replayT0.Add(-time.Hour), 0, 0)
	if len(early.W.Frames) != 1 || early.W.Frames[0].BlueTeam.TotalKills != 0 {
		t.Errorf("before the first frame the first frame shows, got %+v", early.W.Frames)
	}
}

// ---- clock ---------------------------------------------------------------------

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func testReplay(t *testing.T) (*replay, *fakeClock) {
	t.Helper()
	return testReplayOf(t, synthFrames())
}

// testReplayOf is a replay of a finished game whose feed is frames, fully loaded.
func testReplayOf(t *testing.T, frames []frame) (*replay, *fakeClock) {
	t.Helper()
	sw := &synthWindow{frames: frames}
	st := newSynthStore(sw)
	for b := st.first; b <= st.last; b++ {
		w, _ := sw.win("g1", startingTime(bucketStart(b)))
		st.absorb(w, detailsResp{})
		st.done[b] = true
	}
	clk := &fakeClock{t: replayT0.Add(time.Hour)}
	r := &replay{now: clk.now, st: st, speed: 1, at: clk.t}
	r.sy = newMMSync(st.spawn, mmFPS, st.pausesList)
	r.total = r.gameOf(st.end.Sub(st.spawn))
	return r, clk
}

func TestReplayGameClockAroundPauses(t *testing.T) {
	r, _ := testReplay(t)
	if got, want := r.total, 176500*time.Millisecond; got != want { // 200 s − 3.5 s skew − 20 s pause
		t.Errorf("total = %v, want %v", got, want)
	}
	for _, g := range []time.Duration{0, 30 * time.Second, 56500 * time.Millisecond, 57 * time.Second, 100 * time.Second, r.total} {
		if back := r.gameOf(r.feedOf(g)); back != g {
			t.Errorf("gameOf(feedOf(%v)) = %v", g, back)
		}
	}
	if got := r.feedOf(0); got != 3500*time.Millisecond {
		t.Errorf("feedOf(0) = %v, want the skew", got)
	}
	if got := r.feedOf(100 * time.Second); got != 123500*time.Millisecond {
		t.Errorf("feedOf(100s) = %v, want 100 s + skew + the 20 s pause", got)
	}
}

func TestReplayPlaybackControls(t *testing.T) {
	r, clk := testReplay(t)
	adv := func(d time.Duration) { clk.t = clk.t.Add(d) }

	adv(10 * time.Second)
	if p := r.State().Pos; p != 10*time.Second {
		t.Errorf("1x for 10 s: pos = %v", p)
	}
	r.CycleSpeed(1)
	adv(10 * time.Second)
	if p := r.State().Pos; p != 30*time.Second {
		t.Errorf("2x for 10 s: pos = %v, want 30 s", p)
	}
	r.CycleSpeed(1)
	r.CycleSpeed(1) // the end stays
	if s := r.State().Speed; s != 4 {
		t.Errorf("speed = %v, want 4", s)
	}
	adv(5 * time.Second)
	if p := r.State().Pos; p != 50*time.Second {
		t.Errorf("4x for 5 s: pos = %v, want 50 s", p)
	}
	r.CycleSpeed(-1)
	r.CycleSpeed(-1)
	r.CycleSpeed(-1)
	if s := r.State().Speed; s != 1 {
		t.Errorf("speed = %v, want 1", s)
	}

	r.TogglePause()
	adv(time.Minute)
	st := r.State()
	if !st.Paused || st.Pos != 50*time.Second {
		t.Errorf("paused: %+v, want the clock frozen at 50 s", st)
	}
	r.Seek(10 * time.Second)
	r.Seek(-100 * time.Second)
	if p := r.State().Pos; p != 0 {
		t.Errorf("seek before the start: pos = %v, want 0", p)
	}
	r.Seek(time.Hour)
	if p := r.State().Pos; p != r.total {
		t.Errorf("seek past the end: pos = %v, want %v", p, r.total)
	}
	r.Seek(-10 * time.Second)
	if p := r.State().Pos; p != r.total-10*time.Second {
		t.Errorf("pos = %v, want 10 s before the end", p)
	}
	r.TogglePause()
	adv(20 * time.Second)
	if s, _ := r.Tick(clk.t, false, false); s.Game != r.total {
		t.Errorf("past the end the header clock stops at %v, got %v", r.total, s.Game)
	}
	if st := r.State(); !st.Paused || st.Pos != r.total {
		t.Errorf("end of game: %+v, want paused at the end", st)
	}
	r.TogglePause()
	if st := r.State(); st.Paused || st.Pos != 0 {
		t.Errorf("resume at the end: %+v, want playing from the start", st)
	}
}

func TestReplayTickShowsPositionAndNoVideoStatus(t *testing.T) {
	r, clk := testReplay(t)
	clk.t = clk.t.Add(100 * time.Second)
	snap, view := r.Tick(clk.t, true, true)
	if snap.Game != 100*time.Second {
		t.Errorf("header clock = %v, want the position 100 s", snap.Game)
	}
	// 100 s of game = 123.5 s of feed (skew + the pause): 4 blue kills (every 30 s).
	if got := snap.W.Frames[0].BlueTeam.TotalKills; got != 4 {
		t.Errorf("kills on the board = %d, want 4", got)
	}
	if !strings.Contains(view.Status, "no official VOD") || !strings.Contains(view.Status, "scoreboard only") {
		t.Errorf("without a VOD the minimap status = %q", view.Status)
	}
	if st := r.State(); st.HasVideo || !strings.Contains(st.Status, "scoreboard only") {
		t.Errorf("state = %+v", st)
	}
}

func TestReplayTickGivesBoardAndMapTheSameExactLives(t *testing.T) {
	frames := synthFrames()
	for i := range frames { // blue 1 dies at 45 s (the counter ticks first) and is back at 58 s of the feed
		sec := parseTime(frames[i].RFC460Timestamp).Sub(replayT0).Seconds()
		p := participant{ParticipantID: 1, Level: 6, CurrentHealth: 1500, MaxHealth: 2000}
		if sec >= 45 {
			p.Deaths = 1
		}
		if sec >= 45.5 && sec < 58 {
			p.CurrentHealth = 0
		}
		frames[i].BlueTeam.Participants = []participant{p}
	}
	r, clk := testReplayOf(t, frames)
	clk.t = clk.t.Add(50 * time.Second) // 50 s of game = 53.5 s of feed
	snap, view := r.Tick(clk.t, false, false)
	for name, lives := range map[string]mmLives{"board snapshot": snap.Lives, "minimap view": view.Lives} {
		if l := lives[1]; !l.Dead || !l.Exact || math.Abs(l.Secs-4.5) > 0.01 {
			t.Errorf("%s: %+v, want dead with exactly 4.5 s left", name, l)
		}
	}
}

func TestReplayVideoPositionUsesEstimateUntilLocked(t *testing.T) {
	r, _ := testReplay(t)
	r.vods = []replayVod{{URL: "u", Label: "YouTube en-US", SpawnAt: 370}}
	r.vid = newReplayVideo(r)
	vt, known := r.videoOf(10)
	if known || vt != 380 {
		t.Errorf("before the lock: %v known=%v, want the estimate 380", vt, known)
	}
	r.haveO, r.oLock = true, -365
	if vt, known = r.videoOf(10); !known || vt != 375 {
		t.Errorf("after the lock: %v known=%v, want 375", vt, known)
	}
	r.forgetOffset()
	if _, known = r.videoOf(10); known {
		t.Error("forgetOffset keeps the offset")
	}
}

func TestReplayInRange(t *testing.T) {
	cases := []struct {
		name          string
		start, lo, hi float64
		ok            bool
		want          float64
		in            bool
	}{
		{"starting up, at start", 100, 0, 0, false, 100, true},
		{"starting up, target moved far", 100, 0, 0, false, 400, false},
		{"playing along", 100, 100, 120, true, 125, true},
		{"small seek back inside history", 100, 100, 120, true, 110, true},
		{"seek back before the oldest decoded frame", 100, 100, 120, true, 80, false},
		{"seek ahead a little: read through", 100, 100, 120, true, 135, true},
		{"seek ahead far: restart", 100, 100, 120, true, 300, false},
	}
	for _, c := range cases {
		if got := replayInRange(c.start, c.lo, c.hi, c.ok, c.want); got != c.in {
			t.Errorf("%s: in range = %v, want %v", c.name, got, c.in)
		}
	}
}

func TestReplayKeys(t *testing.T) {
	r, clk := testReplay(t)
	r.Seek(50 * time.Second)
	var tg toggles
	keys := func(s string) bool {
		ks, rest := decodeKeys([]byte(s))
		ks = append(ks, flushKeys(rest)...)
		quit := false
		for _, k := range ks {
			quit = replayKey(r, &tg, k) || quit
		}
		return quit
	}

	keys("\x1b[C")
	if p := r.State().Pos; p != 60*time.Second {
		t.Errorf("right arrow: pos = %v, want 60 s", p)
	}
	keys("\x1b[D\x1b[D")
	if p := r.State().Pos; p != 40*time.Second {
		t.Errorf("two left arrows in one read: pos = %v, want 40 s", p)
	}
	keys("+")
	keys("=")
	if s := r.State().Speed; s != 4 {
		t.Errorf("+ twice: speed = %v", s)
	}
	keys("-")
	if s := r.State().Speed; s != 2 {
		t.Errorf("-: speed = %v", s)
	}
	keys(" ")
	clk.t = clk.t.Add(time.Minute)
	if st := r.State(); !st.Paused || st.Pos != 40*time.Second {
		t.Errorf("space: %+v, want paused at 40 s", st)
	}
	keys("b")
	if tg.view != 1 {
		t.Errorf("b: view = %d", tg.view)
	}
	if keys("x\x1b") {
		t.Error("an unknown key or lone escape quits")
	}
	if !keys("q") {
		t.Error("q does not quit")
	}
}

func TestReplayBarFitsWidth(t *testing.T) {
	st := replayState{Pos: 90 * time.Second, Total: 1800 * time.Second, Speed: 2, Status: "YouTube en-US · sync ✓ ±0.4s"}
	for _, w := range []int{120, 80, 50} {
		bar := replayBar(st, w, true)
		if bar.vis() > w {
			t.Errorf("width %d: bar is %d wide", w, bar.vis())
		}
	}
	var sb strings.Builder
	for _, s := range replayBar(st, 120, true) {
		sb.WriteString(s.text)
	}
	for _, want := range []string{"1:30 / 30:00", "2×", "sync ✓"} {
		if !strings.Contains(sb.String(), want) {
			t.Errorf("bar %q lacks %q", sb.String(), want)
		}
	}
}

// ---- event details and VODs ----------------------------------------------------

const replayEventJSON = `{"data":{"event":{"league":{"id":"L","slug":"worlds","name":"Worlds"},"match":{
 "strategy":{"count":5},
 "teams":[{"id":"T1","name":"MVK Esports","code":"MVK"},{"id":"T2","name":"FURIA","code":"FUR"}],
 "games":[
  {"number":1,"id":"G1","state":"completed","teams":[{"id":"T2","side":"blue"},{"id":"T1","side":"red"}],"vods":[
    {"parameter":"DE","locale":"de-DE","provider":"youtube","offset":0,"firstFrameTime":"2026-10-15T18:00:26.487Z","startMillis":18607000,"endMillis":21560000},
    {"parameter":"EN","locale":"en-US","provider":"youtube","offset":0,"firstFrameTime":null,"startMillis":1000,"endMillis":2954000},
    {"parameter":"TW","locale":"en-US","provider":"twitch","offset":0,"firstFrameTime":null,"startMillis":5000,"endMillis":2954000},
    {"parameter":"AF","locale":"ko-KR","provider":"afreecatv","offset":0,"firstFrameTime":null,"startMillis":1000,"endMillis":2954000}]},
  {"number":2,"id":"G2","state":"completed","teams":[],"vods":[]},
  {"number":4,"id":"G4","state":"unneeded","teams":[],"vods":[]}]}}}}`

func TestReplayEventAndVods(t *testing.T) {
	var er replayEventResp
	if err := json.Unmarshal([]byte(replayEventJSON), &er); err != nil {
		t.Fatal(err)
	}
	ev, g, err := replayEvent("M1", er, 1)
	if err != nil || g.ID != "G1" {
		t.Fatalf("game 1: %v %+v", err, g)
	}
	if ev.League.Slug != "worlds" || ev.Match.Teams[0].Code != "FUR" || ev.Match.Teams[1].Code != "MVK" {
		t.Errorf("teams must be blue first: %+v", ev.Match.Teams)
	}
	if len(ev.Match.Games) != 5 || ev.Match.Games[0].State != "completed" || ev.Match.Games[1].State != "unstarted" {
		t.Errorf("game counter: %+v", ev.Match.Games)
	}
	if _, _, err := replayEvent("M1", er, 3); err == nil {
		t.Error("a game number the match does not have must fail")
	}

	spawn := time.Date(2026, 10, 15, 23, 11, 17, 655e6, time.UTC)
	vods := replayVods(g, spawn)
	if len(vods) != 3 || !strings.HasSuffix(vods[0].URL, "EN") || !strings.HasSuffix(vods[1].URL, "DE") || !strings.HasSuffix(vods[2].URL, "TW") {
		t.Fatalf("order = %+v, want English YouTube, German YouTube, English Twitch (afreecatv skipped)", vods)
	}
	if vods[0].SpawnAt != 1+replayVodLag {
		t.Errorf("no first-frame time: estimate %v, want start + lag", vods[0].SpawnAt)
	}
	// the German first-frame time lies inside its segment: spawn − firstFrame = 18651.168 s
	if got := vods[1].SpawnAt; got < 18651 || got > 18652 {
		t.Errorf("first-frame based estimate = %v, want 18651.2", got)
	}
	// a first-frame time outside the segment is not trusted
	g.Vods[0].FirstFrameTime = "2026-10-16T18:00:00Z"
	if got := replayVods(g, spawn)[1].SpawnAt; got != 18607+replayVodLag {
		t.Errorf("first-frame time after the game: estimate %v, want start + lag", got)
	}
}

func TestFmtClock(t *testing.T) {
	for _, c := range []struct {
		s    float64
		want string
	}{{0, "0:00"}, {59.9, "0:59"}, {61, "1:01"}, {2125.4, "35:25"}, {-3, "0:00"}} {
		if got := fmtClock(c.s); got != c.want {
			t.Errorf("fmtClock(%v) = %q, want %q", c.s, got, c.want)
		}
	}
}

func TestReplayStoreBackfillIsPaced(t *testing.T) {
	sw := &synthWindow{frames: synthFrames()}
	st := newSynthStore(sw)
	st.pace = 100 * time.Millisecond
	st.setFocus(replayT0, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t0 := time.Now()
	go st.run(ctx)
	time.Sleep(450 * time.Millisecond)
	n, el := st.requestCount(), time.Since(t0)
	// the windows around the focus are urgent (focus..focus+replayNear); the rest go one per pace
	if budget := replayNear + 1 + int(el/st.pace) + 1; n > budget {
		t.Errorf("%d requests in %v, budget %d", n, el, budget)
	}
	if n < replayNear+1 {
		t.Errorf("only %d requests: the windows next to the focus were not fetched first", n)
	}
}

// replayFeed answers the gateway and the livestats feed with the synthetic game and counts
// the requests per kind.
type replayFeed struct {
	sw     *synthWindow
	mu     sync.Mutex
	window int // livestats window requests
	event  int // getEventDetails requests
}

func (f *replayFeed) RoundTrip(r *http.Request) (*http.Response, error) {
	body := ""
	switch {
	case strings.HasSuffix(r.URL.Path, "/getEventDetails"):
		f.mu.Lock()
		f.event++
		f.mu.Unlock()
		body = `{"data":{"event":{"league":{"id":"l","slug":"s","name":"L"},"match":{"strategy":{"count":1},` +
			`"teams":[{"id":"a","name":"A","code":"AA"},{"id":"b","name":"B","code":"BB"}],` +
			`"games":[{"number":1,"id":"g1","state":"completed","teams":[{"id":"a","side":"blue"},{"id":"b","side":"red"}]}]}}}}`
	case strings.Contains(r.URL.Path, "/window/g1"):
		f.mu.Lock()
		f.window++
		f.mu.Unlock()
		at := replayT0
		if q := r.URL.Query().Get("startingTime"); q != "" {
			ts, err := time.Parse("2006-01-02T15:04:05.000Z", q)
			if err != nil {
				return nil, err
			}
			at = ts
		}
		at = time.Unix(min(max(at.Unix(), replayT0.Unix()), replayT0.Unix()+200), 0).UTC() // past the end → final frames
		w, _ := f.sw.win("g1", startingTime(at))
		b, _ := json.Marshal(w)
		body = string(b)
	default:
		return nil, fmt.Errorf("unexpected request %s", (&url.URL{Path: r.URL.Path}).String())
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: r}, nil
}

func TestReplayReopenedGameCostsNoWindowRequest(t *testing.T) {
	feed := &replayFeed{sw: &synthWindow{frames: synthFrames()}}
	old, oldPace := httpClient.Transport, replayPace
	httpClient.Transport, replayPace = feed, 0
	replayCache.mu.Lock()
	replayCache.stores = nil
	replayCache.mu.Unlock()
	defer func() { httpClient.Transport, replayPace = old, oldPace }()

	open := func() *replay {
		r, err := newReplay(context.Background(), replayOpts{MatchID: "m", Game: 1, Poll: 20 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := open()
	waitFor(t, "every window", func() bool { return r.st.progress() == 1 })
	r.Close()
	feed.mu.Lock()
	games := feed.window
	feed.mu.Unlock()
	if want := int(r.st.last-r.st.first) + 1 + 2; games != want { // one per window + the two probes of the first open
		t.Errorf("first open made %d window requests, want %d", games, want)
	}
	for i := 0; i < 30; i++ {
		r := open()
		if p := r.st.progress(); p != 1 {
			t.Fatalf("reopened game has progress %v", p)
		}
		r.Close()
	}
	feed.mu.Lock()
	defer feed.mu.Unlock()
	if feed.window != games {
		t.Errorf("30 reopens made %d new window requests, want 0", feed.window-games)
	}
	if feed.event != 31 {
		t.Errorf("getEventDetails requests = %d, want 31", feed.event)
	}
}
