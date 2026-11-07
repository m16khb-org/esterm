package main

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const (
	feedDiscoverEvery = 30 * time.Second             // live-event lookup cadence (wall clock)
	feedKeep          = 5 * time.Minute              // how much frame history stays buffered
	feedProbeStep     = 10 * time.Second             // livestats startingTime granularity
	feedProbeMax      = 18                           // ~3 min of back-stepping before the first frame
	feedResync        = feedProbeStep * feedProbeMax // newest frame older than this: re-probe the live edge
	feedLagWindow     = 8                            // polls considered by the adaptive playout delay
	feedDelayMargin   = 500 * time.Millisecond
	feedSkew          = 3500 * time.Millisecond // feed time since spawn minus the HUD game clock
	feedPauseGap      = 1500 * time.Millisecond // a gap after a paused frame longer than this is a game pause
	feedHistWorkers   = 4                       // concurrent window requests of the pause history scan
	feedHistRetry     = 2 * time.Minute         // wait before a failed pause history scan runs again
)

// startingTime formats t as the 10 s-aligned startingTime query value of the livestats feed.
func startingTime(t time.Time) string {
	return t.UTC().Truncate(feedProbeStep).Format("2006-01-02T15:04:05.000Z")
}

// feedSnap is the state the renderer needs for one instant.
type feedSnap struct {
	Ev     event
	GID    string
	Spawn  time.Time
	W      windowResp // GameMetadata + exactly the current playout frame (or none)
	Det    detailsResp
	Series []goldPt
	Lag    time.Duration // wall clock minus the current frame's timestamp
	Game   time.Duration // game clock of the current frame as the broadcast shows it; -1 if unknown
	Lives  champLives    // who is dead and until when at the shown instant; nil without frames
	Obj    objView       // objective timers at the shown instant; zero without frames
}

type stamped[T any] struct {
	ts time.Time
	v  T
}

// mergeStamped merges add into buf: sorted by timestamp, duplicates replaced by the
// newer copy, anything older than keep before the newest entry dropped. Only the part of
// buf at or after the first added timestamp is rebuilt, so appending a fresh tail is cheap.
// buf is reused and must not be shared.
func mergeStamped[T any](buf, add []stamped[T], keep time.Duration) []stamped[T] {
	if len(add) == 0 {
		return buf
	}
	add = append([]stamped[T]{}, add...)
	sort.SliceStable(add, func(i, j int) bool { return add[i].ts.Before(add[j].ts) })
	n := 0
	for _, e := range add {
		if n > 0 && add[n-1].ts.Equal(e.ts) {
			add[n-1] = e
			continue
		}
		add[n] = e
		n++
	}
	add = add[:n]
	k := sort.Search(len(buf), func(i int) bool { return !buf[i].ts.Before(add[0].ts) })
	tail := append([]stamped[T]{}, buf[k:]...)
	out := buf[:k]
	i, j := 0, 0
	for i < len(tail) || j < len(add) {
		switch {
		case j == len(add) || (i < len(tail) && tail[i].ts.Before(add[j].ts)):
			out = append(out, tail[i])
			i++
		case i < len(tail) && tail[i].ts.Equal(add[j].ts):
			i++ // replaced by the newer copy
		default:
			out = append(out, add[j])
			j++
		}
	}
	cut := out[len(out)-1].ts.Add(-keep)
	d := 0
	for d < len(out)-1 && out[d].ts.Before(cut) {
		d++
	}
	return out[d:]
}

// pickStamped returns the latest entry with ts <= t, else the oldest entry.
func pickStamped[T any](buf []stamped[T], t time.Time) (stamped[T], bool) {
	if len(buf) == 0 {
		return stamped[T]{}, false
	}
	i := sort.Search(len(buf), func(i int) bool { return buf[i].ts.After(t) })
	if i == 0 {
		return buf[0], true
	}
	return buf[i-1], true
}

// feedPause is a game pause: the feed emitted a paused frame at Start and no frame for
// Dur. Feed timestamps are wall clock, so the game clock stands still over [Start, Start+Dur].
type feedPause struct {
	Start time.Time
	Dur   time.Duration
}

// pauseShift is the total paused time up to feed time t; ps must be sorted by Start.
func pauseShift(ps []feedPause, t time.Time) time.Duration {
	var sum time.Duration
	for _, p := range ps {
		if !t.After(p.Start) {
			break
		}
		sum += min(t.Sub(p.Start), p.Dur)
	}
	return sum
}

