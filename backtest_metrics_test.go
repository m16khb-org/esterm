//go:build backtest

package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Marker states of one champion on one frame, as the app would draw it.
const (
	btNone   = iota // frame outside the measured stretch of the game
	btGood          // drawn at a position found within the last mmStaleAfter frames
	btStale         // drawn dimmed at the last seen position
	btAbsent        // alive per the feed, minimap on, nothing drawn
	btDeadSt        // dead per the feed: drawn at the base with the respawn timer
	btHidden        // alive, but the minimap is hidden on the broadcast
)

type btFlip struct{ tv, d, o float64 }

type btChamp struct {
	PID            int     `json:"pid"`
	Champ          string  `json:"champ"`
	Team           int     `json:"team"`
	Role           string  `json:"role"`
	AliveFrames    int     `json:"aliveFrames"`
	Fresh          int     `json:"fresh"`
	Ratio          float64 `json:"detectedRatio"`
	LongestMissSec float64 `json:"longestMissSec"` // longest run of alive frames without a fresh detection
	LongestGapSec  float64 `json:"longestGapSec"`  // longest run of frames where the marker was missing or stale
}

type btDeath struct {
	PID     int     `json:"pid"`
	Champ   string  `json:"champ"`
	FeedSec float64 `json:"feedSec"`
	Tracked bool    `json:"tracked"` // the champion was measured on the minimap within the minute before it died
	AgeSec  float64 `json:"ageSec"`  // seconds between that last measured position and the death
	LastX   float64 `json:"lastX"`   // normalized map position, last measured before the death
	LastY   float64 `json:"lastY"`
	Shown   bool    `json:"shown"`  // a dead marker was drawn within a few seconds after the death
	ShownX  float64 `json:"shownX"` // where it was drawn
	ShownY  float64 `json:"shownY"`
	Dist    float64 `json:"dist"` // normalized distance between the two
}

type btGap struct {
	Champ    string         `json:"champ"`
	Team     int            `json:"team"`
	FeedSec  float64        `json:"feedSec"`
	VideoSec float64        `json:"videoSec"`
	DurSec   float64        `json:"durSec"`
	Cause    string         `json:"cause"`
	Causes   map[string]int `json:"causes"` // frames per cause
	mid      int            // frame index of the middle, for the annotated frame
	k        int
}

// btHide is an interval of more than 3 s of the game during which the pipeline showed no
// minimap although the broadcast was in a layout it had locked. A hide is FALSE when most
// of its frames still show the minimap (map fits, terrain) and the HUD plate: live play the
// pipeline failed to recognise; else TRUE (replay, desk, draft, ad: the broadcast is
// elsewhere).
type btHide struct {
	VideoSec float64 `json:"videoSec"`
	FeedSec  float64 `json:"feedSec"`
	DurSec   float64 `json:"durSec"`
	Class    string  `json:"class"` // "true" | "false"
	Detail   string  `json:"detail"`
	PlateAvg float64 `json:"plateShareAvg"`
	FitAvg   float64 `json:"fitAvg"`
	ClockOK  float64 `json:"clockReadShare"`
	mid      int
}

type btCauseSum struct {
	Count    int     `json:"count"`
	TotalSec float64 `json:"totalSec"`
	Longest  float64 `json:"longestSec"`
}

type btStats struct {
	Label    string  `json:"label"`
	Layout   string  `json:"layout"`
	Frames   int     `json:"frames"`
	GameMin  float64 `json:"gameMinutes"`
	RealTime float64 `json:"realTime"` // video seconds decoded and processed per wall second

	LockSec      float64 `json:"layoutLockSec"` // -1: never
	SyncSec      float64 `json:"syncLockSec"`
	LayoutOK     bool    `json:"layoutOK"`
	Switches     int     `json:"layoutSwitches"`
	Relocks      int     `json:"relocks"`
	RelocksWrong int     `json:"relocksWrong"` // relocks the kill flips do not confirm

	Flips          int       `json:"flips"` // matched kill flips measured
	FlipsOK        int       `json:"flipsWithinTol"`
	FlipsUnsynced  int       `json:"flipsBeforeLock"`
	FlipsTight     int       `json:"flipsTightBracket"` // flips whose old/new counter frames are at most 1 s apart
	FlipsTightOK   int       `json:"flipsTightBracketWithinTol"`
	FlipsLate      int       `json:"flipsAfterThird"` // flips after the third matched one, when the skew is learned
	FlipsLateOK    int       `json:"flipsAfterThirdWithinTol"`
	FlipFalse      int       `json:"hudFlipsUnknownToFeed"`
	FlipMissed     int       `json:"feedKillsNeverOnHUD"`
	Deltas         []float64 `json:"-"`
	FlipShare      float64   `json:"flipShare"`
	FlipTightShare float64   `json:"flipShareTightBracket"`
	FlipMedian     float64   `json:"flipMedianAbs"`
	FlipP90        float64   `json:"flipP90Abs"`

	AliveN        int       `json:"aliveChampFrames"`
	AliveHit      int       `json:"aliveFound"`
	AliveCov      int       `json:"aliveCoveredNotMeasured"` // of those, inferred under another icon, not measured on the frame
	IsoN          int       `json:"aliveIsolatedChampFrames"`
	IsoHit        int       `json:"aliveIsolatedFound"`
	ClusN         int       `json:"aliveClusteredChampFrames"`
	ClusHit       int       `json:"aliveClusteredFound"`
	DeadN         int       `json:"deadChampFrames"`
	DeadHit       int       `json:"deadFound"`
	DeadAtBase    int       `json:"deadFoundNearOwnFountain"` // of those, found within 0.25 of the team's fountain corner
	AliveRatio    float64   `json:"aliveDetected"`
	AliveMeasured float64   `json:"aliveMeasured"` // share of alive champion frames with a measurement of the champion's icon on that frame
	IsoRatio      float64   `json:"aliveDetectedIsolated"`
	ClusterRatio  float64   `json:"aliveDetectedClustered"`
	DeadRatio     float64   `json:"deadDetected"`
	TrackedFrames int       `json:"trackedFrames"`
	Champs        []btChamp `json:"champions,omitempty"`

	RespErrs   []float64 `json:"respawnErrors,omitempty"`
	RespN      int       `json:"respawnN"`
	RespMedian float64   `json:"respawnMedianAbs"`
	RespP90    float64   `json:"respawnP90Abs"`

	Deaths       []btDeath `json:"deaths,omitempty"`
	DeathMedian  float64   `json:"deathPosMedianDist"`
	DeathP90     float64   `json:"deathPosP90Dist"`
	DeathTracked int       `json:"deathsTracked"`
	DeathWithin  int       `json:"deathsWithinTol"` // tracked deaths whose marker was drawn within btDeathTol of the last measured position
	DeathBase    int       `json:"deathsDrawnAtBase"`

	Flickers, MapBlips int
	FlickerRate        float64 `json:"flickersPerMin"`

	ClockN, ClockOK int
	ClockShare      float64 `json:"clockReadShare"`

	Hides    []btHide              `json:"hides,omitempty"`
	HideSum  map[string]btCauseSum `json:"hidesByClass"`
	Gaps     []btGap               `json:"gaps,omitempty"`
	GapSum   map[string]btCauseSum `json:"gapsByCause"`
	GapTotal btCauseSum            `json:"gapsTotal"`

	MsFrame, CPUMsFrame, WallMsFrame float64
	Verbose                          []string `json:"-"`
	Fails                            []string `json:"fails,omitempty"`
}

