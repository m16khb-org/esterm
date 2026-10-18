package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// tBase is the image id block every test frame uses, so ids are fixed.
const tBase = 1024

func init() { kittyNewIDBase = func() int { return tBase } }

// kcmd is one graphics command of an output stream with the cell the cursor was addressed
// to when it ran (row, col are 1-based; 0 means "not addressed").
type kcmd struct {
	ctl      map[string]string
	data     string // concatenated base64 payload of a chunked transmission
	row, col int
}

var (
	reAPC = regexp.MustCompile(`^\x1b_G([^;\x1b]*)(?:;([A-Za-z0-9+/=]*))?$`)
	reCUP = regexp.MustCompile(`^\x1b\[(\d+);(\d+)H`)
)

// parseKitty splits out into graphics commands (continuation chunks are merged into their
// transmission) and fails on anything that is not ESC 7, ESC 8, a CUP or a well-formed APC.
func parseKitty(t *testing.T, out string) []kcmd {
	t.Helper()
	var cmds []kcmd
	row, col := 0, 0
	saved := 0
	var open *kcmd
	for out != "" {
		switch {
		case strings.HasPrefix(out, "\x1b7"):
			saved++
			out = out[2:]
		case strings.HasPrefix(out, "\x1b8"):
			out = out[2:]
		case reCUP.MatchString(out):
			m := reCUP.FindStringSubmatch(out)
			row, _ = strconv.Atoi(m[1])
			col, _ = strconv.Atoi(m[2])
			out = out[len(m[0]):]
		case strings.HasPrefix(out, "\x1b_G"):
			end := strings.Index(out, "\x1b\\")
			if end < 0 {
				t.Fatalf("unterminated APC: %.80q", out)
			}
			m := reAPC.FindStringSubmatch(out[:end])
			if m == nil {
				t.Fatalf("malformed APC: %.80q", out[:end])
			}
			if len(m[2]) > kittyChunk {
				t.Fatalf("chunk payload %d bytes", len(m[2]))
			}
			ctl := map[string]string{}
			for _, kv := range strings.Split(m[1], ",") {
				k, v, _ := strings.Cut(kv, "=")
				ctl[k] = v
			}
			if open != nil { // continuation: only m=
				if len(ctl) != 1 || ctl["m"] == "" {
					t.Fatalf("continuation chunk with keys %v", ctl)
				}
				open.data += m[2]
				if ctl["m"] == "0" {
					cmds = appendKitty(cmds, *open)
					open = nil
				}
			} else {
				c := kcmd{ctl: ctl, data: m[2], row: row, col: col}
				if ctl["m"] == "1" {
					open = &c
				} else {
					cmds = appendKitty(cmds, c)
				}
			}
			out = out[end+2:]
		default:
			t.Fatalf("stray bytes in kitty output: %.40q", out)
		}
	}
	if open != nil {
		t.Fatal("transmission without its last chunk")
	}
	if saved > 1 {
		t.Errorf("cursor saved %d times", saved)
	}
	return cmds
}

// appendKitty adds c to cmds; an a=T (transmit and display) command is split into the
// a=t transmission and the a=p placement it stands for, at the cursor cell it ran at, so the
// tests state what an image is and where it is shown without caring how it was sent.
func appendKitty(cmds []kcmd, c kcmd) []kcmd {
	if c.ctl["a"] != "T" {
		return append(cmds, c)
	}
	tr, pl := map[string]string{"a": "t"}, map[string]string{"a": "p"}
	for k, v := range c.ctl {
		switch k {
		case "f", "s", "v", "m", "t":
			tr[k] = v
		case "a":
		case "q", "i":
			tr[k], pl[k] = v, v
		default:
			pl[k] = v
		}
	}
	delete(tr, "m")
	return append(cmds, kcmd{ctl: tr, data: c.data, row: c.row, col: c.col}, kcmd{ctl: pl, row: c.row, col: c.col})
}

func kittyCount(cmds []kcmd, kv ...string) int {
	n := 0
next:
	for _, c := range cmds {
		for i := 0; i+1 < len(kv); i += 2 {
			if v, ok := c.ctl[kv[i]]; !ok || (kv[i+1] != "*" && v != kv[i+1]) {
				continue next
			}
		}
		n++
	}
	return n
}

func kittyFind(cmds []kcmd, kv ...string) []kcmd {
	var out []kcmd
next:
	for _, c := range cmds {
		for i := 0; i+1 < len(kv); i += 2 {
			if c.ctl[kv[i]] != kv[i+1] {
				continue next
			}
		}
		out = append(out, c)
	}
	return out
}

func testSlot(icons ...mmIcon) *mmSlot {
	return &mmSlot{Row: 1, Col: 2, Cols: 40, Rows: 20, CellW: 10, CellH: 20, Ver: "v-test", Icons: icons}
}

func iconAt(champ string, team int, x, y float64) mmIcon {
	return mmIcon{Team: team, Champ: champ, X: x, Y: y}
}

// testPos returns the placement of the champion named champ in slot s.
func testPos(s *mmSlot, champ string) (mmPlace, kittyPos) {
	w, h, d := mmKittyGeometry(s)
	cols, rows := kittySpriteCells(d, s.CellW, s.CellH)
	for _, p := range mmPlacements(nil, w, h, d, s.Icons) {
		if p.Champ == champ {
			return p, kittyPosOf(p, cols, rows, s)
		}
	}
	panic("no champion " + champ)
}

// testVariantID is the image id of champion champ as sprite slot i of a frame of s.
func testVariantID(s *mmSlot, champ string, i int) int {
	p, pos := testPos(s, champ)
	return tBase + kittyVariantID(i, kittyVariant(p.State, pos))
}

func isSpriteID(id int) bool { return id >= tBase+kittySpriteID && id < tBase+kittyDigitID }

func testDecode(t *testing.T, c kcmd) image.Image {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(c.data)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("image %s is not a PNG: %v", c.ctl["i"], err)
	}
	return img
}

// testTileCount is the number of map images (every tile and the backing) a frame of slot s transmits.
func testTileCount(s *mmSlot) int {
	w, h, _ := mmKittyGeometry(s)
	mw, mh := mmMapRaster(w, h)
	g := kittyTileGrid(s.Cols, s.Rows, mw, mh)
	return g.nx*g.ny + 1
}

func TestKittyEveryImageIsTransmittedAndPlacedByOneCommand(t *testing.T) {
	offlineMap(t)
	hangNetwork(t)
	var k kittyOut
	s := testSlot(iconAt("A", 100, 0.5, 0.5), iconAt("B", 200, 0.25, 0.75))
	out := k.Frame(s, true)
	if strings.Contains(out, "\x1b_Ga=t") || strings.Contains(out, "\x1b_Ga=p") {
		t.Fatal("a first frame must not leave an image waiting for a separate placement")
	}
	n := testTileCount(s) + 2
	if got := strings.Count(out, "\x1b_Ga=T,f=100,i="); got != n {
		t.Fatalf("%d a=T commands, want %d (tiles, backing, 2 sprites)", got, n)
	}
	// each a=T opens right after a cursor move (the first one after the saved cursor)
	if strings.Count(out, "H\x1b_Ga=T") != n {
		t.Errorf("not every a=T directly follows the CUP to its cell")
	}
	// the lowest image id goes first in the stream: the backing
	first := strings.Index(out, "\x1b_Ga=T,f=100,i=")
	if !strings.HasPrefix(out[first:], fmt.Sprintf("\x1b_Ga=T,f=100,i=%d,p=1,c=%d,r=%d,z=0,", tBase+kittyBackID, s.Cols, s.Rows)) {
		t.Errorf("first image is not the full-slot backing: %.80q", out[first:])
	}
	// later frames only move placements: no transmission at all
	a := iconAt("A", 100, 0.6, 0.5)
	if got := k.Frame(testSlot(a, iconAt("B", 200, 0.25, 0.75)), false); strings.Contains(got, "a=T") || strings.Contains(got, "a=t") {
		t.Errorf("a move must be an a=p only: %.80q", got)
	}
}

