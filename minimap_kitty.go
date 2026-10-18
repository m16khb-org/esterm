package main

import (
	"bytes"
	"cmp"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/draw"
	"math"
	"math/rand"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Pixel minimap. One compositor (Data Dragon map + circle-cropped champion icons with team
// rings) feeds two outputs: the Kitty graphics protocol (real pixels, icons) and truecolor
// half-blocks (map pixels + 2-letter labels). Terminals without either keep braille + labels.

const (
	mmModeAuto   = "auto"
	mmModeKitty  = "kitty"
	mmModeBlocks = "blocks"
	mmModeText   = "text"

	kittyChunk = 4096
	kittyQuery = "\x1b_Gi=31,s=1,v=1,a=q,t=d,f=24;AAAA\x1b\\\x1b[c"
)

// mmIcon is one champion to draw as an icon.
type mmIcon struct {
	Team  int
	Champ string
	Label string  // lane letter (T/J/M/B/S, else the champion's first letter) for the badge
	X, Y  float64 // normalized 0..1, origin top-left; fractional positions are drawn at sub-cell offsets
	State mmIconState
	Secs  int // mmIconStale: seconds since it was last seen; mmIconDead: seconds until the respawn
}

// mmSlot is the cell rectangle the panel reserves for the kitty image, in canvas cells.
type mmSlot struct {
	Row, Col, Cols, Rows int
	CellW, CellH         int // cell size in pixels
	Icons                []mmIcon
	Objs                 []mmObj // the dragon pit and baron pit markers, in that order; empty: none shown
	Ver                  string  // Data Dragon version of the icons
}

var (
	mmTeamBlue = color.RGBA{0x3f, 0xa7, 0xff, 0xff}
	mmTeamRed  = color.RGBA{0xff, 0x4a, 0x4a, 0xff}
)

// ---------------- mode selection ----------------

func mmTrueColor() bool {
	c := os.Getenv("COLORTERM")
	return c == "truecolor" || c == "24bit"
}

func kittyEnvSupported() bool {
	switch strings.ToLower(os.Getenv("TERM_PROGRAM")) {
	case "ghostty", "wezterm", "herdr":
		return true
	}
	return os.Getenv("KITTY_WINDOW_ID") != "" || os.Getenv("TERM") == "xterm-kitty"
}

func isTTY(f *os.File) bool {
	var ws winsize
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws)))
	return e == 0
}

// ttyQuery writes query to the terminal and returns what comes back in raw mode: it reads
// until a DA1 reply (CSI ? ... c, which the query must end with) or until `reads` reads of
// 100 ms found none. Nil when stdin cannot be put in raw mode.
func ttyQuery(query string, reads int) []byte {
	fd := int(os.Stdin.Fd())
	var old syscall.Termios
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(ioctlGetTermios), uintptr(unsafe.Pointer(&old))); e != 0 {
		return nil
	}
	raw := old
	raw.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.ISIG
	raw.Cc[syscall.VMIN] = 0
	raw.Cc[syscall.VTIME] = 1 // 100 ms per read
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(ioctlSetTermios), uintptr(unsafe.Pointer(&raw))); e != 0 {
		return nil
	}
	defer syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(ioctlSetTermios), uintptr(unsafe.Pointer(&old)))
	fmt.Print(query)
	var got []byte
	buf := make([]byte, 256)
	for i := 0; i < reads; i++ {
		n, _ := syscall.Read(fd, buf)
		got = append(got, buf[:max(n, 0)]...)
		if bytes.Contains(got, []byte("\x1b[?")) && bytes.HasSuffix(got, []byte("c")) {
			break
		}
	}
	return got
}

// kittyProbe asks the terminal whether it speaks the graphics protocol: a 1x1 query
// followed by a DA1 request, which every terminal answers, so unsupported terminals end
// the wait immediately. Gives up after ~200 ms.
func kittyProbe() bool {
	return bytes.Contains(ttyQuery(kittyQuery, 2), []byte("_Gi=31;OK"))
}

// mmPickMode resolves the -icons flag: auto prefers kitty, then blocks, then text.
func mmPickMode(flag string, kitty, truecolor bool) (string, error) {
	switch flag {
	case mmModeKitty, mmModeBlocks, mmModeText:
		return flag, nil
	case mmModeAuto, "":
		switch {
		case kitty:
			return mmModeKitty, nil
		case truecolor:
			return mmModeBlocks, nil
		}
		return mmModeText, nil
	}
	return "", fmt.Errorf("-icons wants auto|kitty|blocks|text, got %q", flag)
}

// mmResolveMode detects terminal capabilities (only for auto) and picks the minimap mode.
// In kitty mode it also learns the cell size when the tty does not report pixels. Call
// before the key reader goroutine starts: the probes read the tty.
func mmResolveMode(flag string) (string, error) {
	kitty := false
	if flag == mmModeAuto || flag == "" {
		if isTTY(os.Stdout) {
			kitty = kittyEnvSupported() || (isTTY(os.Stdin) && kittyProbe())
		}
	}
	mode, err := mmPickMode(flag, kitty, mmTrueColor())
	if err == nil && mode == mmModeKitty && isTTY(os.Stdout) && isTTY(os.Stdin) {
		if w, _ := winsizeCellPx(); w == 0 {
			w, h := cellProbe()
			termCell.Lock()
			termCell.w, termCell.h = w, h
			termCell.Unlock()
		}
	}
	return mode, err
}

// termCell is the cell size learned from the terminal's XTWINOPS replies (0: not learned).
// A window resize leaves the cell size alone, so one answer at startup serves the run.
var termCell struct {
	sync.Mutex
	w, h int
}

// winsizeCellPx returns the cell size from the tty's pixel size, or 0, 0 when it reports none.
func winsizeCellPx() (int, int) {
	var ws winsize
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, os.Stdout.Fd(), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws)))
	if e != 0 || ws.Col == 0 || ws.Row == 0 || ws.Xpixel == 0 || ws.Ypixel == 0 {
		return 0, 0
	}
	return max(int(ws.Xpixel)/int(ws.Col), 1), max(int(ws.Ypixel)/int(ws.Row), 1)
}

// termCellPx returns the cell size in pixels: the tty's pixel size, else what the terminal
// answered to the startup query, else 8x16.
func termCellPx() (int, int) {
	if w, h := winsizeCellPx(); w > 0 {
		return w, h
	}
	termCell.Lock()
	defer termCell.Unlock()
	if termCell.w > 0 && termCell.h > 0 {
		return termCell.w, termCell.h
	}
	return 8, 16
}

var (
	reCell16 = regexp.MustCompile(`\x1b\[6;(\d+);(\d+)t`) // CSI 6 ; height ; width t: cell size in pixels
	reArea14 = regexp.MustCompile(`\x1b\[4;(\d+);(\d+)t`) // CSI 4 ; height ; width t: text area in pixels
)

// parseCellReplies extracts the cell size from XTWINOPS replies to CSI 16 t and CSI 14 t:
// the 16 t answer as it is, else the 14 t text area divided by cols x rows. 0, 0: neither.
func parseCellReplies(got []byte, cols, rows int) (int, int) {
	if m := reCell16.FindSubmatch(got); m != nil {
		h, _ := strconv.Atoi(string(m[1]))
		w, _ := strconv.Atoi(string(m[2]))
		if w > 0 && h > 0 {
			return w, h
		}
	}
	if m := reArea14.FindSubmatch(got); m != nil && cols > 0 && rows > 0 {
		h, _ := strconv.Atoi(string(m[1]))
		w, _ := strconv.Atoi(string(m[2]))
		if w >= cols && h >= rows {
			return w / cols, h / rows
		}
	}
	return 0, 0
}

// cellProbe asks the terminal for its cell size (CSI 16 t, then the text area with CSI 14 t)
// followed by a DA1 request, which every terminal answers, so one that ignores XTWINOPS ends
// the wait at once. Gives up after ~400 ms; 0, 0 when nothing usable came back.
func cellProbe() (int, int) {
	cols, rows := termSize()
	return parseCellReplies(ttyQuery("\x1b[16t\x1b[14t\x1b[c", 4), cols, rows)
}

// ---------------- compositor ----------------

