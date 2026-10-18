//go:build backtest

package main

// Backtest of the broadcast-minimap pipeline on completed official games.
//
//	go test -tags backtest -run TestBacktest -timeout 3h -v .
//
// Each game of btGames is processed whole, at full decode speed, by the code the app runs:
// startMMVideo (ffmpeg) -> mmPipeline.Process (layout lock, HUD OCR, map fit, tracker) ->
// mmSync (clock and kill-flip sync against the replay store's feed frames), with the death
// logic of deaths.go and the drawing decision mmChampsOf. No labels are involved: the
// livestats feed is the ground truth for who is alive or dead and when a kill happened.
//
// Videos and feed windows are cached under os.UserCacheDir()/esterm/backtest (the video
// clips are about 1 GB per game, ~15 GB for the list below): delete that directory to
// clean up (ESTERM_BACKTEST_CACHE moves it). ESTERM_BACKTEST_GAMES is a regexp selecting
// games by their label ("worlds MVK-FUR g1"); ESTERM_BACKTEST_V=1 lists every flip and
// flicker.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// btGame is one completed game of the backtest. Without Vod the game's own official VOD
// (the best of replayVods) is used. Vod overrides it with a YouTube video id for games the
// API has no VOD for (LPL: the official LPL_English upload); At is then the video second of
// the game's first feed frame, good to about ±10 s (found by scanning the video for the
// game clock: clock zero minus the skew of the league).
type btGame struct {
	Layout string // broadcast layout the pipeline must lock: worlds | lck | lpl | lec
	Match  string // lolesports match id
	Game   int
	Vod    string
	At     float64
}

var btGames = []btGame{
	// Worlds 2026 Play-Ins, five series on four days; the pause game and a 4th game included.
	{Layout: "worlds", Match: "115660588583171798", Game: 1}, // MVK-FUR 2026-10-18 KST
	{Layout: "worlds", Match: "115660588583171798", Game: 2}, // MVK-FUR 2026-10-18 KST
	{Layout: "worlds", Match: "115660588583171798", Game: 4}, // MVK-FUR 2026-10-18 KST
	{Layout: "worlds", Match: "115660588583040710", Game: 1}, // MVK-FUR 10-16
	{Layout: "worlds", Match: "115660588583171790", Game: 2}, // KC-MVK 10-17
	{Layout: "worlds", Match: "115660588583171794", Game: 1}, // FUR-C9 10-17
	{Layout: "worlds", Match: "115660588583171786", Game: 1}, // KC-C9 10-16, game paused for 202 s
	// LCK 2026 (the VOD is the full-day English upload).
	{Layout: "lck", Match: "117030752644841643", Game: 1}, // Finals GEN-HLE
	{Layout: "lck", Match: "117030752644841643", Game: 3}, // Finals GEN-HLE
	{Layout: "lck", Match: "115548147900750229", Game: 2}, // Week 13 NS-BFX
	// LPL 2026: the API lists no VODs, the official LPL_English upload of the finals is used.
	{Layout: "lpl", Match: "117155436343202202", Game: 1, Vod: "sJGvudwNkH0", At: 3493},  // Finals AL-BLG
	{Layout: "lpl", Match: "117155436343202202", Game: 3, Vod: "sJGvudwNkH0", At: 10450}, // Finals AL-BLG
	{Layout: "lpl", Match: "117155436343202202", Game: 4, Vod: "sJGvudwNkH0", At: 14142}, // Finals AL-BLG
	// LEC 2026.
	{Layout: "lec", Match: "115548681803406333", Game: 1}, // Finals G2-MKOI
	{Layout: "lec", Match: "115548681803406333", Game: 3}, // Finals G2-MKOI
	{Layout: "lec", Match: "115548681803406327", Game: 2}, // Playoffs KC-MKOI
}

