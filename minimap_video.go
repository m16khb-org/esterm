package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// mmVideoOpts configures the broadcast video pipeline (yt-dlp + ffmpeg).
type mmVideoOpts struct {
	Src string  // page URL (youtube/twitch/...), direct media URL, or local file
	SS  float64 // start offset in seconds (VOD/file)
	FPS int     // default 2

	// IdxBase is the index of the first frame; replay sets it to SS·FPS so that frame
	// idx/FPS is the true stream time wherever the video starts.
	IdxBase int
	// VOD marks Src as the direct media URL or file of a finished video: it is never
	// resolved through yt-dlp and never taken for a live stream, even at SS 0.
	VOD bool

	// OnHeight, when set, receives the pixel height of the stream yt-dlp resolved.
	OnHeight func(h int)

	// Main asks for the main view as well (mmFrame.Main, mmMainFPS frames per second).
	Main bool
}

const (
	mmYTFormat     = "bv*[vcodec^=avc1][height<=1080]/bv*[height<=1080]"
	mmURLRefresh   = 5 * time.Hour
	mmBackoffStart = time.Second
	mmBackoffMax   = 30 * time.Second

	// A child stalled on the network gives up after this long, so a yt-dlp or ffmpeg left
	// behind by a dead parent exits by itself.
	mmNetTimeoutSec = "15"       // yt-dlp --socket-timeout, seconds
	mmNetTimeoutUs  = "15000000" // ffmpeg -rw_timeout, microseconds
)

func mmIsHTTP(src string) bool {
	return strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://")
}

// errMMArchive marks a finished broadcast that YouTube serves as a live archive: ffmpeg
// cannot seek it, so retrying the same URL never helps.
var errMMArchive = errors.New("live archive VOD is not seekable by ffmpeg; try another VOD")

// mmIsPage reports whether src needs yt-dlp to resolve a media URL.
func mmIsPage(src string) bool {
	if !mmIsHTTP(src) {
		return false
	}
	path := strings.ToLower(strings.SplitN(strings.SplitN(src, "?", 2)[0], "#", 2)[0])
	for _, ext := range []string{".m3u8", ".mp4", ".mkv", ".webm", ".ts", ".flv", ".mpd"} {
		if strings.HasSuffix(path, ext) {
			return false
		}
	}
	return true
}

func mmIsYouTube(src string) bool {
	return strings.Contains(src, "youtube.com/") || strings.Contains(src, "youtu.be/")
}

func mmTool(envName, name string) string {
	if p := os.Getenv(envName); p != "" {
		return p
	}
	return name
}

func mmHasTool(path string) bool {
	_, err := exec.LookPath(path)
	return err == nil
}

// mmToolsMissing lists the required executables that are not available for src.
func mmToolsMissing(src string) []string {
	var missing []string
	if f := mmTool("ESTERM_FFMPEG", "ffmpeg"); !mmHasTool(f) {
		missing = append(missing, f)
	}
	if mmIsPage(src) {
		if y := mmTool("ESTERM_YTDLP", "yt-dlp"); !mmHasTool(y) {
			missing = append(missing, y)
		}
		if mmIsYouTube(src) && len(mmJSRuntimeArgs(mmHasTool)) == 0 && !mmHasTool("deno") {
			missing = append(missing, "deno|bun|node")
		}
	}
	return missing
}

// mmJSRuntimeArgs returns the yt-dlp flag selecting a JS runtime when deno is absent.
func mmJSRuntimeArgs(has func(string) bool) []string {
	if has("deno") {
		return nil
	}
	for _, rt := range []string{"bun", "node"} {
		if has(rt) {
			return []string{"--js-runtimes", rt}
		}
	}
	return nil
}

// mmRegionFilter builds the ffmpeg filter that outputs, per sampled frame, the minimap
// region (mmRegionMM) with the HUD region (mmRegionHUD, padded to the same width) stacked
// below it: mmStackW wide, mmStackH tall. The source is first scaled to the 1920x1080
// reference grid the regions are expressed in; ffmpeg skips that scale when the source is
// already 1920x1080, and a source of another aspect ratio is stretched onto the grid.
// Both regions are cropped before the rgb24 conversion, so only ~0.3 Mpx are converted.
func mmRegionFilter(fps int) string {
	m, h := mmRegionMM, mmRegionHUD
	return fmt.Sprintf("fps=%d,scale=1920:1080,split[m][h];"+
		"[m]crop=%d:%d:%d:%d,format=rgb24[m2];"+
		"[h]crop=%d:%d:%d:%d,format=rgb24,pad=%d:%d:0:0[h2];"+
		"[m2][h2]vstack",
		fps, m.W, m.H, m.X, m.Y, h.W, h.H, h.X, h.Y, m.W, h.H)
}