func TestKittyBackingIsAnOpaqueThumbnailOfTheRaster(t *testing.T) {
	raster := image.NewRGBA(image.Rect(0, 0, 400, 200))
	for i := 0; i < len(raster.Pix); i += 4 {
		raster.Pix[i], raster.Pix[i+1], raster.Pix[i+2], raster.Pix[i+3] = 10, 20, 30, 255
	}
	b := kittyBacking(raster)
	if b.Bounds().Dx() != kittyBackPx || b.Bounds().Dy() != kittyBackPx/2 {
		t.Fatalf("backing %v, want %dx%d", b.Bounds(), kittyBackPx, kittyBackPx/2)
	}
	for i := 0; i < len(b.Pix); i += 4 {
		if b.Pix[i] != 10 || b.Pix[i+1] != 20 || b.Pix[i+2] != 30 || b.Pix[i+3] != 255 {
			t.Fatalf("pixel %d = %v, want the raster colour", i/4, b.Pix[i:i+4])
		}
	}
	if tiny := kittyBacking(image.NewRGBA(image.Rect(0, 0, 3, 2))); tiny.Bounds().Dx() != 3 || tiny.Bounds().Dy() != 2 {
		t.Errorf("a raster smaller than the backing stays as it is: %v", tiny.Bounds())
	}
}

func TestKittyMapRasterNeverExceedsDataDragon(t *testing.T) {
	for _, c := range []struct{ w, h, ww, wh int }{
		{384, 384, 384, 384}, {512, 512, 512, 512}, {864, 864, 512, 512}, {1152, 576, 512, 256}, {576, 1152, 256, 512}, {5000, 1, 512, 1},
	} {
		if w, h := mmMapRaster(c.w, c.h); w != c.ww || h != c.wh {
			t.Errorf("raster of %dx%d = %dx%d, want %dx%d", c.w, c.h, w, h, c.ww, c.wh)
		}
	}
}

func TestKittyTilesCoverTheRasterOnceWithinBudget(t *testing.T) {
	for _, c := range []struct{ cols, rows, cw, ch int }{
		{40, 20, 10, 20}, {48, 24, 8, 16}, {54, 24, 15, 36}, {64, 32, 18, 38}, {120, 60, 20, 40}, {3, 2, 16, 32}, {1, 1, 8, 16}, {200, 100, 8, 16},
	} {
		w, h := c.cols*c.cw, c.rows*c.ch
		mw, mh := mmMapRaster(w, h)
		g := kittyTileGrid(c.cols, c.rows, mw, mh)
		if g.nx*g.ny > kittyMaxTiles {
			t.Errorf("%+v: %d tiles, max %d", c, g.nx*g.ny, kittyMaxTiles)
		}
		px := make([]int, mw*mh)
		cells := make([]int, c.cols*c.rows)
		for i := 0; i < g.nx*g.ny; i++ {
			cell, r := g.rect(i, c.cols, c.rows, mw, mh)
			if r.Dx() <= 0 || r.Dy() <= 0 || cell.Dx() <= 0 || cell.Dy() <= 0 {
				t.Fatalf("%+v: tile %d is empty: %v %v", c, i, cell, r)
			}
			if b := r.Dx() * r.Dy() * 4; b > kittyTileBytes*5/4 && g.nx*g.ny < kittyMaxTiles {
				t.Errorf("%+v: tile %d is %v px, %d raw bytes", c, i, r, b)
			}
			for y := r.Min.Y; y < r.Max.Y; y++ {
				for x := r.Min.X; x < r.Max.X; x++ {
					px[y*mw+x]++
				}
			}
			for y := cell.Min.Y; y < cell.Max.Y; y++ {
				for x := cell.Min.X; x < cell.Max.X; x++ {
					cells[y*c.cols+x]++
				}
			}
		}
		for i, n := range px {
			if n != 1 {
				t.Fatalf("%+v: raster pixel %d,%d covered %d times", c, i%mw, i/mw, n)
			}
		}
		for i, n := range cells {
			if n != 1 {
				t.Fatalf("%+v: cell %d,%d covered %d times", c, i%c.cols, i/c.cols, n)
			}
		}
	}
}

func TestKittyTilesReassembleTheMap(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for i := 0; i < len(src.Pix); i += 4 {
		src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = uint8(i), uint8(i>>3), uint8(i>>6), 255
	}
	mmBg.Lock()
	old := mmBg.img
	mmBg.img = src
	mmBg.Unlock()
	t.Cleanup(func() {
		mmBg.Lock()
		mmBg.img, mmBg.cache = old, nil
		mmBg.Unlock()
	})
	var k kittyOut
	s := testSlot()
	cmds := parseKitty(t, k.Frame(s, true))
	w, h, _ := mmKittyGeometry(s)
	mw, mh := mmMapRaster(w, h)
	want := mmScaledMap(src, mw, mh)
	g := kittyTileGrid(s.Cols, s.Rows, mw, mh)
	for i := 0; i < g.nx*g.ny; i++ {
		_, r := g.rect(i, s.Cols, s.Rows, mw, mh)
		raw, err := base64.StdEncoding.DecodeString(kittyFind(cmds, "a", "t", "i", strconv.Itoa((tBase+kittyMapID)+i))[0].data)
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if img.Bounds().Dx() != r.Dx() || img.Bounds().Dy() != r.Dy() {
			t.Fatalf("tile %d is %v, want %v", i, img.Bounds(), r)
		}
		for y := 0; y < r.Dy(); y++ {
			for x := 0; x < r.Dx(); x++ {
				gr, gg, gb, _ := img.At(x, y).RGBA()
				o := want.PixOffset(r.Min.X+x, r.Min.Y+y)
				if uint8(gr>>8) != want.Pix[o] || uint8(gg>>8) != want.Pix[o+1] || uint8(gb>>8) != want.Pix[o+2] {
					t.Fatalf("tile %d pixel %d,%d differs from the map", i, x, y)
				}
			}
		}
	}
}

func TestKittyChunksFraming(t *testing.T) {
	data := make([]byte, 7000) // base64 length 9336 -> 3 chunks
	out := string(kittyChunks(nil, "a=t,f=100,i=9,q=2", data))
	parts := strings.Split(strings.TrimSuffix(out, "\x1b\\"), "\x1b\\")
	if len(parts) != 3 {
		t.Fatalf("chunks = %d", len(parts))
	}
	for i, p := range parts {
		m := reAPC.FindStringSubmatch(p)
		if m == nil {
			t.Fatalf("chunk %d malformed: %.60q", i, p)
		}
		more := "m=1"
		if i == len(parts)-1 {
			more = "m=0"
		}
		if !strings.HasSuffix(m[1], more) {
			t.Errorf("chunk %d keys %q want %s", i, m[1], more)
		}
		if (i == 0) != strings.HasPrefix(m[1], "a=t,f=100,i=9,q=2") {
			t.Errorf("only the first chunk carries the control keys: %q", m[1])
		}
	}
	if single := string(kittyChunks(nil, "a=t", []byte("x"))); strings.Count(single, "\x1b_G") != 1 || !strings.Contains(single, "m=0;") {
		t.Errorf("single chunk: %q", single)
	}
	if !strings.HasSuffix(out, "\x1b\\") {
		t.Error("stream must end with ST")
	}
}