// Pass criteria, applied to every game.
const (
	btKillFlipTol  = 1.0  // seconds: |HUD kill flip mapped to feed time - feed kill|
	btKillFlipMin  = 0.95 // share of kill flips within btKillFlipTol
	btAliveMin     = 0.90 // alive champions the tracker found on a frame with a visible minimap
	btDeadMax      = 0.01 // dead champions the tracker claims to have found
	btRespMedian   = 1.5  // seconds, median |live respawn estimate - feed respawn|
	btRespP90      = 3.0  // seconds, 90th percentile of the same
	btFlickerMax   = 0.5  // unexplained marker flickers per gameplay minute
	btClockMin     = 0.90 // share of gameplay frames whose clock was read
	btLockMax      = 10.0 // seconds from the first readable clock to the layout lock
	btRelockTol    = 1.5  // seconds: a relock is right when the kill flips moved by the sync's step within this (each flip carries about ±1 s)
	btFlickerSpan  = 6.0  // seconds: a marker that is back within this long flickered
	btEdgeMargin   = 3.0  // seconds around a death or respawn where alive/dead is ambiguous
	btClusterScale = 0.8  // champions closer than this many icon diameters overlap
	btPreRoll      = 45.0 // seconds of video kept before the game's first feed frame (with At)
	btApiPreRoll   = 150.0
	btPostRoll     = 60.0
	btApiPostRoll  = 120.0
)

// ---- cache ------------------------------------------------------------------------

func btCacheDir(sub string) string {
	root := os.Getenv("ESTERM_BACKTEST_CACHE")
	if root == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			base = os.TempDir()
		}
		root = filepath.Join(base, "esterm", "backtest")
	}
	dir := filepath.Join(root, sub)
	os.MkdirAll(dir, 0o755)
	return dir
}

func btWriteFile(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.part")
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(f.Name())
		return werr
	}
	return os.Rename(f.Name(), path)
}

// btGet returns the body of url, cached on disk under key (finished games never change).
func btGet(key, url string) ([]byte, error) {
	path := filepath.Join(btCacheDir("api"), key)
	if b, err := os.ReadFile(path); err == nil {
		return b, nil
	}
	var lastErr error
	for try := 0; try < 4; try++ {
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("Origin", origin)
		resp, err := httpClient.Do(req)
		if err == nil {
			b, rerr := io.ReadAll(resp.Body)
			resp.Body.Close()
			switch {
			case rerr != nil:
				err = rerr
			case resp.StatusCode != 200:
				err = fmt.Errorf("%s: %s", url, resp.Status)
			default:
				return b, btWriteFile(path, b)
			}
		}
		lastErr = err
		time.Sleep(time.Duration(try+1) * time.Second)
	}
	return nil, lastErr
}

func btGetJSON(key, url string, v any) error {
	b, err := btGet(key, url)
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil
	}
	return json.Unmarshal(b, v)
}

// ---- prepared game ----------------------------------------------------------------

type btPrep struct {
	g      btGame
	label  string
	gid    string
	st     *replayStore
	frames []stamped[frame]
	spawn  time.Time
	endSec float64 // feed seconds from spawn to the game's last frame
	clip   string  // cached video clip
	vodURL string
	err    error
}

func btLabel(g btGame, er replayEventResp) string {
	var codes []string
	for _, t := range er.Data.Event.Match.Teams {
		codes = append(codes, t.Code)
	}
	return fmt.Sprintf("%s %s g%d", g.Layout, strings.Join(codes, "-"), g.Game)
}

