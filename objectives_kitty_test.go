package main

import (
	"image"
	"image/color"
	"strconv"
	"strings"
	"testing"
	"time"
)

func objSlot(objs ...mmObj) *mmSlot {
	s := testSlot()
	s.Objs = objs
	return s
}

func objWait(icon string, letter rune, x, y float64, text string) mmObj {
	return mmObj{Icon: icon, Letter: letter, X: x, Y: y, State: objWaiting, Text: text}
}

// preloaded makes the CommunityDragon icon called name available as a solid square.
func preloaded(t *testing.T, name string) image.Image {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 200, 90, 30, 255
	}
	// Set the image on the memo's own entry (a load that is already running stays tracked
	// by waitDDIdle): the next call returns it.
	objIconMemo.Lock()
	if objIconMemo.m == nil {
		objIconMemo.m = map[string]*ddAsync{}
	}
	a := objIconMemo.m[name]
	if a == nil {
		a = &ddAsync{}
		objIconMemo.m[name] = a
	}
	objIconMemo.Unlock()
	a.mu.Lock()
	a.img = img
	a.mu.Unlock()
	t.Cleanup(func() {
		a.mu.Lock()
		a.img = nil
		a.mu.Unlock()
	})
	return img
}

func objImageID(slot, state int) string { return strconv.Itoa(tBase + kittyObjID + slot*2 + state) }

func TestKittyObjectiveIdsFitBetweenTheMapAndTheSprites(t *testing.T) {
	if kittyObjID <= kittyMapID+kittyMaxTiles-1 || kittyObjID+2*kittyObjSlots > kittySpriteID {
		t.Errorf("objective ids %d..%d overlap the map tiles or the sprites", kittyObjID, kittyObjID+2*kittyObjSlots-1)
	}
	if kittyObjPID <= kittyDigitPID+kittyMaxIcons*kittyMaxDigits-1 {
		t.Error("objective placement ids overlap the champions' digit placements")
	}
	if kittyDigitID+kittyGlyphs > kittyIDSpan {
		t.Error("glyph ids leave the id block")
	}
}

func TestKittyObjectiveIconIsSentOnceAndPlacedOnWholeCells(t *testing.T) {
	offlineMap(t)
	hangNetwork(t)
	var k kittyOut
	slot := objSlot(objWait("dragon", 'D', objPitXY[0][0], objPitXY[0][1], "3:12"), objWait("baron", 'B', objPitXY[1][0], objPitXY[1][1], "16:40"))
	cmds := parseKitty(t, k.Frame(slot, true))
	_, cols, rows := kittyObjGeometry(33, slot)
	for i := range slot.Objs {
		tr := kittyFind(cmds, "a", "t", "i", objImageID(i, 0))
		pl := kittyFind(cmds, "a", "p", "i", objImageID(i, 0))
		if len(tr) != 1 || len(pl) != 1 {
			t.Fatalf("marker %d: %d transmissions and %d placements of the dim icon, want 1 each", i, len(tr), len(pl))
		}
		c := pl[0]
		wantRow, wantCol := kittyObjCell(slot.Objs[i], cols, rows, 400, 400, slot)
		if c.row != wantRow+1 || c.col != wantCol+1 || c.ctl["c"] != strconv.Itoa(cols) || c.ctl["r"] != strconv.Itoa(rows) || c.ctl["z"] != strconv.Itoa(kittyObjZ) {
			t.Errorf("marker %d placed at %d;%d %v, want cell %d;%d %dx%d z=%d", i, c.row, c.col, c.ctl, wantRow+1, wantCol+1, cols, rows, kittyObjZ)
		}
		if _, off := c.ctl["X"]; off {
			t.Error("a pixel offset (X) on a placement")
		}
	}
	if len(kittyFind(cmds, "i", objImageID(0, 1))) != 0 {
		t.Error("the bright icon was sent while waiting")
	}
	if got := len(kittyFind(cmds, "a", "p", "z", strconv.Itoa(kittyDigitZ))); got != 4+5 {
		t.Errorf("%d countdown glyphs placed, want 4 (3:12) + 5 (16:40)", got)
	}
	if out := k.Frame(slot, false); out != "" {
		t.Errorf("an unchanged frame emits %d bytes", len(out))
	}
}