func btPctl(xs []float64, q float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	c := append([]float64(nil), xs...)
	sort.Float64s(c)
	return c[min(len(c)-1, int(q*float64(len(c)-1)+0.5))]
}

func btRatio(a, b int) float64 {
	if b == 0 {
		return math.NaN()
	}
	return float64(a) / float64(b)
}

func btAbs(xs []float64) []float64 {
	out := make([]float64, len(xs))
	for i, x := range xs {
		out[i] = math.Abs(x)
	}
	return out
}

// derive fills the figures computed from the raw counts and samples.
func (s *btStats) derive() {
	s.FlipShare = btRatio(s.FlipsOK, s.Flips)
	s.FlipTightShare = btRatio(s.FlipsTightOK, s.FlipsTight)
	s.FlipMedian, s.FlipP90 = btPctl(btAbs(s.Deltas), 0.5), btPctl(btAbs(s.Deltas), 0.9)
	s.AliveRatio, s.DeadRatio = btRatio(s.AliveHit, s.AliveN), btRatio(s.DeadHit, s.DeadN)
	s.AliveMeasured = btRatio(s.AliveHit-s.AliveCov, s.AliveN)
	s.IsoRatio, s.ClusterRatio = btRatio(s.IsoHit, s.IsoN), btRatio(s.ClusHit, s.ClusN)
	s.RespN = len(s.RespErrs)
	s.RespMedian, s.RespP90 = btPctl(btAbs(s.RespErrs), 0.5), btPctl(btAbs(s.RespErrs), 0.9)
	var dd []float64
	s.DeathTracked, s.DeathWithin = 0, 0
	for _, d := range s.Deaths {
		if d.Tracked && d.Shown {
			dd = append(dd, d.Dist)
			if d.Dist <= btDeathTol {
				s.DeathWithin++
			}
		}
		if d.Tracked {
			s.DeathTracked++
		}
	}
	s.DeathMedian, s.DeathP90 = btPctl(dd, 0.5), btPctl(dd, 0.9)
	if s.GameMin > 0 {
		s.FlickerRate = float64(s.Flickers+s.MapBlips) / s.GameMin
	}
	s.ClockShare = btRatio(s.ClockOK, s.ClockN)
	s.HideSum = map[string]btCauseSum{}
	for _, h := range s.Hides {
		c := s.HideSum[h.Class]
		c.Count++
		c.TotalSec += h.DurSec
		c.Longest = math.Max(c.Longest, h.DurSec)
		s.HideSum[h.Class] = c
	}
	s.GapSum, s.GapTotal = map[string]btCauseSum{}, btCauseSum{}
	for _, g := range s.Gaps {
		c := s.GapSum[g.Cause]
		c.Count++
		c.TotalSec += g.DurSec
		c.Longest = math.Max(c.Longest, g.DurSec)
		s.GapSum[g.Cause] = c
		s.GapTotal.Count++
		s.GapTotal.TotalSec += g.DurSec
		s.GapTotal.Longest = math.Max(s.GapTotal.Longest, g.DurSec)
	}
}

// gate lists the pass criteria s misses.
func (s *btStats) gate() []string {
	var f []string
	add := func(format string, a ...any) { f = append(f, fmt.Sprintf(format, a...)) }
	if !s.LayoutOK || s.Switches > 0 {
		add("layout: locked %v, switches %d (want only %q)", s.LayoutOK, s.Switches, s.Layout)
	}
	if !(s.LockSec >= 0 && s.LockSec <= btLockMax) {
		add("layout lock %.1fs > %.0fs", s.LockSec, btLockMax)
	}
	if s.RelocksWrong > 0 {
		add("%d wrong sync relocks of %d (want 0)", s.RelocksWrong, s.Relocks)
	}
	// Only flips whose old and new counter frames are at most 1 s apart can be judged to 1 s:
	// the flip time is the middle of that bracket (see btMeasure). The share over all flips
	// is reported next to it.
	if !(s.FlipTightShare >= btKillFlipMin) {
		add("kill flips within %.0fs %.1f%% (%d/%d with a tight bracket; %.1f%% of all %d) < %.0f%%", btKillFlipTol, 100*s.FlipTightShare, s.FlipsTightOK, s.FlipsTight, 100*s.FlipShare, s.Flips, 100*btKillFlipMin)
	}
	if !(s.AliveRatio >= btAliveMin) {
		add("alive detected %.1f%% < %.0f%%", 100*s.AliveRatio, 100*btAliveMin)
	}
	if !(s.DeadRatio <= btDeadMax) {
		add("dead detected %.2f%% > %.0f%%", 100*s.DeadRatio, 100*btDeadMax)
	}
	if !(s.RespMedian <= btRespMedian && s.RespP90 <= btRespP90) {
		add("respawn |err| median %.2fs p90 %.2fs (limits %.1f/%.1f)", s.RespMedian, s.RespP90, btRespMedian, btRespP90)
	}
	if !(s.FlickerRate <= btFlickerMax) {
		add("flicker %.2f/min > %.1f", s.FlickerRate, btFlickerMax)
	}
	if !(s.ClockShare >= btClockMin) {
		add("clock read %.1f%% < %.0f%%", 100*s.ClockShare, 100*btClockMin)
	}
	return f
}

