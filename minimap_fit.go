package main

import (
	"image"
	"math"
	"sync"
)

// Locating the minimap square inside the wide capture region mmRegionMM: the map is
// correlated against the Data Dragon map image (the broadcast draws the same terrain),
// coarse to fine over position and scale.

const (
	mmFitPos    = 20   // search radius in px around the nominal top-left
	mmFitScale  = 0.08 // search radius as a share of the nominal side
	mmFitMinNCC = 0.40 // lowest map11 correlation accepted as "the minimap is here"
)

// mmIconD is the champion icon diameter in mmFrameSize space per layout, measured as the
// template size with the best mean top-4 match on map-fitted crops of each league's VOD:
// worlds 24, lck/lpl 25, lec 26 (neighbouring sizes score 0.01-0.04 lower).
var mmIconD = map[string]int{"worlds": 24, "lck": 25, "lpl": 25, "lec": 26}

// mmMapRef holds the Data Dragon map as gray, plus its area-resampled versions by side.
var mmMapRef struct {
	mu    sync.Mutex
	gray  []float32
	w, h  int
	sides map[int]*mmRef
}

// mmRef is a zero-mean reference square for the correlation.
type mmRef struct {
	side int
	v    []float32
	norm float32
}

// mmSetMapRef installs the map image (Data Dragon map11.png) used by mmFitMap.
func mmSetMapRef(img image.Image) {
	b := img.Bounds()
	g := make([]float32, b.Dx()*b.Dy())
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			r, gg, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			g[y*b.Dx()+x] = float32(0.299*float64(r>>8) + 0.587*float64(gg>>8) + 0.114*float64(bl>>8))
		}
	}
	mmMapRef.mu.Lock()
	mmMapRef.gray, mmMapRef.w, mmMapRef.h = g, b.Dx(), b.Dy()
	mmMapRef.sides = map[int]*mmRef{}
	mmMapRef.mu.Unlock()
}

// mmRefFor returns the map resampled to side×side (cached).
func mmRefFor(side int) *mmRef {
	mmMapRef.mu.Lock()
	defer mmMapRef.mu.Unlock()
	if r := mmMapRef.sides[side]; r != nil {
		return r
	}
	v := make([]float32, side*side)
	xs, ys := areaTaps(mmMapRef.w, side), areaTaps(mmMapRef.h, side)
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			var s float32
			for _, ty := range ys[y] {
				for _, tx := range xs[x] {
					s += ty.w * tx.w * mmMapRef.gray[ty.i*mmMapRef.w+tx.i]
				}
			}
			v[y*side+x] = s
		}
	}
	var mean float32
	for _, p := range v {
		mean += p
	}
	mean /= float32(len(v))
	var nn float32
	for i := range v {
		v[i] -= mean
		nn += v[i] * v[i]
	}
	r := &mmRef{side, v, float32(math.Sqrt(float64(nn)))}
	mmMapRef.sides[side] = r
	return r
}

// mmTap is one source sample of an area resample.
type mmTap struct {
	i int
	w float32
}

// areaTaps returns, for every destination index of an n→m resample, the source samples
// covered by that destination cell with their area weights (summing to 1).
func areaTaps(n, m int) [][]mmTap {
	out := make([][]mmTap, m)
	s := float64(n) / float64(m)
	for d := range out {
		a, b := float64(d)*s, float64(d+1)*s
		for i := int(a); i < n && float64(i) < b; i++ {
			w := math.Min(b, float64(i+1)) - math.Max(a, float64(i))
			if w > 0 {
				out[d] = append(out[d], mmTap{i, float32(w / s)})
			}
		}
	}
	return out
}

// mmGray is a gray image.
type mmGray struct {
	p    []float32
	w, h int
}

func mmToGray(rgb []byte, w, h int) mmGray {
	g := mmGray{make([]float32, w*h), w, h}
	for i := range g.p {
		p := rgb[3*i : 3*i+3 : 3*i+3]
		g.p[i] = 0.299*float32(p[0]) + 0.587*float32(p[1]) + 0.114*float32(p[2])
	}
	return g
}

// down averages f×f blocks.
func (g mmGray) down(f int) mmGray {
	o := mmGray{make([]float32, (g.w/f)*(g.h/f)), g.w / f, g.h / f}
	inv := 1 / float32(f*f)
	for y := 0; y < o.h; y++ {
		for x := 0; x < o.w; x++ {
			var s float32
			for dy := 0; dy < f; dy++ {
				row := g.p[(y*f+dy)*g.w+x*f : (y*f+dy)*g.w+x*f+f]
				for _, p := range row {
					s += p
				}
			}
			o.p[y*o.w+x] = s * inv
		}
	}
	return o
}

// ncc correlates the reference with the window whose top-left is (x, y).
func (g mmGray) ncc(r *mmRef, x, y int) float32 {
	var s, ss, sx float32
	for ry := 0; ry < r.side; ry++ {
		row := g.p[(y+ry)*g.w+x : (y+ry)*g.w+x+r.side]
		v := r.v[ry*r.side : (ry+1)*r.side]
		for j, p := range row {
			s += p
			ss += p * p
			sx += p * v[j]
		}
	}
	n := float32(r.side * r.side)
	va := ss - s*s/n
	if va < n*mmVarFloor/4 {
		return -1
	}
	return sx / (float32(math.Sqrt(float64(va))) * r.norm)
}

type mmFitHit struct {
	x, y, w int // top-left and side in this level's units
	score   float32
}

