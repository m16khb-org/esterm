package main

import (
	"image"
	"image/color"
	"math"
	"math/rand"
	"testing"
)

// synthMap is a smooth, textured terrain-coloured square standing in for map11.png.
func synthMap(side int) *image.RGBA {
	rng := rand.New(rand.NewSource(7))
	const cell = 16
	n := side/cell + 2
	lat := make([]float64, n*n)
	for i := range lat {
		lat[i] = rng.Float64()
	}
	im := image.NewRGBA(image.Rect(0, 0, side, side))
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			fx, fy := float64(x)/cell, float64(y)/cell
			ix, iy := int(fx), int(fy)
			ax, ay := fx-float64(ix), fy-float64(iy)
			v := lat[iy*n+ix]*(1-ax)*(1-ay) + lat[iy*n+ix+1]*ax*(1-ay) + lat[(iy+1)*n+ix]*(1-ax)*ay + lat[(iy+1)*n+ix+1]*ax*ay
			im.Set(x, y, color.RGBA{uint8(60 + 110*v), uint8(55 + 100*v), uint8(25 + 40*v), 255})
		}
	}
	return im
}

// synthRegion renders a mmRegionMM capture: noise everywhere, the map pasted into box.
func synthRegion(m *image.RGBA, box mmBox) []byte {
	rng := rand.New(rand.NewSource(3))
	mm := make([]byte, mmRegionMM.W*mmRegionMM.H*3)
	rng.Read(mm)
	for y := 0; y < box.H; y++ {
		for x := 0; x < box.W; x++ {
			c := m.RGBAAt(x*m.Bounds().Dx()/box.W, y*m.Bounds().Dy()/box.H)
			i := 3 * ((box.Y-mmRegionMM.Y+y)*mmRegionMM.W + box.X - mmRegionMM.X + x)
			mm[i], mm[i+1], mm[i+2] = c.R, c.G, c.B
		}
	}
	return mm
}

func TestFitMapFindsBoxFromOffNominal(t *testing.T) {
	m := synthMap(512)
	mmSetMapRef(m)
	truth := mmBox{1641, 803, 271, 271}
	mm := synthRegion(m, truth)
	// nominal is off by a few px and a few percent, as another league's layout would be
	box, score, ok := mmFitMap(mm, mmBox{1647, 797, 261, 261})
	if !ok {
		t.Fatalf("map not found, score %.2f", score)
	}
	if absInt(box.X-truth.X) > 1 || absInt(box.Y-truth.Y) > 1 || absInt(box.W-truth.W) > 1 || box.W != box.H {
		t.Fatalf("fitted %+v, want %+v", box, truth)
	}
}

func TestFitMapRejectsNonMap(t *testing.T) {
	mmSetMapRef(synthMap(512))
	noise := make([]byte, mmRegionMM.W*mmRegionMM.H*3)
	rand.New(rand.NewSource(5)).Read(noise)
	flat := make([]byte, len(noise))
	for i := range flat {
		flat[i] = 90
	}
	for name, mm := range map[string][]byte{"noise": noise, "flat": flat} {
		if _, score, ok := mmFitMap(mm, mmBox{1641, 803, 271, 271}); ok {
			t.Errorf("%s accepted, score %.2f", name, score)
		}
	}
}

func TestCropMapResamplesBox(t *testing.T) {
	// horizontal ramp: the crop must span exactly the box, edge to edge
	mm := make([]byte, mmRegionMM.W*mmRegionMM.H*3)
	for y := 0; y < mmRegionMM.H; y++ {
		for x := 0; x < mmRegionMM.W; x++ {
			i := 3 * (y*mmRegionMM.W + x)
			mm[i], mm[i+1], mm[i+2] = byte(x/2), byte(y/2), 7
		}
	}
	box := mmBox{1560, 700, 200, 200}
	dst := make([]byte, mmFrameSize*mmFrameSize*3)
	mmCropMap(mm, box, dst)
	at := func(x, y int) (byte, byte) { i := 3 * (y*mmFrameSize + x); return dst[i], dst[i+1] }
	wantR := func(srcX float64) byte { return byte((srcX + float64(box.X-mmRegionMM.X)) / 2) }
	for _, p := range [][2]int{{0, 0}, {mmFrameSize - 1, 0}, {144, 144}} {
		r, g := at(p[0], p[1])
		sx := (float64(p[0]) + 0.5) * float64(box.W) / mmFrameSize
		sy := (float64(p[1]) + 0.5) * float64(box.H) / mmFrameSize
		wantG := byte((sy + float64(box.Y-mmRegionMM.Y)) / 2)
		if math.Abs(float64(r)-float64(wantR(sx))) > 1 || math.Abs(float64(g)-float64(wantG)) > 1 {
			t.Errorf("pixel %v: got (%d,%d), want (%d,%d)", p, r, g, wantR(sx), wantG)
		}
	}
}

func TestHasTerrain(t *testing.T) {
	m := synthMap(512)
	box := mmBox{1639, 797, 281, 281}
	if !mmHasTerrain(synthRegion(m, box), box) {
		t.Error("map terrain not recognised")
	}
	if mmHasTerrain(make([]byte, mmRegionMM.W*mmRegionMM.H*3), box) {
		t.Error("blank region reported as terrain")
	}
}
