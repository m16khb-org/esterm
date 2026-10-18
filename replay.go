package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Replay of a finished game: the livestats frames play back on a position clock the viewer
// controls (pause, ±10 s, 1×/2×/4×), and the official VOD's broadcast minimap follows the
// same position through the video↔feed sync estimator.
//
// The position is the game clock the header shows; the feed time of a position is
// spawn + position + skew + the game pauses before it (feedOf), the inverse of the
// live view's game clock.

const (
	replaySeekStep = 10 * time.Second
	// replayVodLag is the video seconds from a VOD segment's start to the game's first feed
	// frame: 337–364 s measured on the official broadcasts. The first seek assumes the
	// late end of that range: landing in the game, where the HUD clock reads, locks the sync
	// within seconds; landing in the pre-game does not until the game starts.
	replayVodLag = 370.0
)

var replaySpeeds = []float64{1, 2, 4}

type replayOpts struct {
	MatchID string        // lolesports match id
	Game    int           // 1-based game number in the match
	Src     string        // optional video override (page URL or file); "" = official VOD from getEventDetails
	Poll    time.Duration // how long a failed window waits before it is fetched again
	Block   string        // optional header block name (e.g. "Play-Ins"); "" shows "Replay"
}

// replayState is what the player bar shows.
type replayState struct {
	Pos, Total time.Duration // game clock position and length
	Paused     bool
	Speed      float64
	HasVideo   bool      // a VOD (or -src) is known for this game
	Status     string    // loading progress, VOD and sync state, or why there is no video
	Start      time.Time // when the game started (first feed frame)
}

// replayVod is one candidate video of the game.
type replayVod struct {
	URL     string
	Label   string  // e.g. "YouTube en-US"
	SpawnAt float64 // estimate of the video second showing the game's first feed frame
}

type replay struct {
	o      replayOpts
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	now    func() time.Time

	ev   event
	st   *replayStore
	sy   *mmSync
	mm   *minimap
	vid  *replayVideo
	vods []replayVod

	mu      sync.Mutex
	pos     time.Duration // game clock position at `at`
	at      time.Time
	paused  bool
	speed   float64
	total   time.Duration
	haveO   bool    // the sync locked once: oLock maps feed time to video time from then on
	oLock   float64 // video-to-feed offset (seconds) of the last lock
	details bool
}

// ---- event details -------------------------------------------------------------

type replayEventResp struct {
	Data struct {
		Event struct {
			League struct {
				ID   string `json:"id"`
				Slug string `json:"slug"`
				Name string `json:"name"`
			} `json:"league"`
			Match struct {
				Strategy struct {
					Count int `json:"count"`
				} `json:"strategy"`
				Teams []struct {
					ID     string `json:"id"`
					Name   string `json:"name"`
					Code   string `json:"code"`
					Result *struct {
						GameWins int `json:"gameWins"`
					} `json:"result"`
				} `json:"teams"`
				Games []replayGameJSON `json:"games"`
			} `json:"match"`
		} `json:"event"`
	} `json:"data"`
}

type replayGameJSON struct {
	Number int    `json:"number"`
	ID     string `json:"id"`
	State  string `json:"state"`
	Teams  []struct {
		ID   string `json:"id"`
		Side string `json:"side"`
	} `json:"teams"`
	Vods []struct {
		Parameter      string `json:"parameter"`
		Locale         string `json:"locale"`
		Provider       string `json:"provider"`
		Offset         int    `json:"offset"`
		FirstFrameTime string `json:"firstFrameTime"`
		StartMillis    int64  `json:"startMillis"`
		EndMillis      int64  `json:"endMillis"`
	} `json:"vods"`
}

