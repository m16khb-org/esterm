package main

import (
	"image"
	"image/color"
	"math"
)

// The objective icons of the kitty minimap: one image per pit and state, on whole cells (no
// pixel offsets, see the Tern caution in .issueops/cautions), transmitted by an a=T right
// after the cursor moved to the cell and re-placed only when the look, the state or the
// cell changes. The countdown under the icon is made of the same cell-sized glyph images as
// the champions' (kittyGlyphPixels); an icon that has not loaded yet is a coloured disc with
// the letter D, E or B.

const (
	kittyObjSlots  = 2 // the dragon pit and the baron pit
	kittyObjDigits = 5 // "19:59"
	// Image kittyObjID + slot*2 + state (0 dim, 1 bright): between the map tiles (ids up to
	// kittyMapID+kittyMaxTiles-1) and the champion sprites (from kittySpriteID), so with the
	// layer shared with the first champion the champion stays on top.
	kittyObjID  = 80
	kittyObjPID = kittyDigitPID + kittyMaxIcons*kittyMaxDigits // slot s: icon placement kittyObjPID+s*(1+kittyObjDigits), digit j that +1+j
	kittyObjZ   = 2
)

// kittyObjLook is what a slot's transmitted images were built from; a change re-creates them.
type kittyObjLook struct {
	icon   image.Image // nil: the placeholder disc
	name   string
	letter rune
}

// kittyObj is the terminal-side state of one objective marker.
type kittyObj struct {
	look     kittyObjLook
	sent     [2]bool // dim and bright images in the terminal
	cur      int     // the state image placed, while placed
	placed   bool
	row, col int
	digits   [kittyObjDigits]kittyDigit
}

// kittyObjGeometry returns the icon diameter in pixels and its placement in cells: one and
// a half champion circles, so the pit stays readable beside the champions fighting at it.
func kittyObjGeometry(d int, s *mmSlot) (od, cols, rows int) {
	od = max(min(d*3/2, s.Cols*s.CellW, s.Rows*s.CellH), 1)
	return od, (od + s.CellW - 1) / s.CellW, (od + s.CellH - 1) / s.CellH
}

// kittyObjCell returns the top-left cell of a marker's placement: the cells nearest to
// centring the icon on its pit, kept inside the slot.
func kittyObjCell(m mmObj, cols, rows, w, h int, s *mmSlot) (row, col int) {
	left := m.X*float64(w) - float64(cols*s.CellW)/2
	top := m.Y*float64(h) - float64(rows*s.CellH)/2
	c := min(max(int(math.Round(left/float64(s.CellW))), 0), max(s.Cols-cols, 0))
	r := min(max(int(math.Round(top/float64(s.CellH))), 0), max(s.Rows-rows, 0))
	return s.Row + r, s.Col + c
}

// kittyObjCountCell returns the cell of the first of n countdown characters of a marker at
// (row, col): centred in the cell row below the icon, above it when the slot ends there.
func kittyObjCountCell(row, col, cols, rows, n int, s *mmSlot) (int, int) {
	r := row + rows
	if r >= s.Row+s.Rows {
		r = max(row-1, s.Row)
	}
	c := min(max(col+(cols-n)/2, s.Col), max(s.Col+s.Cols-n, s.Col))
	return r, c
}

// objFrame appends to *b the commands for the objective markers of slot s (icon diameter d of a
// champion circle); replace says every placement moves (see frame).
func (k *kittyOut) objFrame(b *[]byte, s *mmSlot, w, h, d int, replace bool, goTo func(row, col int)) {
	od, cols, rows := kittyObjGeometry(d, s)
	for i := range k.objs {
		o := &k.objs[i]
		if i >= len(s.Objs) {
			*b = k.hideObj(*b, i)
			continue
		}
		m := s.Objs[i]
		look := kittyObjLook{objIconImg(m.Icon), m.Icon, m.Letter}
		if o.look != look {
			*b = k.freeObj(*b, i) // both state images carry the old icon
			o.look = look
		}
		bright := 0
		if m.State == objUp {
			bright = 1
		}
		pid := kittyObjPID + i*(1+kittyObjDigits)
		row, col := kittyObjCell(m, cols, rows, w, h, s)
		if !o.placed || replace || o.cur != bright || o.row != row || o.col != col {
			id := k.id(kittyObjID + i*2 + bright)
			goTo(row, col)
			if o.sent[bright] {
				*b = kittyPlacement(*b, id, pid, cols, rows, kittyObjZ)
			} else {
				img := objSprite(look.icon, look.letter, od, cols*s.CellW, rows*s.CellH, bright == 1)
				*b = kittyTransmitPlace(*b, id, pid, cols, rows, kittyObjZ, k.png.encode(img, true))
				o.sent[bright] = true
			}
			if o.placed && o.cur != bright { // the new image is up: only now drop the old one
				*b = kittyUnplace(*b, k.id(kittyObjID+i*2+o.cur), pid)
			}
			o.placed, o.cur, o.row, o.col = true, bright, row, col
		}
		tr, tc := kittyObjCountCell(row, col, cols, rows, len(m.Text), s)
		k.digitsFrame(b, o.digits[:], pid+1, m.Text, 0, tr, tc, s, replace, goTo)
	}
}

// hideObj drops the placements of marker i and keeps its images.
func (k *kittyOut) hideObj(b []byte, i int) []byte {
	o := &k.objs[i]
	pid := kittyObjPID + i*(1+kittyObjDigits)
	if o.placed {
		b = kittyUnplace(b, k.id(kittyObjID+i*2+o.cur), pid)
		o.placed = false
	}
	return k.hideDigits(b, o.digits[:], pid+1)
}

