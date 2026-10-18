//go:build backtest

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var btOut = flag.String("backtest.out", "", "write the backtest results (per game and overall, JSON) to this file; annotated gap frames go to <file>.gaps/")

// btFrame is what the pipeline made of one video frame, with the sync's offset at that time.
type btFrame struct {
	t      float64 // video seconds since the clip start
	res    mmResult
	off    float64 // sync offset (feed - video) as the app would read it right then
	offOK  bool
	locked bool // the sync was locked
	seen   bool // the map was fitted and tracked on this frame (not held under a banner)
	plate  bool // the broadcast HUD plate is on screen: a gameplay frame
	box    mmBox
	haveB  bool

	// Evidence gathered on frames the pipeline hid (layout locked, minimap not shown): does
	// the broadcast show a minimap and its HUD plate anyway?
	hidden bool
	fitOK  bool
	fit    float32 // map fit correlation
	terr   bool    // the fitted box shows terrain
	plateS float64 // share of the plate's edge pixels still on screen
}

type btRun struct {
	frames    []btFrame
	firstRead int // index of the first frame whose clock a league font read, -1
	lockFrame int // index of the first frame with a locked layout, -1
	syncFrame int // index of the first frame with a locked sync, -1
	relocks   int
	relockAt  []float64      // video seconds of each relock
	layouts   map[string]int // frames per layout name
	procWall  time.Duration  // time inside Process
	wall      time.Duration  // decode + process of the whole clip
	cpu       time.Duration  // user+system CPU of this process meanwhile
	sy        *mmSync
	icons     []champIcon
	rangeEnd  float64 // video second after which frames are not processed (0: whole clip)
}

func btCPU() time.Duration {
	var ru syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

func btProcess(ctx context.Context, p *btPrep) (*btRun, error) {
	meta := p.st.metadata()
	refs := mmChampRefs(meta)
	icons, ver, err := mmLoadIcons(ctx, refs, meta.GameMetadata.PatchVersion)
	if err != nil {
		return nil, err
	}
	if err := mmPrimeMapRef(ctx, ver); err != nil {
		return nil, err
	}
	sy := newMMSync(p.spawn, mmFPS, p.st.pausesList)
	sy.ObserveFeedKills(p.st.killsList())
	sy.ObserveFeedLatest(p.st.end)
	pipe := newMMPipeline(icons, sy)
	pipe.livesAt = func(t time.Time) mmLives { return p.st.livesAt(t, sy.Skew()) }

	run := &btRun{firstRead: -1, lockFrame: -1, syncFrame: -1, layouts: map[string]int{}, sy: sy, icons: icons}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	wall0, cpu0 := time.Now(), btCPU()
	// ESTERM_BACKTEST_RANGE=a-b processes only game seconds a..b (feed time since spawn):
	// quick iteration on the tracker; the sync lock and the HUD read need no earlier video.
	vo := mmVideoOpts{Src: p.clip, FPS: mmFPS, VOD: true, Main: os.Getenv("ESTERM_BACKTEST_NOMV") == ""}
	var ra, rb float64
	if n, _ := fmt.Sscanf(os.Getenv("ESTERM_BACKTEST_RANGE"), "%f-%f", &ra, &rb); n == 2 {
		vo.SS = math.Max(0, p.atClip+ra-10)
		vo.IdxBase = int(math.Round(vo.SS * mmFPS))
		vo.SS = float64(vo.IdxBase) / mmFPS
		run.rangeEnd = p.atClip + rb
	}
	frames, err := startMMVideo(cctx, vo, func(s string) {
		if strings.HasPrefix(s, "error") {
			fmt.Fprintf(os.Stderr, "  [%s] %s\n", p.label, s)
		}
	})
	if err != nil {
		return nil, err
	}
	for f := range frames {
		i := len(run.frames)
		bf := btFrame{t: float64(f.Idx) / mmFPS}
		if run.rangeEnd > 0 && bf.t > run.rangeEnd {
			mmReleaseFrame(f)
			cancel()
			continue // drain
		}
		if pipe.lock.cur == nil && run.firstRead < 0 && mmClockLayout(f.HUD, f.MM) != nil {
			run.firstRead = i
		}
		bf.plateS = pipe.plateShare(f.HUD)
		bf.plate = bf.plateS >= mmPlateLive
		t0 := time.Now()
		bf.res = pipe.Process(f)
		run.procWall += time.Since(t0)
		mmReleaseFrame(f)
		bf.seen = bf.res.visible && pipe.cover == 0
		if bf.res.layout != "" && !bf.res.visible {
			bf.hidden = true
			for _, l := range mmLayouts {
				if l.Name == bf.res.layout {
					var box mmBox
					box, bf.fit, bf.fitOK = mmFitMap(f.MM, l.Map)
					bf.terr = bf.fitOK && mmHasTerrain(f.MM, box)
				}
			}
		}
		bf.box, bf.haveB = pipe.box, pipe.haveBox
		bf.off, _, bf.offOK = sy.Offset()
		bf.locked = sy.Locked()
		if n := sy.Relocks(); n > run.relocks {
			run.relocks = n
			run.relockAt = append(run.relockAt, bf.t)
		}
		if bf.res.layout != "" {
			run.layouts[bf.res.layout]++
			if run.lockFrame < 0 {
				run.lockFrame = i
			}
		}
		if run.syncFrame < 0 && bf.locked {
			run.syncFrame = i
		}
		run.frames = append(run.frames, bf)
	}
	run.wall, run.cpu = time.Since(wall0), btCPU()-cpu0
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return run, nil
}

// btLeftover lists processes still running on the cache directory (ffmpeg of a download or
// decode that was not reaped).
func btLeftover() []string {
	out, _ := exec.Command("pgrep", "-fl", btCacheDir("vod")).Output()
	var l []string
	for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if ln != "" && !strings.Contains(ln, "pgrep") {
			l = append(l, ln)
		}
	}
	return l
}