// The main view: the game picture above the HUD, which the health bars of the champions
// are read from (mainview*.go). It is cut at 1080p, full width, rows 0..mmMainH, and sent
// as rgba, mmMainFPS frames per second, over a second pipe (mmMainPipe: an inherited file
// descriptor, a named pipe on Windows).
const (
	mmMainFPS   = 2
	mmMainW     = 1920
	mmMainH     = 816
	mmMainBytes = mmMainW * mmMainH * 4
)

// mmRegionFilterMain is mmRegionFilter that also yields the main view: the stacked
// minimap and HUD frame as [mm] and the main view as [mv].
func mmRegionFilterMain(fps int) string {
	m, h := mmRegionMM, mmRegionHUD
	return fmt.Sprintf("fps=%d,scale=1920:1080,split=3[m][h][v];"+
		"[m]crop=%d:%d:%d:%d,format=rgb24[m2];"+
		"[h]crop=%d:%d:%d:%d,format=rgb24,pad=%d:%d:0:0[h2];"+
		"[m2][h2]vstack[mm];"+
		"[v]fps=%d,crop=%d:%d:0:0,format=rgba[mv]",
		fps, m.W, m.H, m.X, m.Y, h.W, h.H, h.X, h.Y, m.W, h.H, mmMainFPS, mmMainW, mmMainH)
}

// mmFFmpegArgs builds the ffmpeg command line reading media and writing raw rgb24 to stdout.
func mmFFmpegArgs(media string, ss float64, live, hw bool, vf string, oneFrame bool) []string {
	return mmFFmpegArgsMain(media, ss, live, hw, vf, oneFrame, "")
}

// mmFFmpegArgsMain is mmFFmpegArgs; with a mainOut, vf is a filter graph with the outputs
// [mm] and [mv] (mmRegionFilterMain), the second written to mainOut (see mmMainPipe).
func mmFFmpegArgsMain(media string, ss float64, live, hw bool, vf string, oneFrame bool, mainOut string) []string {
	args := []string{"-hide_banner", "-loglevel", "error"}
	if mainOut != "" {
		args = append(args, "-y") // a named pipe exists already: no question about overwriting it
	}
	if live {
		args = append(args, "-fflags", "nobuffer", "-flags", "low_delay")
	}
	if hw && runtime.GOOS == "darwin" {
		args = append(args, "-hwaccel", "videotoolbox")
	}
	if ss > 0 {
		args = append(args, "-ss", strconv.FormatFloat(ss, 'f', 3, 64))
	}
	if mmIsHTTP(media) {
		args = append(args, "-rw_timeout", mmNetTimeoutUs)
	}
	if mainOut != "" {
		args = append(args, "-i", media, "-an", "-filter_complex", vf, "-map", "[mm]")
		if oneFrame {
			args = append(args, "-frames:v", "1")
		}
		args = append(args, "-f", "rawvideo", "-pix_fmt", "rgb24", "pipe:1")
		return append(args, "-map", "[mv]", "-f", "rawvideo", "-pix_fmt", "rgba", mainOut)
	}
	args = append(args, "-i", media, "-an", "-vf", vf)
	if oneFrame {
		args = append(args, "-frames:v", "1")
	}
	return append(args, "-f", "rawvideo", "-pix_fmt", "rgb24", "pipe:1")
}

// mmStreamRank orders broadcast streams: lower is better.
func mmStreamRank(s streamInfo) int {
	prov := map[string]int{"youtube": 0, "twitch": 1} // YouTube serves 1080p; Twitch channels of the leagues stop at 720p60
	p, ok := prov[strings.ToLower(s.Provider)]
	if !ok {
		p = 2
	}
	loc := strings.ToLower(s.Locale)
	l := 2
	switch {
	case strings.HasPrefix(loc, "en"):
		l = 0
	case strings.HasPrefix(loc, "ko"):
		l = 1
	}
	return p*3 + l
}