// freeObj appends deletes for the images of marker i and forgets them.
func (k *kittyOut) freeObj(b []byte, i int) []byte {
	o := &k.objs[i]
	for st, sent := range o.sent {
		if sent {
			b = kittyFree(b, k.id(kittyObjID+i*2+st))
		}
	}
	o.sent, o.placed = [2]bool{}, false
	return b
}

// objPlaceholder is the disc colour and letter fill of an icon that has not loaded.
func objPlaceholder(letter rune) color.RGBA {
	switch letter {
	case 'B':
		return color.RGBA{0xa8, 0x44, 0xc0, 0xff}
	case 'E':
		return color.RGBA{0xd0, 0x3c, 0x3c, 0xff}
	}
	return color.RGBA{0xe0, 0x7a, 0x2a, 0xff}
}

// objSprite draws a marker into a w*h px straight-alpha image: the icon (nil: a coloured
// disc with the letter) of diameter od, centred, over a dark disc. A dim marker (the
// monster is not there) is darker and more transparent; a bright one (up) has a green ring.
func objSprite(icon image.Image, letter rune, od, w, h int, bright bool) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	ox, oy := (w-od)/2, (h-od)/2
	r0 := float64(od) / 2
	k, ka := 0.5, 0.75 // dim: colour and opacity of the icon
	if bright {
		k, ka = 1, 1
	}
	ring := math.Max(1, float64(od)/16)
	var ib image.Rectangle
	if icon != nil {
		ib = icon.Bounds()
	}
	glyph, scale, gx, gy := objGlyph(letter, od)
	for dy := 0; dy < od; dy++ {
		for dx := 0; dx < od; dx++ {
			r := math.Hypot(float64(dx)+0.5-r0, float64(dy)+0.5-r0)
			cov := math.Min(math.Max(r0-r+0.5, 0), 1)
			if cov == 0 {
				continue
			}
			// backing disc
			br, bg, bb, ba := 14.0, 18.0, 24.0, 0.7*cov
			if !bright {
				ba *= 0.7
			}
			sr, sg, sb, sa := 0.0, 0.0, 0.0, 0.0 // the icon pixel, straight alpha 0..1
			if icon != nil {
				sr, sg, sb, sa = objIconPixel(icon, ib, dx, dy, od)
			} else {
				pc := objPlaceholder(letter)
				sr, sg, sb, sa = float64(pc.R), float64(pc.G), float64(pc.B), cov*0.9
				if gy2, gx2 := (dy-gy)/scale, (dx-gx)/scale; dy >= gy && dx >= gx && gy2 < 7 && gx2 < 5 && glyph[gy2][gx2] == 'X' {
					sr, sg, sb, sa = 255, 255, 255, 1
				}
			}
			sr, sg, sb, sa = sr*k, sg*k, sb*k, sa*ka
			if bright && r > r0-ring {
				t := math.Min(math.Max(r-(r0-ring)+0.5, 0), 1) * cov
				sr, sg, sb, sa = 0x4a*t+sr*(1-t), 0xe0*t+sg*(1-t), 0x6a*t+sb*(1-t), t+sa*(1-t)
			}
			a := sa + ba*(1-sa)
			if a == 0 {
				continue
			}
			o := img.PixOffset(ox+dx, oy+dy)
			img.Pix[o] = uint8((sr*sa+br*ba*(1-sa))/a + 0.5)
			img.Pix[o+1] = uint8((sg*sa+bg*ba*(1-sa))/a + 0.5)
			img.Pix[o+2] = uint8((sb*sa+bb*ba*(1-sa))/a + 0.5)
			img.Pix[o+3] = uint8(a*255 + 0.5)
		}
	}
	return img
}

// objGlyph returns the 5x7 glyph of a placeholder letter, its magnification and its top-left
// corner in an od px square, centred.
func objGlyph(letter rune, od int) (g [7]string, scale, x, y int) {
	g, ok := badgeGlyphs[letter]
	if !ok {
		g = badgeGlyphs['?']
	}
	scale = max(1, od/12)
	return g, scale, (od - 5*scale) / 2, (od - 7*scale) / 2
}

// objIconPixel is the area average of the source icon over the dx, dy pixel of an od px
// square, as straight-alpha colour 0..255 and alpha 0..1 (averaged premultiplied, so the
// transparent border does not darken the edge).
func objIconPixel(icon image.Image, ib image.Rectangle, dx, dy, od int) (r, g, b, a float64) {
	sx0, sx1 := dx*ib.Dx()/od, max((dx+1)*ib.Dx()/od, dx*ib.Dx()/od+1)
	sy0, sy1 := dy*ib.Dy()/od, max((dy+1)*ib.Dy()/od, dy*ib.Dy()/od+1)
	var sr, sg, sb, sa, n float64
	for y := sy0; y < sy1; y++ {
		for x := sx0; x < sx1; x++ {
			pr, pg, pb, pa := icon.At(ib.Min.X+x, ib.Min.Y+y).RGBA() // premultiplied, 16 bit
			sr, sg, sb, sa, n = sr+float64(pr), sg+float64(pg), sb+float64(pb), sa+float64(pa), n+1
		}
	}
	if sa == 0 {
		return 0, 0, 0, 0
	}
	return sr / sa * 255, sg / sa * 255, sb / sa * 255, sa / n / 65535
}
