package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"math"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Orchestrator of the broadcast minimap: video -> layout -> HUD -> map fit -> tracker,
// results played out either on the shared sync clock or on a fixed delay.

const (
	mmPlayoutBuffer = 6 * time.Second // frames arrive in bursts (~10 per 5 s); independent playout hides the gaps
	mmToolsHint     = "brew install yt-dlp ffmpeg deno"
	mmFPS           = 5
	mmCloseWait     = 3 * time.Second        // how long exit paths wait for ffmpeg/yt-dlp to be reaped
	mmFitEvery      = 10                     // frames between map box refits
	mmLayoutVotes   = 5                      // frames whose clock font must pick the same layout to lock it
	mmClockFresh    = mmFPS                  // frames a clock read keeps the minimap live on its own: reads miss now and then, replays and desk shots hide the whole HUD
	mmClockLong     = 20 * mmFPS             // frames after a clock read during which the HUD still counts as up while its plate is on screen (mmPlateLive): the clock text fails to read for up to a few seconds in live play
	mmPlateLive     = 0.32                   // share of the plate's edge pixels that must still match the last clock-read HUD for the plate to count as on screen (live play with an unreadable clock: >= 0.41, replays and desk shots: <= 0.3)
	mmPlateEdge     = 160                    // summed |dR|+|dG|+|dB| step between neighbouring pixels from which a plate pixel is an edge (text, logos, rules)
	mmPlateMinEdges = 100                    // fewest edge pixels a HUD needs to serve as the plate
	mmPlateTol      = 48                     // summed |dR|+|dG|+|dB| up to which two HUD pixels match
	mmCoverMax      = 20 * mmFPS             // frames an objective banner may hide the map while the HUD stays up before the minimap counts as gone
	mmHoldMax       = 90 * mmFPS             // frames a champion the tracker lost is kept at its last position
	mmHoldShort     = 3 * mmFPS / 2          // frames a lost champion is kept when nothing says it is alive (no feed): 1.5 s
	mmMotionTick    = 100 * time.Millisecond // render cadence while a minimap is on screen: markers move between the tracker results
	mmLockTimeout   = 45 * time.Second       // a stream whose layout is still unread after this long is given up for the next one
	mmStallAfter    = 8 * time.Second        // without a video frame this long the stream counts as stalled
	mmResultsKeep   = 600 * mmFPS            // results buffered at most (nothing consumes them in -json mode)
	mmKeepBack      = 30 * mmFPS             // results kept behind the one shown, for offsets that move back
)

// mmTickFor is how often a screen is redrawn: while the broadcast minimap is on screen
// its markers move between the tracker results (mmFPS a second), which takes frequent frames to look
// smooth (renderDiff and the kitty diff keep each frame's output small); the rest of the
// board changes once a second at most.
func mmTickFor(minimapShown bool) time.Duration {
	if minimapShown {
		return mmMotionTick
	}
	return 500 * time.Millisecond
}

// minimapCfg selects the broadcast source. With an empty Src the best stream of the
// live match is looked up when the pipeline starts.
type minimapCfg struct {
	Src string
	SS  float64
}

// mmResult is the pipeline output for one frame.
type mmResult struct {
	due     time.Time // independent playout time
	idx     int       // stream frame index
	restart bool      // first frame after a stream restart
	layout  string    // locked broadcast layout, "" while undetected
	hud     mmHUD
	visible bool // the minimap is on screen (not a replay, draft or desk shot)
	dets    []mmDet
}

// minimap owns at most one running pipeline. All methods are safe for concurrent use;
// Snapshot and SnapshotAt never block on video, detection or network.
type minimap struct {
	cfg minimapCfg
	// lockTimeout is how long a stream may stay without a readable layout before the
	// next candidate is tried.
	lockTimeout time.Duration
	// OnFrame, when set before Start, receives every processed frame in processing
	// order (not playout order) from the pipeline goroutine.
	OnFrame func(r mmResult)

	mu      sync.Mutex
	gid     string
	cancel  context.CancelFunc
	done    chan struct{}
	status  string
	srcNote string // what is known about the source quality while the layout is undetected
	labels  map[int]string
	ver     string // Data Dragon version of the champion icons
	results []mmResult
	base    time.Time // wall time at which stream frame 0 would have been decoded
	hasBase bool
	arrived time.Time               // wall time of the newest frame (of Start before the first)
	idle    atomic.Bool             // the panel is hidden: the pipeline reads the HUD but does not track
	last    int                     // newest processed frame index, -1 before the first
	sy      *mmSync                 // nil without a feed to sync against
	lives   func(time.Time) mmLives // who is dead at a feed time, for the tracker; nil without a feed
	gen     int                     // pipeline generation; bumped on every Start/Stop so superseded pipelines go silent
	closed  bool
}

func newMinimap(cfg minimapCfg) *minimap {
	return &minimap{cfg: cfg, lockTimeout: mmLockTimeout, last: -1}
}

func (m *minimap) setStatusGen(gen int, s string) {
	m.mu.Lock()
	if m.gen == gen {
		m.status = s
	}
	m.mu.Unlock()
}