// replayEvent builds the header's event: the teams blue first, the game counter at game n.
func replayEvent(matchID string, er replayEventResp, n int) (event, replayGameJSON, error) {
	m := er.Data.Event.Match
	if len(m.Games) == 0 {
		return event{}, replayGameJSON{}, fmt.Errorf("match %s not found", matchID)
	}
	var g replayGameJSON
	for _, c := range m.Games {
		if c.Number == n {
			g = c
		}
	}
	if g.ID == "" {
		return event{}, g, fmt.Errorf("match %s has no game %d (%d games)", matchID, n, len(m.Games))
	}
	ev := event{ID: matchID, State: "completed", Type: "match", BlockName: "Replay"}
	ev.League.ID, ev.League.Slug, ev.League.Name = er.Data.Event.League.ID, er.Data.Event.League.Slug, er.Data.Event.League.Name
	side := map[string]string{}
	for _, t := range g.Teams {
		side[t.ID] = t.Side
	}
	teams := make([]teamResult, 0, len(m.Teams))
	// getEventDetails carries only the final series score: it is the score after the
	// match's last played game, unknown (nil) while an earlier game is replayed.
	last := 0
	for _, c := range m.Games {
		if c.State == "completed" && c.Number > last {
			last = c.Number
		}
	}
	for _, t := range m.Teams {
		tr := teamResult{ID: t.ID, Name: t.Name, Code: t.Code}
		if n == last && t.Result != nil {
			tr.Result = &struct {
				Outcome  *string `json:"outcome"`
				GameWins int     `json:"gameWins"`
			}{GameWins: t.Result.GameWins}
		}
		if side[t.ID] == "red" {
			teams = append(teams, tr)
		} else {
			teams = append([]teamResult{tr}, teams...)
		}
	}
	games := max(m.Strategy.Count, len(m.Games))
	ev.Match = &match{ID: matchID, Teams: teams}
	for i := 1; i <= games; i++ {
		state := "unstarted"
		if i <= n {
			state = "completed"
		}
		ev.Match.Games = append(ev.Match.Games, game{ID: fmt.Sprintf("%s-%d", matchID, i), State: state})
	}
	return ev, g, nil
}

// replayVods lists the game's videos best first: YouTube before Twitch, English before
// other languages. The first seek is estimated from the VOD's own timing data.
func replayVods(g replayGameJSON, spawn time.Time) []replayVod {
	type ranked struct {
		v    replayVod
		rank int
	}
	var rs []ranked
	for _, v := range g.Vods {
		var url string
		rank := 0
		name := ""
		switch v.Provider {
		case "youtube":
			url, name = "https://www.youtube.com/watch?v="+v.Parameter, "YouTube"
		case "twitch":
			url, name, rank = "https://www.twitch.tv/videos/"+v.Parameter, "Twitch", 2
		default:
			continue
		}
		if !strings.HasPrefix(v.Locale, "en") {
			rank++
		}
		// The segment's start plus the measured lag; where the VOD gives a first-frame time
		// that lands inside its own segment, that is far more exact (LCK: 0.6 s) — a
		// first-frame time outside the segment (LEC) belongs to another upload.
		start := float64(v.StartMillis) / 1000
		at := start + replayVodLag
		if ff := parseTime(v.FirstFrameTime); !ff.IsZero() {
			if a := spawn.Sub(ff).Seconds() + float64(v.Offset); a >= start && a <= float64(v.EndMillis)/1000 {
				at = a
			}
		}
		label := name + " " + v.Locale
		rs = append(rs, ranked{replayVod{URL: url, Label: label, SpawnAt: at}, rank})
	}
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].rank < rs[j].rank })
	out := make([]replayVod, len(rs))
	for i, r := range rs {
		out[i] = r.v
	}
	return out
}

// ---- construction --------------------------------------------------------------