var mmScaled struct {
	sync.Mutex
	src  image.Image
	w, h int
	img  *image.RGBA
}

// mmScaledMap returns the map scaled to w*h and dimmed (cached), or nil without a map image.
func mmScaledMap(src image.Image, w, h int) *image.RGBA {
	if src == nil {
		return nil
	}
	mmScaled.Lock()
	defer mmScaled.Unlock()
	if mmScaled.src == src && mmScaled.w == w && mmScaled.h == h {
		return mmScaled.img
	}
	img := scaleDim(src, w, h, 0.75)
	mmScaled.src, mmScaled.w, mmScaled.h, mmScaled.img = src, w, h, img
	return img
}

// scaleDim resamples src to w*h (area average when shrinking, bilinear when enlarging)
// and multiplies brightness by dim.
func scaleDim(src image.Image, w, h int, dim float64) *image.RGBA {
	b := src.Bounds()
	s := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(s, s.Bounds(), src, b.Min, draw.Src)
	sw, sh := float64(b.Dx()), float64(b.Dy())
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	at := func(x, y int) [3]float64 {
		x, y = min(max(x, 0), b.Dx()-1), min(max(y, 0), b.Dy()-1)
		p := s.Pix[s.PixOffset(x, y):]
		return [3]float64{float64(p[0]), float64(p[1]), float64(p[2])}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var c [3]float64
			if sw <= float64(w) { // enlarging: bilinear
				fx, fy := (float64(x)+0.5)*sw/float64(w)-0.5, (float64(y)+0.5)*sh/float64(h)-0.5
				x0, y0 := int(math.Floor(fx)), int(math.Floor(fy))
				tx, ty := fx-float64(x0), fy-float64(y0)
				for k := 0; k < 3; k++ {
					c[k] = at(x0, y0)[k]*(1-tx)*(1-ty) + at(x0+1, y0)[k]*tx*(1-ty) + at(x0, y0+1)[k]*(1-tx)*ty + at(x0+1, y0+1)[k]*tx*ty
				}
			} else { // shrinking: box average
				x0, x1 := x*b.Dx()/w, (x+1)*b.Dx()/w
				y0, y1 := y*b.Dy()/h, (y+1)*b.Dy()/h
				n := float64((x1 - x0) * (y1 - y0))
				for sy := y0; sy < y1; sy++ {
					for sx := x0; sx < x1; sx++ {
						p := at(sx, sy)
						c[0], c[1], c[2] = c[0]+p[0], c[1]+p[1], c[2]+p[2]
					}
				}
				c[0], c[1], c[2] = c[0]/n, c[1]/n, c[2]/n
			}
			o := out.PixOffset(x, y)
			out.Pix[o], out.Pix[o+1], out.Pix[o+2], out.Pix[o+3] = uint8(c[0]*dim), uint8(c[1]*dim), uint8(c[2]*dim), 255
		}
	}
	return out
}

var mmIconMemo struct {
	sync.Mutex
	m map[string]*ddAsync
}

// mmIconImg returns a Data Dragon champion icon, or nil while it is not available yet. It
// never blocks: a missing icon is loaded in the background (retried with backoff after a
// failure) and the next call picks it up.
func mmIconImg(ver, champ string) image.Image {
	if ver == "" {
		return nil
	}
	path := "img/champion/" + champ + ".png"
	if img := ddPeek(ver, path); img != nil {
		return img
	}
	key := ver + "/" + champ
	mmIconMemo.Lock()
	a := mmIconMemo.m[key]
	if a == nil {
		if mmIconMemo.m == nil || len(mmIconMemo.m) >= 512 {
			mmIconMemo.m = map[string]*ddAsync{}
		}
		a = &ddAsync{}
		mmIconMemo.m[key] = a
	}
	mmIconMemo.Unlock()
	return a.get(func(ctx context.Context) (image.Image, error) { return ddImage(ctx, ver, path) })
}

func mix(a, b uint8, t float64) uint8 { return uint8(float64(a)*(1-t) + float64(b)*t + 0.5) }

// mmSprite is a pre-rendered d*d circular icon with its team ring and lane badge: r,g,b plus
// the circle's coverage (0..255) per pixel, ready to blit.
type mmSprite struct {
	d   int
	pix []uint8
}

type mmSpriteKey struct {
	icon          image.Image // nil: plain disc
	team, d, ring int
	label         string
	state         mmIconState
}

var mmSprites struct {
	sync.Mutex
	m map[mmSpriteKey]*mmSprite
}

// mmSpriteFor returns the cached sprite of an icon (nil: plain disc), lane label and state
// for a team. Sprites are keyed by the icon image identity, so a placeholder is replaced
// once the icon arrives. A stale sprite is the portrait at ~55% brightness inside a grey
// ring; a dead one is the desaturated portrait with a white cross over it.
func mmSpriteFor(icon image.Image, team, d, ring int, label string, state mmIconState) *mmSprite {
	k := mmSpriteKey{icon, team, d, ring, label, state}
	mmSprites.Lock()
	defer mmSprites.Unlock()
	if sp := mmSprites.m[k]; sp != nil {
		return sp
	}
	if mmSprites.m == nil || len(mmSprites.m) >= 64 {
		mmSprites.m = map[mmSpriteKey]*mmSprite{}
	}
	rc := mmTeamBlue
	if team == 200 {
		rc = mmTeamRed
	}
	if state != mmIconLive {
		rc = mmRingGrey
	}
	sp := renderSprite(d, ring, rc, icon, label)
	switch state {
	case mmIconStale:
		shadeSprite(sp, false, 0.55)
	case mmIconDead:
		shadeSprite(sp, true, 0.7)
		drawCross(sp)
	}
	mmSprites.m[k] = sp
	return sp
}

// renderSprite draws a circle-cropped d*d icon with a ring of the given width and a lane
// badge showing label. icon may be nil (plain disc).
func renderSprite(d, ring int, rc color.RGBA, icon image.Image, label string) *mmSprite {
	sp := &mmSprite{d: d, pix: make([]uint8, 4*d*d)}
	r0 := float64(d) / 2
	var ib image.Rectangle
	if icon != nil {
		ib = icon.Bounds()
	}
	for dy := 0; dy < d; dy++ {
		for dx := 0; dx < d; dx++ {
			r := math.Hypot(float64(dx)+0.5-r0, float64(dy)+0.5-r0)
			cov := math.Min(math.Max(r0-r+0.5, 0), 1)
			if cov == 0 {
				continue
			}
			c := color.RGBA{rc.R / 3, rc.G / 3, rc.B / 3, 255}
			if icon != nil {
				sx0, sx1 := dx*ib.Dx()/d, max((dx+1)*ib.Dx()/d, dx*ib.Dx()/d+1)
				sy0, sy1 := dy*ib.Dy()/d, max((dy+1)*ib.Dy()/d, dy*ib.Dy()/d+1)
				var sr, sg, sb, n uint32
				for sy := sy0; sy < sy1; sy++ {
					for sx := sx0; sx < sx1; sx++ {
						pr, pg, pb, _ := icon.At(ib.Min.X+sx, ib.Min.Y+sy).RGBA()
						sr, sg, sb, n = sr+pr>>8, sg+pg>>8, sb+pb>>8, n+1
					}
				}
				c = color.RGBA{uint8(sr / n), uint8(sg / n), uint8(sb / n), 255}
			}
			rt := math.Min(math.Max(r-(r0-float64(ring))+0.5, 0), 1)
			o := 4 * (dy*d + dx)
			sp.pix[o], sp.pix[o+1], sp.pix[o+2], sp.pix[o+3] = mix(c.R, rc.R, rt), mix(c.G, rc.G, rt), mix(c.B, rc.B, rt), uint8(cov*255+0.5)
		}
	}
	drawBadge(sp, rc, label)
	return sp
}

// mmRingGrey is the ring colour of a stale or dead sprite.
var mmRingGrey = color.RGBA{0x9a, 0x9a, 0x9a, 0xff}