// btRespawns compares, for every death whose end the frames show, the live respawn
// estimate (deathTimer from the counter tick) with the respawn the frames after it show.
func btRespawns(fr []stamped[frame], spawn time.Time, skew time.Duration, pauses []feedPause) []float64 {
	clock := func(ts time.Time) time.Duration { return ts.Sub(spawn) - skew - pauseShift(pauses, ts) }
	var errs []float64
	for c := 1; c < len(fr); c++ {
		for _, team := range [2][]participant{fr[c].v.BlueTeam.Participants, fr[c].v.RedTeam.Participants} {
			for _, cur := range team {
				prev, ok := participantIn(fr[c-1].v, cur.ParticipantID)
				if !ok || cur.Deaths != prev.Deaths+1 {
					continue
				}
				if end, j := followDeath(fr, c, c, cur.ParticipantID, true); end == deathRespawned {
					est := lifeOf(fr, c, cur, fr[c].ts, clock, false)
					if est.Dead && !est.Exact {
						errs = append(errs, est.Secs-(clock(fr[j].ts)-clock(fr[c].ts)).Seconds())
					}
				}
			}
		}
	}
	return errs
}

func btMeasure(p *btPrep, run *btRun) *btStats {
	s := &btStats{Label: p.label, Layout: p.g.Layout, Frames: len(run.frames), Relocks: run.relocks, LockSec: -1, SyncSec: -1}
	fs := run.frames
	sy := run.sy
	if run.firstRead >= 0 && run.lockFrame >= 0 {
		s.LockSec = float64(run.lockFrame-run.firstRead) / mmFPS
		if run.syncFrame >= 0 {
			s.SyncSec = float64(max(run.syncFrame, run.lockFrame)-run.firstRead) / mmFPS
		}
	}
	s.LayoutOK = len(run.layouts) == 1 && run.layouts[p.g.Layout] > 0
	s.Switches = max(0, len(run.layouts)-1)
	if n := float64(len(fs)); n > 0 {
		ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) / n }
		s.MsFrame, s.CPUMsFrame, s.WallMsFrame = ms(run.procWall), ms(run.cpu), ms(run.wall)
		s.RealTime = n / mmFPS / run.wall.Seconds()
	}

	// Kill flips: the HUD's counter flips (debounced by the sync itself) against the feed's
	// kills, with the offset the app held just before the flip showed.
	sy.mu.Lock()
	hudKills, feedKills := map[[2]int]float64{}, map[[2]int]float64{}
	for k, v := range sy.hudKills {
		hudKills[k] = v
	}
	for k, v := range sy.feedKills {
		feedKills[k] = v
	}
	sy.mu.Unlock()
	var flips []btFlip
	for key, tv := range hudKills {
		tf, ok := feedKills[key]
		if !ok {
			s.FlipFalse++
			s.Verbose = append(s.Verbose, fmt.Sprintf("flip team %d count %d at video %.1fs has no feed kill", key[0], key[1], tv))
			continue
		}
		i := sort.Search(len(fs), func(i int) bool { return fs[i].t > tv-0.75 }) - 1
		if i < 0 || !fs[i].offOK {
			s.FlipsUnsynced++
			continue
		}
		d := fs[i].off - (tf - tv)
		// The flip's time is the middle between the last frame showing the old counter and
		// the first showing the new one: its uncertainty is half that bracket, wide when the
		// counter could not be read around the kill (an overlay slides over it).
		lo := sort.Search(len(fs), func(x int) bool { return fs[x].t >= tv }) - 1
		hi := lo + 1
		for lo > 0 && !fs[lo].res.hud.KillsOK {
			lo--
		}
		for hi < len(fs)-1 && !fs[hi].res.hud.KillsOK {
			hi++
		}
		width := fs[hi].t - fs[lo].t
		flips = append(flips, btFlip{tv, d, tf - tv})
		s.Flips++
		s.Deltas = append(s.Deltas, d)
		tight := width <= 1.0
		if tight {
			s.FlipsTight++
		}
		if math.Abs(d) <= btKillFlipTol {
			s.FlipsOK++
			if tight {
				s.FlipsTightOK++
			}
		} else {
			s.Verbose = append(s.Verbose, fmt.Sprintf("flip team %d count %d at video %.1fs: offset %.2f vs flip %.2f (%+.2f s), counter unread for a %.1fs bracket", key[0], key[1], tv, fs[i].off, tf-tv, d, width))
		}
	}
	sort.Slice(flips, func(a, b int) bool { return flips[a].tv < flips[b].tv })
	for n, f := range flips {
		if n >= 3 {
			s.FlipsLate++
			if math.Abs(f.d) <= btKillFlipTol {
				s.FlipsLateOK++
			}
		}
	}
	for key := range feedKills {
		if _, ok := hudKills[key]; !ok {
			s.FlipMissed++
		}
	}
	// A relock is right when the kill flips agree with it: the offsets the flips imply move
	// by the step the sync took. A relock with no flips on one side cannot be judged and is
	// not counted as wrong.
	for _, t := range run.relockAt {
		i := sort.Search(len(fs), func(i int) bool { return fs[i].t >= t })
		step := fs[i].off - fs[max(0, i-1)].off
		var before, after []float64
		for _, f := range flips {
			switch {
			case f.tv < t && f.tv > t-180:
				before = append(before, f.o)
			case f.tv >= t && f.tv < t+180:
				after = append(after, f.o)
			}
		}
		verdict := "not judged: no kill flips on both sides"
		if len(before) > 0 && len(after) > 0 {
			moved := btPctl(after, 0.5) - btPctl(before, 0.5)
			if math.Abs(moved-step) <= btRelockTol {
				verdict = fmt.Sprintf("right: flips moved %.1f s, sync %.1f s", moved, step)
			} else {
				verdict = fmt.Sprintf("WRONG: flips moved %.1f s, sync %.1f s", moved, step)
				s.RelocksWrong++
			}
		}
		var around []string
		for j := max(0, i-8); j < min(len(fs), i+8); j++ {
			around = append(around, fmt.Sprintf("%.1f:%d/%v/off%.1f", fs[j].t, fs[j].res.hud.ClockSec, fs[j].res.hud.ClockOK, fs[j].off))
		}
		s.Verbose = append(s.Verbose, fmt.Sprintf("sync relock at video %.1fs (%s); offset %.2f before, %.2f after; clocks %s", t, verdict, fs[max(0, i-1)].off, fs[i].off, strings.Join(around, " ")))
	}
	for _, ps := range p.st.pausesList() {
		s.Verbose = append(s.Verbose, fmt.Sprintf("feed pause at %.1fs since spawn for %.1fs", ps.Start.Sub(p.spawn).Seconds(), ps.Dur.Seconds()))
	}
	s.RespErrs = btRespawns(p.frames, p.spawn, sy.Skew(), p.st.pausesList())
	btTruth(p, run, s)
	s.derive()
	s.Fails = s.gate()
	return s
}

