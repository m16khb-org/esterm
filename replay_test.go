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

// synthFramesAt moves the synthetic game so that its first frame is at t0.
func synthFramesAt(t0 time.Time) []frame {
	out := synthFrames()
	d := t0.Sub(replayT0)
	for i := range out {
		out[i].RFC460Timestamp = parseTime(out[i].RFC460Timestamp).Add(d).Format(time.RFC3339Nano)
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
	st.win = func(_ context.Context, gid, start string) (windowResp, error) { return sw.win(gid, start) }
	st.det = func(context.Context, string, string) (detailsResp, error) { return detailsResp{}, nil }
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

// A window request still in flight when the replay closes must not hold Close for the HTTP
// timeout, and the canceled window must stay retryable rather than be marked failed.
func TestReplayStoreRunReturnsWhenCanceledMidFetch(t *testing.T) {
	st := newReplayStore("g1", replayT0, replayT0.Add(200*time.Second), 20*time.Millisecond)
	st.pace = 0
	started := make(chan struct{}, 1)
	st.win = func(ctx context.Context, gid, start string) (windowResp, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done() // a slow server: only the request's cancellation ends the call
		return windowResp{}, ctx.Err()
	}
	st.det = func(context.Context, string, string) (detailsResp, error) { return detailsResp{}, nil }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { st.run(ctx); close(done) }()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run kept waiting on canceled window requests")
	}
	st.mu.Lock()
	failed := len(st.failed)
	st.mu.Unlock()
	if failed != 0 {
		t.Errorf("canceled windows recorded as failed: %d", failed)
	}
}

func TestReplayStoreSnapshot(t *testing.T) {
	sw := &synthWindow{frames: synthFrames()}
	st := newSynthStore(sw)
	for b := st.first; b <= st.last; b++ {
		w, _ := sw.win("g1", startingTime(bucketStart(b)))
		st.absorb(w, detailsResp{})
	}
	snap := st.snapshot(replayT0.Add(95*time.Second), 90*time.Second)
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
	early := st.snapshot(replayT0.Add(-time.Hour), 0)
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
	if s := r.Tick(clk.t); s.Game != r.total {
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

func TestReplayTickShowsPosition(t *testing.T) {
	r, clk := testReplay(t)
	clk.t = clk.t.Add(100 * time.Second)
	snap := r.Tick(clk.t)
	if snap.Game != 100*time.Second {
		t.Errorf("header clock = %v, want the position 100 s", snap.Game)
	}
	// 100 s of game = 123.5 s of feed (skew + the pause): 4 blue kills (every 30 s).
	if got := snap.W.Frames[0].BlueTeam.TotalKills; got != 4 {
		t.Errorf("kills on the board = %d, want 4", got)
	}
	if st := r.State(); st.Status != "" {
		t.Errorf("a loaded game still reports %q", st.Status)
	}
}

func TestReplayTickShowsExactLives(t *testing.T) {
	frames := synthFrames()
	for i := range frames { // blue 1 (level 3: a 12 s timer) dies at 45 s (the counter ticks first) and is back at 58 s of the feed
		sec := parseTime(frames[i].RFC460Timestamp).Sub(replayT0).Seconds()
		p := participant{ParticipantID: 1, Level: 3, CurrentHealth: 1500, MaxHealth: 2000}
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
	snap := r.Tick(clk.t)
	if l := snap.Lives[1]; !l.Dead || !l.Exact || math.Abs(l.Secs-4.5) > 0.01 {
		t.Errorf("%+v, want dead with exactly 4.5 s left", l)
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
	st := replayState{Pos: 90 * time.Second, Total: 1800 * time.Second, Speed: 2, Status: "loading game data 40%"}
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
	for _, want := range []string{"1:30 / 30:00", "2×", "40%"} {
		if !strings.Contains(sb.String(), want) {
			t.Errorf("bar %q lacks %q", sb.String(), want)
		}
	}
}

// ---- event details -------------------------------------------------------------

const replayEventJSON = `{"data":{"event":{"league":{"id":"L","slug":"worlds","name":"Worlds"},"match":{
 "strategy":{"count":5},
 "teams":[{"id":"T1","name":"MVK Esports","code":"MVK"},{"id":"T2","name":"FURIA","code":"FUR"}],
 "games":[
  {"number":1,"id":"G1","state":"completed","teams":[{"id":"T2","side":"blue"},{"id":"T1","side":"red"}]},
  {"number":2,"id":"G2","state":"completed","teams":[]},
  {"number":4,"id":"G4","state":"unneeded","teams":[]}]}}}}`

func TestReplayEvent(t *testing.T) {
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
}

// newReplay sets the length from the first and last windows alone, so a game whose pauses
// are revealed later (the store loads the rest in the background) played past its end: the
// bar, seeking and the end-of-game pause all used a length that was too long.
func TestReplayTotalShrinksByPausesLearnedAfterOpen(t *testing.T) {
	sw := &synthWindow{frames: synthFrames()}
	st := newSynthStore(sw)
	w0, _ := sw.win("g1", startingTime(replayT0))
	wEnd, _ := sw.win("g1", startingTime(bucketStart(bucketOf(replayT0.Add(200*time.Second)))))
	st.absorb(w0, detailsResp{})
	st.absorb(wEnd, detailsResp{})
	now := replayT0.Add(300 * time.Second)
	r := &replay{st: st, now: func() time.Time { return now }, speed: 1}
	r.total = r.gameOf(st.end.Sub(st.spawn))
	r.at = now
	if want := 196500 * time.Millisecond; r.total != want {
		t.Fatalf("length at open = %v, want %v before the pause is known", r.total, want)
	}

	for b := st.first; b <= st.last; b++ { // the background load reveals the 20 s pause
		w, _ := sw.win("g1", startingTime(bucketStart(b)))
		st.absorb(w, detailsResp{})
	}
	r.pos = r.total + 30*time.Second // a seek past the old end
	s := r.State()
	if want := 176500 * time.Millisecond; s.Total != want {
		t.Fatalf("bar length = %v, want the paused game length %v", s.Total, want)
	}
	if s.Pos != s.Total {
		t.Fatalf("position = %v, want it pulled back to the end %v", s.Pos, s.Total)
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
// the requests per kind. spawn/end default to the synthetic game; published, when set,
// decides whether the feed serves a window (the real feed rejects the newest ~20 s).
type replayFeed struct {
	sw        *synthWindow
	spawn     time.Time
	end       time.Time
	published func(time.Time) bool
	mu        sync.Mutex
	window    int // livestats window requests
	event     int // getEventDetails requests
	rejected  int // windows the feed refused as not published yet
}

func (f *replayFeed) RoundTrip(r *http.Request) (*http.Response, error) {
	spawn, end := f.spawn, f.end
	if spawn.IsZero() {
		spawn = replayT0
	}
	if end.IsZero() {
		end = spawn.Add(200 * time.Second)
	}
	body := ""
	code := 200
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
		at := spawn
		if q := r.URL.Query().Get("startingTime"); q != "" {
			ts, err := time.Parse("2006-01-02T15:04:05.000Z", q)
			if err != nil {
				return nil, err
			}
			at = ts
		}
		if f.published != nil && !f.published(at) {
			f.mu.Lock()
			f.rejected++
			f.mu.Unlock()
			code = 400
			body = `{"httpStatus":400,"errorCode":"BAD_QUERY_PARAMETER","message":"disallowed window ahead of broadcast"}`
			break
		}
		at = time.Unix(min(max(at.Unix(), spawn.Unix()), end.Unix()), 0).UTC() // past the end → final frames
		w, _ := f.sw.win("g1", startingTime(at))
		b, _ := json.Marshal(w)
		body = string(b)
	default:
		return nil, fmt.Errorf("unexpected request %s", (&url.URL{Path: r.URL.Path}).String())
	}
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: r}, nil
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

// A game that finished less than 6 hours ago used to fail: the end probe was spawn+6h,
// which is still ahead of the feed's broadcast and rejected as a future window.
func TestReplayOpensRecentlyFinishedGame(t *testing.T) {
	spawn := time.Now().UTC().Add(-2 * time.Hour).Truncate(feedProbeStep)
	end := spawn.Add(200 * time.Second)
	feed := &replayFeed{
		sw:    &synthWindow{frames: synthFramesAt(spawn)},
		spawn: spawn,
		end:   end,
		published: func(at time.Time) bool {
			return !at.Add(feedProbeStep).After(time.Now().Add(-20 * time.Second))
		},
	}
	old := httpClient.Transport
	httpClient.Transport = feed
	replayCache.mu.Lock()
	replayCache.stores = nil
	replayCache.mu.Unlock()
	defer func() { httpClient.Transport = old }()

	r, err := newReplay(context.Background(), replayOpts{MatchID: "m", Game: 1, Poll: 20 * time.Millisecond})
	if err != nil {
		t.Fatalf("a game that finished two hours ago must open: %v", err)
	}
	defer r.Close()
	if !r.st.end.Equal(end) {
		t.Errorf("end = %v, want %v", r.st.end, end)
	}
	feed.mu.Lock()
	rejected := feed.rejected
	feed.mu.Unlock()
	if rejected != 0 {
		t.Errorf("end probe asked for an unpublished window %d times", rejected)
	}
}

func TestReplayRefusesGameWithoutPublishedEnd(t *testing.T) {
	spawn := time.Now().UTC().Add(-2 * time.Hour).Truncate(feedProbeStep)
	frames := synthFramesAt(spawn)
	frames[len(frames)-1].GameState = "in_game"
	feed := &replayFeed{sw: &synthWindow{frames: frames}, spawn: spawn}
	old := httpClient.Transport
	httpClient.Transport = feed
	replayCache.mu.Lock()
	replayCache.stores = nil
	replayCache.mu.Unlock()
	defer func() { httpClient.Transport = old }()

	_, err := newReplay(context.Background(), replayOpts{MatchID: "m", Game: 1, Poll: 20 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "has no end in the feed yet") {
		t.Fatalf("a game whose last frame is still in_game must not open: %v", err)
	}
}
