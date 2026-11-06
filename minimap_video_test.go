package main

import (
	"bytes"
	"io"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestMMRegionFilter(t *testing.T) {
	got := mmRegionFilter(2)
	want := "fps=2,scale=1920:1080,split[m][h];" +
		"[m]crop=432:432:1488:648,format=rgb24[m2];" +
		"[h]crop=240:112:840:0,format=rgb24,pad=432:112:0:0[h2];" +
		"[m2][h2]vstack"
	if got != want {
		t.Fatalf("got %s", got)
	}
	if mmStackW != mmRegionMM.W || mmStackH != mmRegionMM.H+mmRegionHUD.H || mmRegionHUD.W > mmStackW {
		t.Fatal("stack constants disagree with the regions")
	}
}

func TestMMSplitFrame(t *testing.T) {
	buf := make([]byte, mmStackBytes)
	for y := 0; y < mmStackH; y++ {
		for x := 0; x < mmStackW; x++ {
			o := (y*mmStackW + x) * 3
			buf[o], buf[o+1] = byte(y), byte(x)
		}
	}
	f := mmSplitFrame(7, buf)
	if f.Idx != 7 || len(f.MM) != mmRegionMM.W*mmRegionMM.H*3 || len(f.HUD) != mmRegionHUD.W*mmRegionHUD.H*3 {
		t.Fatalf("sizes: idx %d mm %d hud %d", f.Idx, len(f.MM), len(f.HUD))
	}
	if o := (5*mmRegionMM.W + 9) * 3; f.MM[o] != 5 || f.MM[o+1] != 9 {
		t.Fatal("MM content shifted")
	}
	if o := (3*mmRegionHUD.W + 11) * 3; int(f.HUD[o]) != (mmRegionMM.H+3)%256 || f.HUD[o+1] != 11 {
		t.Fatal("HUD content shifted")
	}
}

func TestMMFFmpegArgs(t *testing.T) {
	live := strings.Join(mmFFmpegArgs("u", 0, true, false, "vf", false), " ")
	if !strings.Contains(live, "-fflags nobuffer -flags low_delay") || strings.Contains(live, "-ss") {
		t.Fatalf("live args: %s", live)
	}
	vod := strings.Join(mmFFmpegArgs("u", 1320, false, true, "vf", true), " ")
	if !strings.Contains(vod, "-ss 1320.000 -i u") || !strings.Contains(vod, "-frames:v 1") || strings.Contains(vod, "nobuffer") {
		t.Fatalf("vod args: %s", vod)
	}
	if hw := strings.Contains(vod, "-hwaccel videotoolbox"); hw != (runtime.GOOS == "darwin") {
		t.Fatalf("hwaccel presence wrong: %s", vod)
	}
	if !strings.HasSuffix(vod, "-f rawvideo -pix_fmt rgb24 pipe:1") {
		t.Fatalf("tail: %s", vod)
	}
}

func TestMMJSRuntimeArgs(t *testing.T) {
	has := func(names ...string) func(string) bool {
		return func(n string) bool {
			for _, x := range names {
				if x == n {
					return true
				}
			}
			return false
		}
	}
	if got := mmJSRuntimeArgs(has("deno", "bun")); got != nil {
		t.Fatalf("deno present: %v", got)
	}
	if got := mmJSRuntimeArgs(has("bun", "node")); !reflect.DeepEqual(got, []string{"--js-runtimes", "bun"}) {
		t.Fatalf("bun: %v", got)
	}
	if got := mmJSRuntimeArgs(has("node")); !reflect.DeepEqual(got, []string{"--js-runtimes", "node"}) {
		t.Fatalf("node: %v", got)
	}
	if got := mmJSRuntimeArgs(has()); got != nil {
		t.Fatalf("none: %v", got)
	}
}

func TestMMRankStreams(t *testing.T) {
	if _, err := mmRankStreams(nil); err == nil {
		t.Fatal("want error")
	}
	got, err := mmRankStreams([]streamInfo{
		{Provider: "afreecatv", Locale: "en-US", Parameter: "a"},
		{Provider: "twitch", Locale: "en-US", Parameter: "ten"},
		{Provider: "youtube", Locale: "ko-KR", Parameter: "yko"},
		{Provider: "youtube", Locale: "en-US", Parameter: "yen"},
		{Provider: "twitch", Locale: "ko-KR", Parameter: "tko"},
		{Provider: "youtube", Locale: "en-US"},
	})
	want := []string{
		"https://www.youtube.com/watch?v=yen",
		"https://www.youtube.com/watch?v=yko",
		"https://www.twitch.tv/ten",
		"https://www.twitch.tv/tko",
		streamURL("afreecatv", "a"),
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q %v", got, err)
	}
}

func TestMMYTFormatPrefers1080(t *testing.T) {
	// the selector must cap at 1080 rows (never pick 4K) and prefer H.264
	if !strings.Contains(mmYTFormat, "[vcodec^=avc1][height<=1080]") || !strings.Contains(mmYTFormat, "/bv*[height<=1080]") {
		t.Fatalf("selector %q", mmYTFormat)
	}
}

func TestMMSourceNote(t *testing.T) {
	for h, want := range map[int]string{0: "", 1080: "", 720: "720p source"} {
		if got := mmSourceNote(h); got != want {
			t.Errorf("%d: %q", h, got)
		}
	}
}

func TestMMIsPage(t *testing.T) {
	for src, want := range map[string]bool{
		"https://www.youtube.com/watch?v=x": true,
		"https://x/live.m3u8?token=1":       false,
		"https://x/a.mp4":                   false,
		"/tmp/a.mp4":                        false,
		"https://www.twitch.tv/lck":         true,
	} {
		if mmIsPage(src) != want {
			t.Errorf("%s: want %v", src, want)
		}
	}
}

func TestMMFFmpegArgsTimeOutStalledNetworkInput(t *testing.T) {
	url := strings.Join(mmFFmpegArgs("https://h/live.m3u8", 0, true, false, "vf", false), " ")
	if !strings.Contains(url, "-rw_timeout "+mmNetTimeoutUs+" -i https://h/live.m3u8") {
		t.Fatalf("a network input needs a read timeout before -i: %s", url)
	}
	if file := strings.Join(mmFFmpegArgs("/tmp/a.mp4", 0, false, false, "vf", false), " "); strings.Contains(file, "rw_timeout") {
		t.Fatalf("a local file takes no network timeout: %s", file)
	}
}

func TestMMPooledFrameBuffersKeepFrameContent(t *testing.T) {
	fill := func(buf []byte, seed byte) {
		for i := range buf[:mmStackBytes] {
			buf[i] = byte(i) ^ seed
		}
	}
	buf := mmGetFrameBuf()
	fill(buf, 1)
	f := mmSplitFrame(3, buf)
	want := mmSplitFrame(3, append([]byte(nil), buf[:mmStackBytes]...)) // unpooled: own HUD copy
	if !bytes.Equal(f.MM, want.MM) || !bytes.Equal(f.HUD, want.HUD) {
		t.Fatal("pooled frame differs from the unpooled split")
	}
	mmReleaseFrame(f)
	mmReleaseFrame(want) // not pooled: must be a no-op
	buf = mmGetFrameBuf()
	fill(buf, 2)
	g := mmSplitFrame(4, buf)
	if !bytes.Equal(g.HUD, mmSplitFrame(4, append([]byte(nil), buf[:mmStackBytes]...)).HUD) {
		t.Fatal("recycled buffer produced a stale HUD")
	}
}

func TestMMFFmpegArgsWithMainView(t *testing.T) {
	args := strings.Join(mmFFmpegArgsMain("u", 0, false, false, mmRegionFilterMain(mmFPS), false, "pipe:4"), " ")
	for _, want := range []string{"-filter_complex", "-map [mm] -f rawvideo -pix_fmt rgb24 pipe:1", "-map [mv] -f rawvideo -pix_fmt rgba pipe:4"} {
		if !strings.Contains(args, want) {
			t.Fatalf("missing %q in %s", want, args)
		}
	}
	g := mmRegionFilterMain(mmFPS)
	for _, want := range []string{"[mm]", "[mv]", "fps=2,crop=1920:816:0:0"} {
		if !strings.Contains(g, want) {
			t.Fatalf("filter graph lacks %q: %s", want, g)
		}
	}
	if plain := strings.Join(mmFFmpegArgs("u", 0, false, false, "vf", false), " "); strings.Contains(plain, "pipe:4") {
		t.Fatalf("the plain command line must not open the main-view pipe: %s", plain)
	}
}

func TestMMMainDueAttachesEachMainFrameToItsMinimapFrame(t *testing.T) {
	ch := make(chan mmMainFrame, 8)
	for k := 0; k < 4; k++ {
		ch <- mmMainFrame{k: k, buf: []byte{byte(k)}}
	}
	var pending *mmMainFrame
	var got []int
	for n := 0; n <= 9; n++ {
		var b []byte
		b, pending = mmMainDue(ch, pending, n)
		if b != nil {
			got = append(got, int(b[0])*100+n)
		}
	}
	// main frame k is due at minimap frame round(k*mmFPS/mmMainFPS)
	want := []int{0, 103, 205, 308}
	if mmFPS != 5 || mmMainFPS != 2 {
		t.Skip("the expected frame numbers are for 5 fps minimap and 2 fps main view")
	}
	if len(got) != len(want) {
		t.Fatalf("attached %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("attached %v, want %v", got, want)
		}
	}
}

func TestMMReadMainNeverBlocksTheWriter(t *testing.T) {
	pr, pw := io.Pipe()
	ch := mmReadMain(pr)
	done := make(chan struct{})
	go func() {
		defer close(done)
		frame := make([]byte, mmMainBytes)
		for k := 0; k < 12; k++ { // nobody reads the channel: the oldest frames are dropped
			frame[0] = byte(k)
			if _, err := pw.Write(frame); err != nil {
				return
			}
		}
		pw.Close()
	}()
	<-done
	var ks []int
	for m := range ch {
		ks = append(ks, m.k)
	}
	if len(ks) == 0 || len(ks) > 4 || ks[len(ks)-1] != 11 {
		t.Fatalf("kept frames %v, want at most 4 ending with the newest (11)", ks)
	}
}