// btTruth walks the frames with the feed's word on who is alive: detection, drawn
// markers (flicker and gaps), HUD reads and the position a champion dies at.
func btTruth(p *btPrep, run *btRun, s *btStats) {
	fs := run.frames
	skew := run.sy.Skew()
	pauses := p.st.pausesList()
	clock := func(ts time.Time) time.Duration { return ts.Sub(p.spawn) - skew - pauseShift(pauses, ts) }
	feedAt := func(f btFrame, d float64) time.Time {
		return p.spawn.Add(time.Duration((f.t + f.off + d) * float64(time.Second)))
	}
	nk := len(run.icons)
	chIdx := map[int]int{} // participant id -> template index
	for k, ic := range run.icons {
		chIdx[ic.ref.PID] = k
	}

	// Per frame: inside the measured stretch of the game, and the feed's lives.
	in := make([]bool, len(fs))
	lv := make([]mmLives, len(fs))
	for i, f := range fs {
		ts := f.t + f.off
		if f.res.layout == "" || !f.offOK || ts < 3 || ts > p.endSec-3 {
			continue
		}
		in[i] = true
		lv[i] = livesAt(p.frames, feedAt(f, 0), clock, true)
		if lv[i] == nil {
			in[i] = false
		}
	}
	// Per frame and champion: how it is drawn.
	state := make([][]byte, len(fs))
	for i, f := range fs {
		if !in[i] {
			continue
		}
		state[i] = make([]byte, nk)
		if !f.res.visible {
			for k := range state[i] {
				state[i][k] = btHidden
				if lf, ok := lv[i][run.icons[k].ref.PID]; ok && lf.Dead {
					state[i][k] = btDeadSt
				}
			}
			continue
		}
		c := mmChampsOf(f.res, nil, 0, lv[i], nil)
		byChamp := map[string]mmIconState{}
		for _, ic := range c.Icons {
			byChamp[ic.Champ] = ic.State
		}
		for k, ic := range run.icons {
			st, drawn := byChamp[ic.ref.Champ]
			switch {
			case drawn && st == mmIconDead:
				state[i][k] = btDeadSt
			case drawn && st == mmIconLive:
				state[i][k] = btGood
			case drawn:
				state[i][k] = btStale
			default:
				state[i][k] = btAbsent
			}
		}
	}

	// Detection against the feed.
	champ := make([]btChamp, nk)
	missRun := make([]int, nk)
	for k, ic := range run.icons {
		champ[k] = btChamp{PID: ic.ref.PID, Champ: ic.ref.Champ, Team: ic.ref.Team, Role: ic.ref.Role}
	}
	nearOf := func(f btFrame, k int, l mmLives) float64 {
		d := f.res.dets[k]
		if !d.Visible {
			return math.Inf(1)
		}
		best := math.Inf(1)
		for m, o := range f.res.dets {
			if m == k || !o.Visible || l[o.PID].Dead {
				continue
			}
			best = math.Min(best, math.Hypot(o.X-d.X, o.Y-d.Y))
		}
		return best
	}
	for i, f := range fs {
		if !in[i] || !f.seen || len(f.res.dets) != nk || !f.locked {
			for k := range missRun {
				missRun[k] = 0
			}
			continue
		}
		s.TrackedFrames++
		lm, lp := livesAt(p.frames, feedAt(f, -btEdgeMargin), clock, true), livesAt(p.frames, feedAt(f, btEdgeMargin), clock, true)
		clusterR := btClusterScale * float64(mmIconD[p.g.Layout]) / mmFrameSize
		cluster := false
		for k, d := range f.res.dets {
			pid := d.PID
			l0, a, b := lv[i][pid], lm[pid], lp[pid]
			fresh := d.Visible && d.Age == 0
			switch {
			case !l0.Dead && !a.Dead && !b.Dead:
				champ[k].AliveFrames++
				s.AliveN++
				near := nearOf(f, k, lv[i])
				if near < clusterR {
					s.ClusN++
					cluster = true
				} else {
					s.IsoN++
				}
				if fresh {
					champ[k].Fresh++
					s.AliveHit++
					if d.Covered {
						s.AliveCov++
					}
					if near < clusterR {
						s.ClusHit++
					} else {
						s.IsoHit++
					}
					missRun[k] = 0
				} else {
					missRun[k]++
					champ[k].LongestMissSec = math.Max(champ[k].LongestMissSec, float64(missRun[k])/mmFPS)
				}
			case l0.Dead && a.Dead && b.Dead:
				s.DeadN++
				if fresh {
					s.DeadHit++
					fx, fy := 0.05, 0.95
					if d.Team == 200 {
						fx, fy = 0.95, 0.05
					}
					if math.Hypot(d.X-fx, d.Y-fy) < 0.25 {
						s.DeadAtBase++
					}
				}
				missRun[k] = 0
			default:
				missRun[k] = 0
			}
		}
		_ = cluster
	}
	for k := range champ {
		champ[k].Ratio = btRatio(champ[k].Fresh, champ[k].AliveFrames)
	}

	// Gameplay minutes, clock reads.
	nIn := 0
	for i, f := range fs {
		if !in[i] {
			continue
		}
		nIn++
		if f.plate {
			s.ClockN++
			if f.res.hud.ClockOK {
				s.ClockOK++
			}
		}
	}
	s.GameMin = float64(nIn) / mmFPS / 60

	// Flicker: a drawn marker that goes missing and is back within btFlickerSpan seconds
	// with the feed saying alive throughout; and map blips: the minimap hidden for a
	// shorter time than that between two visible frames.
	for k := 0; k < nk; k++ {
		last := -1
		gap := false
		for i := range fs {
			var st byte
			if in[i] {
				st = state[i][k]
			}
			switch st {
			case btGood, btStale:
				if gap && last >= 0 && fs[i].t-fs[last].t <= btFlickerSpan {
					s.Flickers++
					s.Verbose = append(s.Verbose, fmt.Sprintf("flicker %s at video %.1fs, back after %.1fs", champ[k].Champ, fs[last].t, fs[i].t-fs[last].t))
				}
				last, gap = i, false
			case btAbsent:
				if last >= 0 {
					gap = true
				}
			default:
				last, gap = -1, false
			}
		}
	}
	for i := 0; i < len(fs); i++ {
		if !in[i] || fs[i].res.visible {
			continue
		}
		j := i
		for j < len(fs) && in[j] && !fs[j].res.visible {
			j++
		}
		if i > 0 && j < len(fs) && in[i-1] && in[j] && fs[j].t-fs[i-1].t <= btFlickerSpan {
			s.MapBlips++
			s.Verbose = append(s.Verbose, fmt.Sprintf("map hidden %.1fs at video %.1fs", fs[j].t-fs[i-1].t, fs[i].t))
		}
		i = j
	}

	// Gaps: runs of more than 5 s where an alive champion's marker is missing or stale.
	everFresh := make([]int, nk)
	for k := range everFresh {
		everFresh[k] = -1
	}
	cause := make([][]string, len(fs)) // per frame and champion, why it is not shown fresh
	for i, f := range fs {
		if !in[i] {
			continue
		}
		cause[i] = make([]string, nk)
		for k := 0; k < nk; k++ {
			if len(f.res.dets) == nk && f.res.dets[k].Visible && f.res.dets[k].Age == 0 && f.seen {
				everFresh[k] = i
			}
			switch state[i][k] {
			case btGood, btDeadSt, btNone:
				continue
			}
			var c string
			switch {
			case i > 0 && f.t-fs[i-1].t > 1.01:
				c = "pipeline: video frames missing"
			case !f.locked:
				c = "pipeline: sync not locked"
			case !f.res.visible && !f.plate:
				c = "pipeline: HUD gone (replay/desk/draft shot)"
			case !f.res.visible:
				c = "pipeline: map gate off while HUD up"
			case !f.seen:
				c = "pipeline: objective banner cover"
			default:
				d := f.res.dets[k]
				near := nearOf(f, k, lv[i])
				switch {
				case !d.Visible && everFresh[k] < 0:
					c = "tracker: never acquired"
				case !d.Visible && float64(i-everFresh[k])/mmFPS > mmHoldMax/mmFPS:
					c = "display: hold expired (90 s)"
				case !d.Visible:
					c = "tracker: marks reset or filtered (seen before its death)"
				case near < float64(mmIconD[p.g.Layout])/mmFrameSize:
					c = "tracker: lost beside another icon"
				default:
					c = "tracker: lost in the open"
				}
			}
			cause[i][k] = c
		}
	}
	for k := 0; k < nk; k++ {
		for i := 0; i < len(fs); i++ {
			bad := func(i int) bool {
				return i < len(fs) && in[i] && (state[i][k] == btStale || state[i][k] == btAbsent || state[i][k] == btHidden)
			}
			if !bad(i) {
				continue
			}
			j := i
			for bad(j) {
				j++
			}
			if dur := fs[j-1].t - fs[i].t + 1.0/mmFPS; dur > 5 {
				g := btGap{Champ: champ[k].Champ, Team: champ[k].Team, FeedSec: fs[i].t + fs[i].off, VideoSec: fs[i].t, DurSec: dur, Causes: map[string]int{}, mid: (i + j) / 2, k: k}
				for x := i; x < j; x++ {
					g.Causes[cause[x][k]]++
				}
				best := 0
				for c, n := range g.Causes {
					if n > best || (n == best && c < g.Cause) {
						best, g.Cause = n, c
					}
				}
				s.Gaps = append(s.Gaps, g)
				champ[k].LongestGapSec = math.Max(champ[k].LongestGapSec, dur)
			}
			i = j
		}
	}
	sort.Slice(s.Gaps, func(a, b int) bool { return s.Gaps[a].DurSec > s.Gaps[b].DurSec })
	s.Champs = champ

	// Hidden minimap intervals longer than 3 s: true or false hides.
	for i := 0; i < len(fs); i++ {
		if !in[i] || !fs[i].hidden {
			continue
		}
		j := i
		var present, clockOK int
		var plate, fit float64
		for j < len(fs) && in[j] && fs[j].hidden {
			f := fs[j]
			if f.fitOK && f.terr && f.plateS >= 0.6 {
				present++
			}
			if f.res.hud.ClockOK {
				clockOK++
			}
			plate += f.plateS
			fit += float64(f.fit)
			j++
		}
		n := float64(j - i)
		if dur := n / mmFPS; dur > 3 {
			h := btHide{VideoSec: fs[i].t, FeedSec: fs[i].t + fs[i].off, DurSec: dur, Class: "true", PlateAvg: plate / n, FitAvg: fit / n, ClockOK: float64(clockOK) / n, mid: (i + j) / 2}
			switch {
			case float64(present) >= n/2 && h.ClockOK < 0.5:
				h.Class, h.Detail = "false", "minimap and HUD plate on screen, clock unread"
			case float64(present) >= n/2:
				h.Class, h.Detail = "false", "minimap, plate and clock all readable"
			case h.PlateAvg < mmPlateLive:
				h.Detail = "HUD plate gone (replay, desk, draft or ad)"
			default:
				h.Detail = "plate on, minimap not in view"
			}
			s.Hides = append(s.Hides, h)
		}
		i = j
	}
	sort.Slice(s.Hides, func(a, b int) bool { return s.Hides[a].DurSec > s.Hides[b].DurSec })

	// Deaths: the dead marker the app draws after a death (see mmChampsOf) against the last
	// position the champion was measured at before it died. The feed has no positions, so the
	// last measurement is the nearest thing to the truth there is: the marker must stand where
	// the champion's icon was last seen.
	fr := p.frames
	for c := 1; c < len(fr); c++ {
		for _, team := range [2][]participant{fr[c].v.BlueTeam.Participants, fr[c].v.RedTeam.Participants} {
			for _, cur := range team {
				prev, ok := participantIn(fr[c-1].v, cur.ParticipantID)
				k, known := chIdx[cur.ParticipantID]
				if !ok || !known || cur.Deaths != prev.Deaths+1 {
					continue
				}
				fsec := fr[c].ts.Sub(p.spawn).Seconds()
				i := sort.Search(len(fs), func(i int) bool { return fs[i].offOK && fs[i].t+fs[i].off > fsec }) - 1
				if i < 0 || !in[i] {
					continue
				}
				ref := run.icons[k].ref
				d := btDeath{PID: ref.PID, Champ: ref.Champ, FeedSec: fsec}
				// the feed's counter ticks about half a second before the champion's health
				// reaches 0 (deathTickLead) and the sync is good to about a second: the icon
				// goes on moving until then, so measurements up to btDeathLate after the tick count
				e := i
				for e+1 < len(fs) && fs[e+1].t-fs[i].t <= btDeathLate {
					e++
				}
				for x := e; x >= 0 && fs[i].t-fs[x].t <= 60; x-- {
					if len(fs[x].res.dets) == nk && fs[x].seen && fs[x].res.dets[k].Visible && fs[x].res.dets[k].Age == 0 && !fs[x].res.dets[k].Covered {
						dt := fs[x].res.dets[k]
						d.Tracked, d.AgeSec, d.LastX, d.LastY = true, fs[i].t-fs[x].t, dt.X, dt.Y
						break
					}
				}
				for j := i; j < len(fs) && fs[j].t-fs[i].t <= btDeathShown; j++ {
					if !in[j] || !fs[j].res.visible || !lv[j][ref.PID].Dead {
						continue
					}
					for _, ic := range mmChampsOf(fs[j].res, nil, 0, lv[j], nil).Icons {
						if ic.Champ == ref.Champ && ic.State == mmIconDead {
							d.Shown, d.ShownX, d.ShownY = true, ic.X, ic.Y
						}
					}
					if d.Shown {
						break
					}
				}
				if d.Tracked && d.Shown {
					d.Dist = math.Hypot(d.ShownX-d.LastX, d.ShownY-d.LastY)
					if bx, by := mmBasePos(ref.Team, ref.PID); math.Hypot(d.ShownX-bx, d.ShownY-by) < 1e-9 {
						s.DeathBase++
					}
				}
				s.Deaths = append(s.Deaths, d)
			}
		}
	}
}