// Start launches the pipeline for game gid. It is a no-op while gid is already running
// (also after the pipeline ended: Stop first to retry) and after Close; a new game
// restarts the pipeline. With a non-zero spawn (the game's first feed frame) the
// pipeline syncs against the feed, whose pauses pauses reports; see Sync.
func (m *minimap) Start(gid, matchID string, meta windowResp, spawn time.Time, pauses func() []feedPause) {
	m.mu.Lock()
	if m.closed || (m.cancel != nil && m.gid == gid) {
		m.mu.Unlock()
		return
	}
	if m.cancel != nil {
		m.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	m.gen++
	gen := m.gen
	m.gid, m.cancel, m.done = gid, cancel, done
	m.status, m.srcNote, m.labels, m.results, m.hasBase, m.last = "starting", "", nil, nil, false, -1
	m.arrived = time.Now()
	m.sy = nil
	if !spawn.IsZero() {
		m.sy = newMMSync(spawn, mmFPS, pauses)
	}
	sy := m.sy
	m.mu.Unlock()
	go func() {
		defer close(done)
		m.run(ctx, gen, matchID, meta, sy)
	}()
}

// SetLives sets what the next pipeline asks who is dead at a feed time (the feed's death
// timer); without it the tracker looks for every champion on every frame.
func (m *minimap) SetLives(f func(time.Time) mmLives) {
	m.mu.Lock()
	m.lives = f
	m.mu.Unlock()
}

// Sync returns the estimator of the running pipeline, nil when there is none or it has
// no feed to sync against.
func (m *minimap) Sync() *mmSync {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sy
}

// statusView is a view that shows only a status line.
func (m *minimap) statusView(s string) mmView {
	m.mu.Lock()
	defer m.mu.Unlock()
	return mmView{Ver: m.ver, Status: s}
}

// attach makes m the passive result store of a pipeline the caller drives (replay): it
// supplies the labels, icon version and sync estimator that views are built from; the
// caller feeds results with push and clears them with reset when the video jumps.
func (m *minimap) attach(labels map[int]string, ver string, sy *mmSync) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.labels, m.ver, m.sy = labels, ver, sy
}

// reset drops the buffered results and the pacing base (the video's timeline jumped).
func (m *minimap) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.results, m.hasBase, m.last = nil, false, -1
}

// span returns the stream times (seconds) of the oldest and newest buffered result.
func (m *minimap) span() (lo, hi float64, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.results) == 0 {
		return 0, 0, false
	}
	return float64(m.results[0].idx) / mmFPS, float64(m.results[len(m.results)-1].idx) / mmFPS, true
}

// setStatus sets the status line shown while no result is available.
func (m *minimap) setStatus(s string) {
	m.mu.Lock()
	m.status = s
	m.mu.Unlock()
}

// setSrcNote sets the source-quality note of the status line.
func (m *minimap) setSrcNote(s string) {
	m.mu.Lock()
	m.srcNote = s
	m.mu.Unlock()
}

// VideoLatest returns the stream time (seconds) of the newest processed frame.
func (m *minimap) VideoLatest() (float64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return float64(m.last) / mmFPS, m.last >= 0
}

// Stop cancels the running pipeline without waiting: its goroutine and child processes
// wind down on their own and can no longer touch the displayed state.
func (m *minimap) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
}

func (m *minimap) stopLocked() {
	if m.cancel == nil {
		return
	}
	m.cancel()
	m.cancel, m.gid, m.sy = nil, "", nil
	m.gen++
	m.results, m.last = nil, -1
}

// Close stops the pipeline for good (Start becomes a no-op) and waits up to wait for its
// child processes to be reaped. Call it on exit paths.
func (m *minimap) Close(wait time.Duration) {
	m.mu.Lock()
	m.closed = true
	done := m.done
	m.stopLocked()
	m.mu.Unlock()
	if done == nil {
		return
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
	}
}

// Done is closed when the running pipeline has ended (EOF, fatal error, or Stop); nil if never started.
func (m *minimap) Done() <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.done
}

// mmChampRefs lists the champions of a game: blue team participants are team 100, red 200.
func mmChampRefs(meta windowResp) []champRef {
	var refs []champRef
	for _, t := range []struct {
		team int
		m    teamMeta
	}{{100, meta.GameMetadata.BlueTeamMetadata}, {200, meta.GameMetadata.RedTeamMetadata}} {
		for _, p := range t.m.Participants {
			if p.ChampionID != "" {
				refs = append(refs, champRef{PID: p.ParticipantID, Team: t.team, Champ: p.ChampionID, Role: p.Role})
			}
		}
	}
	return refs
}

// champIcon is a champion with its Data Dragon square icon.
type champIcon struct {
	ref champRef
	img image.Image
}

// mmLoadIcons loads the champion icons and returns them with the Data Dragon version
// they came from.
func mmLoadIcons(ctx context.Context, refs []champRef, patch string) ([]champIcon, string, error) {
	ver := resolveDDVersion(ctx, patch)
	if ver == "" {
		var err error
		if ver, err = ddLatestVersion(ctx); err != nil {
			return nil, "", err
		}
	}
	var icons []champIcon
	for _, r := range refs {
		img, err := ddImage(ctx, ver, "img/champion/"+r.Champ+".png")
		if err != nil {
			continue
		}
		icons = append(icons, champIcon{r, img})
	}
	if len(icons) == 0 {
		return nil, "", fmt.Errorf("no champion icons for patch %s", patch)
	}
	return icons, ver, nil
}

var mmMapPrimed struct {
	sync.Mutex
	done bool
}

// mmPrimeMapRef installs the Data Dragon map as the reference of the map fit, once.
func mmPrimeMapRef(ctx context.Context, ver string) error {
	mmMapPrimed.Lock()
	defer mmMapPrimed.Unlock()
	if mmMapPrimed.done {
		return nil
	}
	img, err := ddImage(ctx, ver, "img/map/map11.png")
	if err != nil {
		return err
	}
	mmSetMapRef(img)
	mmMapPrimed.done = true
	return nil
}

