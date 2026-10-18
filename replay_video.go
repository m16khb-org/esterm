package main

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The replay's video side: one controller goroutine keeps a decoding segment (yt-dlp
// resolved URL → ffmpeg from some second on) around the video second the position maps
// to. Frames carry their true stream time (frame index = second·fps), so the sync
// estimator's offset stays valid across seeks. The segment decodes at most replayAhead
// seconds beyond the target: the pipe to ffmpeg fills up and ffmpeg waits, which is how
// pause and speed are paced without touching the process.

const (
	replayAhead       = 8.0                    // video seconds decoded beyond the target
	replayLead        = 2.0                    // a new segment starts this far before the target
	replayJump        = 20.0                   // a target this far ahead of the decoded frames is read through, not sought
	replayDebounce    = 400 * time.Millisecond // a seek settles this long before the video restarts
	replayProbeGiveUp = 3 * replayProbeSpan    // seconds scanned without finding the clock before the next VOD is tried
	replayRetryMax    = 30 * time.Second

	// Before the sync has locked once, the video position is only an estimate of a few
	// tens of seconds. A probe segment starts replayProbe seconds past it, where the game
	// clock is surely on screen, and decodes up to replayProbeSpan seconds without pacing
	// until the sync locks; the real position is then known and the video seeks to it.
	replayProbe     = 30.0
	replayProbeSpan = 60.0
)

// replayInRange reports whether the decoded frames [lo, hi] (ok: there are some) of a
// segment that started at start can serve target want without restarting the video.
func replayInRange(start, lo, hi float64, ok bool, want float64) bool {
	if !ok {
		return want >= start-0.5 && want <= start+replayJump // starting up
	}
	return want >= lo-0.5 && want <= hi+replayJump
}

type replaySeg struct {
	cancel context.CancelFunc
	done   chan struct{}
	start  float64 // video second of the first frame
	probe  bool    // looks for the game clock instead of following the target
	frames atomic.Int64
}

func (s *replaySeg) ended() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

type replayVideo struct {
	r    *replay
	poke chan struct{}

	mu     sync.Mutex
	on     bool
	panel  bool
	want   float64 // target video second at wantAt
	rate   float64 // video seconds per wall second (0 while paused)
	wantAt time.Time
	jumpAt time.Time
	cur    int // candidate VOD

	// owned by the controller goroutine
	pipe       *mmPipeline
	media      string
	seg        *replaySeg
	fails      int
	probeShift float64 // how far the probes moved on, in seconds, while the clock stayed unseen
	retryAt    time.Time
}

func newReplayVideo(r *replay) *replayVideo {
	return &replayVideo{r: r, poke: make(chan struct{}, 1), panel: true}
}

func (v *replayVideo) wake() {
	select {
	case v.poke <- struct{}{}:
	default:
	}
}

func (v *replayVideo) candidate() replayVod {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.r.vods[v.cur]
}

func (v *replayVideo) spawnAt() float64 { return v.candidate().SpawnAt }
func (v *replayVideo) label() string    { return v.candidate().Label }

func (v *replayVideo) enabled() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.on
}

func (v *replayVideo) setEnabled(on bool) {
	v.mu.Lock()
	changed := v.on != on
	v.on = on
	v.mu.Unlock()
	if changed {
		v.wake()
	}
}

func (v *replayVideo) setPanel(shown bool) {
	v.mu.Lock()
	v.panel = shown
	v.mu.Unlock()
}

// setTarget sets the video second the position maps to and how fast it advances; jump
// marks a seek, which the controller lets settle before it restarts the video.
func (v *replayVideo) setTarget(vt, rate float64, now time.Time, jump bool) {
	v.mu.Lock()
	v.want, v.rate, v.wantAt = vt, rate, now
	if jump {
		v.jumpAt = now
	}
	v.mu.Unlock()
	v.wake()
}

// target is the video second wanted at wall time now.
func (v *replayVideo) target(now time.Time) float64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.want + v.rate*now.Sub(v.wantAt).Seconds()
}