// ---- reporting ----------------------------------------------------------------------

func btPool(label string, list []*btStats) *btStats {
	o := &btStats{Label: label, Layout: label}
	for _, s := range list {
		o.Frames += s.Frames
		o.GameMin += s.GameMin
		o.Relocks += s.Relocks
		o.RelocksWrong += s.RelocksWrong
		o.Flips += s.Flips
		o.FlipsOK += s.FlipsOK
		o.FlipsTight += s.FlipsTight
		o.FlipsTightOK += s.FlipsTightOK
		o.FlipsLate += s.FlipsLate
		o.FlipsLateOK += s.FlipsLateOK
		o.FlipsUnsynced += s.FlipsUnsynced
		o.FlipFalse += s.FlipFalse
		o.FlipMissed += s.FlipMissed
		o.Deltas = append(o.Deltas, s.Deltas...)
		o.AliveCov += s.AliveCov
		o.AliveN, o.AliveHit, o.IsoN, o.IsoHit, o.ClusN, o.ClusHit = o.AliveN+s.AliveN, o.AliveHit+s.AliveHit, o.IsoN+s.IsoN, o.IsoHit+s.IsoHit, o.ClusN+s.ClusN, o.ClusHit+s.ClusHit
		o.DeadN, o.DeadHit, o.DeadAtBase, o.TrackedFrames = o.DeadN+s.DeadN, o.DeadHit+s.DeadHit, o.DeadAtBase+s.DeadAtBase, o.TrackedFrames+s.TrackedFrames
		o.RespErrs = append(o.RespErrs, s.RespErrs...)
		o.Deaths = append(o.Deaths, s.Deaths...)
		o.DeathBase += s.DeathBase
		o.Flickers += s.Flickers
		o.MapBlips += s.MapBlips
		o.ClockN += s.ClockN
		o.ClockOK += s.ClockOK
		o.Gaps = append(o.Gaps, s.Gaps...)
		o.Hides = append(o.Hides, s.Hides...)
		o.MsFrame += s.MsFrame * float64(s.Frames)
		o.CPUMsFrame += s.CPUMsFrame * float64(s.Frames)
		o.WallMsFrame += s.WallMsFrame * float64(s.Frames)
		o.LockSec = math.Max(o.LockSec, s.LockSec)
		o.SyncSec = math.Max(o.SyncSec, s.SyncSec)
		o.Fails = append(o.Fails, s.Fails...)
	}
	if o.Frames > 0 {
		o.MsFrame /= float64(o.Frames)
		o.CPUMsFrame /= float64(o.Frames)
		o.WallMsFrame /= float64(o.Frames)
		o.RealTime = 1000 / mmFPS / o.WallMsFrame * mmFPS
	}
	o.LayoutOK = true
	o.derive()
	return o
}