// newReplay resolves the game, reads its start and end from the feed and fetches the first
// windows; the rest of the game loads in the background. It fails when the game has no
// livestats data.
func newReplay(ctx context.Context, o replayOpts) (*replay, error) {
	if o.Game < 1 {
		o.Game = 1
	}
	if o.Poll <= 0 {
		o.Poll = 3 * time.Second
	}
	var er replayEventResp
	if err := getJSONCtx(ctx, gwBase+"/getEventDetails?hl=en-US&id="+o.MatchID, &er); err != nil {
		return nil, err
	}
	ev, g, err := replayEvent(o.MatchID, er, o.Game)
	if err != nil {
		return nil, err
	}
	if o.Block != "" {
		ev.BlockName = o.Block
	}
	// A game opened before keeps its fetched windows: no request but getEventDetails.
	st := cachedReplayStore(g.ID)
	if st == nil {
		var w0, wEnd windowResp
		if err := getJSONCtx(ctx, feedBase+"/window/"+g.ID, &w0); err != nil {
			return nil, err
		}
		if len(w0.Frames) == 0 {
			return nil, fmt.Errorf("game %d of match %s has no livestats data", o.Game, o.MatchID)
		}
		spawn := parseTime(w0.Frames[0].RFC460Timestamp)
		// A startingTime past the end returns the final frames.
		if err := getJSONCtx(ctx, feedBase+"/window/"+g.ID+"?startingTime="+startingTime(spawn.Add(6*time.Hour)), &wEnd); err != nil {
			return nil, err
		}
		end := spawn
		if n := len(wEnd.Frames); n > 0 {
			end = parseTime(wEnd.Frames[n-1].RFC460Timestamp)
		}
		if !end.After(spawn) {
			return nil, fmt.Errorf("game %d of match %s has no end in the feed yet", o.Game, o.MatchID)
		}
		st = newReplayStore(g.ID, spawn, end, o.Poll)
		st.absorb(w0, detailsResp{})
		st.absorb(wEnd, detailsResp{})
		keepReplayStore(st)
	}
	spawn, end := st.spawn, st.end

	rctx, cancel := context.WithCancel(ctx)
	r := &replay{o: o, ctx: rctx, cancel: cancel, now: time.Now, ev: ev, speed: 1}
	r.st = st
	r.sy = newMMSync(spawn, mmFPS, r.st.pausesList)
	r.mm = newMinimap(minimapCfg{Src: o.Src})
	r.total = r.gameOf(end.Sub(spawn))
	r.at = r.now()

	switch {
	case o.Src != "":
		at := 0.0 // a source that is not one of the game's VODs is assumed to start with the game
		for _, v := range replayVods(g, spawn) {
			if id := v.URL[strings.LastIndexAny(v.URL, "=/")+1:]; id != "" && strings.Contains(o.Src, id) {
				at = v.SpawnAt
			}
		}
		r.vods = []replayVod{{URL: o.Src, Label: "source", SpawnAt: at}}
	default:
		r.vods = replayVods(g, spawn)
	}

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.st.run(rctx)
	}()
	r.st.setFocus(spawn.Add(r.feedOf(0)), false)
	if len(r.vods) > 0 {
		r.vid = newReplayVideo(r)
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			r.vid.run()
		}()
	}
	return r, nil
}

// Close stops the background work and the video processes, and waits for them.
func (r *replay) Close() {
	r.cancel()
	r.wg.Wait()
}

// Event returns the match for the header: teams blue first, league, game counter.
func (r *replay) Event() event { return r.ev }

// ---- game clock ----------------------------------------------------------------

// feedOf returns the feed time (since spawn) at which the game clock shows g.
func (r *replay) feedOf(g time.Duration) time.Duration {
	skew, shift := r.sy.Skew(), time.Duration(0)
	for _, p := range r.st.pausesList() {
		st := p.Start.Sub(r.st.spawn)
		if g+skew+shift <= st {
			break
		}
		shift += p.Dur
	}
	return g + skew + shift
}

// gameOf returns the game clock at feed time x since spawn, never negative.
func (r *replay) gameOf(x time.Duration) time.Duration {
	return max(0, x-r.sy.Skew()-pauseShift(r.st.pausesList(), r.st.spawn.Add(x)))
}

// posAt is the position at now. Callers hold r.mu.
func (r *replay) posAt(now time.Time) time.Duration {
	p := r.pos
	if !r.paused {
		p += time.Duration(float64(now.Sub(r.at)) * r.speed)
	}
	return min(max(p, 0), r.total)
}

// commit moves the clock's reference point to now; the end of the game pauses it.
// Callers hold r.mu.
func (r *replay) commit(now time.Time) {
	r.pos, r.at = r.posAt(now), now
	if r.pos >= r.total {
		r.paused = true
	}
}