func TestKittyObjectiveStateChangeSwapsImagesAndClearsTheCountdown(t *testing.T) {
	offlineMap(t)
	hangNetwork(t)
	var k kittyOut
	dragon := objWait("dragon", 'D', objPitXY[0][0], objPitXY[0][1], "0:02")
	k.Frame(objSlot(dragon), true)

	up := dragon
	up.State, up.Text = objUp, ""
	cmds := parseKitty(t, k.Frame(objSlot(up), false))
	if len(kittyFind(cmds, "a", "t", "i", objImageID(0, 1))) != 1 || len(kittyFind(cmds, "a", "p", "i", objImageID(0, 1))) != 1 {
		t.Errorf("the bright icon was not transmitted and placed: %+v", cmds)
	}
	pid := strconv.Itoa(kittyObjPID)
	if len(kittyFind(cmds, "a", "d", "d", "i", "i", objImageID(0, 0), "p", pid)) != 1 {
		t.Errorf("the dim placement was not removed after the bright one was up: %+v", cmds)
	}
	if got := len(kittyFind(cmds, "a", "d", "d", "i")); got != 1+4 { // dim icon + "0:02" glyphs
		t.Errorf("%d placements removed, want the dim icon and 4 glyphs", got)
	}

	// Back to dim (the next drake spawn after another kill): the dim image is already in the
	// terminal, so it is only placed again.
	cmds = parseKitty(t, k.Frame(objSlot(dragon), false))
	if len(kittyFind(cmds, "a", "p", "i", objImageID(0, 0))) != 1 || len(kittyFind(cmds, "a", "t", "i", objImageID(0, 0))) != 0 {
		t.Errorf("returning to dim must only re-place the image: %+v", cmds)
	}
}

func TestKittyObjectiveCountdownOnlyReplacesTheGlyphsThatChanged(t *testing.T) {
	offlineMap(t)
	hangNetwork(t)
	var k kittyOut
	at := func(text string) *mmSlot {
		return objSlot(objWait("dragon", 'D', objPitXY[0][0], objPitXY[0][1], text))
	}
	k.Frame(at("3:12"), true)
	k.Frame(at("3:11"), false)
	cmds := parseKitty(t, k.Frame(at("3:12"), false)) // both glyph images are in the terminal by now
	place := kittyFind(cmds, "a", "p")
	unplace := kittyFind(cmds, "a", "d", "d", "i")
	if len(place) != 1 || len(unplace) != 1 {
		t.Fatalf("one glyph changed: %d placements, %d removals: %+v", len(place), len(unplace), cmds)
	}
	if want := strconv.Itoa(kittyObjPID + 1 + 3); place[0].ctl["p"] != want {
		t.Errorf("replaced placement %s, want the last glyph's %s", place[0].ctl["p"], want)
	}
	// A longer text (up to five characters, "19:59") moves the whole countdown.
	cmds = parseKitty(t, k.Frame(at("19:59"), false))
	if got := len(kittyFind(cmds, "a", "p", "z", strconv.Itoa(kittyDigitZ))); got != 5 {
		t.Errorf("19:59 placed %d glyphs, want 5", got)
	}
}