// layoutLock picks the broadcast layout. A layout is adopted only after its clock font
// read the HUD on mmLayoutVotes consecutive reading frames (the clock is decisive:
// leagues share kill digits), and kept until a different layout's clock reads that
// consistently: a hidden HUD (replays, desk shots) never drops the lock.
type layoutLock struct {
	cur   *mmLayout
	cand  *mmLayout
	votes int
}

// vote counts one frame on which layout l read the clock (nil: none did). It reports
// whether the locked layout changed.
func (k *layoutLock) vote(l *mmLayout) bool {
	if l == nil {
		return false
	}
	if l == k.cur {
		k.cand, k.votes = nil, 0
		return false
	}
	if l != k.cand {
		k.cand, k.votes = l, 0
	}
	if k.votes++; k.votes < mmLayoutVotes {
		return false
	}
	k.cur, k.cand, k.votes = l, nil, 0
	return true
}

// mmClockLayout returns the layout whose clock font reads this HUD, nil if none does.
func mmClockLayout(hud, mm []byte) *mmLayout {
	var reads []*mmLayout
	for _, l := range mmLayouts {
		if mmReadHUD(l, hud).ClockOK {
			reads = append(reads, l)
		}
	}
	switch len(reads) {
	case 0:
		return nil
	case 1:
		return reads[0]
	}
	det, _ := mmDetectLayout(hud, mm)
	for _, l := range reads {
		if l == det {
			return l
		}
	}
	return reads[0]
}

// mmPipeline turns decoded frames into results; it is driven by one goroutine.
type mmPipeline struct {
	icons []champIcon
	sy    *mmSync // may be nil
	track bool    // run the map fit and tracker; false while nobody looks at the panel

	lock     layoutLock
	tplFor   *mmLayout // layout the tracker's templates were built for
	trk      *mmTracker
	box      mmBox
	haveBox  bool
	sinceFit int
	clockAt  int  // index of the newest frame whose clock the locked layout read
	visible  bool // the map was seen on the last frame (mapVisible)
	cover    int  // consecutive frames the HUD was up but the map was not (an objective banner)
	crop     []byte
	cropRGBA *image.RGBA // crop as the main-view reader wants it
	mvView   mvView      // the camera viewport last found on the map

	// the HUD of the newest frame whose clock was read: while its plate (everything but
	// the changing digits) is still on screen the HUD is up even if the clock text is not
	// readable for a moment
	plate      []byte
	plateEdges []int32 // indexes of the plate's edge pixels
	plateOK    bool
	// livesAt, when set, tells who is dead at a feed time: the tracker is told who is dead
	// at each frame's synced instant (see lives)
	livesAt func(time.Time) mmLives

	// the last HUD reading: a frame whose HUD bytes equal the previous frame's (the clock
	// ticks once a second, the video runs at mmFPS) reads the same, so it is not read again
	hudPrev []byte
	hudLay  *mmLayout
	hudRead mmHUD
}

// readHUD is mmReadHUD for the pipeline's frames, skipping the OCR when the HUD region is
// byte-identical to the one it last read with the same layout.
func (p *mmPipeline) readHUD(lay *mmLayout, hud []byte) mmHUD {
	if p.hudLay == lay && bytes.Equal(p.hudPrev, hud) {
		return p.hudRead
	}
	p.hudRead, p.hudLay = mmReadHUD(lay, hud), lay
	p.hudPrev = append(p.hudPrev[:0], hud...)
	return p.hudRead
}

func newMMPipeline(icons []champIcon, sy *mmSync) *mmPipeline {
	return &mmPipeline{icons: icons, sy: sy, track: true, clockAt: -mmClockFresh - 1, crop: make([]byte, mmFrameSize*mmFrameSize*3)}
}

// restartSegment forgets what depends on frame continuity (map box, tracking) when the
// video jumps to frame idx; the sync's clock samples are the caller's to keep or drop.
func (p *mmPipeline) restartSegment(idx int) {
	p.haveBox, p.visible, p.cover, p.plateOK = false, false, 0, false
	p.mvView = mvView{}
	p.clockAt = idx - mmClockFresh - 1
	if p.trk != nil {
		p.trk.Reset()
	}
}

// setLayout rebuilds the templates (icon size differs per layout) and forgets the fit.
func (p *mmPipeline) setLayout(l *mmLayout) {
	tpls := make([]champTpl, len(p.icons))
	for i, ic := range p.icons {
		tpls[i] = newChampTpl(ic.ref, ic.img, mmIconD[l.Name])
	}
	p.trk, p.tplFor = newMMTracker(tpls), l
	p.haveBox, p.visible, p.cover, p.plateOK = false, false, 0, false
}