func btDrawn(ds []btDeath) int {
	n := 0
	for _, d := range ds {
		if d.Tracked && d.Shown {
			n++
		}
	}
	return n
}

func btNum(v float64, format string) string {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return "-"
	}
	return fmt.Sprintf(format, v)
}

func btPct(v float64) string {
	if math.IsNaN(v) {
		return "-"
	}
	return fmt.Sprintf("%.1f", 100*v)
}

const btHead = "game                    frames  lock  sync rlk | flips  ≤1s%  med   p90 | alive%  iso%  clus% dead% | resp med/p90 (n) | flick/min | clock% | gaps>5s (tot s) | ms/frame cpu  × | verdict"

func btRow(s *btStats) string {
	v := "PASS"
	if len(s.Fails) > 0 {
		v = "FAIL"
	}
	return fmt.Sprintf("%-23s %6d %5s %5s %3d | %5d %5s %5s %5s | %6s %5s %6s %5s | %5s/%-5s (%3d) | %9.2f | %6s | %4d (%5.0f)   | %7.1f %5.1f %4.1f | %s",
		s.Label, s.Frames, btNum(s.LockSec, "%.1f"), btNum(s.SyncSec, "%.1f"), s.Relocks,
		s.Flips, btPct(s.FlipTightShare), btNum(s.FlipMedian, "%.2f"), btNum(s.FlipP90, "%.2f"),
		btPct(s.AliveRatio), btPct(s.IsoRatio), btPct(s.ClusterRatio), btNum(100*s.DeadRatio, "%.2f"),
		btNum(s.RespMedian, "%.2f"), btNum(s.RespP90, "%.2f"), s.RespN,
		s.FlickerRate, btPct(s.ClockShare), s.GapTotal.Count, s.GapTotal.TotalSec,
		s.MsFrame, s.CPUMsFrame, s.RealTime, v)
}