// TogglePause pauses or resumes; resuming at the end starts over.
func (r *replay) TogglePause() {
	now := r.now()
	r.mu.Lock()
	r.commit(now)
	if r.paused && r.pos >= r.total {
		r.pos = 0
	}
	r.paused = !r.paused
	r.mu.Unlock()
	r.pushTarget(now, false)
}

// Seek moves the position by d, clamped to the game.
func (r *replay) Seek(d time.Duration) {
	now := r.now()
	r.mu.Lock()
	r.commit(now)
	r.pos = min(max(r.pos+d, 0), r.total)
	r.mu.Unlock()
	r.pushTarget(now, true)
}

// CycleSpeed steps 1× → 2× → 4× for dir > 0 and back for dir < 0; the ends stay.
func (r *replay) CycleSpeed(dir int) {
	now := r.now()
	r.mu.Lock()
	r.commit(now)
	i := 0
	for k, s := range replaySpeeds {
		if s == r.speed {
			i = k
		}
	}
	if dir > 0 {
		i = min(i+1, len(replaySpeeds)-1)
	} else if dir < 0 {
		i = max(i-1, 0)
	}
	r.speed = replaySpeeds[i]
	r.mu.Unlock()
	r.pushTarget(now, false)
}

// SetDetailsView tells the replay whether the board on screen shows per-player details,
// which are fetched around the position only then.
func (r *replay) SetDetailsView(on bool) {
	r.mu.Lock()
	r.details = on
	r.mu.Unlock()
}

// State returns the player bar's data.
func (r *replay) State() replayState {
	r.mu.Lock()
	now := r.now()
	s := replayState{Pos: r.posAt(now), Total: r.total, Paused: r.paused, Speed: r.speed, HasVideo: len(r.vods) > 0, Start: r.st.spawn}
	r.mu.Unlock()
	s.Status = r.status()
	return s
}

func (r *replay) status() string {
	if p := r.st.progress(); p < 1 {
		return fmt.Sprintf("loading game data %d%%", int(p*100))
	}
	switch {
	case r.vid == nil:
		return "no official VOD for this game · scoreboard only"
	case !r.vid.enabled():
		return "video off"
	}
	return r.vid.label() + " · " + r.sy.Status()
}

// ---- video position ------------------------------------------------------------

// videoOf maps feed time x (seconds since spawn) to video seconds. known is false until the
// sync has locked once: before that the mapping is the VOD's estimate.
func (r *replay) videoOf(x float64) (vt float64, known bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.haveO {
		return x - r.oLock, true
	}
	return x + r.vid.spawnAt(), false
}

// forgetOffset drops the learned offset: another VOD has its own timeline.
func (r *replay) forgetOffset() {
	r.mu.Lock()
	r.haveO = false
	r.mu.Unlock()
}

// offsetKnown reports whether the sync has locked once.
func (r *replay) offsetKnown() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.haveO
}

// learnOffset keeps the sync's offset once it is trustworthy: it maps the whole game, also
// after a seek made the estimator forget its samples.
func (r *replay) learnOffset() {
	if !r.sy.Locked() {
		return
	}
	if o, _, ok := r.sy.Offset(); ok {
		r.mu.Lock()
		r.haveO, r.oLock = true, o
		r.mu.Unlock()
	}
}

// pushTarget hands the video the position it should show; jump marks a seek.
func (r *replay) pushTarget(now time.Time, jump bool) {
	if r.vid == nil {
		return
	}
	r.mu.Lock()
	p, rate := r.posAt(now), r.speed
	if r.paused {
		rate = 0
	}
	r.mu.Unlock()
	vt, _ := r.videoOf(r.feedOf(p).Seconds())
	r.vid.setTarget(vt, rate, now, jump)
}