// findPauses returns the pauses visible in the sorted frames.
func findPauses(fr []stamped[frame]) []feedPause {
	var out []feedPause
	for i := 1; i < len(fr); i++ {
		if gap := fr[i].ts.Sub(fr[i-1].ts); fr[i-1].v.GameState == "paused" && gap > feedPauseGap {
			out = append(out, feedPause{fr[i-1].ts, gap})
		}
	}
	return out
}

// feed polls the livestats API in the background and plays buffered frames out in
// timestamp order on the 0.5 s render tick, so rendering never touches the network.
type feed struct {
	lid     string
	matchID string // when set, only this match counts as live (the app's selection)
	poll    time.Duration

	// injectable for tests; newFeed wires the real implementations.
	now       func() time.Time
	liveEvent func(lid, matchID string) (event, string, bool, error)
	spawnAt   func(gid string) time.Time
	window    func(gid, start string) (windowResp, error)
	details   func(gid, start string) (detailsResp, error)

	mu       sync.Mutex
	ev       event
	gid      string
	spawn    time.Time
	lastDisc time.Time
	meta     windowResp // latest response carrying game metadata (Frames cleared)
	frames   []stamped[frame]
	dets     []stamped[detailsFrame]
	lags     []time.Duration
	lastDisp time.Time
	series   []goldPt
	pauses   []feedPause
	objs     []objEvent // every objective kill seen, in time order (objMerge)
	detView  bool       // the view on screen shows details
	lastDet  time.Time  // wall time of the last details request
	hist     string     // game whose pauses before the buffer are known or being looked up
	histTry  time.Time  // earliest retry of a failed pause history scan
}

func newFeed(lid string, poll time.Duration) *feed {
	return &feed{
		lid:       lid,
		poll:      poll,
		now:       time.Now,
		liveEvent: fetchLiveEvent,
		spawnAt:   fetchSpawn,
		window: func(gid, start string) (windowResp, error) {
			var w windowResp
			err := getJSON(feedBase+"/window/"+gid+"?startingTime="+start, &w)
			return w, err
		},
		details: func(gid, start string) (detailsResp, error) {
			var d detailsResp
			err := getJSON(feedBase+"/details/"+gid+"?startingTime="+start, &d)
			return d, err
		},
	}
}

// Run fetches every poll interval until ctx is done; the first cycle runs one interval
// after the call, so callers wanting data immediately call FetchOnce first.
func (f *feed) Run(ctx context.Context) {
	t := time.NewTicker(f.poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			f.FetchOnce()
			if gid, from, to, ok := f.claimHistory(); ok {
				go func() {
					defer crashGuard()
					f.scanHistory(ctx, gid, from, to)
				}()
			}
		}
	}
}

// FetchOnce runs one synchronous cycle: live-event discovery when due, then window+details.
func (f *feed) FetchOnce() {
	now := f.now()
	f.mu.Lock()
	due := f.lastDisc.IsZero() || now.Sub(f.lastDisc) >= feedDiscoverEvery
	f.mu.Unlock()
	if due {
		f.discover(now)
	}
	f.fetchFrames()
}

func (f *feed) spawnZero() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.spawn.IsZero()
}

// SetDetailsView tells the feed whether the view on screen shows per-player details;
// otherwise they are fetched at most once per window.
func (f *feed) SetDetailsView(on bool) {
	f.mu.Lock()
	f.detView = on
	f.mu.Unlock()
}

func (f *feed) discover(now time.Time) {
	ev, gid, found, err := f.liveEvent(f.lid, f.matchID)
	if !found {
		ev, gid = event{}, ""
	}
	f.mu.Lock()
	f.lastDisc = now
	if err != nil {
		// A failed lookup says nothing about the game: keep the buffer, the header and the
		// game id, and try again on the normal cadence.
		f.mu.Unlock()
		return
	}
	changed := gid != f.gid
	f.mu.Unlock()
	var spawn time.Time
	if gid != "" && (changed || f.spawnZero()) {
		spawn = f.spawnAt(gid)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ev = ev
	if gid == f.gid && f.spawn.IsZero() {
		f.spawn = spawn // the first lookup failed: try again until it works
	}
	if gid != f.gid {
		f.gid = gid
		f.spawn = spawn
		f.lastDet = time.Time{}
		f.meta = windowResp{}
		f.frames, f.dets, f.lags, f.series, f.pauses, f.objs = nil, nil, nil, nil, nil, nil
		f.lastDisp, f.histTry = time.Time{}, time.Time{}
	}
}

// fetchPair fetches window and, when det is set, details for the same startingTime concurrently.
func (f *feed) fetchPair(gid, start string, det bool) (windowResp, detailsResp, error) {
	var (
		w    windowResp
		werr error
		d    detailsResp
		wg   sync.WaitGroup
	)
	if det {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, _ = f.details(gid, start)
		}()
	}
	w, werr = f.window(gid, start)
	wg.Wait()
	return w, d, werr
}