func TestKittyObjectiveHidesAndFrees(t *testing.T) {
	offlineMap(t)
	hangNetwork(t)
	var k kittyOut
	slot := objSlot(objWait("dragon", 'D', objPitXY[0][0], objPitXY[0][1], "3:12"))
	k.Frame(slot, true)
	cmds := parseKitty(t, k.Frame(objSlot(), false)) // the game ended: no markers
	if got := len(kittyFind(cmds, "a", "d", "d", "i")); got != 1+4 {
		t.Errorf("%d placements removed, want the icon and 4 glyphs", got)
	}
	if len(kittyFind(cmds, "a", "d", "d", "I")) != 0 {
		t.Error("hiding freed image data")
	}
	if out := k.Frame(objSlot(), false); out != "" {
		t.Errorf("a hidden marker costs %d bytes per frame", len(out))
	}
	k.Frame(slot, false)
	freed := map[string]bool{}
	for _, c := range kittyFind(parseKitty(t, k.Frame(nil, false)), "a", "d", "d", "I") {
		freed[c.ctl["i"]] = true
	}
	if !freed[objImageID(0, 0)] {
		t.Errorf("the objective image is not freed with the panel: %v", freed)
	}
}

func TestKittyObjectiveIconReplacesThePlaceholderWhenItLoads(t *testing.T) {
	offlineMap(t)
	hangNetwork(t)
	var k kittyOut
	slot := objSlot(objWait("dragon_infernal", 'D', objPitXY[0][0], objPitXY[0][1], "3:12"))
	cmds := parseKitty(t, k.Frame(slot, true))
	first := kittyFind(cmds, "a", "t", "i", objImageID(0, 0))
	if len(first) != 1 {
		t.Fatal("placeholder not sent")
	}
	preloaded(t, "dragon_infernal")
	cmds = parseKitty(t, k.Frame(slot, false))
	if len(kittyFind(cmds, "a", "d", "d", "I", "i", objImageID(0, 0))) != 1 || len(kittyFind(cmds, "a", "t", "i", objImageID(0, 0))) != 1 {
		t.Errorf("the loaded icon must replace the placeholder image: %+v", cmds)
	}
	if first[0].data == kittyFind(cmds, "a", "t", "i", objImageID(0, 0))[0].data {
		t.Error("the icon image equals the placeholder")
	}
	if out := k.Frame(slot, false); out != "" {
		t.Errorf("a settled frame emits %d bytes", len(out))
	}
}

func TestObjSpriteIsACircleDimmedUntilTheMonsterIsUp(t *testing.T) {
	icon := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for i := 0; i < len(icon.Pix); i += 4 {
		icon.Pix[i], icon.Pix[i+1], icon.Pix[i+2], icon.Pix[i+3] = 220, 100, 30, 255
	}
	dim, bright := objSprite(icon, 'D', 40, 50, 60, false), objSprite(icon, 'D', 40, 50, 60, true)
	for _, sp := range []*image.RGBA{dim, bright} {
		if sp.Bounds().Dx() != 50 || sp.Bounds().Dy() != 60 {
			t.Fatalf("sprite size %v", sp.Bounds())
		}
		if sp.RGBAAt(0, 0).A != 0 || sp.RGBAAt(49, 59).A != 0 {
			t.Error("the corners of the image are not transparent")
		}
		if sp.RGBAAt(25, 30).A == 0 {
			t.Error("the centre is transparent")
		}
	}
	d, b := dim.RGBAAt(25, 30), bright.RGBAAt(25, 30)
	if int(d.R)+int(d.G)+int(d.B) >= int(b.R)+int(b.G)+int(b.B) || d.A >= b.A {
		t.Errorf("dim %v is not darker and more transparent than bright %v", d, b)
	}
	if ring := bright.RGBAAt(25, 11); ring.G < ring.R {
		t.Errorf("the bright sprite's rim %v is not the green ring", ring)
	}
	ph := objSprite(nil, 'B', 40, 40, 40, true)
	var white int
	for y := 0; y < 40; y++ {
		for x := 0; x < 40; x++ {
			if c := ph.RGBAAt(x, y); c == (color.RGBA{255, 255, 255, 255}) {
				white++
			}
		}
	}
	if white < 10 {
		t.Errorf("the placeholder letter has %d white pixels", white)
	}
}