// Tick returns the board state and minimap view for now; mode and cell size of the view are
// the caller's to fill in. video and panel say whether the broadcast video runs and its
// panel is on screen.
func (r *replay) Tick(now time.Time, video, panel bool) (feedSnap, mmView) {
	r.mu.Lock()
	r.commit(now)
	p, details := r.pos, r.details
	r.mu.Unlock()
	x := r.feedOf(p)
	T := r.st.spawn.Add(x)
	r.st.setFocus(T, details)
	snap := r.st.snapshot(T, p, r.sy.Skew())
	snap.Ev = r.ev

	var view mmView
	switch {
	case r.vid == nil:
		view.Status = "no official VOD for this game · scoreboard only"
	case !video:
		r.vid.setEnabled(false)
		view.Status = "video off"
	default:
		r.sy.ObserveFeedKills(r.st.killsList())
		r.sy.ObserveFeedLatest(r.st.edge())
		r.learnOffset()
		r.vid.setEnabled(true)
		r.vid.setPanel(panel)
		r.pushTarget(now, false)
		vt, known := r.videoOf(x.Seconds())
		view = r.vid.view(vt, known, snap.Lives)
	}
	view.Lives = snap.Lives
	view.Obj = snap.Obj
	return snap, view
}

// ---- command -------------------------------------------------------------------

func cmdReplay(args []string) {
	fs := flag.NewFlagSet("lol replay", flag.ExitOnError)
	matchID := fs.String("match", "", "lolesports match id (without it: recent completed matches are listed)")
	gameNo := fs.Int("g", 1, "game number in the match")
	src := fs.String("src", "", "video source: page URL or file (default: the official VOD)")
	icons := fs.String("icons", mmModeAuto, "minimap drawing: auto|kitty|blocks|text")
	league := fs.String("l", "", "league slug for the match list")
	fs.Parse(args)

	if *matchID == "" {
		listRecentMatches(*league)
		return
	}
	mode, err := mmResolveMode(*icons)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rp, err := newReplay(ctx, replayOpts{MatchID: *matchID, Game: *gameNo, Src: *src})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	defer rp.Close()
	runReplayScreen(rp, mode)
}

// listRecentMatches prints recent completed matches with the ids replay takes.
func listRecentMatches(slug string) {
	lid, ok := leagueID(slug)
	if !ok {
		fmt.Fprintln(os.Stderr, "unknown league:", slug)
		return
	}
	url := gwBase + "/getSchedule?hl=en-US"
	if lid != "" {
		url += "&leagueId=" + lid
	}
	var sr scheduleResp
	if err := getJSON(url, &sr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return
	}
	var done []event
	for _, ev := range sr.Data.Schedule.Events {
		if ev.Type == "match" && ev.State == "completed" && ev.Match != nil {
			done = append(done, ev)
		}
	}
	sort.SliceStable(done, func(i, j int) bool { return parseTime(done[i].StartTime).After(parseTime(done[j].StartTime)) })
	if len(done) == 0 {
		fmt.Println("  (no completed matches — try -l <league>)")
		return
	}
	fmt.Printf("  %-20s %-5s %-6s %s\n", "MATCH ID", "DATE", "LEAGUE", "MATCH")
	for _, ev := range done[:min(len(done), 15)] {
		l, rt := "TBD", "TBD"
		if len(ev.Match.Teams) >= 2 {
			l, rt = codeOr(ev.Match.Teams[0].Code, "TBD"), codeOr(ev.Match.Teams[1].Code, "TBD")
		}
		fmt.Printf("  %-20s %-5s %-6s %s\n", ev.Match.ID, parseTime(ev.StartTime).In(kst).Format("01-02"),
			strings.ToUpper(ev.League.Slug), l+" vs "+rt)
	}
	fmt.Println("\n  esterm lol replay -match <MATCH ID> [-g N]")
}