// btPrepare loads the game's feed into a replay store (every window, cached on disk) and
// makes sure its video clip is cached.
func btPrepare(ctx context.Context, g btGame, dl chan struct{}) *btPrep {
	p := &btPrep{g: g, label: fmt.Sprintf("%s %s g%d", g.Layout, g.Match, g.Game)}
	var er replayEventResp
	if err := btGetJSON("event_"+g.Match+".json", gwBase+"/getEventDetails?hl=en-US&id="+g.Match, &er); err != nil {
		p.err = err
		return p
	}
	_, gj, err := replayEvent(g.Match, er, g.Game)
	if err != nil {
		p.err = err
		return p
	}
	p.label = btLabel(g, er)
	p.gid = gj.ID
	if slug := er.Data.Event.League.Slug; slug != g.Layout && !(g.Layout == "worlds" && slug == "worlds") {
		p.err = fmt.Errorf("match %s is league %q, not %q", g.Match, slug, g.Layout)
		return p
	}

	var w0, wEnd windowResp
	if err := btGetJSON("w0_"+gj.ID+".json", feedBase+"/window/"+gj.ID, &w0); err != nil {
		p.err = err
		return p
	}
	if len(w0.Frames) == 0 {
		p.err = errors.New("no livestats frames")
		return p
	}
	spawn := parseTime(w0.Frames[0].RFC460Timestamp)
	p.label += " " + spawn.In(kst).Format("0102")
	if err := btGetJSON("wend_"+gj.ID+".json", feedBase+"/window/"+gj.ID+"?startingTime="+startingTime(spawn.Add(6*time.Hour)), &wEnd); err != nil {
		p.err = err
		return p
	}
	end := spawn
	if n := len(wEnd.Frames); n > 0 {
		end = parseTime(wEnd.Frames[n-1].RFC460Timestamp)
	}
	st := newReplayStore(gj.ID, spawn, end, 3*time.Second)
	st.pace = 0
	st.win = func(gid, start string) (windowResp, error) {
		var w windowResp
		err := btGetJSON("win_"+gid+"_"+strings.NewReplacer(":", "", ".", "").Replace(start)+".json", feedBase+"/window/"+gid+"?startingTime="+start, &w)
		return w, err
	}
	st.det = func(gid, start string) (detailsResp, error) { return detailsResp{}, nil }
	st.absorb(w0, detailsResp{})
	st.absorb(wEnd, detailsResp{})
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { st.run(rctx); close(done) }()
	for st.progress() < 1 {
		select {
		case <-ctx.Done():
			cancel()
			p.err = ctx.Err()
			return p
		case <-time.After(200 * time.Millisecond):
		}
	}
	cancel()
	<-done
	p.st, p.spawn = st, spawn
	st.mu.Lock()
	p.frames = append([]stamped[frame](nil), st.frames...)
	st.mu.Unlock()
	if len(p.frames) == 0 {
		p.err = errors.New("empty feed")
		return p
	}
	p.endSec = p.frames[len(p.frames)-1].ts.Sub(spawn).Seconds()

	// The video: the API's best VOD or the override.
	var at, pre, post float64
	if g.Vod != "" {
		p.vodURL, at, pre, post = "https://www.youtube.com/watch?v="+g.Vod, g.At, btPreRoll, btPostRoll
	} else {
		vods := replayVods(gj, spawn)
		if len(vods) == 0 {
			p.err = errors.New("the API lists no VOD and the game has no Vod override")
			return p
		}
		p.vodURL, at, pre, post = vods[0].URL, vods[0].SpawnAt, btApiPreRoll, btApiPostRoll
	}
	from := math.Max(0, math.Floor(at-pre))
	dur := math.Ceil(at + p.endSec + post - from)
	name := regexp.MustCompile(`[^A-Za-z0-9]+`).ReplaceAllString(p.vodURL[strings.LastIndexAny(p.vodURL, "=/")+1:], "_")
	p.clip = filepath.Join(btCacheDir("vod"), fmt.Sprintf("%s_%d_%d.mkv", name, int(from), int(dur)))
	if fi, err := os.Stat(p.clip); err == nil && fi.Size() > 0 {
		return p
	}
	dl <- struct{}{}
	defer func() { <-dl }()
	if err := btDownload(ctx, p.vodURL, from, dur, p.clip); err != nil {
		p.err = err
	}
	return p
}

// btDownload copies the video stream of a VOD page between from and from+dur into a clip.
func btDownload(ctx context.Context, page string, from, dur float64, clip string) error {
	media, _, _, err := mmResolve(ctx, page)
	if err != nil {
		return err
	}
	part := clip + ".part"
	args := []string{"-hide_banner", "-loglevel", "error", "-y", "-ss", fmt.Sprint(from), "-rw_timeout", mmNetTimeoutUs,
		"-i", media, "-t", fmt.Sprint(dur), "-an", "-c:v", "copy", "-f", "matroska", part}
	cmd := exec.Command(mmTool("ESTERM_FFMPEG", "ffmpeg"), args...)
	var errb strings.Builder
	cmd.Stderr = &errb
	stop, err := mmStart(ctx, cmd)
	if err != nil {
		return err
	}
	if err := stop(false); err != nil {
		os.Remove(part)
		return fmt.Errorf("ffmpeg download: %v: %s", err, strings.TrimSpace(errb.String()))
	}
	return os.Rename(part, clip)
}