func btReport(all []*btStats) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n%s\n", btHead)
	byLeague := map[string][]*btStats{}
	var leagues []string
	for _, s := range all {
		b.WriteString(btRow(s) + "\n")
		if _, ok := byLeague[s.Layout]; !ok {
			leagues = append(leagues, s.Layout)
		}
		byLeague[s.Layout] = append(byLeague[s.Layout], s)
	}
	sort.Strings(leagues)
	b.WriteString("\nper league (pooled)\n" + btHead + "\n")
	for _, l := range leagues {
		b.WriteString(btRow(btPool(l, byLeague[l])) + "\n")
	}
	tot := btPool("ALL", all)
	b.WriteString(btRow(tot) + "\n")
	fmt.Fprintf(&b, "\nalive detected: %s%% (measured on the frame: %s%%, the rest inferred under another icon); per league", btPct(tot.AliveRatio), btPct(tot.AliveMeasured))
	for _, l := range leagues {
		p := btPool(l, byLeague[l])
		fmt.Fprintf(&b, " %s %s/%s", l, btPct(p.AliveRatio), btPct(p.AliveMeasured))
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "\ngap causes, all games (alive per feed, marker missing or stale > 5 s)\n")
	var causes []string
	for c := range tot.GapSum {
		causes = append(causes, c)
	}
	sort.Slice(causes, func(i, j int) bool { return tot.GapSum[causes[i]].TotalSec > tot.GapSum[causes[j]].TotalSec })
	for _, c := range causes {
		g := tot.GapSum[c]
		fmt.Fprintf(&b, "  %-58s %4d gaps %7.0f s total %6.1f s longest\n", c, g.Count, g.TotalSec, g.Longest)
	}
	fmt.Fprintf(&b, "\nminimap hidden > 3 s during the game (true: the broadcast shows no minimap; false: it does and the pipeline hid it)\n")
	for _, c := range []string{"true", "false"} {
		g := tot.HideSum[c]
		fmt.Fprintf(&b, "  %-6s %4d intervals %7.0f s total %6.1f s longest\n", c, g.Count, g.TotalSec, g.Longest)
	}
	fmt.Fprintf(&b, "\ndeaths: dead marker vs last measured position: %d of %d deaths measured in the minute before, marker drawn for %d; distance median %s p90 %s (normalized map), within %.2f: %d; drawn at the base: %d\n",
		tot.DeathTracked, len(tot.Deaths), btDrawn(tot.Deaths), btNum(tot.DeathMedian, "%.3f"), btNum(tot.DeathP90, "%.3f"), btDeathTol, tot.DeathWithin, tot.DeathBase)
	for _, s := range all {
		for _, f := range s.Fails {
			fmt.Fprintf(&b, "FAIL %s: %s\n", s.Label, f)
		}
		if os.Getenv("ESTERM_BACKTEST_V") != "" {
			for _, v := range s.Verbose {
				fmt.Fprintf(&b, "  %s: %s\n", s.Label, v)
			}
		}
	}
	return b.String()
}

func btJSON(all []*btStats, took time.Duration) map[string]any {
	by := map[string][]*btStats{}
	for _, s := range all {
		by[s.Layout] = append(by[s.Layout], s)
	}
	leagues := map[string]*btStats{}
	for l, list := range by {
		leagues[l] = btPool(l, list)
	}
	return map[string]any{
		"took": took.String(),
		"thresholds": map[string]float64{
			"killFlipTolSec": btKillFlipTol, "killFlipShare": btKillFlipMin, "aliveDetected": btAliveMin, "deadDetected": btDeadMax,
			"respawnMedianSec": btRespMedian, "respawnP90Sec": btRespP90, "flickerPerMin": btFlickerMax, "clockRead": btClockMin, "layoutLockSec": btLockMax,
		},
		"games":   all,
		"leagues": leagues,
		"overall": btPool("ALL", all),
	}
}

