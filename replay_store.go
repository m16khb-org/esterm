package main

import (
	"context"
	"sort"
	"sync"
	"time"
)

// The frames of a finished game, kept for random access. The livestats feed serves them in
// 10 s windows addressed by startingTime; a window is fetched at most once, so seeking
// around never repeats a request. The windows around the playback position come first,
// the rest of the game is read sequentially in the background (it also reveals the game
// pauses, which the game clock needs) at replayPace, so a board that is opened does not
// burst through the feed. The stores of the last replayCacheGames games are kept, so
// opening a game again costs no request.

const (
	replayWorkers  = 4               // concurrent window requests
	replayNear     = 3               // windows fetched ahead of the position first
	replayKeepAll  = 24 * time.Hour  // nothing is ever trimmed
	replayKillGap  = 2 * time.Second // a kill counted across a longer frame gap has an unknown time
	replayIdleWake = time.Second     // pump wake-up when only failed windows are left

	replayCacheGames = 3 // games whose fetched windows outlive their board
)

// replayPace is the gap between background window requests (4 per second); a variable
// so that tests can run unpaced.
var replayPace = 250 * time.Millisecond

// replayCache holds the stores of the most recently opened games, newest first.
var replayCache struct {
	mu     sync.Mutex
	stores []*replayStore
}

// cachedReplayStore returns the kept store of game gid, or nil, and makes it the newest.
func cachedReplayStore(gid string) *replayStore {
	replayCache.mu.Lock()
	defer replayCache.mu.Unlock()
	for i, s := range replayCache.stores {
		if s.gid == gid {
			copy(replayCache.stores[1:i+1], replayCache.stores[:i])
			replayCache.stores[0] = s
			return s
		}
	}
	return nil
}

// keepReplayStore makes s the newest cached store and drops the oldest beyond the limit.
func keepReplayStore(s *replayStore) {
	replayCache.mu.Lock()
	defer replayCache.mu.Unlock()
	replayCache.stores = append([]*replayStore{s}, replayCache.stores...)
	if len(replayCache.stores) > replayCacheGames {
		replayCache.stores = replayCache.stores[:replayCacheGames]
	}
}

// bucketOf is the index of the 10 s window holding t.
func bucketOf(t time.Time) int64 { return t.UnixNano() / int64(feedProbeStep) }

// bucketStart is the startingTime of window b.
func bucketStart(b int64) time.Time { return time.Unix(0, b*int64(feedProbeStep)).UTC() }

type replayStore struct {
	gid         string
	spawn, end  time.Time
	first, last int64 // first and last window of the game
	win         func(gid, start string) (windowResp, error)
	det         func(gid, start string) (detailsResp, error)
	retry       time.Duration
	pace        time.Duration // gap between background requests
	now         func() time.Time
	wake        chan struct{}

	mu       sync.Mutex
	meta     windowResp
	frames   []stamped[frame]
	dets     []stamped[detailsFrame]
	pauses   []feedPause
	kills    []feedKill
	done     map[int64]bool // windows fetched
	doneDet  map[int64]bool // detail windows fetched
	busy     map[int64]bool
	busyDet  map[int64]bool
	failed   map[int64]time.Time
	focus    int64
	bulkAt   time.Time // earliest start of the next background request
	detView  bool
	requests int // window and details requests issued
}

func newReplayStore(gid string, spawn, end time.Time, retry time.Duration) *replayStore {
	return &replayStore{
		gid: gid, spawn: spawn, end: end,
		first: bucketOf(spawn), last: bucketOf(end),
		win: func(gid, start string) (windowResp, error) {
			var w windowResp
			err := getJSON(feedBase+"/window/"+gid+"?startingTime="+start, &w)
			return w, err
		},
		det: func(gid, start string) (detailsResp, error) {
			var d detailsResp
			err := getJSON(feedBase+"/details/"+gid+"?startingTime="+start, &d)
			return d, err
		},
		retry: retry, pace: replayPace, now: time.Now, wake: make(chan struct{}, 1),
		done: map[int64]bool{}, doneDet: map[int64]bool{}, busy: map[int64]bool{}, busyDet: map[int64]bool{},
		failed: map[int64]time.Time{},
	}
}

func (s *replayStore) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// absorb merges a fetched window and its details and re-derives pauses and kills.
func (s *replayStore) absorb(w windowResp, d detailsResp) {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if w.GameMetadata.PatchVersion != "" || len(w.GameMetadata.BlueTeamMetadata.Participants) > 0 {
		s.meta = w
		s.meta.Frames = nil
	}
	s.dets = mergeStamped(s.dets, df, replayKeepAll)
	if len(wf) == 0 {
		return
	}
	s.frames = mergeStamped(s.frames, wf, replayKeepAll)
	s.pauses = findPauses(s.frames)
	s.kills = replayKills(s.frames)
}