func TestKittyFirstFrameTransmitsOnceAndPlaces(t *testing.T) {
	offlineMap(t)
	hangNetwork(t)
	var k kittyOut
	s := testSlot(iconAt("A", 100, 0.5, 0.5), iconAt("B", 200, 0.25, 0.75))
	out := k.Frame(s, true)
	if strings.Count(out, "\x1b7") != 1 || !strings.HasSuffix(out, "\x1b8") {
		t.Errorf("placements must be saved once and restored at the end: %.20q ... %.20q", out, out[len(out)-20:])
	}
	cmds := parseKitty(t, out)

	// the map is cut into tiles, each transmitted once and placed once at the bottom layer
	w, h, d := mmKittyGeometry(s)
	mw, mh := mmMapRaster(w, h)
	grid := kittyTileGrid(s.Cols, s.Rows, mw, mh)
	n := grid.nx * grid.ny
	if n < 2 {
		t.Fatalf("a %dx%d px map must not be one image: %d tiles", w, h, n)
	}
	covered := image.NewRGBA(image.Rect(0, 0, s.Cols, s.Rows)) // cells seen by tile placements
	for i := 0; i < n; i++ {
		id := strconv.Itoa((tBase + kittyMapID) + i)
		if c := kittyCount(kittyFind(cmds, "a", "t", "i", id), "f", "100", "q", "2"); c != 1 || kittyCount(cmds, "a", "t", "i", id) != 1 {
			t.Fatalf("map tile %d transmissions = %d", i, kittyCount(cmds, "a", "t", "i", id))
		}
		mp := kittyFind(cmds, "a", "p", "i", id)
		if len(mp) != 1 || mp[0].ctl["p"] != "1" || mp[0].ctl["z"] != "1" || mp[0].ctl["C"] != "1" || mp[0].ctl["q"] != "2" {
			t.Fatalf("map tile %d placement %v", i, mp)
		}
		c, _ := strconv.Atoi(mp[0].ctl["c"])
		r, _ := strconv.Atoi(mp[0].ctl["r"])
		x0, y0 := mp[0].col-3, mp[0].row-2 // slot origin is cell 2;3 (1-based)
		for y := y0; y < y0+r; y++ {
			for x := x0; x < x0+c; x++ {
				if x < 0 || y < 0 || x >= s.Cols || y >= s.Rows || covered.Pix[4*(y*s.Cols+x)] != 0 {
					t.Fatalf("tile %d covers cell %d,%d outside the slot or twice", i, x, y)
				}
				covered.Pix[4*(y*s.Cols+x)] = 1
			}
		}
	}
	for y := 0; y < s.Rows; y++ {
		for x := 0; x < s.Cols; x++ {
			if covered.Pix[4*(y*s.Cols+x)] == 0 {
				t.Fatalf("cell %d,%d of the slot is not covered by the map", x, y)
			}
		}
	}
	if bk := kittyFind(cmds, "a", "p", "i", strconv.Itoa((tBase + kittyBackID))); len(bk) != 1 || bk[0].ctl["z"] != "0" || bk[0].ctl["c"] != strconv.Itoa(s.Cols) || bk[0].ctl["r"] != strconv.Itoa(s.Rows) || bk[0].row != 2 || bk[0].col != 3 {
		t.Errorf("backing placement %v, want the whole slot at z=0", bk)
	}
	if kittyCount(cmds, "a", "t", "i", strconv.Itoa((tBase+kittyBackID))) != 1 {
		t.Error("backing not transmitted once")
	}
	if len(cmds) != 2*n+2+2+2 {
		t.Errorf("%d commands, want %d map tiles + backing (t,p) + 2x(t,p)", len(cmds), n)
	}
	// one transmission per sprite variant, one placement each above the map; the sprite box
	// is one cell larger than the circle's cells in each axis
	cols, rows := kittySpriteCells(d, s.CellW, s.CellH)
	for i, champ := range []string{"A", "B"} {
		id := strconv.Itoa(testVariantID(s, champ, i))
		if n := kittyCount(cmds, "a", "t", "i", id); n != 1 {
			t.Errorf("sprite %s transmissions = %d", champ, n)
		}
		pl := kittyFind(cmds, "a", "p", "i", id)
		if len(pl) != 1 || pl[0].ctl["p"] != strconv.Itoa(i+1) || pl[0].ctl["z"] != strconv.Itoa(i+2) || pl[0].ctl["C"] != "1" || pl[0].ctl["q"] != "2" ||
			pl[0].ctl["c"] != strconv.Itoa(cols+1) || pl[0].ctl["r"] != strconv.Itoa(rows+1) {
			t.Errorf("sprite %s placement %v", champ, pl)
		}
	}
	// the payloads are PNGs: map tiles opaque and small, sprites straight RGBA
	for _, c := range kittyFind(cmds, "a", "t") {
		img := testDecode(t, c)
		if id, _ := strconv.Atoi(c.ctl["i"]); !isSpriteID(id) {
			if px := img.Bounds().Dx() * img.Bounds().Dy(); px*4 > kittyTileBytes*5/4 {
				t.Errorf("map tile %s is %v: %d raw bytes, want about %d", c.ctl["i"], img.Bounds(), px*4, kittyTileBytes)
			}
			if _, ok := img.(*image.RGBA); !ok {
				t.Errorf("map tile decoded as %T, want opaque RGB", img)
			}
			continue
		}
		if img.Bounds().Dx() != (cols+1)*s.CellW || img.Bounds().Dy() != (rows+1)*s.CellH {
			t.Errorf("sprite %v, want %dx%d px (whole cells)", img.Bounds(), (cols+1)*s.CellW, (rows+1)*s.CellH)
		}
		if _, ok := img.(*image.NRGBA); !ok {
			t.Errorf("sprite decoded as %T, want straight RGBA", img)
		}
		if _, _, _, a := img.At(img.Bounds().Dx()-1, img.Bounds().Dy()-1).RGBA(); a != 0 {
			t.Error("padding must be transparent")
		}
	}
	for i, champ := range []string{"A", "B"} { // the circle's centre is opaque, at its phase
		_, pos := testPos(s, champ)
		img := testDecode(t, kittyFind(cmds, "a", "t", "i", strconv.Itoa(testVariantID(s, champ, i)))[0])
		cx, cy := kittyPhaseOffset(pos.px, s.CellW)+d/2, kittyPhaseOffset(pos.py, s.CellH)+d/2
		if _, _, _, a := img.At(cx, cy).RGBA(); a != 0xffff {
			t.Errorf("sprite %s: circle centre %d,%d must be opaque", champ, cx, cy)
		}
	}
}

func TestKittySteadyTicksEmitOnlyMovedPlacements(t *testing.T) {
	offlineMap(t)
	hangNetwork(t)
	var k kittyOut
	s := testSlot(iconAt("A", 100, 0.5, 0.5), iconAt("B", 200, 0.25, 0.75))
	k.Frame(s, true)
	if got := k.Frame(s, false); got != "" {
		t.Fatalf("unchanged frame must emit nothing, got %q", got)
	}
	sub := testSlot(iconAt("A", 100, 0.5+0.1/400, 0.5), iconAt("B", 200, 0.25, 0.75)) // a tenth of a pixel
	if got := k.Frame(sub, false); got != "" {
		t.Errorf("a sub-pixel move must not emit, got %q", got)
	}
	moved := testSlot(iconAt("A", 100, 0.6, 0.5), iconAt("B", 200, 0.25, 0.75))
	out := k.Frame(moved, false)
	cmds := parseKitty(t, out)
	if len(cmds) != 1 || cmds[0].ctl["a"] != "p" || cmds[0].ctl["i"] != strconv.Itoa(testVariantID(moved, "A", 0)) {
		t.Fatalf("only A's placement may be re-issued: %v", cmds)
	}
	// 400x400 slot, d=33 in a 5x3 cell box: circle left edge px 224 -> quarter 90 -> cell 22 and
	// phase 2 across, top edge px 184 -> quarter 37 -> cell 9 and phase 1 down: no pixel offset
	c := cmds[0]
	if c.row != 1+9+1 || c.col != 2+22+1 || c.ctl["X"] != "" || c.ctl["Y"] != "" {
		t.Errorf("cell %d;%d X=%q Y=%q", c.row, c.col, c.ctl["X"], c.ctl["Y"])
	}
	if len(out) > 120 {
		t.Errorf("one moved icon cost %d bytes", len(out))
	}
	if kittyCount(cmds, "a", "t") != 0 {
		t.Error("a move must not re-transmit pixels")
	}
}

func TestKittySpritesSitOnWholeCellsAndStayRound(t *testing.T) {
	offlineMap(t)
	hangNetwork(t)
	var k kittyOut
	var icons []mmIcon
	for i := 0; i < 12; i++ { // positions all over the slot, incl. its corners and edges
		icons = append(icons, iconAt(fmt.Sprintf("C%d", i), 100, float64(i%4)/3, float64(i/4)/2))
	}
	s := testSlot(icons...)
	out := k.Frame(s, true)
	if strings.Contains(out, ",X=") || strings.Contains(out, ",Y=") {
		t.Fatal("sprite placements must not carry pixel offsets: terminals that shrink the c x r box by the offset squeeze the circle into an ellipse")
	}
	_, _, d := mmKittyGeometry(s)
	cols, rows := kittySpriteCells(d, s.CellW, s.CellH)
	for _, c := range kittyFind(parseKitty(t, out), "a", "p") {
		if id, _ := strconv.Atoi(c.ctl["i"]); !isSpriteID(id) {
			continue
		}
		if c.ctl["c"] != strconv.Itoa(cols+1) || c.ctl["r"] != strconv.Itoa(rows+1) {
			t.Errorf("sprite box %sx%s cells, want %dx%d", c.ctl["c"], c.ctl["r"], cols+1, rows+1)
		}
		if c.col-1 < s.Col || c.row-1 < s.Row || c.col-1+cols+1 > s.Col+s.Cols || c.row-1+rows+1 > s.Row+s.Rows {
			t.Errorf("sprite box at cell %d;%d leaves the slot", c.row, c.col)
		}
	}
}

