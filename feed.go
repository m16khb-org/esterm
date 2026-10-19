package main

import (
	"context"
	"sort"
	"sync"
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
	feedPriorSkew     = 3500 * time.Millisecond // feed time since spawn minus the HUD game clock until sync learns it
	feedPauseGap      = 1500 * time.Millisecond // a gap after a paused frame longer than this is a game pause
	feedBackoffMin    = 2 * time.Second         // pause between backfills, doubled while they find nothing older
	feedBackoffMax    = 2 * time.Minute
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
	Lives  mmLives       // who is dead and until when at the shown instant; nil without frames
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

// feedKill is a change of a team's kill count seen in the feed: Team 0 blue, 1 red; T is
// the timestamp of the first frame showing Count.
type feedKill struct {
	Team, Count int
	T           time.Time
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

// findKills returns every single-kill increase between consecutive frames. Jumps of more
// than one kill have no unambiguous count and are skipped.
func findKills(fr []stamped[frame]) []feedKill {
	var out []feedKill
	for i := 1; i < len(fr); i++ {
		p, c := fr[i-1].v, fr[i].v
		if c.BlueTeam.TotalKills == p.BlueTeam.TotalKills+1 {
			out = append(out, feedKill{0, c.BlueTeam.TotalKills, fr[i].ts})
		}
		if c.RedTeam.TotalKills == p.RedTeam.TotalKills+1 {
			out = append(out, feedKill{1, c.RedTeam.TotalKills, fr[i].ts})
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
	liveEvent func(lid, matchID string) (event, string, bool)
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
	skew     time.Duration // 0 until SetSkew
	kills    []feedKill    // every kill-count increase seen, in discovery order
	start    time.Time     // when the game's first gold showed, once its frames were seen
	killSeen map[[2]int]bool
	objs     []objEvent    // every objective kill seen, in time order (objMerge)
	fetching bool          // a backfill is running
	nextTry  time.Time     // earliest start of the next backfill
	backoff  time.Duration // wait after the last backfill that found nothing older
	detView  bool          // the view on screen shows details
	lastDet  time.Time     // wall time of the last details request
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
	ev, gid, found := f.liveEvent(f.lid, f.matchID)
	if !found {
		ev, gid = event{}, ""
	}
	f.mu.Lock()
	changed := gid != f.gid
	f.mu.Unlock()
	var spawn time.Time
	if gid != "" && (changed || f.spawnZero()) {
		spawn = f.spawnAt(gid)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastDisc = now
	f.ev = ev
	if gid == f.gid && f.spawn.IsZero() {
		f.spawn = spawn // the first lookup failed: try again until it works
	}
	if gid != f.gid {
		f.gid = gid
		f.spawn = spawn
		f.nextTry, f.backoff, f.lastDet = time.Time{}, 0, time.Time{}
		f.meta = windowResp{}
		f.frames, f.dets, f.lags, f.series, f.pauses, f.skew = nil, nil, nil, nil, nil, 0
		f.kills, f.killSeen, f.objs, f.start = nil, nil, nil, time.Time{}
		f.lastDisp = time.Time{}
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
	// (the video still shows the end of the game) instead of dropping the buffer and
	// flickering between an empty and a refilled scoreboard.
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
	f.mu.Unlock()
	if gid == "" {
		return
	}

	var (
		w windowResp
		d detailsResp
	)
	if !newest.IsZero() && !(f.pausedLast() && at.Sub(newest) > 2*feedProbeStep) {
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
			var err error
			w, d, err = f.fetchPair(gid, startingTime(at.Add(-feedProbeStep*time.Duration(i+1))), det)
			if err != nil {
				return
			}
			if len(w.Frames) > 0 {
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
	if n := len(f.frames); n > 0 {
		f.lags = append(f.lags, at.Sub(f.frames[n-1].ts))
		if len(f.lags) > feedLagWindow {
			f.lags = f.lags[len(f.lags)-feedLagWindow:]
		}
	}
}

// absorbLocked merges a fetched window and its details into the buffers and derives the
// pauses and kills they reveal. Callers hold f.mu.
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
	// Pauses and kills only change around the frames just added: scan from the one before.
	first := wf[0].ts
	for _, e := range wf[1:] {
		if e.ts.Before(first) {
			first = e.ts
		}
	}
	from := sort.Search(len(f.frames), func(i int) bool { return !f.frames[i].ts.Before(first) })
	tail := f.frames[max(from-1, 0):]
	f.pauses = mergePauses(f.pauses, findPauses(tail))
	for _, k := range findKills(tail) {
		key := [2]int{k.Team, k.Count}
		if f.killSeen == nil {
			f.killSeen = map[[2]int]bool{}
		}
		if !f.killSeen[key] {
			f.killSeen[key] = true
			f.kills = append(f.kills, k)
		}
	}
	f.objs = objMerge(f.objs, objDerive(tail, replayKillGap))
	if f.start.IsZero() {
		f.start, _ = gameStart(f.frames)
	}
}

// Need makes sure the buffer reaches back to feed time t: when it starts later, the
// missing windows are fetched in the background (at most one backfill runs at a time).
// A t older than the buffer keeps is never fetched, it would be trimmed right back, and
// attempts that find nothing older back off exponentially in wall-clock time.
func (f *feed) Need(t time.Time) {
	f.mu.Lock()
	if f.fetching || f.gid == "" || len(f.frames) == 0 || !t.Before(f.frames[0].ts) ||
		t.Before(f.frames[len(f.frames)-1].ts.Add(-feedKeep)) {
		f.mu.Unlock()
		return
	}
	gid, oldest, now := f.gid, f.frames[0].ts, f.now()
	if now.Before(f.nextTry) {
		f.mu.Unlock()
		return
	}
	f.fetching = true
	f.mu.Unlock()
	go f.backfill(gid, oldest, t)
}

// backfill fetches the 10 s windows from the one holding from back to the one holding to,
// stopping at the start of what the buffer keeps. It schedules the next attempt: soon
// after one that reached further back, exponentially later after one that did not.
func (f *feed) backfill(gid string, from, to time.Time) {
	defer func() {
		f.mu.Lock()
		f.fetching = false
		if len(f.frames) > 0 && f.frames[0].ts.Before(from) {
			f.backoff = feedBackoffMin
		} else {
			f.backoff = min(max(2*f.backoff, feedBackoffMin), feedBackoffMax)
		}
		f.nextTry = f.now().Add(f.backoff)
		f.mu.Unlock()
	}()
	last := to.Truncate(feedProbeStep)
	for i := 0; i < feedProbeMax*2; i++ {
		start := from.Truncate(feedProbeStep).Add(-feedProbeStep * time.Duration(i))
		f.mu.Lock()
		if f.gid != gid {
			f.mu.Unlock()
			return
		}
		var floor time.Time
		if n := len(f.frames); n > 0 {
			floor = f.frames[n-1].ts.Add(-feedKeep).Truncate(feedProbeStep)
		}
		det := f.detView
		f.mu.Unlock()
		if start.Before(floor) {
			return
		}
		w, d, err := f.fetchPair(gid, startingTime(start), det)
		if err != nil {
			return
		}
		f.mu.Lock()
		if f.gid != gid {
			f.mu.Unlock()
			return
		}
		f.absorbLocked(w, d)
		f.mu.Unlock()
		if !start.After(last) {
			return
		}
	}
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

func (f *feed) skewLocked() time.Duration {
	if f.skew == 0 {
		return feedPriorSkew
	}
	return f.skew
}

// gameTime converts a frame timestamp to the game clock the broadcast HUD shows.
func (f *feed) gameTime(ts time.Time) time.Duration {
	return ts.Sub(f.spawn) - f.skewLocked() - pauseShift(f.pauses, ts)
}

// SetSkew sets the feed-to-HUD clock skew the sync estimator learned; 0 restores the prior.
func (f *feed) SetSkew(d time.Duration) {
	f.mu.Lock()
	f.skew = d
	f.mu.Unlock()
}

// Pauses returns the game pauses seen so far.
func (f *feed) Pauses() []feedPause {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]feedPause(nil), f.pauses...)
}

// KillsSince returns the kill-count increases seen after the first from ones and the
// number seen in total, to pass to the next call.
func (f *feed) KillsSince(from int) ([]feedKill, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if from > len(f.kills) {
		from = 0
	}
	return append([]feedKill(nil), f.kills[from:]...), len(f.kills)
}

// GameStart returns when the game's first gold showed, once the feed has seen the frames
// around it (a feed that joined later never does).
func (f *feed) GameStart() (time.Time, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.start, !f.start.IsZero()
}

// Kills returns every kill-count increase seen so far.
func (f *feed) Kills() []feedKill {
	ks, _ := f.KillsSince(0)
	return ks
}

// Position returns the feed time the adaptive playout last showed.
func (f *feed) Position() (time.Time, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastDisp, !f.lastDisp.IsZero()
}

// LivesAt is who is dead at feed time t (see livesAt), nil while no frame is buffered.
func (f *feed) LivesAt(t time.Time) mmLives {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.frames) == 0 {
		return nil
	}
	return livesAt(f.frames, t, f.gameClockLocked(), false)
}

// Meta returns the live event and game with its metadata but no frame.
func (f *feed) Meta() feedSnap {
	f.mu.Lock()
	defer f.mu.Unlock()
	return feedSnap{Ev: f.ev, GID: f.gid, Spawn: f.spawn, W: f.meta, Game: -1}
}

// Latest returns the timestamp of the newest buffered frame.
func (f *feed) Latest() (time.Time, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.frames) == 0 {
		return time.Time{}, false
	}
	return f.frames[len(f.frames)-1].ts, true
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

// SnapshotAt returns the state to draw at feed time t: the latest buffered frame not past t.
func (f *feed) SnapshotAt(now, t time.Time) feedSnap {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.frames) > 0 && t.Before(f.frames[0].ts) {
		// Nothing at or before t is buffered (yet): show no frame rather than a later one.
		return feedSnap{Ev: f.ev, GID: f.gid, Spawn: f.spawn, W: f.meta, Game: -1}
	}
	return f.snapLocked(now, t)
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
