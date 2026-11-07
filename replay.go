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
// controls (pause, ±10 s, 1×/2×/4×).
//
// The position is the game clock the header shows; the feed time of a position is
// spawn + position + skew + the game pauses before it (feedOf), the inverse of the
// live view's game clock.

const (
	replaySeekStep = 10 * time.Second
	// replayFeedLag is how far behind the wall clock the livestats feed publishes: a window
	// whose end is less than ~20 s old is rejected, and a window spans 10 s. Asking for the
	// newest published window finds the final frames of a game that just finished; a fixed
	// offset from spawn (the old +6 h) can still be ahead of the broadcast.
	replayFeedLag = 40 * time.Second
)

var replaySpeeds = []float64{1, 2, 4}

type replayOpts struct {
	MatchID string        // lolesports match id
	Game    int           // 1-based game number in the match
	Poll    time.Duration // how long a failed window waits before it is fetched again
	Block   string        // optional header block name (e.g. "Play-Ins"); "" shows "Replay"
}

// replayState is what the player bar shows.
type replayState struct {
	Pos, Total time.Duration // game clock position and length
	Paused     bool
	Speed      float64
	Status     string    // loading progress while the game's windows load
	Start      time.Time // when the game started (first feed frame)
}

type replay struct {
	cancel context.CancelFunc
	wg     sync.WaitGroup
	now    func() time.Time

	ev event
	st *replayStore

	mu      sync.Mutex
	pos     time.Duration // game clock position at `at`
	at      time.Time
	paused  bool
	speed   float64
	total   time.Duration
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
	ev := event{ID: matchID, State: "completed", Type: "match", BlockName: tr("replay")}
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
		// A startingTime past the end returns the final frames. Ask for the newest window
		// the feed has published, not a fixed offset from spawn: a game that just finished
		// is still ahead of a +6 h probe, and the feed rejects such a future window.
		endAt := time.Now().Add(-replayFeedLag)
		if !endAt.After(spawn) {
			return nil, fmt.Errorf("game %d of match %s has no end in the feed yet", o.Game, o.MatchID)
		}
		if err := getJSONCtx(ctx, feedBase+"/window/"+g.ID+"?startingTime="+startingTime(endAt), &wEnd); err != nil {
			return nil, err
		}
		end := spawn
		if n := len(wEnd.Frames); n > 0 {
			end = parseTime(wEnd.Frames[n-1].RFC460Timestamp)
		}
		if !end.After(spawn) {
			return nil, fmt.Errorf("game %d of match %s has no end in the feed yet", o.Game, o.MatchID)
		}
		if st := wEnd.Frames[len(wEnd.Frames)-1].GameState; st != "finished" {
			return nil, fmt.Errorf("game %d of match %s has no end in the feed yet", o.Game, o.MatchID)
		}
		st = newReplayStore(g.ID, spawn, end, o.Poll)
		st.absorb(w0, detailsResp{})
		st.absorb(wEnd, detailsResp{})
		keepReplayStore(st)
	}
	spawn, end := st.spawn, st.end

	rctx, cancel := context.WithCancel(ctx)
	r := &replay{cancel: cancel, now: time.Now, ev: ev, st: st, speed: 1}
	r.total = r.gameOf(end.Sub(spawn))
	r.at = r.now()

	r.wg.Add(1)
	go func() {
		defer crashGuard()
		defer r.wg.Done()
		r.st.run(rctx)
	}()
	r.st.setFocus(spawn.Add(r.feedOf(0)), false)
	return r, nil
}

// Close stops the background loading and waits for it.
func (r *replay) Close() {
	r.cancel()
	r.wg.Wait()
}

// Event returns the match for the header: teams blue first, league, game counter.
func (r *replay) Event() event { return r.ev }

// ---- game clock ----------------------------------------------------------------

// feedOf returns the feed time (since spawn) at which the game clock shows g.
func (r *replay) feedOf(g time.Duration) time.Duration {
	shift := time.Duration(0)
	for _, p := range r.st.pausesList() {
		st := p.Start.Sub(r.st.spawn)
		if g+feedSkew+shift <= st {
			break
		}
		shift += p.Dur
	}
	return g + feedSkew + shift
}

// gameOf returns the game clock at feed time x since spawn, never negative.
func (r *replay) gameOf(x time.Duration) time.Duration {
	return max(0, x-feedSkew-pauseShift(r.st.pausesList(), r.st.spawn.Add(x)))
}