func TestKittySpritePixelsDrawTheRoundCircleAtItsPhase(t *testing.T) {
	const cw, ch, d = 10, 20, 33
	cols, rows := kittySpriteCells(d, cw, ch)
	sp := mmSpriteFor(nil, 100, d, kittyRing, "M", mmIconLive)
	seen := map[string]bool{}
	for py := 0; py < kittyPhases; py++ {
		for px := 0; px < kittyPhases; px++ {
			img := kittySpritePixels(sp, cols, rows, cw, ch, px, py)
			if img.Bounds().Dx() != (cols+1)*cw || img.Bounds().Dy() != (rows+1)*ch {
				t.Fatalf("phase %d,%d: image %v is not one cell larger than the circle", px, py, img.Bounds())
			}
			minx, maxx, miny, maxy := img.Bounds().Dx(), -1, img.Bounds().Dy(), -1
			for y := 0; y < img.Bounds().Dy(); y++ {
				for x := 0; x < img.Bounds().Dx(); x++ {
					if img.Pix[img.PixOffset(x, y)+3] > 128 {
						minx, maxx, miny, maxy = min(minx, x), max(maxx, x), min(miny, y), max(maxy, y)
					}
				}
			}
			if w, h := maxx-minx+1, maxy-miny+1; w != d || h != d {
				t.Errorf("phase %d,%d: circle bounding box %dx%d, want %dx%d (round)", px, py, w, h, d, d)
			}
			if minx != kittyPhaseOffset(px, cw) || miny != kittyPhaseOffset(py, ch) {
				t.Errorf("phase %d,%d: circle at %d,%d, want %d,%d", px, py, minx, miny, kittyPhaseOffset(px, cw), kittyPhaseOffset(py, ch))
			}
			seen[string(img.Pix)] = true
		}
	}
	if len(seen) != kittyPhases*kittyPhases {
		t.Errorf("%d distinct phase images, want %d", len(seen), kittyPhases*kittyPhases)
	}
}

func TestKittyMoveWithinAPhaseStepEmitsNothing(t *testing.T) {
	offlineMap(t)
	hangNetwork(t)
	var k kittyOut
	k.Frame(testSlot(iconAt("A", 100, 0.5, 0.5)), true)
	// one pixel right and down stays on the same quarter step (2.5 px across, 5 px down)
	if got := k.Frame(testSlot(iconAt("A", 100, 0.5+1.0/400, 0.5+1.0/400)), false); got != "" {
		t.Errorf("a move inside one phase step must not emit, got %q", got)
	}
}

func TestParseCellReplies(t *testing.T) {
	for _, c := range []struct {
		name       string
		got        string
		cols, rows int
		w, h       int
	}{
		{"cell size reply", "\x1b[6;36;18t\x1b[?62;4c", 100, 40, 18, 36},
		{"cell size wins over the area", "\x1b[6;20;10t\x1b[4;800;1000t\x1b[?62c", 100, 40, 10, 20},
		{"text area divided by the grid", "\x1b[4;816;1192t\x1b[?62c", 149, 51, 8, 16},
		{"area without a grid", "\x1b[4;816;1192t\x1b[?62c", 0, 0, 0, 0},
		{"area smaller than the grid", "\x1b[4;10;10t\x1b[?62c", 149, 51, 0, 0},
		{"zero cell size", "\x1b[6;0;0t\x1b[?62c", 100, 40, 0, 0},
		{"only DA1", "\x1b[?62;4c", 100, 40, 0, 0},
		{"nothing", "", 100, 40, 0, 0},
	} {
		if w, h := parseCellReplies([]byte(c.got), c.cols, c.rows); w != c.w || h != c.h {
			t.Errorf("%s: got %dx%d, want %dx%d", c.name, w, h, c.w, c.h)
		}
	}
}

func TestTermCellPxPrefersTTYThenProbeThenDefault(t *testing.T) {
	termCell.Lock()
	old := [2]int{termCell.w, termCell.h}
	termCell.w, termCell.h = 0, 0
	termCell.Unlock()
	t.Cleanup(func() {
		termCell.Lock()
		termCell.w, termCell.h = old[0], old[1]
		termCell.Unlock()
	})
	if w, h := winsizeCellPx(); w == 0 { // no tty pixel size (go test): the probe answer, then 8x16
		if cw, ch := termCellPx(); cw != 8 || ch != 16 {
			t.Errorf("default cell %dx%d, want 8x16", cw, ch)
		}
		termCell.Lock()
		termCell.w, termCell.h = 18, 36
		termCell.Unlock()
		if cw, ch := termCellPx(); cw != 18 || ch != 36 {
			t.Errorf("probed cell %dx%d, want 18x36", cw, ch)
		}
	} else if cw, ch := termCellPx(); cw != w || ch != h {
		t.Errorf("tty cell %dx%d, want %dx%d", cw, ch, w, h)
	}
}

func TestKittyHiddenChampionDropsPlacementKeepsImage(t *testing.T) {
	offlineMap(t)
	hangNetwork(t)
	var k kittyOut
	both := testSlot(iconAt("A", 100, 0.5, 0.5), iconAt("B", 200, 0.25, 0.75))
	k.Frame(both, true)
	idA := strconv.Itoa(testVariantID(both, "A", 0))
	only := testSlot(iconAt("B", 200, 0.25, 0.75))
	cmds := parseKitty(t, k.Frame(only, false))
	if len(cmds) != 1 || cmds[0].ctl["a"] != "d" || cmds[0].ctl["d"] != "i" || cmds[0].ctl["i"] != idA || cmds[0].ctl["p"] != "1" {
		t.Fatalf("hiding A must delete only its placement (d=i keeps the data): %v", cmds)
	}
	if k.Frame(only, false) != "" {
		t.Error("a hidden champion is deleted once")
	}
	cmds = parseKitty(t, k.Frame(both, false))
	if len(cmds) != 1 || cmds[0].ctl["a"] != "p" || cmds[0].ctl["i"] != idA {
		t.Fatalf("showing A again re-places the kept image without re-sending pixels: %v", cmds)
	}
}

func TestKittyIconLoadedRetransmitsOnlyThatSprite(t *testing.T) {
	offlineMap(t)
	hangNetwork(t)
	var k kittyOut
	s := testSlot(iconAt("A", 100, 0.5, 0.5), iconAt("B", 200, 0.25, 0.75))
	k.Frame(s, true)
	ddImgMu.Lock()
	ddImgMemo["v-test/img/champion/A.png"] = image.NewRGBA(image.Rect(0, 0, 4, 4))
	ddImgMu.Unlock()
	cmds := parseKitty(t, k.Frame(s, false))
	id := strconv.Itoa(testVariantID(s, "A", 0))
	if kittyCount(cmds, "a", "d", "d", "I", "i", id) != 1 || kittyCount(cmds, "a", "t", "i", id) != 1 || kittyCount(cmds, "a", "p", "i", id) != 1 || len(cmds) != 3 {
		t.Fatalf("the real icon replaces the placeholder: free, transmit and place of A's variant only: %v", cmds)
	}
	if k.Frame(s, false) != "" {
		t.Error("nothing changed afterwards")
	}
}

func TestKittyFullRepaintAndResizeReplaceEverything(t *testing.T) {
	offlineMap(t)
	hangNetwork(t)
	var k kittyOut
	s := testSlot(iconAt("A", 100, 0.5, 0.5), iconAt("B", 200, 0.25, 0.75))
	k.Frame(s, true)
	for name, next := range map[string]*mmSlot{"full repaint": s, "resize": func() *mmSlot { r := *s; r.Cols, r.Rows = 30, 15; return &r }()} {
		cmds := parseKitty(t, k.Frame(next, name == "full repaint"))
		n := testTileCount(next)
		if got := kittyCount(cmds, "a", "d", "d", "I"); got != testTileCount(s)+2 {
			t.Errorf("%s: %d image deletes, want the map tiles + 2 sprites", name, got)
		}
		if kittyCount(cmds, "a", "t") != n+2 || kittyCount(cmds, "a", "p") != n+2 {
			t.Errorf("%s: want %d transmissions and placements, got %v", name, n+2, cmds)
		}
		k.Frame(s, true) // back to a known state for the next case
	}
}

func TestKittyIconVersionArrivingKeepsTheMap(t *testing.T) {
	offlineMap(t)
	hangNetwork(t)
	var k kittyOut
	s := testSlot(iconAt("A", 100, 0.5, 0.5))
	s.Ver = "" // the panel opens before the Data Dragon version is known
	k.Frame(s, true)
	late := *s
	late.Ver = "v-test"
	cmds := parseKitty(t, k.Frame(&late, false))
	for _, c := range cmds {
		if c.ctl["a"] == "d" || (c.ctl["a"] == "t" && c.ctl["i"] != strconv.Itoa(tBase+kittySpriteID)) {
			t.Errorf("the map or a delete was re-sent when the version arrived: %v", c)
		}
	}
}