func TestBacktest(t *testing.T) {
	for _, tool := range []string{mmTool("ESTERM_FFMPEG", "ffmpeg"), mmTool("ESTERM_YTDLP", "yt-dlp")} {
		if !mmHasTool(tool) {
			t.Skipf("%s is not installed", tool)
		}
	}
	sel := regexp.MustCompile(os.Getenv("ESTERM_BACKTEST_GAMES"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Downloads run ahead of the (serial, so that its CPU figures mean something) processing.
	dl := make(chan struct{}, 3)
	var games []btGame
	for _, g := range btGames {
		if sel.MatchString(fmt.Sprintf("%s %s g%d", g.Layout, g.Match, g.Game)) {
			games = append(games, g)
		}
	}
	preps := make([]chan *btPrep, len(games))
	var wg sync.WaitGroup
	for i, g := range games {
		preps[i] = make(chan *btPrep, 1)
		wg.Add(1)
		go func(i int, g btGame) {
			defer wg.Done()
			preps[i] <- btPrepare(ctx, g, dl)
		}(i, g)
	}
	if os.Getenv("ESTERM_BACKTEST_DOWNLOAD_ONLY") != "" { // fill the cache and stop
		for i := range games {
			if p := <-preps[i]; p.err != nil {
				t.Errorf("%s: prepare: %v", p.label, p.err)
			}
		}
		wg.Wait()
		return
	}
	start := time.Now()
	var all []*btStats
	for i := range games {
		p := <-preps[i]
		if p.err != nil {
			t.Errorf("%s: prepare: %v", p.label, p.err)
			continue
		}
		t0 := time.Now()
		run, err := btProcess(ctx, p)
		if err != nil {
			t.Errorf("%s: process: %v", p.label, err)
			continue
		}
		s := btMeasure(p, run)
		fmt.Fprintf(os.Stderr, "done %-22s %5d frames in %v (%.1f× real time)\n", p.label, s.Frames, time.Since(t0).Round(time.Second), s.RealTime)
		btSaveGaps(p, run, s, *btOut)
		all = append(all, s)
		fmt.Println(btRow(s))
	}
	wg.Wait()
	fmt.Print(btReport(all))
	if left := btLeftover(); len(left) > 0 {
		t.Errorf("leftover processes: %v", left)
	}
	if *btOut != "" {
		b, _ := json.MarshalIndent(btJSON(all, time.Since(start)), "", " ")
		if err := os.WriteFile(*btOut, b, 0o644); err != nil {
			t.Error(err)
		}
	}
	for _, s := range all {
		for _, f := range s.Fails {
			t.Errorf("%s: %s", s.Label, f)
		}
	}
	if len(games) == 0 {
		t.Fatal("no game selected")
	}
	_ = math.Pi
}