// runReplayScreen shows the replay fullscreen until q or ctrl+c.
func runReplayScreen(rp *replay, mode string) {
	var kit kittyOut
	keys, leave := enterFullscreen(func() { rp.Close(); fmt.Print(kit.Delete()) })
	defer leave()
	defer func() { fmt.Print(kit.Delete()) }()

	t, h := termSize()
	tg := toggles{video: true, mm: true}
	var prev *canvas
	draw := func() {
		now := time.Now()
		rp.SetDetailsView(tg.view != 0)
		st := rp.State()
		tg.replay = &st
		s, view := rp.Tick(now, tg.video, tg.mm)
		view.Mode = mode
		view.CellW, view.CellH = termCellPx()
		var c *canvas
		if t < minBoardW || h < minTermH {
			c = tooSmallCanvas(t, h, minBoardW, minTermH)
		} else {
			c = buildCanvas(rp.Event(), s.W, s.Det, s.Game, now, t, h-1, 2*time.Second, tg, s.Series, view)
			c = withPlayerBar(c, replayBar(st, t, true))
		}
		fmt.Print(renderDiff(prev, c) + kit.Frame(c.mm, prev == nil || prev.w != c.w || prev.h != c.h))
		prev = c
	}
	cadence := mmTickFor(tg.video && tg.mm)
	tick := time.NewTicker(cadence)
	defer tick.Stop()
	draw()
	for {
		select {
		case <-tick.C:
			if nt, nh := termSize(); nt != t || nh != h {
				t, h = nt, nh
				prev = nil
			}
			draw()
		case k := <-keys:
			if replayKey(rp, &tg, k) {
				return
			}
			prev = nil
			draw()
		}
		if d := mmTickFor(tg.video && tg.mm); d != cadence {
			cadence = d
			tick.Reset(d)
		}
	}
}

// replayKey applies one key and reports whether to quit: the playback keys (space,
// ←/→, +/-) first, then the board toggles.
func replayKey(rp *replay, tg *toggles, k key) (quit bool) {
	switch {
	case k.Kind == keyRight:
		rp.Seek(replaySeekStep)
	case k.Kind == keyLeft:
		rp.Seek(-replaySeekStep)
	case k.Kind == keySpace:
		rp.TogglePause()
	case k.Kind == keyRune && (k.Rune == '+' || k.Rune == '='):
		rp.CycleSpeed(1)
	case k.Kind == keyRune && (k.Rune == '-' || k.Rune == '_'):
		rp.CycleSpeed(-1)
	case k.Kind == keyCtrlC || (k.Kind == keyRune && (k.Rune == 'q' || k.Rune == 'Q')):
		return true
	default:
		boardKey(tg, k, true)
	}
	return false
}

// fmtClock formats seconds as m:ss.
func fmtClock(sec float64) string {
	s := int(sec)
	if s < 0 {
		s = 0
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// replayBar renders the player bar: play state, position / length with a progress bar,
// speed, status and (keys) the key hints; the app lists the keys in its own hint bar.
func replayBar(st replayState, w int, keys bool) line {
	icon, col := "▶", cGreen
	if st.Paused {
		icon, col = "⏸", cYel
	}
	pos, total := st.Pos.Seconds(), st.Total.Seconds()
	head := line{C(" "+icon+" ", col+cBold), S(fmt.Sprintf("%s / %s  ", fmtClock(pos), fmtClock(total))), C(fmt.Sprintf("%g×", st.Speed), cBold)}
	var tail line
	if keys {
		tail = line{C("  space ⏯  ←/→ ±10s  +/- speed  q quit", cDim)}
	}
	status := line{C("  "+st.Status, cGray)}
	barW := w - head.vis() - tail.vis() - status.vis() - 4
	if barW < 8 {
		status = nil
		barW = w - head.vis() - tail.vis() - 4
	}
	if barW < 8 {
		return head
	}
	filled := 0
	if total > 0 {
		filled = int(float64(barW) * pos / total)
	}
	bar := line{S(" ["), C(strings.Repeat("█", filled), col), C(strings.Repeat("░", barW-filled), cDim), S("]")}
	out := append(line{}, head...)
	out = append(out, bar...)
	out = append(out, status...)
	return append(out, tail...)
}

// withPlayerBar returns c with one more row, the player bar, below it.
func withPlayerBar(c *canvas, bar line) *canvas {
	out := newCanvas(c.w, c.h+1)
	copy(out.buf, c.buf)
	out.mm = c.mm
	out.drawLine(c.h, bar)
	return out
}