func TestKittySlotMoveReplacesWithoutRetransmitting(t *testing.T) {
	offlineMap(t)
	hangNetwork(t)
	var k kittyOut
	s := testSlot(iconAt("A", 100, 0.5, 0.5))
	k.Frame(s, true)
	moved := *s
	moved.Row, moved.Col = 3, 6
	cmds := parseKitty(t, k.Frame(&moved, false))
	n := testTileCount(s)
	if kittyCount(cmds, "a", "t") != 0 || kittyCount(cmds, "a", "p") != n+1 {
		t.Fatalf("a slot move re-places the map tiles and the sprite only: %v", cmds)
	}
	if mp := kittyFind(cmds, "a", "p", "i", strconv.Itoa(tBase+kittyMapID)); len(mp) != 1 || mp[0].row != 4 || mp[0].col != 7 {
		t.Errorf("first map tile placed at %v", mp)
	}
}

func TestKittyDeleteFreesEverything(t *testing.T) {
	offlineMap(t)
	hangNetwork(t)
	var k kittyOut
	s := testSlot(iconAt("A", 100, 0.5, 0.5), iconAt("B", 200, 0.25, 0.75))
	if k.Delete() != "" {
		t.Error("nothing shown, nothing to delete")
	}
	n := testTileCount(s)
	k.Frame(s, true)
	out := k.Frame(nil, false)
	cmds := parseKitty(t, out)
	if len(cmds) != n+2 || kittyCount(cmds, "a", "d", "d", "I", "q", "2") != n+2 {
		t.Fatalf("toggle off must free every map tile and both sprites: %q", out)
	}
	for i := 0; i < n-1; i++ {
		if kittyCount(cmds, "i", strconv.Itoa((tBase+kittyMapID)+i)) != 1 {
			t.Errorf("map tile %d not freed", i)
		}
	}
	if kittyCount(cmds, "i", strconv.Itoa((tBase+kittyBackID))) != 1 {
		t.Error("map backing not freed")
	}
	for i, champ := range []string{"A", "B"} {
		if kittyCount(cmds, "i", strconv.Itoa(testVariantID(s, champ, i))) != 1 {
			t.Errorf("sprite %s not freed", champ)
		}
	}
	if k.Frame(nil, false) != "" || k.Delete() != "" {
		t.Error("nothing left to delete")
	}
	if got := parseKitty(t, k.Frame(s, false)); kittyCount(got, "a", "t") != n+2 {
		t.Error("after a delete the next frame transmits again")
	}
}

func TestKittyOneShotUsesRelativePlacements(t *testing.T) {
	offlineMap(t)
	hangNetwork(t)
	s := testSlot(iconAt("A", 100, 0.5, 0.5))
	out := kittyOneShot(s, 30)
	if strings.Count(out, "\x1b7") != 1 || !strings.HasSuffix(out, "\x1b8") {
		t.Errorf("one save, restore at the end: %.30q", out)
	}
	if !strings.Contains(out, "\x1b8\x1b[29A\x1b[3G") { // slot row 1 of a 30-row canvas, col 2
		t.Errorf("map must be placed relative to the cursor below the canvas: %.200q", out)
	}
	for _, want := range []string{fmt.Sprintf("a=T,f=100,i=%d,p=1,", tBase+kittyBackID), fmt.Sprintf("a=T,f=100,i=%d,p=1,", tBase+kittyMapID), fmt.Sprintf("a=T,f=100,i=%d,p=1,", testVariantID(s, "A", 0))} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestKittyAlphaPNGRoundTrip(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 5, 3))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = uint8(i*7), uint8(i*3), uint8(255-i), uint8(i*5)
	}
	var p mmPNG
	got, err := png.Decode(bytes.NewReader(p.encode(img, true)))
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < 3; y++ {
		for x := 0; x < 5; x++ {
			o := img.PixOffset(x, y)
			want := color.NRGBA{img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3]}
			if c := color.NRGBAModel.Convert(got.At(x, y)).(color.NRGBA); c != want {
				t.Fatalf("pixel %d,%d: got %v want %v", x, y, c, want)
			}
		}
	}
}

func TestKittyWalkingChampionsCostLittlePerTick(t *testing.T) {
	offlineMap(t)
	hangNetwork(t)
	var k kittyOut
	s := testSlot()
	walk := func(tick int) []mmIcon {
		var icons []mmIcon
		for i := 0; i < 10; i++ { // every champion crosses a phase step on every tick: the worst a walk can do
			team := 100 + 100*(i/5)
			icons = append(icons, iconAt(fmt.Sprintf("C%d", i), team, 0.1+float64(i)*0.02+3*float64(tick)/400, 0.1+0.06*float64(i)))
		}
		return icons
	}
	k.Frame(testSlot(walk(0)...), true)
	var first, worst, total int
	for pass := 0; pass < 2; pass++ { // the second pass walks the same path: every variant is already there
		for tick := 1; tick <= 40; tick++ {
			s.Icons = walk(tick)
			n := len(k.Frame(s, false))
			if pass == 0 {
				first = max(first, n)
				continue
			}
			worst, total = max(worst, n), total+n
		}
		if pass == 0 {
			s.Icons = walk(0)
			k.Frame(s, false)
		}
	}
	if worst >= 2048 {
		t.Errorf("worst steady tick %d bytes, want < 2 KB for 10 moving champions (mean %d)", worst, total/40)
	}
	if first > 10*8192 {
		t.Errorf("a first pass tick cost %d bytes: more than ten small sprite images", first)
	}
}

func TestKittyAxisSplitsPixelsIntoCellAndPhase(t *testing.T) {
	for _, c := range []struct{ px, cell, box, slot, wc, wp int }{
		{0, 10, 5, 40, 0, 0}, {1, 10, 5, 40, 0, 0}, {2, 10, 5, 40, 0, 1}, {4, 10, 5, 40, 0, 2}, {7, 10, 5, 40, 0, 3},
		{9, 10, 5, 40, 1, 0},     // just short of the next cell wraps to its phase 0
		{10, 10, 5, 40, 1, 0},    // exactly on the cell boundary
		{12, 10, 5, 40, 1, 1},    //
		{-7, 10, 5, 40, 0, 0},    // before the slot clamps to its origin
		{1000, 10, 5, 40, 35, 3}, // past the last box clamps to the last box and phase
		{50, 10, 50, 40, 0, 3},   // a box as wide as the slot cannot move a cell
	} {
		if cell, ph := kittyAxis(c.px, c.cell, c.box, c.slot); cell != c.wc || ph != c.wp {
			t.Errorf("kittyAxis%+v = %d,%d, want %d,%d", c, cell, ph, c.wc, c.wp)
		}
	}
}

func TestKittyAxisErrorIsAtMostHalfAStep(t *testing.T) {
	for _, cell := range []int{8, 10, 18, 36} {
		for px := 0; px < 35*cell; px++ { // below the clamp of the last box
			c, ph := kittyAxis(px, cell, 5, 40)
			if e := abs(c*cell + kittyPhaseOffset(ph, cell) - px); e > (cell+7)/8+1 {
				t.Fatalf("cell %d px %d: drawn %d px off, want at most half a phase step (%d px) plus rounding", cell, px, e, (cell+7)/8)
			}
		}
	}
}

func TestKittyPhaseVariantsAreTransmittedOnceAndKeepTheirIDs(t *testing.T) {
	offlineMap(t)
	hangNetwork(t)
	var k kittyOut
	at := func(i int) *mmSlot { return testSlot(iconAt("A", 100, (200+3*float64(i))/400, 0.5)) } // 3 px: more than a quarter of a 10 px cell
	k.Frame(at(0), true)
	var cmds []kcmd
	phases := map[int]string{}
	for i := 1; i <= 8; i++ { // two cells of quarter steps
		cmds = append(cmds, parseKitty(t, k.Frame(at(i), false))...)
	}
	for i := 0; i <= 8; i++ {
		_, pos := testPos(at(i), "A")
		id := strconv.Itoa(testVariantID(at(i), "A", 0))
		if prev, ok := phases[pos.px]; ok && prev != id {
			t.Errorf("phase %d has two ids: %s and %s", pos.px, prev, id)
		}
		phases[pos.px] = id
	}
	if len(phases) != kittyPhases {
		t.Fatalf("a walk over two cells visited %d phases, want %d", len(phases), kittyPhases)
	}
	ids := map[string]bool{}
	for _, id := range phases {
		ids[id] = true
		if n, _ := strconv.Atoi(id); !isSpriteID(n) || n >= tBase+kittyIDSpan {
			t.Errorf("variant id %s outside the sprite ids of the block", id)
		}
	}
	if len(ids) != kittyPhases {
		t.Errorf("%d distinct variant ids for %d phases", len(ids), kittyPhases)
	}
	for _, c := range kittyFind(cmds, "a", "t") {
		if !ids[c.ctl["i"]] {
			t.Errorf("transmitted unknown image %s", c.ctl["i"])
		}
	}
	for id := range ids {
		if n := kittyCount(cmds, "a", "t", "i", id); n > 1 {
			t.Errorf("variant %s transmitted %d times", id, n)
		}
	}
	for i := 8; i >= 0; i-- { // walking back re-uses every variant: no pixels at all
		if out := k.Frame(at(i), false); strings.Contains(out, "a=T") {
			t.Fatalf("step %d back re-sent a variant: %.60q", i, out)
		}
	}
}