func (f *feed) fetchFrames() {
	at := f.now()
	f.mu.Lock()
	gid := f.gid
	var newest time.Time
	if n := len(f.frames); n > 0 {
		newest = f.frames[n-1].ts
	}
	// A finished game publishes nothing newer, however long ago its last frame was: keep it
	// instead of dropping the buffer and flickering between an empty and a refilled
	// scoreboard.
	if n := len(f.frames); !newest.IsZero() && at.Sub(newest) > feedResync && f.frames[n-1].v.GameState != "finished" {
		// Far behind (e.g. after sleep): drop the stale buffer and probe for the live edge again.
		if f.pausedLast() {
			// Keep the paused frame so the gap after it is still recognised as a pause.
			f.frames, f.dets = f.frames[len(f.frames)-1:], nil
		} else {
			f.frames, f.dets = nil, nil
		}
		f.lags = nil
		f.lastDisp = time.Time{}
		newest = time.Time{}
	}
	det := f.detView || at.Sub(f.lastDet) >= feedProbeStep
	if det {
		f.lastDet = at
	}
	paused := f.pausedLast() // a backfill can replace the buffer, so read it under the lock
	f.mu.Unlock()
	if gid == "" {
		return
	}

	var (
		w windowResp
		d detailsResp
	)
	if !newest.IsZero() && !(paused && at.Sub(newest) > 2*feedProbeStep) {
		// Frame timestamps are irregular, so the newest frame of a window can sit anywhere in
		// its last half second. The next window is empty until the feed publishes it, so it
		// is polled together with the current one only once the newest frame is within a
		// poll of the window's end; then playback moves on without a gap.
		base := newest.Truncate(feedProbeStep)
		next := !newest.Before(base.Add(feedProbeStep - f.poll - feedDelayMargin))
		var (
			w2   windowResp
			d2   detailsResp
			err2 error
			wg   sync.WaitGroup
		)
		if next {
			wg.Add(1)
			go func() {
				defer wg.Done()
				w2, d2, err2 = f.fetchPair(gid, startingTime(base.Add(feedProbeStep)), det)
			}()
		}
		var err error
		w, d, err = f.fetchPair(gid, startingTime(base), det)
		wg.Wait()
		if err != nil && (!next || err2 != nil) {
			return
		}
		if len(w.Frames) == 0 && len(w2.GameMetadata.BlueTeamMetadata.Participants) > 0 {
			w.GameMetadata = w2.GameMetadata
		}
		w.Frames = append(w.Frames, w2.Frames...)
		d.Frames = append(d.Frames, d2.Frames...)
	} else {
		for i := 0; i < feedProbeMax; i++ {
			at = f.now()
			start := startingTime(at.Add(-feedProbeStep * time.Duration(i+1)))
			var err error
			w, d, err = f.fetchPair(gid, start, false) // details belong to the window that wins
			if err != nil {
				// The feed rejects a window whose end is less than ~20 s old. The live
				// edge is found by stepping further back, so keep probing on that 400.
				var he *httpStatusError
				if errors.As(err, &he) && he.Code == http.StatusBadRequest {
					continue
				}
				return
			}
			if len(w.Frames) > 0 {
				if det {
					d, _ = f.details(gid, start)
				}
				break
			}
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gid != gid {
		return
	}
	f.absorbLocked(w, d)
	// A paused frame means the feed is quiet on purpose: that silence is the pause, not
	// jitter, and absorbing it would hold playout back for the whole pause after resume.
	if n := len(f.frames); n > 0 && !f.pausedLast() {
		f.lags = append(f.lags, at.Sub(f.frames[n-1].ts))
		if len(f.lags) > feedLagWindow {
			f.lags = f.lags[len(f.lags)-feedLagWindow:]
		}
	}
}

// absorbLocked merges a fetched window and its details into the buffers and derives the
// pauses and objectives they reveal. Callers hold f.mu.
func (f *feed) absorbLocked(w windowResp, d detailsResp) {
	var wf []stamped[frame]
	for _, fr := range w.Frames {
		if ts := parseTime(fr.RFC460Timestamp); !ts.IsZero() {
			wf = append(wf, stamped[frame]{ts, fr})
		}
	}
	var df []stamped[detailsFrame]
	for _, fr := range d.Frames {
		if ts := parseTime(fr.RFC460Timestamp); !ts.IsZero() {
			df = append(df, stamped[detailsFrame]{ts, fr})
		}
	}
	if w.GameMetadata.PatchVersion != "" || len(w.GameMetadata.BlueTeamMetadata.Participants) > 0 {
		f.meta = w
		f.meta.Frames = nil
	}
	f.frames = mergeStamped(f.frames, wf, feedKeep)
	f.dets = mergeStamped(f.dets, df, feedKeep)
	if len(wf) == 0 {
		return
	}
	// Pauses and objectives only change around the frames just added: scan from the one before.
	first := wf[0].ts
	for _, e := range wf[1:] {
		if e.ts.Before(first) {
			first = e.ts
		}
	}
	from := sort.Search(len(f.frames), func(i int) bool { return !f.frames[i].ts.Before(first) })
	tail := f.frames[max(from-1, 0):]
	f.pauses = mergePauses(f.pauses, findPauses(tail))
	f.objs = objMerge(f.objs, objDerive(tail, replayKillGap))
}

// claimHistory hands out, once per game, the stretch before the buffer: a board opened
// after a pause never buffers it, and the game clock would run ahead by the whole pause.
// A game followed from its start needs no lookup.
func (f *feed) claimHistory() (gid string, from, to time.Time, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gid == "" || f.hist == f.gid || f.spawn.IsZero() || len(f.frames) == 0 || f.now().Before(f.histTry) {
		return "", time.Time{}, time.Time{}, false
	}
	f.hist = f.gid
	if !f.frames[0].ts.After(f.spawn.Add(feedProbeStep)) {
		return "", time.Time{}, time.Time{}, false
	}
	return f.gid, f.spawn, f.frames[0].ts, true
}

// scanHistory reads every 10 s window of game gid from from to to and adds the pauses in
// them. A window rejected as too recent holds nothing the buffer lacks; any other failure
// or ctx ending leaves the lookup to a retry after feedHistRetry.
func (f *feed) scanHistory(ctx context.Context, gid string, from, to time.Time) {
	first := from.Truncate(feedProbeStep)
	wins := make([][]stamped[frame], int(to.Sub(first)/feedProbeStep)+1)
	jobs := make(chan int)
	var (
		wg     sync.WaitGroup
		failed atomic.Bool
	)
	for w := 0; w < feedHistWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				resp, err := f.window(gid, startingTime(first.Add(feedProbeStep*time.Duration(i))))
				var he *httpStatusError
				if err != nil && !(errors.As(err, &he) && he.Code == http.StatusBadRequest) {
					failed.Store(true)
					continue
				}
				for _, fr := range resp.Frames {
					if ts := parseTime(fr.RFC460Timestamp); !ts.IsZero() {
						wins[i] = append(wins[i], stamped[frame]{ts, frame{GameState: fr.GameState}})
					}
				}
			}
		}()
	}
	for i := 0; i < len(wins) && ctx.Err() == nil && !failed.Load(); i++ {
		select {
		case jobs <- i:
		case <-ctx.Done():
		}
	}
	close(jobs)
	wg.Wait()

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gid != gid {
		return
	}
	if ctx.Err() != nil || failed.Load() {
		f.hist, f.histTry = "", f.now().Add(feedHistRetry)
		return
	}
	var all []stamped[frame]
	for _, w := range wins {
		all = append(all, w...)
	}
	// Windows inside a pause repeat the frames before it; the merge drops the copies.
	all = mergeStamped(nil, all, to.Sub(first)+feedProbeStep)
	f.pauses = mergePauses(f.pauses, findPauses(all))
}