// refreshTotal recomputes the game length from the store: newReplay sets it from the first
// and last windows, before the pauses the rest of the game reveals are known, so a game
// with late pauses would otherwise play past its end. A position past the new end is
// pulled back to it.
func (r *replay) refreshTotal() {
	t := r.gameOf(r.st.end.Sub(r.st.spawn))
	r.mu.Lock()
	if t > 0 {
		r.total = t
	}
	if r.pos > r.total {
		r.pos = r.total
	}
	r.mu.Unlock()
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
}

// Seek moves the position by d, clamped to the game.
func (r *replay) Seek(d time.Duration) {
	now := r.now()
	r.mu.Lock()
	r.commit(now)
	r.pos = min(max(r.pos+d, 0), r.total)
	r.mu.Unlock()
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
	r.refreshTotal()
	r.mu.Lock()
	now := r.now()
	s := replayState{Pos: r.posAt(now), Total: r.total, Paused: r.paused, Speed: r.speed, Start: r.st.spawn}
	r.mu.Unlock()
	if p := r.st.progress(); p < 1 {
		s.Status = fmt.Sprintf(tr("loading_game_data"), int(p*100))
	}
	return s
}

// Tick returns the board state for now.
func (r *replay) Tick(now time.Time) feedSnap {
	r.refreshTotal()
	r.mu.Lock()
	r.commit(now)
	p, details := r.pos, r.details
	r.mu.Unlock()
	T := r.st.spawn.Add(r.feedOf(p))
	r.st.setFocus(T, details)
	snap := r.st.snapshot(T, p)
	snap.Ev = r.ev
	return snap
}

// ---- command -------------------------------------------------------------------

func cmdReplay(args []string) {
	fs := flag.NewFlagSet("lol replay", flag.ExitOnError)
	matchID := fs.String("match", "", "lolesports match id (without it: recent completed matches are listed)")
	gameNo := fs.Int("g", 1, "game number in the match")
	league := fs.String("l", "", "league slug for the match list")
	fs.Parse(args)

	if *matchID == "" {
		listRecentMatches(*league)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rp, err := newReplay(ctx, replayOpts{MatchID: *matchID, Game: *gameNo})
	if err != nil {
		cancel() // os.Exit skips the deferred cancel
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	defer rp.Close()
	runReplayScreen(rp)
}

// listRecentMatches prints recent completed matches with the ids replay takes.
func listRecentMatches(slug string) {
	lid, err := leagueID(slug)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	url := gwBase + "/getSchedule?hl=en-US"
	if lid != "" {
		url += "&leagueId=" + lid
	}
	var sr scheduleResp
	if err := getJSON(url, &sr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
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
	fmt.Printf("  %-20s %-10s %-6s %s\n", "MATCH ID", "DATE (KST)", "LEAGUE", "MATCH")
	for _, ev := range done[:min(len(done), 15)] {
		l, rt := "TBD", "TBD"
		if len(ev.Match.Teams) >= 2 {
			l, rt = codeOr(ev.Match.Teams[0].Code, "TBD"), codeOr(ev.Match.Teams[1].Code, "TBD")
		}
		fmt.Printf("  %-20s %-10s %-6s %s\n", ev.Match.ID, parseTime(ev.StartTime).In(kst).Format("2006-01-02"),
			strings.ToUpper(ev.League.Slug), l+" vs "+rt)
	}
	fmt.Println("\n  lolterm lol replay -match <MATCH ID> [-g N]")
}

// runReplayScreen shows the replay fullscreen until q or ctrl+c.
func runReplayScreen(rp *replay) {
	keys, leave := enterFullscreen()
	defer leave()

	t, h := termSize()
	tg := toggles{}
	var prev *canvas
	draw := func() {
		now := time.Now()
		rp.SetDetailsView(tg.view != 0)
		st := rp.State()
		tg.replay = &st
		s := rp.Tick(now)
		var c *canvas
		if t < minBoardW || h < minTermH {
			c = tooSmallCanvas(t, h, minBoardW, minTermH)
		} else {
			c = buildCanvas(rp.Event(), s.W, s.Det, s.Game, now, t, h-1, 2*time.Second, tg, s.Series, s.view())
			c = withPlayerBar(c, replayBar(st, t, true))
		}
		fmt.Print(renderDiff(prev, c))
		prev = c
	}
	tick := time.NewTicker(boardTick)
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
		case k, ok := <-keys:
			if !ok || replayKey(rp, &tg, k) { // !ok: stdin closed, nobody can press q
				return
			}
			if k.Kind == keyCtrl && k.Rune == 'l' {
				prev = nil // ctrl+l: full repaint; other keys draw only the cells that changed
			}
			draw()
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
	out.drawLine(c.h, bar)
	return out
}