func TestKittyVariantChangePlacesTheNewBeforeUnplacingTheOld(t *testing.T) {
	offlineMap(t)
	hangNetwork(t)
	var k kittyOut
	a, b := testSlot(iconAt("A", 100, 0.5, 0.5)), testSlot(iconAt("A", 100, (200+3.0)/400, 0.5))
	if _, pa := testPos(a, "A"); pa.px == func() int { _, pb := testPos(b, "A"); return pb.px }() {
		t.Fatal("test positions share a phase")
	}
	k.Frame(a, true)
	oldID, newID := strconv.Itoa(testVariantID(a, "A", 0)), strconv.Itoa(testVariantID(b, "A", 0))
	for pass := 0; pass < 2; pass++ { // first the new variant is sent, then it is only placed
		cmds := parseKitty(t, k.Frame(b, false))
		place, del := -1, -1
		for i, c := range cmds {
			switch {
			case c.ctl["a"] == "p" && c.ctl["i"] == newID:
				place = i
			case c.ctl["a"] == "d" && c.ctl["i"] == oldID:
				del = i
				if c.ctl["d"] != "i" || c.ctl["p"] != "1" {
					t.Errorf("old variant must lose only its placement: %v", c.ctl)
				}
			}
		}
		if place < 0 || del < 0 || place > del {
			t.Fatalf("pass %d: place at %d, unplace at %d: the new variant must be up before the old goes: %v", pass, place, del, cmds)
		}
		if pass == 0 && kittyCount(cmds, "a", "t", "i", newID) != 1 {
			t.Error("the first use of a variant transmits it")
		}
		if pass == 1 && kittyCount(cmds, "a", "t") != 0 {
			t.Error("a variant already sent must not be transmitted again")
		}
		k.Frame(a, false)
	}
}

func TestKittyOneVariantMoveIsAboutOneHundredBytes(t *testing.T) {
	offlineMap(t)
	hangNetwork(t)
	var k kittyOut
	at := func(i int) *mmSlot { return testSlot(iconAt("A", 100, (200+3*float64(i))/400, 0.5)) }
	k.Frame(at(0), true)
	for i := 1; i <= 8; i++ { // warm every variant
		k.Frame(at(i), false)
	}
	for i := 7; i >= 0; i-- {
		out := k.Frame(at(i), false)
		if out == "" || len(out) > 160 {
			t.Errorf("step %d: a variant change costs %d bytes, want 1 place + 1 unplace (~100 B)", i, len(out))
		}
	}
}

func TestKittyImageIDsStayInTheBlockAndNeverCollide(t *testing.T) {
	seen := map[int]string{}
	add := func(id int, what string) {
		t.Helper()
		if id < 0 || id >= kittyIDSpan {
			t.Fatalf("%s id %d outside the block of %d", what, id, kittyIDSpan)
		}
		if prev, dup := seen[id]; dup {
			t.Fatalf("%s and %s share id %d", what, prev, id)
		}
		seen[id] = what
	}
	add(kittyBackID, "backing")
	for i := 0; i < kittyMaxTiles; i++ {
		add(kittyMapID+i, "map tile")
	}
	for i := 0; i < kittyMaxIcons; i++ {
		for v := 0; v < kittyVariants; v++ {
			add(kittyVariantID(i, v), fmt.Sprintf("sprite %d variant %d", i, v))
		}
	}
	for g := 0; g < kittyGlyphs; g++ {
		add(kittyDigitID+g, fmt.Sprintf("glyph %d", g))
	}
	for i := 0; i < 2*kittyObjSlots; i++ {
		add(kittyObjID+i, fmt.Sprintf("objective image %d", i))
	}
	glyphs := map[int]bool{}
	for style := 0; style < 2; style++ {
		for _, ch := range []byte("0123456789.:") {
			g := kittyGlyph(style, ch)
			if g < 0 || g >= kittyGlyphs || glyphs[g] {
				t.Fatalf("glyph %d of %q style %d out of range or duplicate", g, ch, style)
			}
			glyphs[g] = true
		}
	}
	if kittyDigitPID <= kittyMaxIcons {
		t.Error("digit placement ids overlap the sprites' i+1")
	}
}

func TestKittyFreeAllFreesEveryVariantAndGlyph(t *testing.T) {
	offlineMap(t)
	hangNetwork(t)
	var k kittyOut
	sent := map[string]bool{}
	collect := func(out string) {
		for _, c := range kittyFind(parseKitty(t, out), "a", "t") {
			sent[c.ctl["i"]] = true
		}
	}
	dead := iconAt("B", 200, 0.92, 0.1)
	dead.State, dead.Secs = mmIconDead, 12
	stale := iconAt("C", 200, 0.7, 0.5)
	stale.State, stale.Secs = mmIconStale, 7
	collect(k.Frame(testSlot(iconAt("A", 100, 0.5, 0.5), dead, stale), true))
	for i := 1; i <= 6; i++ { // A visits several phases
		collect(k.Frame(testSlot(iconAt("A", 100, (200+2.5*float64(i))/400, 0.5+0.01*float64(i)), dead, stale), false))
	}
	if len(sent) < 3+4+4 { // A's variants, 3 sprites' worth of states, and the digit glyphs of both styles
		t.Fatalf("only %d images sent", len(sent))
	}
	out := k.Frame(nil, false)
	freed := map[string]int{}
	for _, c := range kittyFind(parseKitty(t, out), "a", "d", "d", "I") {
		freed[c.ctl["i"]]++
	}
	for id := range sent {
		if freed[id] != 1 {
			t.Errorf("image %s freed %d times", id, freed[id])
		}
	}
	if len(freed) != len(sent) {
		t.Errorf("%d images freed, want the %d sent (map tiles, backing, variants, glyphs)", len(freed), len(sent))
	}
}

// A dead champion at its base is a greyed sprite with the respawn seconds as glyph images
// right under the circle. 400x400 slot, 10x20 px cells, d=33: the blue base (0.08, 0.90)
// has its circle's top-left at px 16,344; across that is quarter 6 -> box cell 1 (canvas
// col 3), phase 2 (circle at px 5 of the box); down it is quarter 69 -> box cell 17 (canvas
// row 18), phase 1 (px 5). The circle's last pixel row is box px 37, in box row 1 = canvas
// row 19, so the digits sit in canvas row 20 and are centred on the circle's cells 3..6.
func TestKittyDeadCountdownIsGlyphImagesUnderTheSprite(t *testing.T) {
	offlineMap(t)
	hangNetwork(t)
	var k kittyOut
	dead := func(secs int) *mmSlot {
		ic := iconAt("A", 100, 0.08, 0.90)
		ic.State, ic.Secs = mmIconDead, secs
		return testSlot(ic)
	}
	glyph := func(style int, ch byte) string { return strconv.Itoa(tBase + kittyDigitID + kittyGlyph(style, ch)) }
	pid := func(j int) string { return strconv.Itoa(kittyDigitPID + j) }

	cmds := parseKitty(t, k.Frame(dead(23), true))
	for j, ch := range []byte("23") {
		tr := kittyFind(cmds, "a", "t", "i", glyph(0, ch))
		pl := kittyFind(cmds, "a", "p", "i", glyph(0, ch))
		if len(tr) != 1 || len(pl) != 1 {
			t.Fatalf("glyph %q: %d transmissions, %d placements", ch, len(tr), len(pl))
		}
		if p := pl[0]; p.row != 21 || p.col != 5+j || p.ctl["c"] != "1" || p.ctl["r"] != "1" || p.ctl["p"] != pid(j) || p.ctl["z"] != strconv.Itoa(kittyDigitZ) {
			t.Errorf("digit %d placed at cell %d;%d %v, want 21;%d one cell, placement %s, z=%d", j, p.row, p.col, p.ctl, 5+j, pid(j), kittyDigitZ)
		}
		img := testDecode(t, tr[0])
		if img.Bounds().Dx() != 10 || img.Bounds().Dy() != 20 {
			t.Errorf("glyph %q is %v, want one 10x20 px cell", ch, img.Bounds())
		}
		lit := 0
		for y := 0; y < 20; y++ {
			for x := 0; x < 10; x++ {
				if _, _, _, a := img.At(x, y).RGBA(); a > 0x8000 {
					lit++
				}
			}
		}
		if lit < 10 || lit > 160 {
			t.Errorf("glyph %q has %d solid pixels in its cell", ch, lit)
		}
	}
	if sp := kittyFind(cmds, "a", "p", "i", strconv.Itoa(testVariantID(dead(23), "A", 0))); len(sp) != 1 || sp[0].ctl["z"] != "2" {
		t.Errorf("the dead sprite itself: %v", sp)
	}

	// 23 -> 22: only the second digit changes: one place, one unplace, no pixels
	out := k.Frame(dead(22), false)
	cmds = parseKitty(t, out)
	if len(cmds) != 2 || cmds[0].ctl["a"] != "p" || cmds[0].ctl["i"] != glyph(0, '2') || cmds[0].ctl["p"] != pid(1) || cmds[0].col != 6 ||
		cmds[1].ctl["a"] != "d" || cmds[1].ctl["d"] != "i" || cmds[1].ctl["i"] != glyph(0, '3') || cmds[1].ctl["p"] != pid(1) {
		t.Fatalf("a once-per-second change: %v", cmds)
	}
	if len(out) > 160 {
		t.Errorf("a countdown tick costs %d bytes", len(out))
	}
	if k.Frame(dead(22), false) != "" {
		t.Error("the same second must emit nothing")
	}

	// due: ".." replaces both digits; the "." glyph is sent once
	cmds = parseKitty(t, k.Frame(dead(0), false))
	if kittyCount(cmds, "a", "t", "i", glyph(0, '.')) != 1 || kittyCount(cmds, "a", "p", "i", glyph(0, '.')) != 2 || kittyCount(cmds, "a", "d", "d", "i") != 2 {
		t.Errorf("due respawn: %v", cmds)
	}

	// back to life: the digits go, the sprite changes variant
	live := testSlot(iconAt("A", 100, 0.08, 0.90))
	cmds = parseKitty(t, k.Frame(live, false))
	if kittyCount(cmds, "a", "d", "d", "i", "i", glyph(0, '.')) != 2 {
		t.Errorf("both digits must be removed: %v", cmds)
	}

	// hidden while counting down: the digits are removed with the sprite, the glyphs kept
	k.Frame(dead(5), false)
	cmds = parseKitty(t, k.Frame(testSlot(), false))
	if kittyCount(cmds, "a", "d", "d", "i") != 2 || kittyCount(cmds, "a", "d", "d", "I") != 0 {
		t.Errorf("a hidden dead champion: sprite and one digit placement go, nothing is freed: %v", cmds)
	}
}