// delay is how far behind wall clock playout runs: the worst recent arrival lag plus one
// poll interval (the longest stretch without fresh frames) and a small margin.
func (f *feed) delay() time.Duration {
	if len(f.lags) == 0 {
		return 0
	}
	m := f.lags[0]
	for _, l := range f.lags[1:] {
		if l > m {
			m = l
		}
	}
	return m + f.poll + feedDelayMargin
}

// pausedLast reports whether the newest buffered frame is a paused frame, i.e. the feed
// is expected to go quiet until play resumes. Callers hold f.mu.
func (f *feed) pausedLast() bool {
	n := len(f.frames)
	return n > 0 && f.frames[n-1].v.GameState == "paused"
}

// mergePauses adds the pauses of add that buf does not have yet, sorted by Start.
func mergePauses(buf, add []feedPause) []feedPause {
	for _, p := range add {
		i := sort.Search(len(buf), func(i int) bool { return !buf[i].Start.Before(p.Start) })
		switch {
		case i < len(buf) && buf[i].Start.Equal(p.Start):
			buf[i] = p
		default:
			buf = append(buf, feedPause{})
			copy(buf[i+1:], buf[i:])
			buf[i] = p
		}
	}
	return buf
}

// gameTime converts a frame timestamp to the game clock the broadcast HUD shows.
func (f *feed) gameTime(ts time.Time) time.Duration {
	return ts.Sub(f.spawn) - feedSkew - pauseShift(f.pauses, ts)
}