// btSaveGaps writes annotated frames of the longest gaps of a game next to the JSON output:
// the minimap region at twice its size, every tracked champion ringed (green: found on this
// frame, yellow: held at its last position, team-coloured centre dot), the champion of the
// gap marked with a magenta cross at its last known position.
func btSaveGaps(p *btPrep, run *btRun, s *btStats, out string) {
	if out == "" {
		return
	}
	dir := out + ".gaps"
	os.MkdirAll(dir, 0o755)
	btSaveHides(p, run, s, dir)
	if len(s.Gaps) == 0 {
		return
	}
	for n, g := range s.Gaps[:min(3, len(s.Gaps))] {
		f := run.frames[g.mid]
		args := mmFFmpegArgs(p.clip, math.Max(0, f.t-0.2), false, true, mmRegionFilter(1), true)
		b, err := exec.Command(mmTool("ESTERM_FFMPEG", "ffmpeg"), args...).Output()
		if err != nil || len(b) < mmStackBytes {
			continue
		}
		const z = 2
		img := image.NewRGBA(image.Rect(0, 0, mmRegionMM.W*z, mmRegionMM.H*z))
		for y := 0; y < mmRegionMM.H*z; y++ {
			for x := 0; x < mmRegionMM.W*z; x++ {
				o := 3 * ((y/z)*mmStackW + x/z)
				img.SetRGBA(x, y, color.RGBA{b[o], b[o+1], b[o+2], 255})
			}
		}
		ring := func(cx, cy, r float64, c color.RGBA) {
			for y := int(cy - r - 2); y <= int(cy+r+2); y++ {
				for x := int(cx - r - 2); x <= int(cx+r+2); x++ {
					if d := math.Hypot(float64(x)-cx, float64(y)-cy); d <= r+0.5 && d >= r-1.5 {
						img.SetRGBA(x, y, c)
					}
				}
			}
		}
		pos := func(d mmDet) (float64, float64) {
			return (float64(f.box.X-mmRegionMM.X) + d.X*float64(f.box.W)) * z, (float64(f.box.Y-mmRegionMM.Y) + d.Y*float64(f.box.H)) * z
		}
		rad := float64(mmIconD[p.g.Layout]) / mmFrameSize * float64(f.box.W) * z / 2
		var legend strings.Builder
		fmt.Fprintf(&legend, "%s: %s (team %d) gap %.0fs from video %.0fs (feed %.0fs), cause %q, causes %v\nframe video %.1fs seen=%v visible=%v plate=%v locked=%v\n",
			p.label, g.Champ, g.Team, g.DurSec, g.VideoSec, g.FeedSec, g.Cause, g.Causes, f.t, f.seen, f.res.visible, f.plate, f.locked)
		for k, d := range f.res.dets {
			state := "not found"
			if d.Visible {
				cx, cy := pos(d)
				c := color.RGBA{0, 255, 0, 255}
				state = fmt.Sprintf("found at %.2f,%.2f score %.2f", d.X, d.Y, d.Score)
				if d.Age > 0 {
					c = color.RGBA{255, 255, 0, 255}
					state = fmt.Sprintf("held %.1fs at %.2f,%.2f", float64(d.Age)/mmFPS, d.X, d.Y)
				}
				ring(cx, cy, rad, c)
				tc := color.RGBA{60, 120, 255, 255}
				if d.Team == 200 {
					tc = color.RGBA{255, 60, 60, 255}
				}
				for y := -2; y <= 2; y++ {
					for x := -2; x <= 2; x++ {
						img.SetRGBA(int(cx)+x, int(cy)+y, tc)
					}
				}
				if k == g.k {
					for d := -int(rad) - 6; d <= int(rad)+6; d++ {
						img.SetRGBA(int(cx)+d, int(cy), color.RGBA{255, 0, 255, 255})
						img.SetRGBA(int(cx), int(cy)+d, color.RGBA{255, 0, 255, 255})
					}
				}
			}
			fmt.Fprintf(&legend, "  %-14s team %d %s\n", d.Champ, d.Team, state)
		}
		name := filepath.Join(dir, fmt.Sprintf("%s_%d", strings.ReplaceAll(p.label, " ", "_"), n+1))
		if fh, err := os.Create(name + ".png"); err == nil {
			png.Encode(fh, img)
			fh.Close()
		}
		os.WriteFile(name+".txt", []byte(legend.String()), 0o644)
	}
}

// btSaveHides writes the frames of the longest false hides of a game: the minimap region and
// the HUD crop (3x) side by side, with the scores in a text file.
func btSaveHides(p *btPrep, run *btRun, s *btStats, dir string) {
	n := 0
	for _, h := range s.Hides {
		if h.Class != "false" || n == 3 {
			continue
		}
		n++
		f := run.frames[h.mid]
		args := mmFFmpegArgs(p.clip, math.Max(0, f.t-0.2), false, true, mmRegionFilter(1), true)
		b, err := exec.Command(mmTool("ESTERM_FFMPEG", "ffmpeg"), args...).Output()
		if err != nil || len(b) < mmStackBytes {
			continue
		}
		const z = 3
		w := mmRegionMM.W + mmRegionHUD.W*z
		img := image.NewRGBA(image.Rect(0, 0, w, max(mmRegionMM.H, mmRegionHUD.H*z)))
		for y := 0; y < mmRegionMM.H; y++ {
			for x := 0; x < mmRegionMM.W; x++ {
				o := 3 * (y*mmStackW + x)
				img.SetRGBA(x, y, color.RGBA{b[o], b[o+1], b[o+2], 255})
			}
		}
		for y := 0; y < mmRegionHUD.H*z; y++ {
			for x := 0; x < mmRegionHUD.W*z; x++ {
				o := 3 * ((mmRegionMM.H+y/z)*mmStackW + x/z)
				img.SetRGBA(mmRegionMM.W+x, y, color.RGBA{b[o], b[o+1], b[o+2], 255})
			}
		}
		name := filepath.Join(dir, fmt.Sprintf("falsehide_%s_%d", strings.ReplaceAll(p.label, " ", "_"), n))
		if fh, err := os.Create(name + ".png"); err == nil {
			png.Encode(fh, img)
			fh.Close()
		}
		txt := fmt.Sprintf("%s: false hide of %.0fs from video %.0fs (feed %.0fs): %s\nframe video %.1fs: fit ok=%v corr=%.2f terrain=%v plate share=%.2f clock read=%v; interval averages: plate %.2f fit %.2f clock read %.0f%%\n",
			p.label, h.DurSec, h.VideoSec, h.FeedSec, h.Detail, f.t, f.fitOK, f.fit, f.terr, f.plateS, f.res.hud.ClockOK, h.PlateAvg, h.FitAvg, 100*h.ClockOK)
		os.WriteFile(name+".txt", []byte(txt), 0o644)
	}
}