func TestKittyStaleAgeIsDimGlyphs(t *testing.T) {
	offlineMap(t)
	hangNetwork(t)
	var k kittyOut
	ic := iconAt("A", 100, 0.5, 0.5)
	ic.State, ic.Secs = mmIconStale, 12
	cmds := parseKitty(t, k.Frame(testSlot(ic), true))
	for _, ch := range []byte("12") {
		if kittyCount(cmds, "a", "t", "i", strconv.Itoa(tBase+kittyDigitID+kittyGlyph(1, ch))) != 1 {
			t.Errorf("stale glyph %q (style 1) not sent: %v", ch, cmds)
		}
	}
	if kittyCount(cmds, "a", "t", "i", strconv.Itoa(tBase+kittyDigitID+kittyGlyph(0, '1'))) != 0 {
		t.Error("a stale age must not use the dead style")
	}
	ic.Secs = 0 // just seen: no age to show
	if cmds = parseKitty(t, k.Frame(testSlot(ic), false)); kittyCount(cmds, "a", "d", "d", "i") != 2 {
		t.Errorf("an age of 0 shows nothing: %v", cmds)
	}
}

func TestKittyStaleAndDeadSpritesAreDimmedAndGrey(t *testing.T) {
	icon := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := 0; i < len(icon.Pix); i += 4 {
		icon.Pix[i], icon.Pix[i+1], icon.Pix[i+2], icon.Pix[i+3] = 200, 40, 40, 255
	}
	const d = 32
	live := mmSpriteFor(icon, 100, d, kittyRing, "J", mmIconLive)
	stale := mmSpriteFor(icon, 100, d, kittyRing, "J", mmIconStale)
	dead := mmSpriteFor(icon, 100, d, kittyRing, "J", mmIconDead)
	px := func(sp *mmSprite, x, y int) [4]uint8 {
		o := 4 * (y*d + x)
		return [4]uint8{sp.pix[o], sp.pix[o+1], sp.pix[o+2], sp.pix[o+3]}
	}
	if live == stale || live == dead || stale == dead || string(live.pix) == string(stale.pix) || string(live.pix) == string(dead.pix) {
		t.Fatal("live, stale and dead must be three different sprites")
	}
	l, s, dd := px(live, d/2, 5), px(stale, d/2, 5), px(dead, d/2, 5) // portrait, clear of the cross and the badge
	if int(l[0]) < 150 || int(s[0]) < 70 || int(s[0]) > int(l[0])*65/100 || int(s[0]) < int(s[1])+30 {
		t.Errorf("stale portrait %v vs live %v: want about 55%% brightness, still coloured", s, l)
	}
	if dd[0] != dd[1] || dd[1] != dd[2] || dd[0] == 0 {
		t.Errorf("dead portrait %v must be grey", dd)
	}
	lr, sr, dr := px(live, d/2, 1), px(stale, d/2, 1), px(dead, d/2, 1) // the ring
	if lr[2] <= lr[0] {
		t.Errorf("live ring %v is not the team colour", lr)
	}
	if sr[0] != sr[1] || sr[1] != sr[2] || dr[0] != dr[1] || dr[1] != dr[2] {
		t.Errorf("stale ring %v and dead ring %v must be grey", sr, dr)
	}
	if c := px(dead, d/2, d/2); c[0] < 240 || c[1] < 240 || c[2] < 240 {
		t.Errorf("dead sprite centre %v: want the white cross", c)
	}
	if c := px(live, d/2, d/2); c[0] > 230 && c[1] > 230 {
		t.Errorf("live sprite centre %v must not carry the cross", c)
	}
	for _, sp := range []*mmSprite{stale, dead} { // the circle keeps its shape
		for o := 3; o < len(sp.pix); o += 4 {
			if sp.pix[o] != live.pix[o] && (o/4)%d != d-1 {
				t.Fatalf("coverage changed at pixel %d", o/4)
			}
		}
	}
}

func TestMMPickMode(t *testing.T) {
	for _, c := range []struct {
		flag            string
		kitty, truecolr bool
		want            string
	}{
		{"auto", true, true, "kitty"},
		{"auto", false, true, "blocks"},
		{"auto", false, false, "text"},
		{"text", true, true, "text"},
		{"kitty", false, false, "kitty"},
	} {
		if got, err := mmPickMode(c.flag, c.kitty, c.truecolr); err != nil || got != c.want {
			t.Errorf("%+v: got %q %v", c, got, err)
		}
	}
	if _, err := mmPickMode("icons", false, false); err == nil {
		t.Error("unknown flag must fail")
	}
}

func TestMMBlockCells(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.SetRGBA(0, 0, color.RGBA{1, 2, 3, 255})
	img.SetRGBA(0, 1, color.RGBA{4, 5, 6, 255})
	cells := mmBlockCells(img)
	if len(cells) != 1 || len(cells[0]) != 2 {
		t.Fatalf("cells %dx%d", len(cells), len(cells[0]))
	}
	if cells[0][0].text != "▀" || cells[0][0].color != "\033[38;2;1;2;3;48;2;4;5;6m" {
		t.Errorf("fg must be the upper pixel, bg the lower: %q", cells[0][0].color)
	}
}

func TestPanelViewKittySlot(t *testing.T) {
	offlineMap(t)
	lines, slot := mmPanelView(mmView{Mode: mmModeKitty, CellW: 8, CellH: 16}, 100, 25)
	if slot == nil || slot.Row != 1 || len(lines) != 1+slot.Rows {
		t.Fatalf("slot %+v lines %d", slot, len(lines))
	}
	for i, l := range lines {
		if l.vis() != 100 {
			t.Errorf("line %d width %d", i, l.vis())
		}
	}
	if _, s := mmPanelView(mmView{Mode: mmModeBlocks}, 100, 25); s != nil {
		t.Error("blocks mode has no image slot")
	}
}