// replayKills lists the single-kill increases between frames at most replayKillGap apart:
// across a longer gap (a window not fetched yet) the time of the kill is unknown.
func replayKills(fr []stamped[frame]) []feedKill {
	var out []feedKill
	for i := 1; i < len(fr); i++ {
		if fr[i].ts.Sub(fr[i-1].ts) > replayKillGap {
			continue
		}
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

// setFocus tells the store which feed time is on screen; its windows are fetched first.
func (s *replayStore) setFocus(t time.Time, details bool) {
	b := min(max(bucketOf(t), s.first), s.last)
	s.mu.Lock()
	changed := s.focus != b || s.detView != details
	s.focus, s.detView = b, details
	s.mu.Unlock()
	if changed {
		s.poke()
	}
}

// next picks the window to fetch: the ones around the focus first, then the earliest
// missing one, at most one per replayPace (wait is how long until the next may start).
// It marks the pick as in flight.
func (s *replayStore) next() (b int64, win, det bool, wait time.Duration, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	cool := func(b int64) bool { return now.Sub(s.failed[b]) < s.retry }
	for off := int64(-1); off <= replayNear; off++ {
		b := s.focus + off
		if b < s.first || b > s.last || cool(b) {
			continue
		}
		win = !s.done[b] && !s.busy[b]
		det = s.detView && !s.doneDet[b] && !s.busyDet[b]
		if win || det {
			s.mark(b, win, det, true)
			return b, win, det, 0, true
		}
	}
	for b := s.first; b <= s.last; b++ {
		if !s.done[b] && !s.busy[b] && !cool(b) {
			if d := s.bulkAt.Sub(now); d > 0 {
				return 0, false, false, d, false
			}
			s.bulkAt = now.Add(s.pace)
			s.mark(b, true, false, true)
			return b, true, false, 0, true
		}
	}
	return 0, false, false, 0, false
}

func (s *replayStore) mark(b int64, win, det, on bool) {
	if win {
		s.busy[b] = on
	}
	if det {
		s.busyDet[b] = on
	}
}

// fetch loads window b (and its details) and records the outcome.
func (s *replayStore) fetch(b int64, win, det bool) {
	start := startingTime(bucketStart(b))
	var (
		w          windowResp
		d          detailsResp
		werr, derr error
		wg         sync.WaitGroup
	)
	s.mu.Lock()
	s.requests += btoi(win) + btoi(det)
	s.mu.Unlock()
	if det {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, derr = s.det(s.gid, start)
		}()
	}
	if win {
		w, werr = s.win(s.gid, start)
	}
	wg.Wait()
	if (win && werr == nil) || (det && derr == nil) {
		s.absorb(w, d)
	}
	s.mu.Lock()
	s.mark(b, win, det, false)
	if win && werr == nil {
		s.done[b] = true
	}
	if det && derr == nil {
		s.doneDet[b] = true
	}
	if (win && werr != nil) || (det && derr != nil) {
		s.failed[b] = s.now()
	}
	s.mu.Unlock()
	s.poke()
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// run fetches until ctx ends; at most replayWorkers requests are in flight.
func (s *replayStore) run(ctx context.Context) {
	sem := make(chan struct{}, replayWorkers)
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		select {
		case <-ctx.Done():
			return
		case sem <- struct{}{}:
		}
		b, win, det, wait, ok := s.next()
		if !ok {
			<-sem
			if wait <= 0 {
				wait = replayIdleWake
			}
			t := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-s.wake:
			case <-t.C:
			}
			t.Stop()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.fetch(b, win, det)
			<-sem
		}()
	}
}

// progress is the share of the game's windows fetched.
func (s *replayStore) progress() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for b := s.first; b <= s.last; b++ {
		if s.done[b] {
			n++
		}
	}
	return float64(n) / float64(s.last-s.first+1)
}

// edge is the feed time up to which every window has been fetched.
func (s *replayStore) edge() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	for b := s.first; b <= s.last; b++ {
		if !s.done[b] {
			return bucketStart(b)
		}
	}
	return s.end
}

func (s *replayStore) pausesList() []feedPause {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]feedPause(nil), s.pauses...)
}

func (s *replayStore) killsList() []feedKill {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]feedKill(nil), s.kills...)
}

func (s *replayStore) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

func (s *replayStore) metadata() windowResp {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.meta
}

// snapshot builds the board state at feed time t: the latest frame not past t (the first
// one before the game's own start), the gold series up to it, the lives of the champions
// (exact respawns: the store holds the whole game) and game as the header clock.
func (s *replayStore) snapshot(t time.Time, game, skew time.Duration) feedSnap {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := feedSnap{GID: s.gid, Spawn: s.spawn, W: s.meta, Game: game}
	cur, ok := pickStamped(s.frames, t)
	if !ok {
		return snap
	}
	snap.W.Frames = []frame{cur.v}
	if d, ok := pickStamped(s.dets, cur.ts); ok {
		snap.Det.Frames = []detailsFrame{d.v}
	}
	clock := func(ts time.Time) time.Duration { return ts.Sub(s.spawn) - skew - pauseShift(s.pauses, ts) }
	snap.Lives = livesAt(s.frames, t, clock, true)
	end := sort.Search(len(s.frames), func(i int) bool { return s.frames[i].ts.After(cur.ts) })
	for _, fr := range s.frames[:end] {
		gm := max(0, int(clock(fr.ts).Minutes()))
		pt := goldPt{gm, fr.v.BlueTeam.TotalGold - fr.v.RedTeam.TotalGold}
		if n := len(snap.Series); n > 0 && snap.Series[n-1].gameMin == gm {
			snap.Series[n-1] = pt
		} else {
			snap.Series = append(snap.Series, pt)
		}
	}
	return snap
}