// Process runs one frame through layout, HUD, map fit, visibility gate and tracker.
func (p *mmPipeline) Process(f mmFrame) mmResult {
	r := mmResult{idx: f.Idx, restart: f.Restart}
	if f.Restart {
		// The stream jumped: the sync forgets its clock samples, the map may look different.
		if p.sy != nil {
			p.sy.ObserveRestart()
		}
		p.restartSegment(f.Idx)
	}
	lay := p.lock.cur
	if lay != nil {
		r.hud = p.readHUD(lay, f.HUD)
	}
	if lay == nil || !r.hud.ClockOK {
		if p.lock.vote(mmClockLayout(f.HUD, f.MM)) {
			lay = p.lock.cur
			r.hud = p.readHUD(lay, f.HUD)
		}
	}
	if lay == nil {
		p.visible = false
		return r
	}
	if lay != p.tplFor {
		p.setLayout(lay)
		if p.sy != nil {
			p.sy.SetLayout(lay.Name)
		}
	}
	r.layout = lay.Name
	if r.hud.ClockOK {
		p.clockAt = f.Idx
		p.setPlate(f.HUD)
	}
	if p.sy != nil {
		p.sy.ObserveHUD(f.Idx, r.hud)
	}
	if !p.track {
		p.haveBox, p.visible, p.cover = false, false, 0
		return r
	}
	hudLive := p.hudLive(f.Idx, f.HUD, r.hud.ClockOK)
	switch seen := p.mapVisible(f.MM, lay, hudLive); {
	case seen:
		p.cover = 0
		mmCropMap(f.MM, p.box, p.crop)
		lives := p.lives(f.Idx)
		var obs []mvObs
		if f.Main != nil {
			obs = p.mainObs(f.Idx, f.Main, lay, lives)
		}
		r.dets = p.trk.StepWith(f.Idx, p.crop, lives, obs)
		r.visible = true
	case hudLive && p.cover < mmCoverMax:
		// The HUD is up but the map is not: an objective banner or a one-frame glitch
		// covers it. The tracks coast.
		p.cover++
		r.dets = p.trk.Coast(f.Idx, p.lives(f.Idx))
		r.visible = true
	default:
		// The broadcast shows no minimap (replay, desk or draft shot). The tracks are kept:
		// they only coast, and their search gate widens with the time since they were seen,
		// so every champion is picked up again where the minimap returns.
		if hudLive {
			p.cover++
		} else {
			p.cover = 0
		}
	}
	return r
}

// lives is the feed's word on who is dead at frame idx's synced instant; nil while the
// pipeline has no feed or its sync is not locked.
func (p *mmPipeline) lives(idx int) mmLives {
	if p.livesAt == nil || p.sy == nil || !p.sy.Locked() {
		return nil
	}
	ft, ok := p.sy.FeedTime(float64(idx) / mmFPS)
	if !ok {
		return nil
	}
	return p.livesAt(ft)
}

// hudLive reports whether the scoreboard is up on frame idx: its plate (the HUD but for
// the changing digits) still looks like the last clock-read HUD, and the clock was read
// within mmClockFresh frames or, since the clock text fails to read for a few seconds now
// and then in live play, within mmClockLong frames. Replays, desk and draft shots replace
// the whole plate, so they end it at once; an objective banner covers the map but leaves
// the plate intact.
func (p *mmPipeline) hudLive(idx int, hud []byte, clockOK bool) bool {
	if clockOK {
		return true
	}
	age := idx - p.clockAt
	if p.plateOK && p.plateShare(hud) < mmPlateLive {
		return false
	}
	return age <= mmClockFresh || (age <= mmClockLong && p.plateOK)
}

// setPlate makes hud (a mmRegionHUD-sized rgb24 crop) the reference plate: what it shows
// at its edges, the text, logos and rules that stay put when only the digits change. Flat
// areas say nothing: a replay shot that is dark in the HUD region would match them.
func (p *mmPipeline) setPlate(hud []byte) {
	w, h := mmRegionHUD.W, mmRegionHUD.H
	p.plateOK = false
	if len(hud) != w*h*3 {
		return
	}
	p.plate = append(p.plate[:0], hud...)
	p.plateEdges = p.plateEdges[:0]
	lum := func(i int) int { return int(hud[3*i]) + int(hud[3*i+1]) + int(hud[3*i+2]) }
	for y := 0; y < h-1; y++ {
		for x := 0; x < w-1; x++ {
			i := y*w + x
			if absInt(lum(i)-lum(i+1))+absInt(lum(i)-lum(i+w)) >= mmPlateEdge {
				p.plateEdges = append(p.plateEdges, int32(i))
			}
		}
	}
	p.plateOK = len(p.plateEdges) >= mmPlateMinEdges
}

// plateShare is the share of the reference plate's edge pixels that hud still shows
// within mmPlateTol.
func (p *mmPipeline) plateShare(hud []byte) float64 {
	if !p.plateOK || len(hud) != len(p.plate) {
		return 0
	}
	same := 0
	for _, i := range p.plateEdges {
		o := 3 * int(i)
		if absInt(int(p.plate[o])-int(hud[o]))+absInt(int(p.plate[o+1])-int(hud[o+1]))+absInt(int(p.plate[o+2])-int(hud[o+2])) <= mmPlateTol {
			same++
		}
	}
	return float64(same) / float64(len(p.plateEdges))
}

// refit looks for the map box around the layout's nominal one.
func (p *mmPipeline) refit(mm []byte, l *mmLayout) {
	box, _, ok := mmFitMap(mm, l.Map)
	p.box, p.haveBox, p.sinceFit = box, ok, 0
}

// mapVisible refits the box every mmFitEvery frames and whenever the visibility flips
// (a flip usually means a different scene, whose box may differ), then reports whether
// the live minimap is on screen: the map fits, shows terrain, and the HUD is up (hudLive;
// replays, desk and draft shots hide it, even where they show a replayed minimap).
func (p *mmPipeline) mapVisible(mm []byte, l *mmLayout, hudLive bool) bool {
	if !p.haveBox || p.sinceFit >= mmFitEvery {
		p.refit(mm, l)
	}
	p.sinceFit++
	look := func() bool { return p.haveBox && hudLive && mmHasTerrain(mm, p.box) }
	vis := look()
	if vis != p.visible && p.sinceFit > 1 {
		p.refit(mm, l)
		p.sinceFit++
		vis = look()
	}
	p.visible = vis
	return vis
}