// shadeSprite multiplies the brightness of sp by k and, with gray, first replaces every
// colour by its luminance. Coverage is left alone, so the circle keeps its shape.
func shadeSprite(sp *mmSprite, gray bool, k float64) {
	for o := 0; o < len(sp.pix); o += 4 {
		r, g, b := float64(sp.pix[o]), float64(sp.pix[o+1]), float64(sp.pix[o+2])
		if gray {
			r = 0.299*r + 0.587*g + 0.114*b
			g, b = r, r
		}
		sp.pix[o], sp.pix[o+1], sp.pix[o+2] = uint8(r*k+0.5), uint8(g*k+0.5), uint8(b*k+0.5)
	}
}

// drawCross paints a white X with a dark edge over the inner part of the circle of sp.
func drawCross(sp *mmSprite) {
	d := float64(sp.d)
	a, z := 0.27*d, 0.73*d
	half := math.Max(1, d/18)
	for y := 0; y < sp.d; y++ {
		for x := 0; x < sp.d; x++ {
			o := 4 * (y*sp.d + x)
			if sp.pix[o+3] == 0 {
				continue
			}
			px, py := float64(x)+0.5, float64(y)+0.5
			dist := math.Min(segDist(px, py, a, a, z, z), segDist(px, py, a, z, z, a))
			edge := math.Min(math.Max(half+1.5-dist, 0), 1)
			ink := math.Min(math.Max(half+0.5-dist, 0), 1)
			for c := 0; c < 3; c++ {
				v := float64(sp.pix[o+c]) * (1 - 0.7*edge)
				sp.pix[o+c] = uint8(v*(1-ink) + 255*ink + 0.5)
			}
		}
	}
}

// segDist is the distance from (px, py) to the segment (x0, y0)-(x1, y1).
func segDist(px, py, x0, y0, x1, y1 float64) float64 {
	dx, dy := x1-x0, y1-y0
	t := math.Min(math.Max(((px-x0)*dx+(py-y0)*dy)/(dx*dx+dy*dy), 0), 1)
	return math.Hypot(px-x0-t*dx, py-y0-t*dy)
}

// badgeGlyphs is a 5x7 bitmap font for the lane badge: one string of 7 rows per glyph, 'X'
// is a lit pixel. A-Z cover lane letters and the champion's first letter; '?' is the rest.
var badgeGlyphs = map[rune][7]string{
	'A': {".XXX.", "X...X", "X...X", "XXXXX", "X...X", "X...X", "X...X"},
	'B': {"XXXX.", "X...X", "X...X", "XXXX.", "X...X", "X...X", "XXXX."},
	'C': {".XXX.", "X...X", "X....", "X....", "X....", "X...X", ".XXX."},
	'D': {"XXXX.", "X...X", "X...X", "X...X", "X...X", "X...X", "XXXX."},
	'E': {"XXXXX", "X....", "X....", "XXXX.", "X....", "X....", "XXXXX"},
	'F': {"XXXXX", "X....", "X....", "XXXX.", "X....", "X....", "X...."},
	'G': {".XXX.", "X...X", "X....", "X.XXX", "X...X", "X...X", ".XXXX"},
	'H': {"X...X", "X...X", "X...X", "XXXXX", "X...X", "X...X", "X...X"},
	'I': {".XXX.", "..X..", "..X..", "..X..", "..X..", "..X..", ".XXX."},
	'J': {"..XXX", "...X.", "...X.", "...X.", "...X.", "X..X.", ".XX.."},
	'K': {"X...X", "X..X.", "X.X..", "XX...", "X.X..", "X..X.", "X...X"},
	'L': {"X....", "X....", "X....", "X....", "X....", "X....", "XXXXX"},
	'M': {"X...X", "XX.XX", "X.X.X", "X.X.X", "X...X", "X...X", "X...X"},
	'N': {"X...X", "XX..X", "X.X.X", "X..XX", "X...X", "X...X", "X...X"},
	'O': {".XXX.", "X...X", "X...X", "X...X", "X...X", "X...X", ".XXX."},
	'P': {"XXXX.", "X...X", "X...X", "XXXX.", "X....", "X....", "X...."},
	'Q': {".XXX.", "X...X", "X...X", "X...X", "X.X.X", "X..X.", ".XX.X"},
	'R': {"XXXX.", "X...X", "X...X", "XXXX.", "X.X..", "X..X.", "X...X"},
	'S': {".XXXX", "X....", "X....", ".XXX.", "....X", "....X", "XXXX."},
	'T': {"XXXXX", "..X..", "..X..", "..X..", "..X..", "..X..", "..X.."},
	'U': {"X...X", "X...X", "X...X", "X...X", "X...X", "X...X", ".XXX."},
	'V': {"X...X", "X...X", "X...X", "X...X", "X...X", ".X.X.", "..X.."},
	'W': {"X...X", "X...X", "X...X", "X.X.X", "X.X.X", "X.X.X", ".X.X."},
	'X': {"X...X", "X...X", ".X.X.", "..X..", ".X.X.", "X...X", "X...X"},
	'Y': {"X...X", "X...X", ".X.X.", "..X..", "..X..", "..X..", "..X.."},
	'Z': {"XXXXX", "....X", "...X.", "..X..", ".X...", "X....", "XXXXX"},
	'?': {".XXX.", "X...X", "....X", "...X.", "..X..", ".....", "..X.."},
}

// badgeGeometry returns the glyph scale, the badge size and its top-left corner in a d*d
// sprite: the glyph is magnified with the sprite and the badge sits in the bottom-right
// corner of the circle's square. ok is false when the sprite is too small for a badge.
func badgeGeometry(d int) (scale, bw, bh, x0, y0 int, ok bool) {
	scale = max(1, (d+4)/18)
	pad := (scale + 1) / 2
	bw, bh = 5*scale+2*pad, 7*scale+2*pad
	if bh*2 > d {
		return 0, 0, 0, 0, 0, false
	}
	return scale, bw, bh, d - bw, d - bh, true
}

// drawBadge paints the lane letter on a dark team-coloured plate with a full-colour edge
// and a white glyph over the bottom-right corner of sp. Label runes outside A-Z show '?'.
func drawBadge(sp *mmSprite, rc color.RGBA, label string) {
	scale, bw, bh, x0, y0, ok := badgeGeometry(sp.d)
	if !ok {
		return
	}
	r := []rune(strings.ToUpper(label))
	g, found := badgeGlyphs['?'], false
	if len(r) > 0 {
		g, found = badgeGlyphs[r[0]]
	}
	if !found {
		g = badgeGlyphs['?']
	}
	pad := (scale + 1) / 2
	set := func(x, y int, c color.RGBA) {
		o := 4 * (y*sp.d + x)
		sp.pix[o], sp.pix[o+1], sp.pix[o+2], sp.pix[o+3] = c.R, c.G, c.B, 255
	}
	plate := color.RGBA{rc.R / 3, rc.G / 3, rc.B / 3, 255}
	for y := 0; y < bh; y++ {
		for x := 0; x < bw; x++ {
			c := plate
			if x == 0 || y == 0 || x == bw-1 || y == bh-1 {
				c = rc
			} else if gx, gy := (x-pad)/scale, (y-pad)/scale; x >= pad && y >= pad && gx < 5 && gy < 7 && g[gy][gx] == 'X' {
				c = color.RGBA{255, 255, 255, 255}
			}
			set(x0+x, y0+y, c)
		}
	}
}

// mmPlace is one icon's team, champion, state and top-left pixel in the composed image.
type mmPlace struct {
	Team  int
	Champ string
	Label string // lane letter for the badge
	State mmIconState
	Secs  int
	X, Y  int
}

// mmPlacements converts icons to pixel placements in (team, champion) draw order, each
// centred on its normalized position and kept inside the w*h image. dst is reused.
func mmPlacements(dst []mmPlace, w, h, d int, icons []mmIcon) []mmPlace {
	dst = dst[:0]
	if d <= 0 {
		return dst
	}
	for _, ic := range icons {
		dst = append(dst, mmPlace{
			Team:  ic.Team,
			Champ: ic.Champ,
			Label: ic.Label,
			State: ic.State,
			Secs:  ic.Secs,
			X:     min(max(int(math.Round(ic.X*float64(w)))-d/2, 0), w-d),
			Y:     min(max(int(math.Round(ic.Y*float64(h)))-d/2, 0), h-d),
		})
	}
	slices.SortStableFunc(dst, func(a, b mmPlace) int {
		if a.Team != b.Team {
			return cmp.Compare(a.Team, b.Team)
		}
		return strings.Compare(a.Champ, b.Champ)
	})
	return dst
}