// view returns the minimap at video second vt, with the champions' lives at the same feed
// time. Frames not decoded yet show the status instead of an older scene; before the sync
// locked once the markers are withheld, they would sit at an unknown time.
func (v *replayVideo) view(vt float64, known bool, lives mmLives) mmView {
	mm := v.r.mm
	lo, hi, ok := mm.span()
	if !ok || vt < lo-0.5 || vt > hi+1.5 {
		return mm.statusView("buffering video · " + mm.Status())
	}
	view := mm.SnapshotAt(vt, lives)
	if !known {
		view.Markers, view.Icons = nil, nil
		view.Status = "aligning video · " + v.r.sy.Status()
	}
	return view
}

// run is the controller loop; it returns when the replay's context ends.
func (v *replayVideo) run() {
	defer v.stopSeg()
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-v.r.ctx.Done():
			return
		case <-t.C:
		case <-v.poke:
		}
		v.step()
	}
}

func (v *replayVideo) step() {
	v.mu.Lock()
	on, want, jumpAt := v.on, v.want, v.jumpAt
	v.mu.Unlock()
	if !on {
		v.stopSeg()
		return
	}
	now := time.Now()
	if now.Before(v.retryAt) {
		return
	}
	if v.pipe == nil && !v.prepare() {
		return
	}
	if v.media == "" && !v.resolve() {
		return
	}
	known := v.r.offsetKnown()
	if !known && v.probeShift >= replayProbeGiveUp && v.giveUp() {
		return
	}
	if v.seg != nil && !v.seg.ended() {
		lo, hi, ok := v.r.mm.span()
		switch {
		case v.seg.probe && !known:
			if !ok || hi < v.seg.start+replayProbeSpan-1 {
				return // still looking for the game clock
			}
			v.probeShift += replayProbeSpan // not in this stretch: try the next
		case replayInRange(v.seg.start, lo, hi, ok, want):
			return
		}
	} else if v.seg != nil {
		if v.seg.frames.Load() == 0 {
			v.fails++
			v.retryAt = now.Add(min(time.Second<<min(v.fails, 5), replayRetryMax))
			if v.fails >= 2 {
				v.media = "" // the resolved URL may have expired
			}
		} else {
			v.fails = 0
			if v.seg.probe && !known {
				v.probeShift += replayProbeSpan
			}
		}
		v.seg = nil
		if now.Before(v.retryAt) {
			return
		}
	}
	if now.Sub(jumpAt) < replayDebounce {
		return
	}
	v.stopSeg()
	if known {
		v.probeShift = 0
		v.startSeg(math.Max(0, want-replayLead), false)
	} else {
		v.startSeg(want+replayProbe+v.probeShift, true)
	}
}

// limit is the video second up to which frames may be decoded now: a little beyond the
// target, or the whole probe stretch while the first lock is still missing.
func (v *replayVideo) limit(seg *replaySeg) float64 {
	if seg.probe && !v.r.offsetKnown() {
		return seg.start + replayProbeSpan
	}
	return v.target(time.Now()) + replayAhead
}

// prepare loads the champion icons and map once per game.
func (v *replayVideo) prepare() bool {
	r := v.r
	fail := func(s string) bool {
		if r.ctx.Err() == nil {
			r.mm.setStatus("error: " + s)
			v.retryAt = time.Now().Add(5 * time.Second)
		}
		return false
	}
	meta := r.st.metadata()
	refs := mmChampRefs(meta)
	if len(refs) == 0 {
		return fail("no champions in game metadata")
	}
	r.mm.setStatus("loading champion icons")
	icons, ver, err := mmLoadIcons(r.ctx, refs, meta.GameMetadata.PatchVersion)
	if err != nil {
		return fail(err.Error())
	}
	if err := mmPrimeMapRef(r.ctx, ver); err != nil {
		return fail("map image: " + err.Error())
	}
	r.mm.attach(mmLabels(refs), ver, r.sy)
	v.pipe = newMMPipeline(icons, r.sy)
	v.pipe.livesAt = func(t time.Time) mmLives { return r.st.livesAt(t, r.sy.Skew()) }
	return true
}

