package main

import (
	"image"
	"image/color"
	"math"
	"testing"
)

// synthBar paints a health bar the finder accepts onto img: the learned template tile in
// grey, the bar's fill in team colour on top, and the level window of the given level.
func synthBar(img *image.RGBA, x, y, fill, team, level int) {
	for r := 0; r < mvTileH; r++ {
		for c := 0; c < mvTileW; c++ {
			v := uint8(mvTplMeanA[r*mvTileW+c] / 8)
			img.SetRGBA(x-mvTileX+c, y-mvTileY+r, color.RGBA{v, v, v, 255})
		}
	}
	col := color.RGBA{40, 120, 225, 255}
	if team == 1 {
		col = color.RGBA{215, 50, 40, 255}
	}
	for yy := y + 1; yy < y+10; yy++ {
		for xx := x; xx < x+fill; xx++ {
			img.SetRGBA(xx, yy, col)
		}
	}
	for r := 0; r < mvWinH; r++ {
		for c := 0; c < mvWinW; c++ {
			v := mvLevelTpl[0][level-1][r*mvWinW+c]
			img.SetRGBA(x-mvWinX+c, y+mvWinY+r, color.RGBA{v, v, v, 255})
		}
	}
}

func TestMvFindBarsSynthetic(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1920, 816))
	for i := range img.Pix {
		img.Pix[i] = 40
	}
	synthBar(img, 600, 300, 80, 0, 12)
	synthBar(img, 1200, 500, 50, 1, 7)
	bars := mvFindBars(img, 1)
	if len(bars) != 2 {
		t.Fatalf("found %d bars, want 2: %+v", len(bars), bars)
	}
	for _, b := range bars {
		wantX, wantY, hp, lvl, team := 600+54.0, 300+5.0, 80/108.0, 12, 0
		if b.Team == 1 {
			wantX, wantY, hp, lvl, team = 1200+54, 500+5, 50/108.0, 7, 1
		}
		if b.Team != team || math.Abs(b.X-wantX) > 3 || math.Abs(b.Y-wantY) > 3 {
			t.Errorf("bar %+v, want team %d at (%.0f,%.0f)", b, team, wantX, wantY)
		}
		if math.Abs(b.HPFrac-hp) > 0.05 {
			t.Errorf("hp %.3f, want %.3f", b.HPFrac, hp)
		}
		if b.Level != lvl {
			t.Errorf("level %d, want %d", b.Level, lvl)
		}
	}
}

func TestMvFindBarsRejectsPlainFill(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1920, 816))
	for i := range img.Pix {
		img.Pix[i] = 40
	}
	// a minion-like bar: coloured fill without the level box and frame
	for y := 300; y < 304; y++ {
		for x := 500; x < 560; x++ {
			img.SetRGBA(x, y, color.RGBA{215, 50, 40, 255})
		}
	}
	if bars := mvFindBars(img, 1); len(bars) != 0 {
		t.Fatalf("plain fill accepted: %+v", bars)
	}
}

func TestMvReadLevelAllLevels(t *testing.T) {
	for font := 0; font < mvFonts; font++ {
		for lvl := 1; lvl <= mvLevelMax; lvl++ {
			lum := make([]uint8, 100*60)
			for r := 0; r < mvWinH; r++ {
				for c := 0; c < mvWinW; c++ {
					lum[(20+mvWinY+r)*100+(40-mvWinX+c)] = mvLevelTpl[font][lvl-1][r*mvWinW+c]
				}
			}
			got, corr, _ := mvReadLevel(lum, 100, 60, 40, 20, 1)
			if got != lvl {
				t.Errorf("font %d level %d read as %d (corr %.2f)", font, lvl, got, corr)
			}
		}
	}
}

// synthView draws a one-pixel-wide rectangle (edges listed in skip are omitted).
func synthView(n int, x0, y0, x1, y1 int, skip string) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 90, 100, 70, 255
	}
	w := color.RGBA{250, 250, 250, 255}
	for x := x0; x <= x1; x++ {
		if x >= 0 && x < n {
			if y0 >= 0 && y0 < n && !contains(skip, 't') {
				img.SetRGBA(x, y0, w)
			}
			if y1 >= 0 && y1 < n && !contains(skip, 'b') {
				img.SetRGBA(x, y1, w)
			}
		}
	}
	for y := y0; y <= y1; y++ {
		if y >= 0 && y < n {
			if x0 >= 0 && x0 < n && !contains(skip, 'l') {
				img.SetRGBA(x0, y, w)
			}
			if x1 >= 0 && x1 < n && !contains(skip, 'r') {
				img.SetRGBA(x1, y, w)
			}
		}
	}
	return img
}

func contains(s string, c byte) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return true
		}
	}
	return false
}