// mmBackdrop returns the w*h map pixels: bg itself when it has that size, else a dark fill.
// The result is shared with bg: callers must not modify it.
func mmBackdrop(w, h int, bg *image.RGBA) *image.RGBA {
	if bg != nil && bg.Bounds().Dx() == w && bg.Bounds().Dy() == h {
		return bg
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 0x1b, 0x24, 0x20, 0xff
	}
	return img
}

// mmKittyGeometry returns the slot's pixel size and icon diameter: icons are ~1/12 of the
// map side with a 2 px ring.
func mmKittyGeometry(s *mmSlot) (w, h, d int) {
	w, h = s.Cols*s.CellW, s.Rows*s.CellH
	return w, h, min(min(w, h)/12, w, h)
}

// ---------------- half-block output ----------------

// appendBlockStyle appends the SGR sequence for the given foreground and background.
func appendBlockStyle(b []byte, prefix string, fg, bg [3]uint8) []byte {
	b = append(b, prefix...)
	for i, v := range fg {
		if i > 0 {
			b = append(b, ';')
		}
		b = strconv.AppendUint(b, uint64(v), 10)
	}
	b = append(b, ";48;2;"...)
	for i, v := range bg {
		if i > 0 {
			b = append(b, ';')
		}
		b = strconv.AppendUint(b, uint64(v), 10)
	}
	return append(b, 'm')
}

// mmBlockCells converts a map image (even height) to half-block cell rows: the upper pixel
// is the foreground of "▀", the lower pixel its background.
func mmBlockCells(img *image.RGBA) [][]seg {
	rows := img.Bounds().Dy() / 2
	out := make([][]seg, rows)
	var buf [48]byte
	for r := range out {
		out[r] = make([]seg, img.Bounds().Dx())
		for c := range out[r] {
			t, b := img.RGBAAt(c, 2*r), img.RGBAAt(c, 2*r+1)
			out[r][c] = C("▀", string(appendBlockStyle(buf[:0], "\033[38;2;", [3]uint8{t.R, t.G, t.B}, [3]uint8{b.R, b.G, b.B})))
		}
	}
	return out
}

// mmBlockLabel is the style of a label cell on the mean pixel colour: a live champion's
// letter is bold in its team colour; a stale or dead one's is the team colour at ~55%
// without bold; a countdown digit is light grey.
func mmBlockLabel(img *image.RGBA, c, r, team int, state mmIconState, digit bool) string {
	t, b := img.RGBAAt(c, 2*r), img.RGBAAt(c, 2*r+1)
	fg := mmTeamBlue
	if team == 200 {
		fg = mmTeamRed
	}
	prefix, rgb := "\033[1;38;2;", [3]uint8{fg.R, fg.G, fg.B}
	switch {
	case digit:
		rgb = [3]uint8{0xdc, 0xdc, 0xdc}
	case state != mmIconLive:
		prefix = "\033[38;2;"
		rgb = [3]uint8{uint8(int(fg.R) * 55 / 100), uint8(int(fg.G) * 55 / 100), uint8(int(fg.B) * 55 / 100)}
	}
	var buf [48]byte
	return string(appendBlockStyle(buf[:0], prefix, rgb,
		[3]uint8{uint8((int(t.R) + int(b.R)) / 2 / 2), uint8((int(t.G) + int(b.G)) / 2 / 2), uint8((int(t.B) + int(b.B)) / 2 / 2)}))
}

// mmBlockObjLabel is the style of an objective cell on the mean pixel colour: the letter is
// bold green while the monster is up, bold yellow while it counts down and grey while its
// time is unknown; a countdown character is yellow.
func mmBlockObjLabel(img *image.RGBA, c objCell) string {
	t, b := img.RGBAAt(c.x, 2*c.y), img.RGBAAt(c.x, 2*c.y+1)
	prefix, rgb := "\033[1;38;2;", [3]uint8{0xff, 0xd6, 0x4a}
	switch {
	case !c.letter:
		prefix = "\033[38;2;"
	case c.state == objUp:
		rgb = [3]uint8{0x4a, 0xe0, 0x6a}
	case c.state != objWaiting:
		prefix, rgb = "\033[38;2;", [3]uint8{0x9a, 0x9a, 0x9a}
	}
	var buf [48]byte
	return string(appendBlockStyle(buf[:0], prefix, rgb,
		[3]uint8{uint8((int(t.R) + int(b.R)) / 2 / 2), uint8((int(t.G) + int(b.G)) / 2 / 2), uint8((int(t.B) + int(b.B)) / 2 / 2)}))
}

// ---------------- kitty protocol ----------------
//
// The panel is a set of placements, not one composed picture. The map is cut into tiles on
// the cell grid, each transmitted once as its own small image (tile i is image id
// kittyMapID+i, placement 1, z=1) over one tiny backing image of the whole map (kittyBackID,
// z=0). One big map image does not survive every path to the screen: an image of a few MB
// is dropped on the way while the small sprites arrive, so no image carries more than
// kittyTileBytes of pixels and the map is never sent at more pixels than Data Dragon has.
//
// Champions are sprites above the map (placement i+1, z=2+i for champion slot i) that sit
// on whole cells only: a placement with a pixel offset is shrunk by some relays, which
// squeezes the circle into an ellipse. Smooth motion comes from pre-rendered phase
// variants instead: a sprite image is one cell larger than the circle in each axis and
// the circle is drawn inside it at one of kittyPhases x kittyPhases sub-cell offsets. A
// champion's variant is its (state, phase y, phase x); each is transmitted the first time
// it is needed (image kittySpriteID + slot*kittyVariants + variant) and kept, so a champion
// costs at most kittyVariants images in all and a tick that moves it costs one a=p of the
// new variant and one a=d,d=i of the old one's placement (the new one is placed first, so
// there is no frame without the sprite).
//
// Countdown digits (the seconds until a dead champion respawns, the seconds since a stale
// one was last seen) are cell-sized glyph images, one per digit under the sprite: each
// glyph (style, character) is transmitted once (image kittyDigitID+glyph) and a changing
// number only re-places the glyphs whose character changed, at z above every sprite.
// Text cells would not do: the map tiles are opaque images above all text.
//
// Image ids are offsets into a block of kittyIDSpan ids that each process picks at random:
// the terminal keeps images after esterm exits (the one-shot demo leaves its picture on the
// screen), and a later run that reused an id would replace that picture's image and steal
// its placements.
//
// Order: a terminal or a relay in between may handle the lowest-numbered image of a burst
// worse than the rest (herdr re-sends images to its host terminal in id order, and a first
// image that goes missing was seen on a remote window), so the backing, whose loss only
// shows as a hairline at tile seams, has the lowest id and the tiles follow.
//
// Every image is transmitted and placed by one a=T command sent right after the cursor
// moved to its cell, so no image waits in a terminal-side cache for a separate placement.

const (
	kittyBackID   = 0  // map backing image, the lowest id (see the order note above)
	kittyMapID    = 1  // first map tile; ids kittyMapID .. kittyMapID+kittyMaxTiles-1
	kittyMaxTiles = 64 // placement id of every map image is 1
	kittySpriteID = 96 // sprite variant v of champion slot i is image kittySpriteID+i*kittyVariants+v
	kittyMaxIcons = 32
	kittyPhases   = 4                             // sub-cell positions per axis
	kittyVariants = 3 * kittyPhases * kittyPhases // states x phase y x phase x

	kittyGlyphChars = 12                  // "0123456789.:"
	kittyGlyphs     = 2 * kittyGlyphChars // dead style, stale style
	kittyMaxDigits  = 3                   // longest countdown text
	kittyDigitID    = kittySpriteID + kittyMaxIcons*kittyVariants
	kittyDigitPID   = kittyMaxIcons + 1 // digit j of champion slot i is placement kittyDigitPID+i*kittyMaxDigits+j
	kittyDigitZ     = 2 + kittyMaxIcons // above every sprite

	kittyIDSpan = 2048 // ids per process; kittyDigitID+kittyGlyphs <= kittyIDSpan
	kittyRing   = 2

	kittyTileBytes = 64 << 10 // raw RGBA bytes one tile image may carry (a decoded 128x128 px)
	kittyMapMaxPx  = 512      // longest side of the transmitted map; Data Dragon's map11 is 512 px
	kittyBackPx    = 32       // longest side of the backing image
)