func (m *minimap) run(ctx context.Context, gen int, matchID string, meta windowResp, sy *mmSync) {
	status := func(s string) { m.setStatusGen(gen, s) }
	defer m.finish(ctx, gen)
	refs := mmChampRefs(meta)
	if len(refs) == 0 {
		status("error: no champions in game metadata")
		return
	}
	src := m.cfg.Src
	status("loading champion icons")
	icons, ver, err := mmLoadIcons(ctx, refs, meta.GameMetadata.PatchVersion)
	if err != nil {
		status("error: " + err.Error())
		return
	}
	if err := mmPrimeMapRef(ctx, ver); err != nil {
		status("error: map image: " + err.Error())
		return
	}
	cands := []string{src}
	if src == "" {
		status("finding broadcast")
		if cands, err = pickStreamURLs(ctx, matchID); err != nil {
			status("error: " + err.Error())
			return
		}
	}
	for _, c := range cands {
		if missing := mmToolsMissing(c); len(missing) > 0 {
			status(fmt.Sprintf("missing tools: %s — %s", strings.Join(missing, ", "), mmToolsHint))
			return
		}
	}
	m.mu.Lock()
	if m.gen == gen {
		m.labels, m.ver = mmLabels(refs), ver
	}
	m.mu.Unlock()

	for i, c := range cands {
		if !m.runSource(ctx, gen, c, icons, sy, i+1 < len(cands)) {
			return
		}
		m.mu.Lock()
		if m.gen == gen { // the next stream numbers its frames afresh
			m.results, m.hasBase, m.last = nil, false, -1
		}
		m.mu.Unlock()
	}
}

// mmSourceNote describes a source of the given height for the status line.
func mmSourceNote(h int) string {
	if h > 0 && h < 1080 {
		return fmt.Sprintf("%dp source", h)
	}
	return ""
}

// runSource plays one broadcast through the pipeline. It reports true when the layout
// stayed unread for mmLockTimeout and another stream is left to try.
func (m *minimap) runSource(ctx context.Context, gen int, src string, icons []champIcon, sy *mmSync, hasNext bool) bool {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var height atomic.Int32
	opts := mmVideoOpts{Src: src, SS: m.cfg.SS, FPS: mmFPS, Main: true, OnHeight: func(h int) {
		height.Store(int32(h))
		m.mu.Lock()
		if m.gen == gen {
			m.srcNote = mmSourceNote(h)
		}
		m.mu.Unlock()
	}}
	frames, err := startMMVideo(sctx, opts, func(s string) { m.setStatusGen(gen, s) })
	if err != nil {
		m.setStatusGen(gen, "error: "+err.Error())
		return false
	}
	pipe := newMMPipeline(icons, sy)
	m.mu.Lock()
	pipe.livesAt = m.lives
	m.mu.Unlock()
	timeout := time.NewTimer(m.lockTimeout)
	defer timeout.Stop()
	giveUp := false
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				return giveUp
			}
			if ctx.Err() != nil || giveUp {
				mmReleaseFrame(f)
				continue // drain so the video goroutine can exit
			}
			arrived := time.Now()
			pipe.track = !m.idle.Load()
			r := pipe.Process(f)
			mmReleaseFrame(f)
			if !m.pushGen(gen, arrived, r) {
				continue
			}
			if m.OnFrame != nil {
				m.OnFrame(r)
			}
		case <-timeout.C:
			if pipe.lock.cur != nil {
				continue
			}
			note := mmSourceNote(int(height.Load()))
			if note != "" {
				note += ": "
			}
			note += "HUD unreadable"
			if hasNext {
				note += ", trying next stream"
				giveUp = true
				cancel()
			}
			m.mu.Lock()
			if m.gen == gen {
				m.srcNote = note
			}
			m.mu.Unlock()
		}
	}
}

// finish runs when a pipeline ends. If it was not cancelled (EOF or fatal error) the
// buffered results are dropped so stale markers disappear, and the status says why.
func (m *minimap) finish(ctx context.Context, gen int) {
	if ctx.Err() != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.gen != gen {
		return
	}
	m.results = nil
	if !strings.HasPrefix(m.status, "error") && !strings.HasPrefix(m.status, "missing") {
		m.status = "stream ended"
	}
}

// push stores a result: frame idx is due for independent playout at base + idx/fps +
// buffer, where base is the earliest observed (arrival - idx/fps), i.e. the fastest the
// stream was ever delivered.
func (m *minimap) push(r mmResult, arrived time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pushLocked(r, arrived)
}

// pushGen is push for pipeline gen; it reports false when that pipeline was superseded.
func (m *minimap) pushGen(gen int, arrived time.Time, r mmResult) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.gen != gen {
		return false
	}
	m.pushLocked(r, arrived)
	return true
}