// Pauses returns the game pauses seen so far.
func (f *feed) Pauses() []feedPause {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]feedPause(nil), f.pauses...)
}

// Meta returns the live event and game with its metadata but no frame.
func (f *feed) Meta() feedSnap {
	f.mu.Lock()
	defer f.mu.Unlock()
	return feedSnap{Ev: f.ev, GID: f.gid, Spawn: f.spawn, W: f.meta, Game: -1}
}

// Snapshot returns the state to draw at now, playing out at the adaptive delay. It never
// blocks on the network.
func (f *feed) Snapshot(now time.Time) feedSnap {
	f.mu.Lock()
	defer f.mu.Unlock()
	disp := now.Add(-f.delay())
	if disp.Before(f.lastDisp) {
		disp = f.lastDisp // playback never rewinds when the delay grows
	}
	f.lastDisp = disp
	return f.snapLocked(now, disp)
}

// gameClockLocked is the game clock of a frame timestamp for the death model: the broadcast
// clock, or while the game's start is unknown the time since the oldest buffered frame
// (only the differences matter then). Callers hold f.mu and have frames.
func (f *feed) gameClockLocked() func(time.Time) time.Duration {
	if f.spawn.IsZero() {
		first := f.frames[0].ts
		return func(ts time.Time) time.Duration { return ts.Sub(first) }
	}
	return f.gameTime
}

// snapLocked builds the snapshot for the frame current at feed timestamp disp.
func (f *feed) snapLocked(now, disp time.Time) feedSnap {
	snap := feedSnap{Ev: f.ev, GID: f.gid, Spawn: f.spawn, Game: -1}
	snap.W = f.meta
	cur, ok := pickStamped(f.frames, disp)
	if !ok {
		return snap
	}
	snap.W.Frames = []frame{cur.v}
	if d, ok := pickStamped(f.dets, cur.ts); ok {
		snap.Det.Frames = []detailsFrame{d.v}
	}
	snap.Lives = livesAt(f.frames, disp, f.gameClockLocked(), false)
	snap.Obj = objAt(f.objs, cur.v, cur.ts, f.gameClockLocked(), !f.spawn.IsZero())
	snap.Lag = now.Sub(cur.ts)

	gm := 0
	if !f.spawn.IsZero() {
		if snap.Game = f.gameTime(cur.ts); snap.Game < 0 {
			snap.Game = 0 // frames before the HUD clock starts
		}
		gm = int(snap.Game.Minutes())
	}
	pt := goldPt{gm, cur.v.BlueTeam.TotalGold - cur.v.RedTeam.TotalGold}
	if n := len(f.series); n == 0 || f.series[n-1].gameMin != gm {
		f.series = append(f.series, pt)
		if len(f.series) > 240 {
			f.series = f.series[len(f.series)-240:]
		}
	} else {
		f.series[n-1] = pt
	}
	snap.Series = append([]goldPt(nil), f.series...)
	return snap
}