func mvNear(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestMvFindViewport(t *testing.T) {
	const n = 288
	check := func(name string, v mvView, x0, y0, x1, y1 int) {
		t.Helper()
		if !v.OK {
			t.Fatalf("%s: viewport not found", name)
		}
		tol := 1.5 / n
		if !mvNear(v.X0, float64(x0)/n, tol) || !mvNear(v.Y0, float64(y0)/n, tol) || !mvNear(v.X1, float64(x1)/n, tol) || !mvNear(v.Y1, float64(y1)/n, tol) {
			t.Errorf("%s: got %+v, want %d,%d..%d,%d of %d", name, v, x0, y0, x1, y1, n)
		}
	}
	check("four edges", mvFindViewport(synthView(n, 100, 90, 210, 152, ""), mvView{}), 100, 90, 210, 152)
	check("occluded edge", mvFindViewport(synthView(n, 100, 90, 210, 152, "b"), mvView{}), 100, 90, 210, 152)
	// clipped by the map edge: three edges are enough, two are not without a size
	three := mvFindViewport(synthView(n, 200, 150, 310, 212, "r"), mvView{})
	check("three edges", three, 200, 150, 310, 212)
	clipped := synthView(n, 200, 220, 310, 282, "br")
	if v := mvFindViewport(clipped, mvView{}); v.OK {
		t.Errorf("two edges without a remembered size accepted: %+v", v)
	}
	prev := mvView{X0: 0.3, Y0: 0.3, X1: 0.3 + 110.0/n, Y1: 0.3 + 62.0/n, OK: true}
	got := mvFindViewport(synthView(n, 200, 220, 310, 282, "br"), prev)
	check("two edges with prior", got, 200, 220, 310, 282)
	if v := mvFindViewport(synthView(n, 0, 0, 0, 0, "tblr"), mvView{}); v.OK {
		t.Errorf("empty map gave a viewport: %+v", v)
	}
}

func TestMvToMapCentreAndTilt(t *testing.T) {
	v := mvView{X0: 0.2, Y0: 0.3, X1: 0.6, Y1: 0.5, OK: true}
	if _, _, ok := mvToMap(mvBar{X: 960, Y: 540}, mvView{}); ok {
		t.Fatal("mapped without a viewport")
	}
	x, y, _ := mvToMap(mvBar{X: 960, Y: 540}, v)
	if !mvNear(x, 0.4, 0.01) || y < 0.4 || y > 0.46 {
		t.Errorf("screen centre -> (%.3f, %.3f)", x, y)
	}
	xl, _, _ := mvToMap(mvBar{X: 200, Y: 540}, v)
	xr, _, _ := mvToMap(mvBar{X: 1700, Y: 540}, v)
	if !(xl < x && x < xr) {
		t.Errorf("x not increasing with the screen: %.3f %.3f %.3f", xl, x, xr)
	}
	_, yt, _ := mvToMap(mvBar{X: 960, Y: 100}, v)
	_, yb, _ := mvToMap(mvBar{X: 960, Y: 800}, v)
	if !(yt < y && y < yb) {
		t.Errorf("y not increasing down the screen: %.3f %.3f %.3f", yt, y, yb)
	}
}

func TestMvAssign(t *testing.T) {
	v := mvView{X0: 0.2, Y0: 0.3, X1: 0.6, Y1: 0.5, OK: true}
	champs := []mvChamp{
		{PID: 1, Team: 100, Level: 9, HPFrac: 0.9, Alive: true},
		{PID: 2, Team: 100, Level: 11, HPFrac: 0.5, Alive: true},
		{PID: 3, Team: 100, Level: 11, HPFrac: 0.5, Alive: true},
		{PID: 4, Team: 100, Level: 11, HPFrac: 0.5, Alive: false}, // dead: never assigned
		{PID: 6, Team: 200, Level: 9, HPFrac: 0.9, Alive: true},
	}
	bars := []mvBar{
		{Team: 0, X: 700, Y: 400, HPFrac: 0.88, Level: 9, LevelC: 1, Conf: 1},
		{Team: 0, X: 1300, Y: 600, HPFrac: 0.52, Level: 11, LevelC: 1, Conf: 1},
		{Team: 1, X: 900, Y: 300, HPFrac: 0.9, Level: 9, LevelC: 1, Conf: 1},
	}
	obs := mvAssign(bars, v, champs, 0, nil)
	got := map[int]mvObs{}
	for _, o := range obs {
		got[o.PID] = o
	}
	for _, pid := range []int{1, 2, 6} {
		if _, ok := got[pid]; !ok {
			t.Fatalf("pid %d not assigned: %+v", pid, obs)
		}
	}
	if _, ok := got[4]; ok {
		t.Error("dead champion assigned")
	}
	// pids 2 and 3 tie on level and health: the last sighting decides
	x, y, _ := mvToMap(bars[1], v)
	champs[2].HasPrev, champs[2].PrevX, champs[2].PrevY, champs[2].PrevAge = true, x+0.005, y-0.005, 1
	champs[1].HasPrev, champs[1].PrevX, champs[1].PrevY, champs[1].PrevAge = true, 0.9, 0.9, 1
	obs = mvAssign(bars, v, champs, 0, nil)
	won := 0
	for _, o := range obs {
		if o.PID == 3 && o.X == x && o.Y == y {
			won = 3
		}
	}
	if won != 3 {
		t.Errorf("tie not broken by proximity: %+v", obs)
	}
	if mvAssign(bars, mvView{}, champs, 0, nil) != nil {
		t.Error("observations without a viewport")
	}
}

func TestMvEnlarge(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 100, 50))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	up := mvEnlarge(img, 1/0.88)
	if up.Bounds().Dx() != 113 || up.Bounds().Dy() != 56 {
		t.Fatalf("size %v", up.Bounds())
	}
	if p := up.Pix[up.PixOffset(60, 30)]; p != 200 {
		t.Errorf("flat image changed: %d", p)
	}
}