func (m *minimap) pushLocked(r mmResult, arrived time.Time) {
	if r.restart {
		m.hasBase = false // the stream's timeline jumped: pace the new segment afresh
	}
	if b := arrived.Add(-mmFrameTime(r.idx)); !m.hasBase || b.Before(m.base) {
		m.base, m.hasBase = b, true
	}
	r.due = m.base.Add(mmFrameTime(r.idx) + mmPlayoutBuffer)
	m.results = append(m.results, r)
	if n := len(m.results); n > mmResultsKeep {
		clear(m.results[:n-mmResultsKeep]) // drop the references; append reallocates (copying the kept results) only when the capacity runs out
		m.results = m.results[n-mmResultsKeep:]
	}
	m.last = r.idx
	m.arrived = arrived
}

// SetPanel tells the pipeline whether the minimap panel is on screen. While it is not,
// frames are still read for the HUD (the sync needs it) but not tracked, which saves the
// detection's CPU.
func (m *minimap) SetPanel(shown bool) { m.idle.Store(!shown) }

// Stalled reports whether no video frame arrived for mmStallAfter of wall-clock time,
// with the status line that says so: the pipeline's own status tells whether and when it
// restarts.
func (m *minimap) Stalled() (mmView, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idle := time.Since(m.arrived)
	if m.cancel == nil || idle < mmStallAfter {
		return mmView{}, false
	}
	return mmView{Ver: m.ver, Status: fmt.Sprintf("video stalled %ds · %s", int(idle.Seconds()), m.status)}, true
}

func mmFrameTime(idx int) time.Duration { return time.Duration(idx) * time.Second / mmFPS }

// mmPick returns the latest result due at or before now and the queue starting at it
// (older results are dropped).
func mmPick(results []mmResult, now time.Time) (mmResult, []mmResult, bool) {
	k := -1
	for i, r := range results {
		if r.due.After(now) {
			break
		}
		k = i
	}
	if k < 0 {
		return mmResult{}, results, false
	}
	return results[k], results[k:], true
}

const (
	mmStaleAfter   = 2 * mmFPS // frames (2 s) a champion the tracker lost is still drawn like a tracked one
	mmJumpMax      = 0.25      // a marker is not interpolated over more than a quarter of the map: that is a teleport, a recall or a respawn
	mmInterpMaxGap = 1.5       // results farther apart than this many seconds are not interpolated
)

// mmBasePos is where the dead champion with participant id pid of team waits: in a row
// along the bottom edge for blue (the fountain is bottom-left) and along the top edge
// for red (top-right), five slots to a team.
func mmBasePos(team, pid int) (x, y float64) {
	slot := float64((pid - 1) % 5)
	if team == 200 {
		return 0.92 - 0.085*slot, 0.10
	}
	return 0.08 + 0.085*slot, 0.90
}

// mmChamps are the champions drawn for one result, as terminal markers and as icons, with
// the counts the status line reports.
type mmChamps struct {
	Markers []mmMarker
	Icons   []mmIcon
	Live    int // drawn at a position the tracker found (or lost for a moment)
	Stale   int // drawn at the last seen position of a champion the tracker lost
	Dead    int // dead according to the feed: drawn at the base with the respawn countdown
	Alive   int // every champion that is not dead
}

// mmChampsOf decides what is drawn for result r. With lives (the feed's word on who is
// dead), a dead champion waits at its base with the countdown, a live one the tracker
// lost stays at its last seen position (dimmed after mmStaleAfter frames) until it is
// found again, and a position seen before the champion's last death does not count. Without
// lives only what the tracker found within mmHoldShort frames is drawn. When nx (the next
// result, frac of the way from r to it) is given, a marker found in both moves linearly
// between the two positions unless that is a jump (mmJumpMax).
func mmChampsOf(r mmResult, nx *mmResult, frac float64, lives mmLives, labels map[int]string) mmChamps {
	var c mmChamps
	for k, d := range r.dets {
		lf, known := lives[d.PID]
		if lives != nil && !known {
			lf.AliveFor = math.Inf(1)
		}
		x, y, state, secs := d.X, d.Y, mmIconLive, 0
		switch {
		case lives != nil && lf.Dead:
			if !d.Visible { // never tracked: it waits at its base
				x, y = mmBasePos(d.Team, d.PID)
			}
			state, secs = mmIconDead, int(math.Ceil(lf.Secs))
			c.Dead++
		case !d.Visible:
			continue
		case lives == nil:
			if d.Age > mmHoldShort {
				continue
			}
		default:
			if float64(d.Age)/mmFPS > lf.AliveFor+1 {
				continue // last seen before it died
			}
			if d.Age > mmStaleAfter {
				state, secs = mmIconStale, int(math.Round(float64(d.Age)/mmFPS))
			}
		}
		if state == mmIconLive && nx != nil && frac > 0 && r.visible && nx.visible && k < len(nx.dets) {
			if nd := nx.dets[k]; nd.PID == d.PID && nd.Visible && nd.Age == 0 && math.Hypot(nd.X-d.X, nd.Y-d.Y) < mmJumpMax {
				x, y = d.X+(nd.X-d.X)*frac, d.Y+(nd.Y-d.Y)*frac
			}
		}
		switch state {
		case mmIconLive:
			c.Live++
		case mmIconStale:
			c.Stale++
		}
		c.Markers = append(c.Markers, mmMarker{Team: d.Team, Label: labels[d.PID], X: x, Y: y, State: state, Secs: secs})
		c.Icons = append(c.Icons, mmIcon{Team: d.Team, Champ: d.Champ, Label: labels[d.PID], X: x, Y: y, State: state, Secs: secs})
	}
	c.Alive = len(r.dets) - c.Dead
	return c
}

