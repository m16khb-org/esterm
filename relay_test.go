package main

import (
	"encoding/json"
	"image"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// offlineMap keeps the minimap background from starting a download during tests.
func offlineMap(t *testing.T) {
	t.Helper()
	mmBg.Lock()
	old := mmBg.img
	mmBg.img = image.NewRGBA(image.Rect(0, 0, 64, 64))
	mmBg.Unlock()
	t.Cleanup(func() {
		mmBg.Lock()
		mmBg.img, mmBg.cache = old, nil
		mmBg.Unlock()
	})
}

func TestCanvasFitsTerminal(t *testing.T) {
	offlineMap(t)
	now := time.Now()
	for _, mode := range []string{mmModeText, mmModeBlocks, mmModeKitty} {
		view := mmView{Mode: mode, Status: "tracking", CellW: 8, CellH: 16}
		for _, w := range []int{50, 80, 104, 200} {
			for h := 12; h <= 60; h++ {
				for flags := 0; flags < 16; flags++ {
					tg := toggles{view: flags & 3, spark: flags&4 != 0, omap: flags&8 != 0, mm: true}
					c := demoCanvasAt(w, h, now, 2*time.Second, detailsResp{}, tg, nil, view)
					if c.h > h {
						t.Fatalf("mode %s w=%d h=%d tg=%+v: canvas has %d rows", mode, w, h, tg, c.h)
					}
					if s := c.mm; s != nil && s.Row+s.Rows > c.h-2 {
						t.Fatalf("mode %s w=%d h=%d tg=%+v: kitty slot rows %d..%d overlap the footer of %d rows", mode, w, h, tg, s.Row, s.Row+s.Rows, c.h)
					}
				}
			}
		}
	}
}

func TestMinimapHintWhenNoRoom(t *testing.T) {
	offlineMap(t)
	view := mmView{Mode: mmModeText, Status: "x"}
	c := demoCanvasAt(104, 17, time.Now(), 2*time.Second, detailsResp{}, toggles{mm: true}, nil, view)
	if got := canvasString(c); !strings.Contains(got, "minimap: enlarge terminal") {
		t.Fatalf("expected the enlarge hint at h=17:\n%s", got)
	}
}

var escSeq = regexp.MustCompile(`^\x1b\[([0-9;]*)([A-Za-z])`)

// screen is a minimal terminal: cursor addressing, SGR accumulation, erase and printable runes.
type screen struct {
	cells    map[[2]int]cell
	r, c     int
	sty      string
	w, h     int
	clearAll bool
}

func (s *screen) feed(out string) {
	for len(out) > 0 {
		if m := escSeq.FindStringSubmatch(out); m != nil {
			out = out[len(m[0]):]
			switch m[2] {
			case "m":
				if m[1] == "0" {
					s.sty = ""
				} else {
					s.sty += m[0]
				}
			case "H":
				p := strings.Split(m[1], ";")
				s.r, s.c = 0, 0
				if len(p) == 2 {
					s.r, _ = strconv.Atoi(p[0])
					s.c, _ = strconv.Atoi(p[1])
					s.r, s.c = s.r-1, s.c-1
				}
			case "J":
				s.cells = map[[2]int]cell{}
			case "K":
				for c := s.c; c < s.w; c++ {
					delete(s.cells, [2]int{s.r, c})
				}
			}
			continue
		}
		r := []rune(out)[0]
		out = out[len(string(r)):]
		s.cells[[2]int{s.r, s.c}] = cell{ch: r, sty: s.sty}
		s.c++
	}
}

func (s *screen) matches(t *testing.T, c *canvas, what string) {
	t.Helper()
	for r := 0; r < c.h; r++ {
		for x := 0; x < c.w; x++ {
			if got, want := s.cells[[2]int{r, x}], c.buf[r][x]; got != want {
				t.Fatalf("%s: cell (%d,%d) = %q %q, want %q %q", what, r, x, got.ch, got.sty, want.ch, want.sty)
			}
		}
	}
}

// Styles accumulate in the terminal, so the diff renderer must reset between differently
// styled cells; a full repaint and a diff chain must show the same screen.
func TestRenderDiffNoStyleLeak(t *testing.T) {
	offlineMap(t)
	now := time.Now()
	view := mmView{Mode: mmModeText, Status: "x"}
	a := demoCanvasAt(104, 40, now, 2*time.Second, detailsResp{}, toggles{}, nil, view)
	b := demoCanvasAt(104, 40, now.Add(time.Second), 2*time.Second, detailsResp{}, toggles{view: 1, spark: true, omap: true, mm: true}, nil, view)

	full := &screen{cells: map[[2]int]cell{}, w: 104, h: 40}
	full.feed(renderDiff(nil, b))
	full.matches(t, b, "full repaint")

	chain := &screen{cells: map[[2]int]cell{}, w: 104, h: 40}
	chain.feed(renderDiff(nil, a))
	chain.feed(renderDiff(a, b))
	chain.matches(t, b, "diff after full repaint")
}

// syncPlayout builds a playout whose feed publishes frames up to feed time feedEdge
// (seconds since spawn) and whose minimap holds 2 fps frames starting at video feed time
// videoFrom up to videoLead past the newest feed frame. With locked, the HUD clock of
// every video frame was read, so the estimator knows the video-to-feed offset.
func syncPlayout(feedEdge, videoFrom, videoLead time.Duration, locked bool) *relayPlayout {
	ff := &fakeFeed{clock: t0.Add(feedEdge), gid: "g"}
	f := ff.newFeed(2 * time.Second)
	inner := f.window
	f.window = func(gid, start string) (windowResp, error) {
		w, err := inner(gid, start)
		_ = json.Unmarshal([]byte(`{"participantMetadata":[{"participantId":1}]}`), &w.GameMetadata.BlueTeamMetadata)
		return w, err
	}
	f.FetchOnce()
	lf, _ := f.Latest()
	videoEdge := lf.Sub(t0) + videoLead
	mm := newMinimap(minimapCfg{})
	mm.closed = true // Tick must not launch a real pipeline
	mm.cancel = func() {}
	mm.sy = newMMSync(t0, mmFPS, nil)
	now := time.Unix(5000, 0)
	for i := 0; videoFrom+mmFrameTime(i) <= videoEdge; i++ {
		mm.push(mmResult{idx: i, layout: "lck", visible: true, dets: det(1)}, now)
		if locked { // the HUD shows the feed clock minus the prior skew
			g := int((videoFrom + mmFrameTime(i) - feedPriorSkew).Seconds())
			mm.sy.ObserveHUD(i, mmHUD{ClockSec: g, ClockOK: true})
		}
	}
	mm.arrived = time.Now() // the stream is alive
	return &relayPlayout{fd: f, mm: mm}
}

func TestPlayoutFallsBackToIndependentModeWithoutSync(t *testing.T) {
	pl := syncPlayout(1190*time.Second, 1000*time.Second, -50*time.Second, false)
	s, v := pl.Tick(time.Unix(5000, 0), true)
	if strings.Contains(v.Status, "sync ✓") {
		t.Fatalf("claims sync without a lock: %q", v.Status)
	}
	if len(s.W.Frames) != 1 {
		t.Fatal("the scoreboard must keep playing in independent mode")
	}
}

func TestPlayoutVideoOffIsFeedOnly(t *testing.T) {
	pl := syncPlayout(1190*time.Second, 1000*time.Second, 0, true)
	s, v := pl.Tick(time.Unix(5000, 0), false)
	if len(s.W.Frames) != 1 || v.Status != "waiting for a live game" || pl.mm.Sync() != nil {
		t.Fatalf("frames %d status %q sync %v", len(s.W.Frames), v.Status, pl.mm.Sync())
	}
}

func TestPlayoutSharesTimeWhicheverSourceIsSlower(t *testing.T) {
	for _, tc := range []struct {
		name      string
		videoLead time.Duration // video's newest frame relative to the feed's
	}{
		{"feed behind video", 70 * time.Second},
		{"video behind feed", -3 * time.Second},
	} {
		pl := syncPlayout(1190*time.Second, 1000*time.Second, tc.videoLead, true)
		lf, _ := pl.fd.Latest()
		lv, _ := pl.mm.VideoLatest()
		vf, _ := pl.mm.Sync().FeedTime(lv)
		limit := lf
		if vf.Before(limit) {
			limit = vf
		}
		now := time.Unix(5000, 0)
		for i := 0; i < 30; i++ { // 15 s: the sources deliver nothing new, the clock must hold
			now = now.Add(500 * time.Millisecond)
			pl.mm.arrived = time.Now() // the stream is alive, it just has nothing newer
			s, v := pl.Tick(now, true)
			if len(s.W.Frames) != 1 || !strings.Contains(v.Status, "sync ✓") {
				t.Fatalf("%s tick %d: frames %d status %q", tc.name, i, len(s.W.Frames), v.Status)
			}
			T := t0.Add(s.Game + pl.mm.Sync().Skew())
			if T.After(limit.Add(time.Second)) {
				t.Fatalf("%s tick %d: %s is past the slower source %s", tc.name, i, T.Sub(t0), limit.Sub(t0))
			}
			if want := limit.Add(-syncMargin); i == 0 && (T.Before(want.Add(-time.Second)) || T.After(want.Add(time.Second))) {
				t.Fatalf("%s: started at %s, want about %s", tc.name, T.Sub(t0), want.Sub(t0))
			}
		}
	}
}

func TestPlayoutVideoStallShowsStatusAndKeepsClockRunning(t *testing.T) {
	pl := syncPlayout(1190*time.Second, 1000*time.Second, 70*time.Second, true)
	now := time.Unix(5000, 0)
	s0, v0 := pl.Tick(now, true)
	if !strings.Contains(v0.Status, "sync ✓") {
		t.Fatalf("not synced: %q", v0.Status)
	}
	pl.mm.arrived = time.Now().Add(-mmStallAfter - time.Second) // no frame for a while
	now = now.Add(time.Second)
	s1, v1 := pl.Tick(now, true)
	if !strings.HasPrefix(v1.Status, "video stalled") {
		t.Fatalf("status %q", v1.Status)
	}
	if len(s1.W.Frames) != 1 || s1.Game < s0.Game {
		t.Fatalf("scoreboard frames %d, header %s -> %s", len(s1.W.Frames), s0.Game, s1.Game)
	}
	pl.mm.arrived = time.Now() // frames return
	if _, v := pl.Tick(now.Add(500*time.Millisecond), true); !strings.Contains(v.Status, "sync ✓") {
		t.Fatalf("did not resume: %q", v.Status)
	}
}

func TestPlayoutHandoverDoesNotStepBack(t *testing.T) {
	pl := syncPlayout(1190*time.Second, 1000*time.Second, 70*time.Second, true)
	now := time.Unix(5000, 0)
	pl.fd.Snapshot(now) // the feed-only playout the viewer was watching
	at, _ := pl.fd.Position()
	pl.Tick(now, true)
	if pl.sc.t.Before(at) {
		t.Fatalf("clock started %s before the feed-only position", at.Sub(pl.sc.t))
	}
}

func TestPlayoutHeaderNeverStepsBackBySmallCorrections(t *testing.T) {
	var p relayPlayout
	if g := p.holdGame(100 * time.Second); g != 100*time.Second {
		t.Fatal(g)
	}
	if g := p.holdGame(99 * time.Second); g != 100*time.Second {
		t.Fatalf("stepped back to %s", g)
	}
	if g := p.holdGame(60 * time.Second); g != 60*time.Second {
		t.Fatalf("a real resync must pass: %s", g)
	}
	if g := p.holdGame(-1); g != -1 || p.game != 60*time.Second {
		t.Fatalf("unknown clock must not disturb the high-water mark")
	}
}

func TestPlayoutHandoverToBehindVideoHoldsInsteadOfStepBack(t *testing.T) {
	pl := syncPlayout(1190*time.Second, 1000*time.Second, -10*time.Second, true)
	pl.gid, pl.aligned = "g", true // the video has been followed before: this is a handover after a restart
	now := time.Unix(5000, 0)
	lf, _ := pl.fd.Latest()
	at := lf.Add(-4 * time.Second) // the feed-only playout is ahead of what the video can show
	pl.fd.mu.Lock()
	pl.fd.lastDisp = at
	pl.fd.mu.Unlock()
	shown := pl.fd.SnapshotAt(now, at)
	pl.holdGame(shown.Game)
	s, _ := pl.Tick(now.Add(500*time.Millisecond), true)
	if !pl.sc.on || pl.sc.t.Before(at) {
		t.Fatalf("clock on=%v stepped back by %s", pl.sc.on, at.Sub(pl.sc.t))
	}
	if s.Game < shown.Game {
		t.Fatalf("header stepped back from %s to %s", shown.Game, s.Game)
	}
}

func TestPlayoutPipelineRestartKeepsHeaderWithinGame(t *testing.T) {
	pl := syncPlayout(1190*time.Second, 1000*time.Second, 0, true)
	now := time.Unix(5000, 0)
	pl.Tick(now, true)
	high := pl.game
	pl.sy = nil // the pipeline restarted: a fresh estimator takes over
	pl.Tick(now.Add(500*time.Millisecond), true)
	if pl.game < high {
		t.Fatalf("header %s fell below %s after the restart", pl.game, high)
	}
	pl.fd.mu.Lock()
	pl.fd.gid = "next"
	pl.fd.mu.Unlock()
	pl.Tick(now.Add(time.Second), true)
	if pl.gid != "next" || pl.game > high {
		t.Fatalf("a new game must reset the header: gid %q game %s", pl.gid, pl.game)
	}
}

func TestPlayoutFirstHandoverAlignsToBehindVideo(t *testing.T) {
	pl := syncPlayout(1190*time.Second, 1000*time.Second, -10*time.Second, true)
	now := time.Unix(5000, 0)
	lf, _ := pl.fd.Latest()
	at := lf.Add(-4 * time.Second)
	pl.fd.mu.Lock()
	pl.fd.lastDisp = at
	pl.fd.mu.Unlock()
	pl.Tick(now, true)
	if !pl.sc.on || !pl.sc.t.Before(at) {
		t.Fatalf("the opening alignment must follow the video (on=%v t=%v)", pl.sc.on, pl.sc.t)
	}
}

func TestPlayoutOpeningAlignmentMovesHeaderWithScoreboard(t *testing.T) {
	pl := syncPlayout(1190*time.Second, 1000*time.Second, -6*time.Second, true)
	now := time.Unix(5000, 0)
	lf, _ := pl.fd.Latest()
	at := lf.Add(-4 * time.Second)
	pl.fd.mu.Lock()
	pl.fd.lastDisp = at
	pl.fd.mu.Unlock()
	shown := pl.fd.SnapshotAt(now, at)
	pl.gid = "g"
	pl.holdGame(shown.Game) // the header the viewer saw from the feed-only playout
	pl.Tick(now, true)
	if !pl.sc.t.Before(at) {
		t.Fatalf("clock did not step back (t=%v)", pl.sc.t)
	}
	if pl.game >= shown.Game {
		t.Fatalf("header held at %s although the scoreboard stepped back", pl.game)
	}
}

func TestPlayerRowShowsRespawnCountdownInTheHPColumn(t *testing.T) {
	p := participant{ParticipantID: 3, Level: 9, Kills: 1, Deaths: 2, TotalGold: 9000, CurrentHealth: 0, MaxHealth: 2800}
	m := pmeta{"Name", "Champ"}
	for _, budget := range []int{24, 30, 40, 48, 60, 100} {
		c := pickCols(budget)
		alive := playerRow(p, m, c, cCyan, nil)
		for _, tc := range []struct {
			name string
			lf   mmLife
			want string
		}{
			{"exact", mmLife{Dead: true, Secs: 22.3, Exact: true}, "☠ 23s"},
			{"estimate", mmLife{Dead: true, Secs: 22.3}, "☠ ~23s"},
			{"overdue", mmLife{Dead: true}, "☠ ~0s"},
			{"three digits", mmLife{Dead: true, Secs: 99.5}, "☠ ~100s"},
		} {
			row := playerRow(p, m, c, cCyan, mmLives{3: tc.lf})
			if row.vis() != alive.vis() {
				t.Errorf("budget %d %s: row is %d cells wide, the bar row %d", budget, tc.name, row.vis(), alive.vis())
			}
			if has := strings.Contains(appPlainLine(row), tc.want); has != (c.hpbar > 0) {
				t.Errorf("budget %d %s: %q in row = %v with hpbar %d: %q", budget, tc.name, tc.want, has, c.hpbar, appPlainLine(row))
			}
		}
		if row := playerRow(p, m, c, cCyan, mmLives{3: {AliveFor: 5}}); appPlainLine(row) != appPlainLine(alive) {
			t.Errorf("budget %d: a champion the feed does not call dead lost its bar: %q", budget, appPlainLine(row))
		}
	}
}

func TestBoardWidthDoesNotChangeWhileChampionsAreDead(t *testing.T) {
	now := time.Now()
	lives := mmLives{1: {Dead: true, Secs: 22.3, Exact: true}, 2: {Dead: true, Secs: 4}, 3: {Dead: true, Secs: 80.2}}
	for _, w := range []int{50, 60, 80, 104, 140, 200} {
		plain := appPlain(demoCanvasAt(w, 40, now, 2*time.Second, detailsResp{}, toggles{}, nil, mmView{}))
		dead := appPlain(demoCanvasAt(w, 40, now, 2*time.Second, detailsResp{}, toggles{}, nil, mmView{Lives: lives}))
		a, b := strings.Split(plain, "\n"), strings.Split(dead, "\n")
		if len(a) != len(b) {
			t.Fatalf("w=%d: %d rows with the countdowns, %d without", w, len(b), len(a))
		}
		for i := range a {
			if utf8.RuneCountInString(a[i]) != utf8.RuneCountInString(b[i]) {
				t.Fatalf("w=%d row %d changed width:\n%q\n%q", w, i, a[i], b[i])
			}
		}
		if !strings.Contains(dead, "☠ 23s") || !strings.Contains(dead, "☠ ~4s") {
			t.Errorf("w=%d: the countdowns are missing:\n%s", w, dead)
		}
	}
}

func TestPlayoutHandsTheSameLivesToBoardAndMinimap(t *testing.T) {
	for _, tc := range []struct {
		name  string
		video bool
	}{{"synced", true}, {"video off", false}} {
		pl := syncPlayout(1190*time.Second, 1000*time.Second, 0, true)
		pl.fd.mu.Lock()
		for i := range pl.fd.frames { // blue 1 is dead in every buffered frame, blue 2 is fine
			pl.fd.frames[i].v.BlueTeam.Participants = []participant{
				{ParticipantID: 1, Level: 6, Deaths: 1, MaxHealth: 2000},
				{ParticipantID: 2, Level: 6, CurrentHealth: 1500, MaxHealth: 2000},
			}
		}
		pl.fd.mu.Unlock()
		snap, view := pl.Tick(time.Unix(5000, 0), tc.video)
		for name, lives := range map[string]mmLives{"snapshot": snap.Lives, "view": view.Lives} {
			if l := lives[1]; !l.Dead || l.Exact {
				t.Errorf("%s %s: blue 1 = %+v, want dead with an estimated respawn", tc.name, name, l)
			}
			if l := lives[2]; l.Dead {
				t.Errorf("%s %s: blue 2 = %+v, want alive", tc.name, name, l)
			}
		}
	}
}