func TestObjMarkersFollowTheView(t *testing.T) {
	v := objView{Valid: true, Soul: -1, Point: -1,
		Dragon: objTimer{State: objWaiting, Left: 192 * time.Second, Drake: "infernal"},
		Baron:  objTimer{State: objUp}}
	ms := objMarkers(v)
	if len(ms) != 2 || ms[0].Icon != "dragon_infernal" || ms[0].Text != "3:12" || ms[0].Letter != 'D' || ms[1].Icon != "baron" || ms[1].Text != "" || ms[1].State != objUp {
		t.Fatalf("markers %+v", ms)
	}
	v.Dragon = objTimer{State: objUnknown, Drake: objElderName}
	if ms = objMarkers(v); ms[0].Icon != "dragon_elder" || ms[0].Letter != 'E' || ms[0].Text != "" {
		t.Errorf("elder marker %+v", ms[0])
	}
	v.Dragon = objTimer{State: objWaiting, Left: 10 * time.Second}
	if ms = objMarkers(v); ms[0].Icon != "dragon" {
		t.Errorf("a drake of unknown type uses the generic icon, got %q", ms[0].Icon)
	}
	if objMarkers(objView{}) != nil {
		t.Error("markers for an empty view")
	}
}

func TestObjectiveCellsSitOnThePitWithTheCountdownBelow(t *testing.T) {
	cells := placeObjectives([]mmObj{objWait("dragon", 'D', objPitXY[0][0], objPitXY[0][1], "3:12")}, 40, 20)
	want := []objCell{{26, 13, 'D', objWaiting, true}, {25, 14, '3', objWaiting, false}, {26, 14, ':', objWaiting, false}, {27, 14, '1', objWaiting, false}, {28, 14, '2', objWaiting, false}}
	if len(cells) != len(want) {
		t.Fatalf("cells %+v", cells)
	}
	for i := range want {
		if cells[i] != want[i] {
			t.Errorf("cell %d = %+v, want %+v", i, cells[i], want[i])
		}
	}
	// At the bottom edge the countdown goes above; on a narrow map it stays inside.
	bottom := placeObjectives([]mmObj{objWait("dragon", 'D', 0.99, 0.99, "19:59")}, 10, 6)
	for _, c := range bottom {
		if c.x < 0 || c.x >= 10 || c.y < 0 || c.y >= 6 {
			t.Errorf("cell %+v outside the map", c)
		}
	}
	if bottom[1].y != 4 {
		t.Errorf("countdown row %d, want above the pit (4)", bottom[1].y)
	}
}

func TestPanelShowsObjectivesInEveryMode(t *testing.T) {
	offlineMap(t)
	v := mmView{Obj: objView{Valid: true, Soul: -1, Point: -1,
		Dragon: objTimer{State: objWaiting, Left: 192 * time.Second, Drake: "infernal"},
		Baron:  objTimer{State: objUp}}, Markers: []mmMarker{{Team: 100, Label: "M", X: objPitXY[0][0], Y: objPitXY[0][1]}}}
	for _, mode := range []string{mmModeText, mmModeBlocks} {
		v.Mode = mode
		lines, _ := mmPanelView(v, 60, 20)
		var all strings.Builder
		for _, l := range lines {
			all.WriteString(lineText(l))
		}
		txt := all.String()
		if !strings.Contains(txt, "3:12") || !strings.Contains(txt, "B") || !strings.Contains(txt, "D") {
			t.Errorf("%s panel misses the D marker, B marker or 3:12 countdown", mode)
		}
		if strings.Contains(txt, "M") == false {
			t.Errorf("%s panel lost the champion marker that sits on the dragon pit", mode)
		}
	}
	v.Mode, v.CellW, v.CellH = mmModeKitty, 8, 16
	if _, slot := mmPanelView(v, 100, 25); slot == nil || len(slot.Objs) != 2 {
		t.Errorf("kitty slot carries %+v", slot)
	}
}