// viewLocked builds the panel content for result r (nx: the next result, frac of the way
// to it, see mmChampsOf): markers plus a status line such as "LCK · sync ✓ ±0.4s ·
// tracking 7/8 (1 est.) · 2 dead". The counts are against the champions the feed says are
// alive; est. are those drawn at their last seen position.
func (m *minimap) viewLocked(r mmResult, nx *mmResult, frac float64, lives mmLives) mmView {
	v := mmView{Ver: m.ver}
	var parts []string
	if r.layout == "" {
		parts = append(parts, "detecting broadcast layout")
		if m.srcNote != "" {
			parts = append(parts, m.srcNote)
		}
	} else {
		parts = append(parts, strings.ToUpper(r.layout))
	}
	if m.sy != nil {
		parts = append(parts, m.sy.Status())
	}
	switch {
	case r.layout == "":
	case !r.visible:
		parts = append(parts, "minimap hidden on broadcast")
	default:
		c := mmChampsOf(r, nx, frac, lives, m.labels)
		v.Markers, v.Icons = c.Markers, c.Icons
		s := fmt.Sprintf("tracking %d/%d", c.Live, c.Alive)
		if c.Stale > 0 {
			s += fmt.Sprintf(" (%d est.)", c.Stale)
		}
		parts = append(parts, s)
		if c.Dead > 0 {
			parts = append(parts, fmt.Sprintf("%d dead", c.Dead))
		}
	}
	v.Status = strings.Join(parts, " · ")
	return v
}

// waitingLocked is the view while no result is due: the pipeline status says why.
func (m *minimap) waitingLocked(buffering string) mmView {
	s := m.status
	if buffering != "" {
		s = buffering + " · " + s
	}
	return mmView{Ver: m.ver, Status: s}
}

// Snapshot returns the champions and status line to draw at now, playing the stream out
// on its own delay (the independent mode used while the feed sync is not locked). lives
// is the feed's word on who is dead at that moment, nil without one. Mode and cell size
// are the caller's to fill in.
func (m *minimap) Snapshot(now time.Time, lives mmLives) mmView {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, rest, ok := mmPick(m.results, now)
	if !ok {
		if len(m.results) > 0 {
			return m.waitingLocked(fmt.Sprintf("buffering %ds", int(m.results[0].due.Sub(now).Seconds())+1))
		}
		return m.waitingLocked("")
	}
	m.results = rest
	var nx *mmResult
	frac := 0.0
	if len(rest) > 1 && !rest[1].restart {
		if gap := rest[1].due.Sub(r.due).Seconds(); gap > 0 && gap <= mmInterpMaxGap {
			nx, frac = &rest[1], min(1, max(0, now.Sub(r.due).Seconds()/gap))
		}
	}
	return m.viewLocked(r, nx, frac, lives)
}

// SnapshotAt returns the champions and status line for stream time t seconds: the latest
// frame not past t, with markers moved on towards the next frame by how far t is between
// the two. lives is the feed's word on who is dead at that moment, nil without one. Mode
// and cell size are the caller's to fill in.
func (m *minimap) SnapshotAt(t float64, lives mmLives) mmView {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := sort.Search(len(m.results), func(i int) bool { return float64(m.results[i].idx)/mmFPS > t }) - 1
	if k < 0 {
		return m.waitingLocked("buffering")
	}
	r := m.results[k]
	var nx *mmResult
	frac := 0.0
	if k+1 < len(m.results) && !m.results[k+1].restart {
		n := m.results[k+1]
		tk := float64(r.idx) / mmFPS
		if gap := float64(n.idx)/mmFPS - tk; gap > 0 && gap <= mmInterpMaxGap {
			nx, frac = &n, min(1, max(0, (t-tk)/gap))
		}
	}
	m.results = m.results[max(0, k-mmKeepBack):]
	return m.viewLocked(r, nx, frac, lives)
}

// mmGameXY converts normalized minimap coordinates to game units.
func mmGameXY(x, y float64) (gx, gy float64) {
	return -120 + x*14990, 14980 - y*15100
}

// Status returns the latest pipeline status (video, tools, errors) without playout state.
func (m *minimap) Status() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

type mmJSONChamp struct {
	PID     int      `json:"pid"`
	Champ   string   `json:"champ"`
	Role    string   `json:"role"`
	Team    int      `json:"team"`
	Visible bool     `json:"visible"`
	X       *float64 `json:"x,omitempty"`
	Y       *float64 `json:"y,omitempty"`
	Score   float32  `json:"score"`
	// AgeSec is how long ago the champion was last measured (0: on this frame); Covered:
	// not measured but estimated under another icon; MV: measured from the main view.
	AgeSec  float64 `json:"ageSec"`
	Covered bool    `json:"covered,omitempty"`
	MV      bool    `json:"mv,omitempty"`
}

// mmJSONFrame is one -json line. Present: a broadcast layout is locked; Visible: its
// minimap is on screen; Clock and Kills are the HUD readings, null when unreadable.
type mmJSONFrame struct {
	Idx       int           `json:"idx"`
	Present   bool          `json:"present"`
	Layout    string        `json:"layout"`
	Clock     *int          `json:"clock"`
	Kills     *[2]int       `json:"kills"`
	Visible   bool          `json:"visible"`
	Champions []mmJSONChamp `json:"champions"`
}