// resolve turns the current candidate's page into a media URL.
func (v *replayVideo) resolve() bool {
	r := v.r
	c := v.candidate()
	if missing := mmToolsMissing(c.URL); len(missing) > 0 {
		r.mm.setStatus(fmt.Sprintf("missing tools: %s — %s", strings.Join(missing, ", "), mmToolsHint))
		v.retryAt = time.Now().Add(10 * time.Second)
		return false
	}
	if !mmIsPage(c.URL) {
		v.media = c.URL
		return true
	}
	r.mm.setStatus("resolving " + c.Label)
	media, _, height, err := mmResolve(r.ctx, c.URL)
	if err != nil {
		if r.ctx.Err() != nil {
			return false
		}
		r.mm.setStatus("error: " + err.Error())
		if !v.nextCandidate("") {
			v.retryAt = time.Now().Add(5 * time.Second)
		}
		return false
	}
	v.media = media
	r.mm.setSrcNote(mmSourceNote(height))
	return true
}

// giveUp moves on to the next VOD after a scan that found no game clock; with none left
// it stops and starts the scan over after a pause.
func (v *replayVideo) giveUp() bool {
	if !v.nextCandidate("no game clock found in this VOD, trying the next") {
		v.stopSeg()
		v.probeShift = 0
		v.r.mm.setStatus("no game clock found in any VOD of this game")
		v.retryAt = time.Now().Add(10 * time.Second)
	}
	return true
}

// nextCandidate switches to the next VOD, if any, and forgets what was learned about the
// old one's timeline.
func (v *replayVideo) nextCandidate(why string) bool {
	v.mu.Lock()
	if v.cur+1 >= len(v.r.vods) {
		v.mu.Unlock()
		return false
	}
	v.cur++
	v.mu.Unlock()
	v.stopSeg()
	v.media = ""
	v.probeShift = 0
	v.r.sy.ObserveRestart()
	v.r.forgetOffset()
	if why != "" {
		v.r.mm.setStatus(why)
	}
	return true
}

func (v *replayVideo) startSeg(ss float64, probe bool) {
	r := v.r
	base := int(math.Round(ss * mmFPS))
	ss = float64(base) / mmFPS
	ctx, cancel := context.WithCancel(r.ctx)
	r.mm.setStatus("seeking video to " + fmtClock(ss))
	frames, err := startMMVideo(ctx, mmVideoOpts{Src: v.media, SS: ss, FPS: mmFPS, IdxBase: base, VOD: true, Main: true}, r.mm.setStatus)
	if err != nil {
		cancel()
		r.mm.setStatus("error: " + err.Error())
		v.retryAt = time.Now().Add(5 * time.Second)
		return
	}
	r.mm.reset()
	seg := &replaySeg{cancel: cancel, done: make(chan struct{}), start: ss, probe: probe}
	v.seg = seg
	go v.consume(ctx, seg, frames)
}

func (v *replayVideo) stopSeg() {
	if v.seg == nil {
		return
	}
	v.seg.cancel()
	<-v.seg.done
	v.seg = nil
}

// consume runs the decoded frames through the pipeline, never further than replayAhead
// beyond the target. The first frame of a segment is flagged as a restart: the stream
// jumped.
func (v *replayVideo) consume(ctx context.Context, seg *replaySeg, frames <-chan mmFrame) {
	defer close(seg.done)
	first := true
	for f := range frames {
		if ctx.Err() != nil {
			mmReleaseFrame(f)
			continue // drain so the video goroutine can exit
		}
		for float64(f.Idx)/mmFPS > v.limit(seg) && ctx.Err() == nil {
			select {
			case <-ctx.Done():
			case <-time.After(40 * time.Millisecond):
			}
		}
		if ctx.Err() != nil {
			mmReleaseFrame(f)
			continue
		}
		f.Restart = first
		first = false
		v.mu.Lock()
		v.pipe.track = v.panel
		v.mu.Unlock()
		res := v.pipe.Process(f)
		mmReleaseFrame(f)
		v.r.mm.push(res, time.Now())
		seg.frames.Add(1)
	}
}