// randomKittyIDBase returns the first image id of a new block: a random multiple of
// kittyIDSpan in 2^16 .. 2^30, so it is never 0, never one of the small ids earlier versions
// used, and fits the 32-bit id field.
func randomKittyIDBase() int {
	lo, hi := (1<<16)/kittyIDSpan, (1<<30)/kittyIDSpan
	return (lo + rand.Intn(hi-lo)) * kittyIDSpan
}

// kittyNewIDBase picks a process's id block; a variable so tests can pin it.
var kittyNewIDBase = randomKittyIDBase

// id returns the image id for offset off of this process's block.
func (k *kittyOut) id(off int) int { return k.idBase + off }

// mmMapRaster returns the pixel size the map is transmitted at for a w*h px slot: the slot
// size, scaled down to at most kittyMapMaxPx on the longest side. The terminal scales the
// placement up to the slot, which adds nothing to a bilinear enlargement done here.
func mmMapRaster(w, h int) (int, int) {
	m := max(w, h)
	if m <= kittyMapMaxPx {
		return w, h
	}
	return max(1, (w*kittyMapMaxPx+m/2)/m), max(1, (h*kittyMapMaxPx+m/2)/m)
}

// kittyGrid is how the map slot is cut: tiles of tc x tr cells, nx x ny of them (the last
// column and row may be smaller).
type kittyGrid struct{ tc, tr, nx, ny int }

// kittyTileGrid cuts a cols x rows cell slot, shown from a mw x mh px raster, into tiles of
// about kittyTileBytes of pixels each, at most kittyMaxTiles of them.
func kittyTileGrid(cols, rows, mw, mh int) kittyGrid {
	side := math.Sqrt(kittyTileBytes / 4)
	pw, ph := float64(mw)/float64(cols), float64(mh)/float64(rows)
	g := kittyGrid{tc: min(max(int(side/pw), 1), cols), tr: min(max(int(side/ph), 1), rows)}
	for {
		g.nx, g.ny = (cols+g.tc-1)/g.tc, (rows+g.tr-1)/g.tr
		if g.nx*g.ny <= kittyMaxTiles {
			return g
		}
		if (g.tc < cols && float64(g.tc)*pw <= float64(g.tr)*ph) || g.tr == rows {
			g.tc++
		} else {
			g.tr++
		}
	}
}

// rect returns tile i's cell rectangle (col, row, cols, rows) relative to the slot origin
// and the pixel rectangle it covers in the mw x mh raster. Neighbouring tiles share their
// pixel edge, so the raster is covered exactly once.
func (g kittyGrid) rect(i, cols, rows, mw, mh int) (cell image.Rectangle, px image.Rectangle) {
	tx, ty := i%g.nx, i/g.nx
	c0, r0 := tx*g.tc, ty*g.tr
	c1, r1 := min(c0+g.tc, cols), min(r0+g.tr, rows)
	edge := func(c, n, total int) int { return (c*total + n/2) / n }
	return image.Rect(c0, r0, c1, r1), image.Rect(edge(c0, cols, mw), edge(r0, rows, mh), edge(c1, cols, mw), edge(r1, rows, mh))
}

// kittyBacking returns a kittyBackPx-sized, opaque copy of the map raster. It sits under the
// tiles across the whole slot, so the hairline where a terminal's scaling leaves two
// neighbouring tiles not quite touching shows map colour instead of the screen behind it.
func kittyBacking(raster *image.RGBA) *image.RGBA {
	mw, mh := raster.Bounds().Dx(), raster.Bounds().Dy()
	m := max(mw, mh)
	bw, bh := max(1, min(mw, mw*kittyBackPx/m)), max(1, min(mh, mh*kittyBackPx/m))
	out := image.NewRGBA(image.Rect(0, 0, bw, bh))
	for y := 0; y < bh; y++ {
		y0, y1 := y*mh/bh, max((y+1)*mh/bh, y*mh/bh+1)
		for x := 0; x < bw; x++ {
			x0, x1 := x*mw/bw, max((x+1)*mw/bw, x*mw/bw+1)
			var r, g, bl, n int
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					o := raster.PixOffset(sx, sy)
					r, g, bl, n = r+int(raster.Pix[o]), g+int(raster.Pix[o+1]), bl+int(raster.Pix[o+2]), n+1
				}
			}
			o := out.PixOffset(x, y)
			out.Pix[o], out.Pix[o+1], out.Pix[o+2], out.Pix[o+3] = uint8(r/n), uint8(g/n), uint8(bl/n), 255
		}
	}
	return out
}

// kittyChunks appends data as one chunked APC command stream: the first chunk carries
// control, the rest only the continuation flag.
func kittyChunks(b []byte, control string, data []byte) []byte {
	const raw = kittyChunk / 4 * 3 // raw bytes per full base64 chunk
	for first := true; first || len(data) > 0; first = false {
		n := min(len(data), raw)
		more := byte('0')
		if n < len(data) {
			more = '1'
		}
		b = append(b, "\x1b_G"...)
		if first {
			b = append(b, control...)
			b = append(b, ',')
		}
		b = append(b, 'm', '=', more, ';')
		l, e := len(b), base64.StdEncoding.EncodedLen(n)
		b = slices.Grow(b, e)[:l+e]
		base64.StdEncoding.Encode(b[l:], data[:n])
		b = append(b, "\x1b\\"...)
		data = data[n:]
	}
	return b
}

// kittyPlacement appends an a=p command: image id, placement pid, cols x rows cells at the
// cursor cell, layer z; it neither moves the cursor nor asks for replies.
func kittyPlacement(b []byte, id, pid, cols, rows, z int) []byte {
	b = append(b, "\x1b_Ga=p,i="...)
	b = strconv.AppendInt(b, int64(id), 10)
	b = append(b, ",p="...)
	b = strconv.AppendInt(b, int64(pid), 10)
	b = append(b, ",c="...)
	b = strconv.AppendInt(b, int64(cols), 10)
	b = append(b, ",r="...)
	b = strconv.AppendInt(b, int64(rows), 10)
	b = append(b, ",z="...)
	b = strconv.AppendInt(b, int64(z), 10)
	return append(b, ",C=1,q=2\x1b\\"...)
}

// kittyTransmitPlace appends one a=T command that transmits the PNG data as image id and
// displays it as placement pid of cols x rows cells at the cursor cell, layer z, in one
// command stream; it neither moves the cursor nor asks for replies.
func kittyTransmitPlace(b []byte, id, pid, cols, rows, z int, data []byte) []byte {
	ctl := "a=T,f=100,i=" + strconv.Itoa(id) + ",p=" + strconv.Itoa(pid) + ",c=" + strconv.Itoa(cols) +
		",r=" + strconv.Itoa(rows) + ",z=" + strconv.Itoa(z) + ",C=1,q=2"
	return kittyChunks(b, ctl, data)
}

// kittyFree appends a delete of the image with the given id, its data and all placements.
func kittyFree(b []byte, id int) []byte {
	b = append(b, "\x1b_Ga=d,d=I,i="...)
	b = strconv.AppendInt(b, int64(id), 10)
	return append(b, ",q=2\x1b\\"...)
}

// kittyUnplace appends a delete of one placement that keeps the image data.
func kittyUnplace(b []byte, id, pid int) []byte {
	b = append(b, "\x1b_Ga=d,d=i,i="...)
	b = strconv.AppendInt(b, int64(id), 10)
	b = append(b, ",p="...)
	b = strconv.AppendInt(b, int64(pid), 10)
	return append(b, ",q=2\x1b\\"...)
}