// mmJSONLine encodes one processed frame; positions are in game units.
func mmJSONLine(r mmResult) []byte {
	f := mmJSONFrame{Idx: r.idx, Present: r.layout != "", Layout: r.layout, Visible: r.visible, Champions: make([]mmJSONChamp, 0, len(r.dets))}
	if r.hud.ClockOK {
		f.Clock = &r.hud.ClockSec
	}
	if r.hud.KillsOK {
		f.Kills = &[2]int{r.hud.Blue, r.hud.Red}
	}
	for _, d := range r.dets {
		c := mmJSONChamp{PID: d.PID, Champ: d.Champ, Role: d.Role, Team: d.Team, Visible: d.Visible, Score: d.Score, AgeSec: float64(d.Age) / mmFPS, Covered: d.Covered, MV: d.MV}
		if d.Visible {
			x, y := mmGameXY(d.X, d.Y)
			c.X, c.Y = &x, &y
		}
		f.Champions = append(f.Champions, c)
	}
	b, _ := json.Marshal(f)
	return b
}

func cmdMinimap(args []string) {
	fs := flag.NewFlagSet("lol minimap", flag.ExitOnError)
	src := fs.String("src", "", "video source: page URL, media URL or file (default: the live match's broadcast)")
	ss := fs.Float64("ss", 0, "start offset in seconds (VOD/file)")
	gameID := fs.String("game", "", "livestats game id giving the champion set (default: current live game)")
	n := fs.Int("n", 0, "frames to process, then exit (0 = until the source ends or ctrl+c)")
	asJSON := fs.Bool("json", false, "print one JSON line per processed frame instead of the panel")
	icons := fs.String("icons", mmModeAuto, "panel drawing: auto|kitty|blocks|text")
	fs.Parse(args)

	// Validate -icons before any network call. Only the panel needs the terminal probe.
	var err error
	mode := mmModeText
	if *asJSON {
		_, err = mmPickMode(*icons, false, false)
	} else {
		mode, err = mmResolveMode(*icons)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	var ev event
	gid := *gameID
	if gid == "" || *src == "" {
		live, liveGID, ok := fetchLiveEvent("", "")
		if !ok && gid == "" {
			fmt.Fprintln(os.Stderr, "error: no live game; pass -game <gameId>")
			os.Exit(1)
		}
		if !ok && *src == "" {
			fmt.Fprintln(os.Stderr, "error: no live match; pass -src <url|file>")
			os.Exit(1)
		}
		if ok && gid == "" {
			gid = liveGID
		}
		if ok {
			ev = live
		}
	}
	var meta windowResp
	if err := getJSON(feedBase+"/window/"+gid, &meta); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	mm := newMinimap(minimapCfg{Src: *src, SS: *ss})
	finished := make(chan struct{})
	var count int
	mm.OnFrame = func(r mmResult) {
		if *asJSON {
			fmt.Printf("%s\n", mmJSONLine(r))
		}
		if count++; count == *n {
			close(finished)
		}
	}

	var fail string
	if *asJSON {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
		mm.Start(gid, eventID(ev), meta, time.Time{}, nil)
		select {
		case <-finished:
		case <-mm.Done():
		case <-sig:
		}
		mm.Close(mmCloseWait)
		fail = mm.failure()
	} else {
		fail = runMinimapPanel(mm, mode, gid, eventID(ev), meta, finished)
	}
	if fail != "" {
		fmt.Fprintln(os.Stderr, fail)
		os.Exit(1)
	}
}

// failure returns the pipeline's status if it ended with an error or missing tools, else "".
func (m *minimap) failure() string {
	if st := m.Status(); strings.HasPrefix(st, "error") || strings.HasPrefix(st, "missing") {
		return st
	}
	return ""
}

// runMinimapPanel shows the fullscreen panel until q, ctrl+c, -n frames, or the pipeline
// ends. It restores the terminal before returning the failure status ("" on success).
func runMinimapPanel(mm *minimap, mode, gid, matchID string, meta windowResp, finished <-chan struct{}) string {
	var kit kittyOut
	keys, leave := enterFullscreen(func() { mm.Close(mmCloseWait); fmt.Print(kit.Delete()) })
	defer leave()
	defer func() { fmt.Print(kit.Delete()) }()
	defer mm.Close(mmCloseWait)
	mm.Start(gid, matchID, meta, time.Time{}, nil)
	done := mm.Done()
	var prev *canvas
	tick := time.NewTicker(mmMotionTick)
	defer tick.Stop()
	for {
		t, h := termSize()
		var c *canvas
		var slot *mmSlot
		if t < minBoardW || h < 12 {
			c = tooSmallCanvas(t, h, minBoardW, 12)
		} else {
			view := mm.Snapshot(time.Now(), nil)
			view.Mode = mode
			view.CellW, view.CellH = termCellPx()
			rows := []line{topBorderL(t)}
			var panel []line
			panel, slot = mmPanelView(view, t, h-3)
			if slot != nil {
				slot.Row += len(rows)
			}
			rows = append(rows, panel...)
			rows = append(rows, borderedL(line{C("broadcast minimap · q quit", cDim)}, t), botBorderL(t))
			c = linesToCanvas(rows, t)
		}
		out := renderDiff(prev, c)
		fmt.Print(out + kit.Frame(slot, prev == nil || prev.w != c.w || prev.h != c.h))
		prev = c
		select {
		case <-tick.C:
		case <-done:
			if fail := mm.failure(); fail != "" {
				return fail
			}
			return ""
		case <-finished:
			return ""
		case k := <-keys:
			if k.Kind == keyCtrlC || (k.Kind == keyRune && (k.Rune == 'q' || k.Rune == 'Q')) {
				return ""
			}
			prev = nil
		}
	}
}
