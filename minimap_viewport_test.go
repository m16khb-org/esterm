package main

import (
	"image"
	"image/color"
	"math"
	"testing"
)

// smoothIconFrame is a frame with one soft, portrait-like icon (low spatial frequencies,
// as a 25 px portrait is after the video's compression) at (100, 100) in a blue ring, and
// the template of that icon.
func smoothIconFrame() ([]byte, champTpl) {
	im := image.NewRGBA(image.Rect(0, 0, mmTplSize, mmTplSize))
	for y := 0; y < mmTplSize; y++ {
		for x := 0; x < mmTplSize; x++ {
			v := uint8(128 + 100*math.Sin(2*math.Pi*float64(x)/17+1)*math.Cos(2*math.Pi*float64(y)/13+0.5))
			im.Set(x, y, color.RGBA{v, v, v, 255})
		}
	}
	rgb := synthFrame()
	for y := 0; y < mmTplSize; y++ {
		for x := 0; x < mmTplSize; x++ {
			r, g, b, _ := im.At(x, y).RGBA()
			i := 3 * ((100+y)*mmFrameSize + 100 + x)
			rgb[i], rgb[i+1], rgb[i+2] = byte(r>>8), byte(g>>8), byte(b>>8)
		}
	}
	addRing(rgb, 100, 100, mmTplSize, blueRing)
	return rgb, newChampTpl(champRef{PID: 1, Team: 100, Champ: "C"}, im, mmTplSize)
}

// whiteLines paints a horizontal and a vertical 2 px white line over rgb, the way the
// broadcast draws the camera rectangle.
func whiteLines(rgb []byte, y, x, from, to int) {
	set := func(px, py int) {
		i := 3 * (py*mmFrameSize + px)
		rgb[i], rgb[i+1], rgb[i+2] = 240, 240, 240
	}
	for k := from; k < to; k++ {
		set(k, y)
		set(k, y+1)
		set(x, k)
		set(x+1, k)
	}
}

func TestUnlinePaintsOutTheCameraRectangle(t *testing.T) {
	clean, tpl := smoothIconFrame()
	lined := append([]byte(nil), clean...)
	whiteLines(lined, 110, 110, 40, 200) // across the icon, both ways
	var sc mmLineScratch
	out, found := sc.unline(lined)
	if !found {
		t.Fatal("two 160 px white lines not found")
	}
	i := 3 * (110*mmFrameSize + 90)
	if out[i] > 200 {
		t.Fatalf("the line is still there on the map: pixel %d", out[i])
	}
	// the icon scores as well as it did before the lines, and not so without the repair
	tr := newMMTracker([]champTpl{tpl})
	tr.img.set(lined)
	raw := tr.tpls[0].scan(&tr.img, mmWin{100, 100, 100, 100}, nil, -1, nil)
	d := tr.Step(0, lined, nil)[0]
	expectAt(t, "icon under the lines", d, 100, 100)
	if raw.score > 0.65 || d.Score < raw.score+0.1 {
		t.Fatalf("score %.2f under the lines painted out, %.2f without: want it clearly better, from a weak raw match", d.Score, raw.score)
	}
}

func TestUnlineLeavesPlainFramesAndShortBrightDetailsAlone(t *testing.T) {
	var sc mmLineScratch
	plain, _ := smoothIconFrame()
	if out, found := sc.unline(plain); found || &out[0] != &plain[0] {
		t.Fatalf("a frame without lines was changed (found %v)", found)
	}
	short := append([]byte(nil), plain...)
	whiteLines(short, 200, 250, 150, 150+mmLineRun-5) // a bright dash of an icon, not a camera line
	if _, found := sc.unline(short); found {
		t.Fatalf("a %d px dash taken for a camera line", mmLineRun-5)
	}
}