// mmPNG is a PNG writer tuned for the kitty path: Sub filter, fastest zlib. It encodes ~3x
// faster than image/png at BestSpeed for ~7% more bytes. Its buffers are reused.
type mmPNG struct {
	zw   *zlib.Writer
	idat bytes.Buffer
	row  []byte
	out  bytes.Buffer
}

// encode returns the PNG of img; valid until the next call. With alpha the pixels are
// written as straight (non-premultiplied) RGBA, otherwise as opaque RGB.
func (p *mmPNG) encode(img *image.RGBA, alpha bool) []byte {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	bpp, colorType := 3, byte(2)
	if alpha {
		bpp, colorType = 4, 6
	}
	p.idat.Reset()
	if p.zw == nil {
		p.zw, _ = zlib.NewWriterLevel(&p.idat, zlib.BestSpeed)
	} else {
		p.zw.Reset(&p.idat)
	}
	if cap(p.row) < 1+bpp*w {
		p.row = make([]byte, 1+bpp*w)
	}
	row := p.row[:1+bpp*w]
	row[0] = 1 // Sub filter
	for y := 0; y < h; y++ {
		src := img.Pix[y*img.Stride : y*img.Stride+4*w]
		if alpha {
			copy(row[1:5], src[:4])
			for i := 4; i < len(src); i++ {
				row[1+i] = src[i] - src[i-4]
			}
		} else {
			row[1], row[2], row[3] = src[0], src[1], src[2]
			for x, q := 1, 4; x < w; x, q = x+1, q+3 {
				o := 4 * x
				row[q], row[q+1], row[q+2] = src[o]-src[o-4], src[o+1]-src[o-3], src[o+2]-src[o-2]
			}
		}
		p.zw.Write(row)
	}
	p.zw.Close()
	var ihdr [13]byte
	binary.BigEndian.PutUint32(ihdr[0:], uint32(w))
	binary.BigEndian.PutUint32(ihdr[4:], uint32(h))
	ihdr[8], ihdr[9] = 8, colorType
	p.out.Reset()
	p.out.WriteString("\x89PNG\r\n\x1a\n")
	p.chunk("IHDR", ihdr[:])
	p.chunk("IDAT", p.idat.Bytes())
	p.chunk("IEND", nil)
	return p.out.Bytes()
}

func (p *mmPNG) chunk(typ string, data []byte) {
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(data)))
	p.out.Write(n[:])
	p.out.WriteString(typ)
	p.out.Write(data)
	crc := crc32.Update(crc32.ChecksumIEEE([]byte(typ)), crc32.IEEETable, data)
	binary.BigEndian.PutUint32(n[:], crc)
	p.out.Write(n[:])
}

// kittySpriteCells is the cell size of the circle of a sprite: d px rounded up to whole
// cells. The sprite image is one cell larger in each axis (see kittySpritePixels).
func kittySpriteCells(d, cellW, cellH int) (cols, rows int) {
	return (d + cellW - 1) / cellW, (d + cellH - 1) / cellH
}

// kittyPhaseOffset is the pixel offset of phase ph (0..kittyPhases-1) inside a cell of
// the given size.
func kittyPhaseOffset(ph, cell int) int {
	return (ph*cell + kittyPhases/2) / kittyPhases
}

// kittySpritePixels returns the sprite image of one phase variant: a transparent
// (cols+1)*cellW x (rows+1)*cellH image, straight (non-premultiplied) RGBA, with the
// circle drawn phx/kittyPhases of a cell right of and phy/kittyPhases of a cell below the
// box corner. The extra cell is what lets the circle sit anywhere inside a cell on a
// placement that itself sits on whole cells and is shown unscaled.
func kittySpritePixels(sp *mmSprite, cols, rows, cellW, cellH, phx, phy int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, (cols+1)*cellW, (rows+1)*cellH))
	ox, oy := kittyPhaseOffset(phx, cellW), kittyPhaseOffset(phy, cellH)
	for y := 0; y < sp.d; y++ {
		o := (oy+y)*img.Stride + 4*ox
		copy(img.Pix[o:o+4*sp.d], sp.pix[4*y*sp.d:4*(y+1)*sp.d])
	}
	return img
}

// kittyPos is where a sprite is placed: the top-left cell of its box in canvas cells and
// the phase variant (sub-cell offset of the circle in quarters of a cell).
type kittyPos struct{ row, col, px, py int }

// kittyAxis splits the pixel px of a circle's leading edge, counted from the slot origin on
// one axis, into the box cell and the phase. px is rounded to the nearest of kittyPhases
// steps per cell, so the circle is at most half a step (cell/8 px) off; a position just
// short of the next cell wraps to phase 0 of that cell, and a circle centred on a cell
// boundary lands on the step either side of it. The box is box cells wide inside a slot of
// slot cells, so the last box cell and phase are clamped: a circle at the far edge ends up
// short of it by up to a quarter cell plus what rounding its diameter up to whole cells left.
func kittyAxis(px, cell, box, slot int) (c, ph int) {
	q := (2*kittyPhases*px + cell) / (2 * cell)
	q = min(max(q, 0), max(slot-box, 0)*kittyPhases+kittyPhases-1)
	return q / kittyPhases, q % kittyPhases
}

// kittyPosOf returns the placement of sprite p for circle cells cols x rows in slot s.
func kittyPosOf(p mmPlace, cols, rows int, s *mmSlot) kittyPos {
	c, px := kittyAxis(p.X, s.CellW, cols+1, s.Cols)
	r, py := kittyAxis(p.Y, s.CellH, rows+1, s.Rows)
	return kittyPos{s.Row + r, s.Col + c, px, py}
}

// kittyVariant is the variant index (0..kittyVariants-1) of a state at a position.
func kittyVariant(state mmIconState, pos kittyPos) int {
	return (min(max(int(state), 0), 2)*kittyPhases+pos.py)*kittyPhases + pos.px
}

// kittyVariantID is the id offset of variant v of champion slot i.
func kittyVariantID(i, v int) int { return kittySpriteID + i*kittyVariants + v }

// kittyCountCell returns the cell of the first of n countdown digits for the sprite at pos
// whose circle has diameter d: in the cell row right below the circle's lowest pixel row
// (above its highest when the slot ends there), centred on the circle's cell columns and
// kept inside the slot.
func kittyCountCell(pos kittyPos, d, n int, s *mmSlot) (row, col int) {
	ox, oy := kittyPhaseOffset(pos.px, s.CellW), kittyPhaseOffset(pos.py, s.CellH)
	row = pos.row + (oy+d-1)/s.CellH + 1
	if row >= s.Row+s.Rows {
		row = pos.row + oy/s.CellH - 1
	}
	row = min(max(row, s.Row), s.Row+s.Rows-1)
	left, right := pos.col+ox/s.CellW, pos.col+(ox+d-1)/s.CellW
	col = min(max((left+right)/2-(n-1)/2, s.Col), max(s.Col+s.Cols-n, s.Col))
	return row, col
}

// countGlyphs is a 5x7 bitmap font for the countdown digits, the "." of a due respawn and
// the ":" of an objective countdown, in the notation of badgeGlyphs.
var countGlyphs = map[byte][7]string{
	'0': {".XXX.", "X...X", "X..XX", "X.X.X", "XX..X", "X...X", ".XXX."},
	'1': {"..X..", ".XX..", "..X..", "..X..", "..X..", "..X..", ".XXX."},
	'2': {".XXX.", "X...X", "....X", "...X.", "..X..", ".X...", "XXXXX"},
	'3': {"XXXXX", "...X.", "..X..", "...X.", "....X", "X...X", ".XXX."},
	'4': {"...X.", "..XX.", ".X.X.", "X..X.", "XXXXX", "...X.", "...X."},
	'5': {"XXXXX", "X....", "XXXX.", "....X", "....X", "X...X", ".XXX."},
	'6': {"..XX.", ".X...", "X....", "XXXX.", "X...X", "X...X", ".XXX."},
	'7': {"XXXXX", "....X", "...X.", "..X..", ".X...", ".X...", ".X..."},
	'8': {".XXX.", "X...X", "X...X", ".XXX.", "X...X", "X...X", ".XXX."},
	'9': {".XXX.", "X...X", "X...X", ".XXXX", "....X", "...X.", ".XX.."},
	'.': {".....", ".....", ".....", ".....", ".....", ".XXX.", ".XXX."},
	':': {".....", "..X..", "..X..", ".....", "..X..", "..X..", "....."},
}