// search tries every side in sides and every top-left within ±pos of (cx, cy), all in
// level units, and returns the best correlation.
func (g mmGray) search(sides []int, cx, cy, pos int) mmFitHit {
	best := mmFitHit{score: -2}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, side := range sides {
		if side < 8 || side > g.w || side > g.h {
			continue
		}
		wg.Add(1)
		go func(side int) {
			defer wg.Done()
			r := mmRefFor(side)
			loc := mmFitHit{score: -2}
			for y := max(cy-pos, 0); y <= min(cy+pos, g.h-side); y++ {
				for x := max(cx-pos, 0); x <= min(cx+pos, g.w-side); x++ {
					if s := g.ncc(r, x, y); s > loc.score {
						loc = mmFitHit{x, y, side, s}
					}
				}
			}
			mu.Lock()
			if loc.score > best.score {
				best = loc
			}
			mu.Unlock()
		}(side)
	}
	wg.Wait()
	return best
}

// mmFitMap refines the map square inside the capture region mm (mmRegionMM, rgb24):
// it searches ±mmFitPos px and ±mmFitScale of the nominal box and returns the box that
// best correlates with the map image, with its correlation. ok is false when the best
// correlation is below mmFitMinNCC (no live minimap in view) or no map image is loaded.
func mmFitMap(mm []byte, nominal mmBox) (box mmBox, score float32, ok bool) {
	mmMapRef.mu.Lock()
	loaded := mmMapRef.gray != nil
	mmMapRef.mu.Unlock()
	if !loaded {
		return nominal, 0, false
	}
	full := mmToGray(mm, mmRegionMM.W, mmRegionMM.H)
	l2 := full.down(2)
	l4 := l2.down(2)
	nx, ny, ns := nominal.X-mmRegionMM.X, nominal.Y-mmRegionMM.Y, nominal.W
	dw := int(math.Round(mmFitScale * float64(ns)))

	// coarse: quarter resolution, sides in 4 px steps
	var sides []int
	for w := ns - dw; w <= ns+dw; w += 4 {
		sides = append(sides, w/4)
	}
	h := l4.search(sides, nx/4, ny/4, mmFitPos/4+1)
	// half resolution: ±4 px, side ±4 px in 2 px steps
	sides = sides[:0]
	for w := h.w*4 - 4; w <= h.w*4+4; w += 2 {
		sides = append(sides, w/2)
	}
	h = l2.search(sides, h.x*2, h.y*2, 2)
	// full resolution: ±2 px
	sides = sides[:0]
	for w := h.w*2 - 2; w <= h.w*2+2; w++ {
		sides = append(sides, w)
	}
	h = full.search(sides, h.x*2, h.y*2, 2)
	box = mmBox{h.x + mmRegionMM.X, h.y + mmRegionMM.Y, h.w, h.w}
	return box, h.score, h.score >= mmFitMinNCC
}

// mmFrameTaps returns the taps of an n→mmFrameSize resample, built once per size: the
// map box side only takes a few dozen values (see mmFitScale).
func mmFrameTaps(n int) [][]mmTap {
	if v, ok := mmTapCache.Load(n); ok {
		return v.([][]mmTap)
	}
	v, _ := mmTapCache.LoadOrStore(n, areaTaps(n, mmFrameSize))
	return v.([][]mmTap)
}

var mmTapCache sync.Map // n -> [][]mmTap

// mmCropMap area-resamples box (reference coordinates) out of the capture region mm
// into dst (mmFrameSize² rgb24). Samples outside the region repeat its edge.
func mmCropMap(mm []byte, box mmBox, dst []byte) {
	stride := mmRegionMM.W
	xs, ys := mmFrameTaps(box.W), mmFrameTaps(box.H)
	bx, by := box.X-mmRegionMM.X, box.Y-mmRegionMM.Y
	for y := 0; y < mmFrameSize; y++ {
		for x := 0; x < mmFrameSize; x++ {
			var r, g, b float32
			for _, ty := range ys[y] {
				sy := clampInt(by+ty.i, 0, mmRegionMM.H-1)
				for _, tx := range xs[x] {
					sx := clampInt(bx+tx.i, 0, stride-1)
					w := ty.w * tx.w
					o := 3 * (sy*stride + sx)
					r += w * float32(mm[o])
					g += w * float32(mm[o+1])
					b += w * float32(mm[o+2])
				}
			}
			o := 3 * (y*mmFrameSize + x)
			dst[o], dst[o+1], dst[o+2] = byte(r+0.5), byte(g+0.5), byte(b+0.5)
		}
	}
}

// mmHasTerrain reports whether the region at box shows map ground (olive/tan), which
// separates a minimap from the crowd, desk or draft footage that can replace it while the
// scoreboard stays up. The broadcast border is deliberately not checked: it differs per
// event (Worlds Play-Ins and main event, replays of the same league).
func mmHasTerrain(mm []byte, box mmBox) bool {
	return mmTerrainShare(mm, box) >= mmMinTerrain
}

// mmMinTerrain is the lowest share of olive/tan ground pixels of a live minimap.
const mmMinTerrain = 0.03

// mmTerrainShare returns the share of ground-coloured pixels on a coarse grid over box.
func mmTerrainShare(mm []byte, box mmBox) float64 {
	const grid = 48
	var n, t int
	for gy := 0; gy < grid; gy++ {
		for gx := 0; gx < grid; gx++ {
			x := box.X - mmRegionMM.X + gx*box.W/grid
			y := box.Y - mmRegionMM.Y + gy*box.H/grid
			if x < 0 || y < 0 || x >= mmRegionMM.W || y >= mmRegionMM.H {
				continue
			}
			i := 3 * (y*mmRegionMM.W + x)
			r, g, b := int(mm[i]), int(mm[i+1]), int(mm[i+2])
			n++
			if r >= 70 && g >= 70 && r-b >= 15 && g-b >= 8 && absInt(r-g) <= 35 {
				t++
			}
		}
	}
	return float64(t) / float64(max(n, 1))
}