func TestPanelViewKittyLeavesCountdownsToTheGlyphImages(t *testing.T) {
	offlineMap(t)
	dead := mmIcon{Team: 100, Champ: "A", Label: "T", X: 0.08, Y: 0.90, State: mmIconDead, Secs: 23}
	stale := mmIcon{Team: 200, Champ: "B", Label: "J", X: 0.5, Y: 0.5, State: mmIconStale, Secs: 41}
	lines, slot := mmPanelView(mmView{Mode: mmModeKitty, CellW: 8, CellH: 16, Icons: []mmIcon{dead, stale}}, 100, 25)
	if slot == nil || len(slot.Icons) != 2 {
		t.Fatalf("slot %+v", slot)
	}
	for i, l := range lines[1:] {
		if l.vis() != 100 || strings.TrimSpace(strings.Trim(plain(l), "║")) != "" {
			t.Errorf("slot row %d: width %d, text %q: the countdown is drawn by images, the cells stay blank", i, l.vis(), plain(l))
		}
	}
}

func TestRenderMinimapBlocksReusesStaticRowsAndLabelsMarkers(t *testing.T) {
	offlineMap(t)
	mk := []mmMarker{{Team: 100, Label: "T", X: 0, Y: 0}}
	a := renderMinimapBlocks(mk, nil, "s", 60, 20)
	b := renderMinimapBlocks(nil, nil, "s", 60, 20)
	if len(a) != len(b) || len(a) < 4 {
		t.Fatalf("rows %d vs %d", len(a), len(b))
	}
	if !strings.Contains(plain(a[1]), "T") || strings.Contains(plain(b[1]), "T") {
		t.Errorf("marker row: with=%q without=%q", plain(a[1]), plain(b[1]))
	}
	if &a[2][0] != &b[2][0] {
		t.Error("rows without markers must be served from the cache, not rebuilt each tick")
	}
	allocs := testing.AllocsPerRun(20, func() { renderMinimapBlocks(mk, nil, "s", 60, 20) })
	if allocs > 60 {
		t.Errorf("%.0f allocations per block tick, want a handful", allocs)
	}
}

func plain(l line) string {
	var b strings.Builder
	for _, s := range l {
		b.WriteString(s.text)
	}
	return b.String()
}

func TestKittyIDBlocksAreRandomAndNeverTheOldSmallIDs(t *testing.T) {
	seen := map[int]bool{}
	for i := 0; i < 200; i++ {
		b := randomKittyIDBase()
		if b < 1<<16 || b+kittyIDSpan > 1<<31 || b%kittyIDSpan != 0 {
			t.Fatalf("base %d outside 2^16..2^31 or not block aligned", b)
		}
		seen[b] = true
	}
	if len(seen) < 190 {
		t.Errorf("only %d distinct bases in 200 draws", len(seen))
	}
	if kittyDigitID+kittyGlyphs > kittyIDSpan || kittyBackID >= kittyMapID || kittyMapID+kittyMaxTiles > kittySpriteID || kittySpriteID+kittyMaxIcons*kittyVariants > kittyDigitID {
		t.Error("id offsets overlap or leave the block")
	}
}

func TestKittyTwoProcessesNeverShareAnImageID(t *testing.T) {
	offlineMap(t)
	hangNetwork(t)
	pinned := kittyNewIDBase
	t.Cleanup(func() { kittyNewIDBase = pinned })
	next := 1 << 16
	kittyNewIDBase = func() int { next += kittyIDSpan; return next }
	ids := func(k *kittyOut) map[string]bool {
		out := map[string]bool{}
		for _, c := range parseKitty(t, k.Frame(testSlot(iconAt("A", 100, 0.5, 0.5)), true)) {
			out[c.ctl["i"]] = true
		}
		return out
	}
	var a, b kittyOut
	ia, ib := ids(&a), ids(&b)
	if len(ia) == 0 || len(ib) == 0 {
		t.Fatal("no images")
	}
	for id := range ia {
		if ib[id] {
			t.Errorf("image id %s used by both runs", id)
		}
	}
}

func TestKittySpriteHasLaneBadgeForEveryLetter(t *testing.T) {
	seen := map[string]rune{}
	for _, size := range []int{24, 32, 48, 64} {
		for _, team := range []color.RGBA{mmTeamBlue, mmTeamRed} {
			for _, label := range []string{"T", "J", "M", "B", "S", "A", "Q", "?"} {
				sp := renderSprite(size, kittyRing, team, nil, label)
				_, bw, bh, x0, y0, ok := badgeGeometry(size)
				if !ok {
					t.Fatalf("d=%d: no badge", size)
				}
				var white, plate int
				for y := y0; y < y0+bh; y++ {
					for x := x0; x < x0+bw; x++ {
						o := 4 * (y*size + x)
						if sp.pix[o+3] != 255 {
							t.Fatalf("d=%d %s: badge pixel %d,%d not opaque", size, label, x, y)
						}
						switch [3]uint8{sp.pix[o], sp.pix[o+1], sp.pix[o+2]} {
						case [3]uint8{255, 255, 255}:
							white++
						case [3]uint8{team.R / 3, team.G / 3, team.B / 3}:
							plate++
						}
					}
				}
				if white < 6 || plate < 6 {
					t.Errorf("d=%d label %s: %d white glyph pixels, %d plate pixels", size, label, white, plate)
				}
				if size == 32 && team == mmTeamBlue {
					key := string(sp.pix[4*(y0*size+x0) : 4*((y0+bh-1)*size+x0+bw)])
					if prev, dup := seen[key]; dup {
						t.Errorf("labels %q and %q draw the same badge", string(prev), label)
					}
					seen[key] = []rune(label)[0]
				}
			}
		}
	}
}

func TestBadgeGlyphTableIsComplete(t *testing.T) {
	seen := map[[7]string]rune{}
	for r := 'A'; r <= 'Z'; r++ {
		g, ok := badgeGlyphs[r]
		if !ok {
			t.Fatalf("no glyph for %c", r)
		}
		lit := 0
		for _, row := range g {
			if len(row) != 5 || strings.Trim(row, ".X") != "" {
				t.Fatalf("glyph %c row %q", r, row)
			}
			lit += strings.Count(row, "X")
		}
		if lit < 5 {
			t.Errorf("glyph %c has %d lit pixels", r, lit)
		}
		if prev, dup := seen[g]; dup {
			t.Errorf("glyphs %c and %c are identical", prev, r)
		}
		seen[g] = r
	}
	if _, ok := badgeGlyphs['?']; !ok {
		t.Error("no fallback glyph")
	}
}

func TestKittyBadgeSitsInTheCornerAndKeepsPortraitVisible(t *testing.T) {
	icon := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := 0; i < len(icon.Pix); i += 4 {
		icon.Pix[i], icon.Pix[i+1], icon.Pix[i+2], icon.Pix[i+3] = 200, 40, 40, 255
	}
	const d = 32
	sp := renderSprite(d, kittyRing, mmTeamBlue, icon, "J")
	_, bw, bh, x0, y0, _ := badgeGeometry(d)
	if x0+bw != d || y0+bh != d {
		t.Errorf("badge %dx%d at %d,%d is not in the bottom-right corner", bw, bh, x0, y0)
	}
	if bw*bh*4 > d*d { // at most a quarter of the sprite square
		t.Errorf("badge covers %d of %d px", bw*bh, d*d)
	}
	o := 4 * (d/4*d + d/4) // upper-left of the circle: untouched portrait pixel
	if sp.pix[o] < 150 || sp.pix[o+3] != 255 {
		t.Errorf("portrait pixel %v was overpainted", sp.pix[o:o+4])
	}
}

func TestKittyLabelChangeRetransmitsThatSprite(t *testing.T) {
	offlineMap(t)
	hangNetwork(t)
	var k kittyOut
	a := iconAt("A", 100, 0.5, 0.5)
	a.Label = "T"
	k.Frame(testSlot(a), true)
	if got := k.Frame(testSlot(a), false); got != "" {
		t.Fatalf("same label re-emitted %d bytes", len(got))
	}
	a.Label = "J"
	cmds := parseKitty(t, k.Frame(testSlot(a), false))
	if kittyCount(cmds, "a", "t") != 1 || kittyCount(cmds, "a", "p") != 1 {
		t.Errorf("a new lane letter must re-send the sprite: %v", cmds)
	}
}

func TestMMPreloadAssetsStopsAtTheDeadlineOrWhenReady(t *testing.T) {
	offlineMap(t)
	hangNetwork(t)
	start := time.Now()
	mmPreloadAssets(10*time.Second, "", []mmIcon{{Champ: "A"}}) // no version: nothing to wait for
	if time.Since(start) > time.Second {
		t.Errorf("waited %v for assets that cannot load", time.Since(start))
	}
	start = time.Now()
	mmPreloadAssets(150*time.Millisecond, "v-test", []mmIcon{{Champ: "Nobody"}})
	if d := time.Since(start); d < 100*time.Millisecond || d > 2*time.Second {
		t.Errorf("waited %v, want about the 150 ms deadline", d)
	}
}