// kittyGlyphInk is the colour of a glyph style (0 dead, 1 stale) and how strongly the dark
// edge around it is drawn.
var kittyGlyphInk = [2]struct {
	ink  [3]uint8
	halo float64
}{{[3]uint8{0xff, 0xff, 0xff}, 0.85}, {[3]uint8{0xb4, 0xb4, 0xb4}, 0.65}}

// kittyGlyph is the glyph index (0..kittyGlyphs-1) of character ch ('0'-'9', '.' or ':') in
// style 0 (dead; also the objective countdowns) or 1 (stale).
func kittyGlyph(style int, ch byte) int {
	switch ch {
	case '.':
		return style*kittyGlyphChars + 10
	case ':':
		return style*kittyGlyphChars + 11
	}
	return style*kittyGlyphChars + int(ch-'0')
}

// kittyGlyphPixels draws character ch of a style as a transparent cellW x cellH image
// (straight RGBA) that a one-cell placement shows unscaled: the 5x7 font stretched to
// the cell's 1:2 proportions (smoothed by 3x3 sampling) with a dark edge so it reads on
// any map colour.
func kittyGlyphPixels(ch byte, style, cellW, cellH int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, cellW, cellH))
	g, ok := countGlyphs[ch]
	if !ok {
		return img
	}
	gw := max(cellW-2, 1)
	gh := min(max(cellH-2, 1), 2*gw)
	ox, oy := (cellW-gw)/2, (cellH-gh)/2
	const sub = 3
	cov := make([]float64, cellW*cellH)
	for y := 0; y < gh; y++ {
		for x := 0; x < gw; x++ {
			n := 0
			for sy := 0; sy < sub; sy++ {
				for sx := 0; sx < sub; sx++ {
					if g[(y*sub+sy)*7/(gh*sub)][(x*sub+sx)*5/(gw*sub)] == 'X' {
						n++
					}
				}
			}
			cov[(oy+y)*cellW+ox+x] = float64(n) / (sub * sub)
		}
	}
	st := kittyGlyphInk[style]
	for y := 0; y < cellH; y++ {
		for x := 0; x < cellW; x++ {
			c, near := cov[y*cellW+x], 0.0
			for ny := max(y-1, 0); ny <= min(y+1, cellH-1); ny++ {
				for nx := max(x-1, 0); nx <= min(x+1, cellW-1); nx++ {
					near = math.Max(near, cov[ny*cellW+nx])
				}
			}
			a := c + (1-c)*near*st.halo
			if a == 0 {
				continue
			}
			o := img.PixOffset(x, y)
			for i, v := range st.ink {
				img.Pix[o+i] = uint8(float64(v)*c/a + 0.5)
			}
			img.Pix[o+3] = uint8(a*255 + 0.5)
		}
	}
	return img
}

// kittyGeom is what the transmitted images depend on; a change re-creates them all. The
// icon version is not part of it: it usually arrives a moment after the panel opens, and
// the sprites already re-send themselves when their icon changes, so the map stays.
type kittyGeom struct {
	cols, rows, cellW, cellH int
}

// kittyChamp identifies a champion's sprite slot for the whole run.
type kittyChamp struct {
	team  int
	champ string
}

// kittyDigit is the terminal-side state of one countdown digit placement.
type kittyDigit struct {
	glyph    int // glyph shown while placed
	row, col int
	placed   bool
}

// kittySprite is the terminal-side state of one champion's images and placements.
type kittySprite struct {
	icon     image.Image         // icon the transmitted images were built from; nil: plain disc
	label    string              // lane badge the transmitted images carry
	sent     [kittyVariants]bool // variant images in the terminal
	cur      int                 // variant placed, while placed
	placed   bool
	live     bool // visible in the frame being built
	row, col int  // cell of the placement
	digits   [kittyMaxDigits]kittyDigit
}

// kittyOut tracks what the terminal holds so frames emit only what changed. Safe for
// concurrent use: the signal handler deletes the images while the main loop draws.
type kittyOut struct {
	mu        sync.Mutex
	idBase    int  // first image id of this process's block (see kittyNewIDBase)
	shown     bool // anything of ours may be in the terminal
	geom      kittyGeom
	row, col  int // slot origin of the placements
	mapSent   bool
	mapTiles  int         // map tile images in the terminal
	mapSrc    image.Image // map the transmitted tiles were scaled from; nil: dark fill
	mapPlaced bool
	sprites   []kittySprite
	objs      [kittyObjSlots]kittyObj
	glyphSent [kittyGlyphs]bool // countdown glyph images in the terminal
	ids       map[kittyChamp]int
	scratch   []mmPlace
	buf       []byte
	png       mmPNG
}

// Frame returns the escape output to append after a frame's cell diff. full is true when
// the screen was cleared and repainted (which also removes the terminal's images). A nil
// slot removes everything shown. It never blocks on the network: the map and icons are
// transmitted once their background loads finish.
func (k *kittyOut) Frame(s *mmSlot, full bool) string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.frame(s, full, func(b []byte, row, col int) []byte {
		b = append(b, "\x1b["...)
		b = strconv.AppendInt(b, int64(row+1), 10)
		b = append(b, ';')
		b = strconv.AppendInt(b, int64(col+1), 10)
		return append(b, 'H')
	})
}