// mmRankStreams returns the page URLs of the match's broadcast streams, best first.
func mmRankStreams(streams []streamInfo) ([]string, error) {
	var cands []streamInfo
	for _, s := range streams {
		if s.Parameter != "" {
			cands = append(cands, s)
		}
	}
	if len(cands) == 0 {
		return nil, errors.New("no broadcast streams for this match")
	}
	sort.SliceStable(cands, func(i, j int) bool { return mmStreamRank(cands[i]) < mmStreamRank(cands[j]) })
	urls := make([]string, len(cands))
	for i, c := range cands {
		urls[i] = streamURL(c.Provider, c.Parameter)
	}
	return urls, nil
}

// pickStreamURLs returns the broadcast page URLs of a match/event id, best first.
func pickStreamURLs(ctx context.Context, matchID string) ([]string, error) {
	var ed eventDetailsResp
	if err := getJSONCtx(ctx, gwBase+"/getEventDetails?hl=en-US&id="+matchID, &ed); err != nil {
		return nil, err
	}
	return mmRankStreams(ed.Data.Event.Streams)
}

// mmResolve returns the media URL of a page, whether yt-dlp reports it as a live stream
// and the pixel height of the chosen format (0 when unknown).
func mmResolve(ctx context.Context, page string) (media string, live bool, height int, err error) {
	args := append(mmJSRuntimeArgs(mmHasTool), "--socket-timeout", mmNetTimeoutSec, "--print", "is_live", "--print", "height", "--print", "urls", "-f", mmYTFormat, "--no-playlist", page)
	cmd := exec.Command(mmTool("ESTERM_YTDLP", "yt-dlp"), args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	stop, err := mmStart(ctx, cmd)
	if err != nil {
		return "", false, 0, err
	}
	werr := stop(false)
	if ctx.Err() != nil {
		return "", false, 0, ctx.Err()
	}
	if werr != nil {
		msg := strings.TrimSpace(errb.String())
		if i := strings.LastIndex(msg, "\n"); i >= 0 {
			msg = msg[i+1:]
		}
		return "", false, 0, fmt.Errorf("yt-dlp: %s", msg)
	}
	lines := strings.Fields(out.String())
	if len(lines) < 3 {
		return "", false, 0, errors.New("yt-dlp returned no URL")
	}
	height, _ = strconv.Atoi(lines[1])
	live = lines[0] == "True"
	// A finished broadcast kept as a live archive carries live=1&hang=1: the server holds
	// the response open until a range request, and range requests break ffmpeg's seeking.
	// Such a URL yields no frames, so report it instead of stalling the pipeline.
	if !live && strings.Contains(lines[2], "live=1") {
		return "", false, height, errMMArchive
	}
	return lines[2], live, height, nil
}

// mmStackW, mmStackH and mmStackBytes describe one ffmpeg output frame: region MM with
// region HUD (padded to the same width) below it.
const (
	mmStackW     = 432
	mmStackH     = 432 + 112
	mmStackBytes = mmStackW * mmStackH * 3
)

// mmMainDue returns the main frame to attach to minimap frame n of the run (nil when none
// is due) and the main frame still held back. Main frame k belongs to minimap frame
// round(k*mmFPS/mmMainFPS); the newest due one is used, older ones are dropped.
func mmMainDue(ch <-chan mmMainFrame, pending *mmMainFrame, n int) ([]byte, *mmMainFrame) {
	var due []byte
	for {
		if pending == nil {
			select {
			case m, ok := <-ch:
				if !ok {
					return due, nil
				}
				pending = &m
			default:
				return due, nil
			}
		}
		if (pending.k*mmFPS+mmMainFPS/2)/mmMainFPS > n {
			return due, pending
		}
		if due != nil {
			mmPutMainBuf(due)
		}
		due, pending = pending.buf, nil
	}
}

// mmSplitFrame splits one stacked ffmpeg frame (see mmRegionFilter) into region MM and
// region HUD (the left mmRegionHUD.W columns of the lower part).
func mmSplitFrame(idx int, buf []byte) mmFrame {
	mm := buf[:mmRegionMM.W*mmRegionMM.H*3]
	var hud []byte
	if len(buf) >= mmStackBytes+mmHUDBytes {
		hud = buf[mmStackBytes : mmStackBytes+mmHUDBytes] // pooled frame buffers carry their HUD
	} else {
		hud = make([]byte, mmHUDBytes)
	}
	for y := 0; y < mmRegionHUD.H; y++ {
		off := (mmRegionMM.H + y) * mmStackW * 3
		copy(hud[y*mmRegionHUD.W*3:], buf[off:off+mmRegionHUD.W*3])
	}
	return mmFrame{Idx: idx, MM: mm, HUD: hud}
}

// mmHUDBytes is the size of one frame's HUD region.
const mmHUDBytes = mmHUDW * mmHUDH * 3

// mmFrameBuf is one pooled frame buffer: ffmpeg's stacked frame followed by room for the
// HUD copy, which is what mmSplitFrame fills when handed such a buffer. The pools hold
// array pointers, so a Put stores a pointer and does not allocate an interface box.
type mmFrameBuf [mmStackBytes + mmHUDBytes]byte

// mmMainBuf is one pooled main-view buffer.
type mmMainBuf [mmMainBytes]byte

var (
	mmFrameBufs = sync.Pool{New: func() any { return new(mmFrameBuf) }}
	mmMainBufs  = sync.Pool{New: func() any { return new(mmMainBuf) }}
)

func mmGetFrameBuf() []byte { return mmFrameBufs.Get().(*mmFrameBuf)[:] }

// mmPutFrameBuf recycles b, which must be a whole buffer from mmGetFrameBuf.
func mmPutFrameBuf(b []byte) { mmFrameBufs.Put((*mmFrameBuf)(b)) }

func mmGetMainBuf() []byte { return mmMainBufs.Get().(*mmMainBuf)[:] }

// mmPutMainBuf recycles b, which must be a whole buffer from mmGetMainBuf.
func mmPutMainBuf(b []byte) { mmMainBufs.Put((*mmMainBuf)(b)) }

// mmReleaseFrame hands a frame's buffers back once its consumer is done with it; the frame
// must not be used afterwards. Frames not cut from a pooled buffer are left to the GC.
func mmReleaseFrame(f mmFrame) {
	if cap(f.MM) == mmStackBytes+mmHUDBytes {
		mmPutFrameBuf(f.MM[:cap(f.MM)])
	}
	if cap(f.Main) == mmMainBytes {
		mmPutMainBuf(f.Main[:cap(f.Main)])
	}
}

// mmMainFrame is one main-view frame read from its pipe: the k-th of the run.
type mmMainFrame struct {
	k   int
	buf []byte
}

// mmReadMain reads main-view frames from r until it ends, keeping the newest few in the
// returned channel (it never blocks: a reader that stalls would stall ffmpeg and with it
// the minimap frames, which are what this waits for). The channel is closed at the end.
func mmReadMain(r io.Reader) <-chan mmMainFrame {
	ch := make(chan mmMainFrame, 4)
	go func() {
		defer close(ch)
		for k := 0; ; k++ {
			buf := mmGetMainBuf()
			if _, err := io.ReadFull(r, buf); err != nil {
				mmPutMainBuf(buf)
				return
			}
			for {
				select {
				case ch <- mmMainFrame{k, buf}:
				default:
					select {
					case old := <-ch: // full: the oldest is dropped
						mmPutMainBuf(old.buf)
					default:
					}
					continue
				}
				break
			}
		}
	}()
	return ch
}

// mmRun runs one ffmpeg process and sends its frames, the first flagged Restart when
// restart is set. It returns the number of frames sent and the reason the process ended.
func mmRun(ctx context.Context, media string, o mmVideoOpts, live, hw, restart bool, base int, out chan<- mmFrame) (int, error) {
	var args []string
	var mainOut *mmMainPipe
	if o.Main {
		mo, err := newMMMainPipe()
		if err != nil {
			return 0, err
		}
		mainOut = mo
		defer mainOut.Close()
		args = mmFFmpegArgsMain(media, o.SS, live, hw, mmRegionFilterMain(o.FPS), false, mainOut.target)
	} else {
		args = mmFFmpegArgs(media, o.SS, live, hw, mmRegionFilter(o.FPS), false)
	}
	cmd := exec.Command(mmTool("ESTERM_FFMPEG", "ffmpeg"), args...)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	var mainCh <-chan mmMainFrame
	if mainOut != nil {
		mainOut.attach(cmd)
		mainCh = mmReadMain(mainOut)
	}
	stop, err := mmStart(ctx, cmd)
	if mainOut != nil {
		mainOut.started()
	}
	if err != nil {
		return 0, err
	}
	n := 0
	var rerr error
	var pending *mmMainFrame // the next main frame, held until its minimap frame is due
	for {
		buf := mmGetFrameBuf()
		if _, rerr = io.ReadFull(pipe, buf[:mmStackBytes]); rerr != nil {
			mmPutFrameBuf(buf)
			break
		}
		f := mmSplitFrame(base+n, buf)
		f.Restart = restart && n == 0
		if mainCh != nil {
			f.Main, pending = mmMainDue(mainCh, pending, n)
		}
		select {
		case out <- f:
			n++
		case <-ctx.Done():
			mmPutFrameBuf(buf)
			if f.Main != nil {
				mmPutMainBuf(f.Main)
			}
			if pending != nil {
				mmPutMainBuf(pending.buf)
			}
			stop(true)
			return n, ctx.Err()
		}
	}
	if pending != nil {
		mmPutMainBuf(pending.buf)
	}
	stop(true)
	if rerr == io.EOF && errb.Len() == 0 {
		return n, io.EOF
	}
	msg := strings.TrimSpace(errb.String())
	if msg == "" {
		return n, rerr
	}
	if i := strings.LastIndex(msg, "\n"); i >= 0 {
		msg = msg[i+1:]
	}
	return n, errors.New(msg)
}

// startMMVideo launches the pipeline and returns minimap frames. The channel is
// closed when the pipeline ends (ctx cancelled, file/VOD EOF, or fatal error).
func startMMVideo(ctx context.Context, o mmVideoOpts, status func(string)) (<-chan mmFrame, error) {
	if o.FPS <= 0 {
		o.FPS = 2
	}
	if status == nil {
		status = func(string) {}
	}
	page := !o.VOD && mmIsPage(o.Src)
	check := ""
	if page {
		check = o.Src
	}
	if missing := mmToolsMissing(check); len(missing) > 0 {
		return nil, fmt.Errorf("missing tools: %s", strings.Join(missing, ", "))
	}
	// Pages report liveness when resolved; direct media URLs are live unless a start offset is given.
	live := !page && mmIsHTTP(o.Src) && o.SS == 0 && !o.VOD
	out := make(chan mmFrame, 8)
	go func() {
		defer close(out)
		mmLoop(ctx, o, page, live, status, out)
	}()
	return out, nil
}

func mmLoop(ctx context.Context, o mmVideoOpts, page, live bool, status func(string), out chan<- mmFrame) {
	hw := true
	base := o.IdxBase
	resolved := false // a page's liveness is unknown until yt-dlp answers once
	backoff := mmBackoffStart
	for ctx.Err() == nil {
		media := o.Src
		if page {
			status("resolving stream")
			m, isLive, height, err := mmResolve(ctx, o.Src)
			if err == nil {
				media, live, resolved = m, isLive, true
				if o.OnHeight != nil {
					o.OnHeight(height)
				}
			}
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				status("error: " + err.Error())
				if errors.Is(err, errMMArchive) || (resolved && !live) || !mmSleep(ctx, backoff) {
					return
				}
				backoff = min(backoff*2, mmBackoffMax)
				continue
			}
		}
		runCtx, cancel := ctx, context.CancelFunc(func() {})
		if page && live {
			runCtx, cancel = context.WithTimeout(ctx, mmURLRefresh)
		}
		mode := "sw"
		if hw && runtime.GOOS == "darwin" {
			mode = "hw"
		}
		status(fmt.Sprintf("streaming %dx%d h264 %s", mmStackW, mmStackH, mode))
		n, err := mmRun(runCtx, media, o, live, hw, base > 0, base, out)
		refresh := runCtx.Err() != nil && ctx.Err() == nil
		cancel()
		base += n
		if ctx.Err() != nil {
			return
		}
		if n > 0 {
			backoff = mmBackoffStart
		}
		switch {
		case refresh:
			status("refreshing stream URL")
			continue
		case n == 0 && hw && runtime.GOOS == "darwin" && err != io.EOF:
			hw = false
			status("hw decode failed, software")
			continue
		case !live:
			if err != io.EOF {
				status("error: " + err.Error())
			}
			return
		}
		status(fmt.Sprintf("restarting in %ds", int(backoff/time.Second)))
		if !mmSleep(ctx, backoff) {
			return
		}
		backoff = min(backoff*2, mmBackoffMax)
	}
}

func mmSleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