// frame builds one frame. cup appends the cursor movement to the 0-based cell (row, col);
// it runs after the cursor was saved with ESC 7, and the frame ends with ESC 8.
func (k *kittyOut) frame(s *mmSlot, full bool, cup func(b []byte, row, col int) []byte) string {
	if s == nil {
		return k.deleteLocked()
	}
	w, h, d := mmKittyGeometry(s)
	if w <= 0 || h <= 0 {
		return k.deleteLocked()
	}
	b := k.buf[:0]
	if k.idBase == 0 {
		k.idBase = kittyNewIDBase()
	}
	geom := kittyGeom{s.Cols, s.Rows, s.CellW, s.CellH}
	if full || geom != k.geom {
		b = k.freeAll(b)
	}
	replace := !k.shown || k.row != s.Row || k.col != s.Col // every placement moves
	k.shown, k.geom, k.row, k.col = true, geom, s.Row, s.Col
	saved := false
	goTo := func(row, col int) {
		if !saved {
			b, saved = append(b, "\x1b7"...), true
		}
		b = cup(b, row, col)
	}

	src := mmMapImg()
	mw, mh := mmMapRaster(w, h)
	grid := kittyTileGrid(s.Cols, s.Rows, mw, mh)
	if !k.mapSent || k.mapSrc != src {
		var bg *image.RGBA
		if src != nil {
			bg = mmScaledMap(src, mw, mh)
		}
		raster := mmBackdrop(mw, mh, bg)
		// Every image is transmitted and placed by one a=T command right after the cursor
		// moved to its cell, so none waits in a terminal-side cache for its placement.
		goTo(s.Row, s.Col)
		b = kittyTransmitPlace(b, k.id(kittyBackID), 1, s.Cols, s.Rows, 0, k.png.encode(kittyBacking(raster), false))
		for i := 0; i < grid.nx*grid.ny; i++ {
			cell, px := grid.rect(i, s.Cols, s.Rows, mw, mh)
			goTo(s.Row+cell.Min.Y, s.Col+cell.Min.X)
			b = kittyTransmitPlace(b, k.id(kittyMapID+i), 1, cell.Dx(), cell.Dy(), 1, k.png.encode(raster.SubImage(px).(*image.RGBA), false))
		}
		k.mapSent, k.mapTiles, k.mapSrc, k.mapPlaced = true, grid.nx*grid.ny, src, true
	} else if !k.mapPlaced || replace {
		goTo(s.Row, s.Col)
		b = kittyPlacement(b, k.id(kittyBackID), 1, s.Cols, s.Rows, 0)
		for i := 0; i < k.mapTiles; i++ {
			cell, _ := grid.rect(i, s.Cols, s.Rows, mw, mh)
			goTo(s.Row+cell.Min.Y, s.Col+cell.Min.X)
			b = kittyPlacement(b, k.id(kittyMapID+i), 1, cell.Dx(), cell.Dy(), 1)
		}
		k.mapPlaced = true
	}

	for i := range k.sprites {
		k.sprites[i].live = false
	}
	k.scratch = mmPlacements(k.scratch, w, h, d, s.Icons)
	cols, rows := kittySpriteCells(d, s.CellW, s.CellH)
	bc, br := cols+1, rows+1 // the sprite box: one cell more than the circle in each axis
	for _, p := range k.scratch {
		i := k.slot(kittyChamp{p.Team, p.Champ})
		if i < 0 {
			continue
		}
		sp := &k.sprites[i]
		sp.live = true
		icon := mmIconImg(s.Ver, p.Champ)
		if sp.icon != icon || sp.label != p.Label {
			b = k.freeSprite(b, i) // every variant carries the old portrait or badge
			sp.icon, sp.label = icon, p.Label
		}
		pos := kittyPosOf(p, cols, rows, s)
		v := kittyVariant(p.State, pos)
		if !sp.placed || replace || sp.cur != v || sp.row != pos.row || sp.col != pos.col {
			id := k.id(kittyVariantID(i, v))
			goTo(pos.row, pos.col)
			if sp.sent[v] {
				b = kittyPlacement(b, id, i+1, bc, br, 2+i)
			} else {
				img := kittySpritePixels(mmSpriteFor(icon, p.Team, d, kittyRing, p.Label, p.State), cols, rows, s.CellW, s.CellH, pos.px, pos.py)
				b = kittyTransmitPlace(b, id, i+1, bc, br, 2+i, k.png.encode(img, true))
				sp.sent[v] = true
			}
			if sp.placed && sp.cur != v { // the new variant is up: only now drop the old one
				b = kittyUnplace(b, k.id(kittyVariantID(i, sp.cur)), i+1)
			}
			sp.placed, sp.cur, sp.row, sp.col = true, v, pos.row, pos.col
		}
		text := mmCountText(p.State, p.Secs)
		style := 0
		if p.State == mmIconStale {
			style = 1
		}
		row, col := kittyCountCell(pos, d, len(text), s)
		k.digitsFrame(&b, sp.digits[:], kittyDigitPID+i*kittyMaxDigits, text, style, row, col, s, replace, goTo)
	}
	k.objFrame(&b, s, w, h, d, replace, goTo)
	for i := range k.sprites { // hidden: drop the placements, keep the data
		sp := &k.sprites[i]
		if sp.live {
			continue
		}
		if sp.placed {
			b = kittyUnplace(b, k.id(kittyVariantID(i, sp.cur)), i+1)
			sp.placed = false
		}
		b = k.hideDigits(b, sp.digits[:], kittyDigitPID+i*kittyMaxDigits)
	}
	if saved {
		b = append(b, "\x1b8"...)
	}
	k.buf = b
	return string(b)
}

// digitsFrame appends to *b what brings the countdown glyph placements dg (placement ids pid0 + j) to show
// text, drawn in glyph style from the cell (row, col) on: a glyph is transmitted the first
// time it is shown, and a placement is only touched when its glyph or cell changed.
func (k *kittyOut) digitsFrame(b *[]byte, dg []kittyDigit, pid0 int, text string, style, row, col int, s *mmSlot, replace bool, goTo func(row, col int)) {
	for j := range dg {
		d, pid := &dg[j], pid0+j
		if j >= len(text) {
			if d.placed {
				*b = kittyUnplace(*b, k.id(kittyDigitID+d.glyph), pid)
				d.placed = false
			}
			continue
		}
		g := kittyGlyph(style, text[j])
		if d.placed && !replace && d.glyph == g && d.row == row && d.col == col+j {
			continue
		}
		goTo(row, col+j)
		if k.glyphSent[g] {
			*b = kittyPlacement(*b, k.id(kittyDigitID+g), pid, 1, 1, kittyDigitZ)
		} else {
			*b = kittyTransmitPlace(*b, k.id(kittyDigitID+g), pid, 1, 1, kittyDigitZ, k.png.encode(kittyGlyphPixels(text[j], style, s.CellW, s.CellH), true))
			k.glyphSent[g] = true
		}
		if d.placed && d.glyph != g {
			*b = kittyUnplace(*b, k.id(kittyDigitID+d.glyph), pid)
		}
		*d = kittyDigit{g, row, col + j, true}
	}
}

// hideDigits drops the placements of the glyphs dg (placement ids pid0 + j), keeping the
// images.
func (k *kittyOut) hideDigits(b []byte, dg []kittyDigit, pid0 int) []byte {
	for j := range dg {
		if d := &dg[j]; d.placed {
			b = kittyUnplace(b, k.id(kittyDigitID+d.glyph), pid0+j)
			d.placed = false
		}
	}
	return b
}

// slot returns the sprite index of a champion, allocating one on first sight; -1 when all
// kittyMaxIcons are taken. Indices never change, so image, placement and layer stay stable.
func (k *kittyOut) slot(c kittyChamp) int {
	if i, ok := k.ids[c]; ok {
		return i
	}
	if len(k.sprites) >= kittyMaxIcons {
		return -1
	}
	if k.ids == nil {
		k.ids = map[kittyChamp]int{}
	}
	k.ids[c] = len(k.sprites)
	k.sprites = append(k.sprites, kittySprite{})
	return len(k.sprites) - 1
}

// freeSprite appends deletes for every variant image of champion slot i and forgets them.
func (k *kittyOut) freeSprite(b []byte, i int) []byte {
	sp := &k.sprites[i]
	for v, sent := range sp.sent {
		if sent {
			b = kittyFree(b, k.id(kittyVariantID(i, v)))
		}
	}
	sp.sent, sp.placed = [kittyVariants]bool{}, false
	return b
}

// freeAll appends deletes for every image sent and forgets all terminal-side state.
func (k *kittyOut) freeAll(b []byte) []byte {
	if k.mapSent {
		b = kittyFree(b, k.id(kittyBackID))
	}
	for i := 0; i < k.mapTiles; i++ {
		b = kittyFree(b, k.id(kittyMapID+i))
	}
	for i := range k.sprites {
		b = k.freeSprite(b, i)
		k.sprites[i] = kittySprite{}
	}
	for i := range k.objs {
		b = k.freeObj(b, i)
		k.objs[i] = kittyObj{}
	}
	for g, sent := range k.glyphSent {
		if sent {
			b = kittyFree(b, k.id(kittyDigitID+g))
		}
	}
	k.glyphSent = [kittyGlyphs]bool{}
	k.mapSent, k.mapTiles, k.mapSrc, k.mapPlaced, k.shown = false, 0, nil, false, false
	return b
}

// Delete removes everything shown.
func (k *kittyOut) Delete() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.deleteLocked()
}

func (k *kittyOut) deleteLocked() string {
	if !k.shown {
		return ""
	}
	return string(k.freeAll(nil))
}

// kittyOneShot places the panel for a canvas of total rows that was just printed with a
// trailing newline: each placement moves up to its cell from the saved cursor below the
// output, and the cursor ends there.
func kittyOneShot(s *mmSlot, total int) string {
	var k kittyOut
	return k.frame(s, true, func(b []byte, row, col int) []byte {
		b = append(b, "\x1b8\x1b["...)
		b = strconv.AppendInt(b, int64(total-row), 10)
		b = append(b, "A\x1b["...)
		b = strconv.AppendInt(b, int64(col+1), 10)
		return append(b, 'G')
	})
}

// mmPreloadAssets waits up to timeout for the map image and the portrait of every icon, for
// one-shot output that cannot redraw once the background downloads finish. Every load starts
// at once, so the wait is the slowest one, not their sum; later runs hit the disk cache.
func mmPreloadAssets(timeout time.Duration, ver string, icons []mmIcon) {
	for end := time.Now().Add(timeout); ; time.Sleep(20 * time.Millisecond) {
		ready := mmMapImg() != nil
		for _, ic := range icons {
			if ver != "" && mmIconImg(ver, ic.Champ) == nil {
				ready = false
			}
		}
		if ready || !time.Now().Before(end) {
			return
		}
	}
}
