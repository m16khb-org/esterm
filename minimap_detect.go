package main

import (
	"image"
	"math"
	"sort"
	"sync"
)

// Champion detection on broadcast minimap frames: masked, zero-mean normalized
// cross-correlation (NCC) of grayscale champion icons, with a per-champion tracker.

const (
	mmTplSize      = 24   // default template side in px for mmFrameSize frames (Worlds)
	mmTrimPenalty  = 0.05 // discount on a three-quadrant match score (see scanTrim)
	mmVarFloor     = 144  // per-pixel luminance variance below which a window is considered flat
	mmSureScore    = 0.80 // matches scoring at least this need no ring check (see plausible)
	mmSureColour   = 0.30 // ...but score + mmColourWeight*(uv - mmSatWeight*sat) must reach this
	mmRingLean     = 4    // weaker matches need a ring leaning toward the team colour (ringLean)
	mmSureLean     = 3    // ...within the gate of a track at least this much
	mmColourWeight = 0.5  // plausible: weight of the colour similarity against the NCC score
	mmSatWeight    = 8    // plausible: weight of the mean-colour distance against the chroma correlation
	mmColourMin    = 0.70 // plausible: minimum of score + mmColourWeight*(uv - mmSatWeight*sat)
	mmCoarseSlack  = 0.04 // score a coarse (every second pixel) hit may fall short of its neighbourhood's best
	mmExcl         = 0.6  // icon diameters within which an excluded or claimed icon blocks another match
)

type mmSpan struct{ ty, a, b, off int }

type mmQSpan struct{ ty, a, b, off, q int }

// champTpl is a masked, zero-mean grayscale template of one champion icon.
type champTpl struct {
	ref         champRef
	d           int // template side in px
	spans       []mmSpan
	tv          []float32    // zero-mean values in span order
	raw         []float32    // gray values in span order (tv + mean)
	qs          []mmQSpan    // spans split by quadrant
	qn, qt, qtt [4]float32   // per-quadrant pixel count, sum and sum of squares of raw
	n           float32      // masked pixel count
	norm        float32      // L2 norm of tv
	nlead       int          // spans[:nlead] are the lead spans (see orderSpans)
	trgb        [3][]float32 // rgb values in span order (colour checks)
	leadN       float64      // pixels in the lead spans
	leadSum     float64      // sum of tv over the lead spans
	tailNorm    float64      // L2 norm of tv over the remaining spans
}

// newChampTpl builds a template from a champion square icon: area-averaged gray at d px
// (the champion icon diameter in mmFrameSize space, see mmIconD), disc-masked (radius
// 0.72*d/2) so the portrait ring and background are ignored.
func newChampTpl(ref champRef, icon image.Image, d int) champTpl {
	b := icon.Bounds()
	sw, sh := b.Dx(), b.Dy()
	v := make([]float32, d*d)
	var vc [3][]float32
	for c := range vc {
		vc[c] = make([]float32, d*d)
	}
	for y := 0; y < d; y++ {
		y0, y1 := y*sh/d, (y+1)*sh/d
		if y1 == y0 {
			y1 = y0 + 1
		}
		for x := 0; x < d; x++ {
			x0, x1 := x*sw/d, (x+1)*sw/d
			if x1 == x0 {
				x1 = x0 + 1
			}
			var sum, sr, sg, sb float64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					r, g, bl, _ := icon.At(b.Min.X+sx, b.Min.Y+sy).RGBA()
					sum += 0.299*float64(r>>8) + 0.587*float64(g>>8) + 0.114*float64(bl>>8)
					sr += float64(r >> 8)
					sg += float64(g >> 8)
					sb += float64(bl >> 8)
				}
			}
			cnt := float64((y1 - y0) * (x1 - x0))
			v[y*d+x] = float32(sum / cnt)
			vc[0][y*d+x], vc[1][y*d+x], vc[2][y*d+x] = float32(sr/cnt), float32(sg/cnt), float32(sb/cnt)
		}
	}
	return newChampTplPlanes(ref, d, v, vc)
}

// newChampTplPlanes builds a template from d×d gray values v and rgb planes vc.
func newChampTplPlanes(ref champRef, d int, v []float32, vc [3][]float32) champTpl {
	t := champTpl{ref: ref, d: d}
	rad := 0.72 * float64(d) / 2
	c := float64(d-1) / 2
	inside := func(x, y int) bool { return math.Hypot(float64(x)-c, float64(y)-c) <= rad }
	var sum float32
	for y := 0; y < d; y++ {
		a, e := -1, -1
		for x := 0; x < d; x++ {
			if inside(x, y) {
				if a < 0 {
					a = x
				}
				e = x + 1
				sum += v[y*d+x]
				t.n++
			}
		}
		if a >= 0 {
			t.spans = append(t.spans, mmSpan{y, a, e, 0})
		}
	}
	mean := sum / t.n
	var nn float32
	for i := range t.spans {
		s := &t.spans[i]
		s.off = len(t.tv)
		for x := s.a; x < s.b; x++ {
			z := v[s.ty*d+x] - mean
			t.tv = append(t.tv, z)
			t.raw = append(t.raw, v[s.ty*d+x])
			nn += z * z
		}
	}
	t.norm = float32(math.Sqrt(float64(nn)))
	t.setColour(vc, d)
	mid := d / 2
	for _, sp := range t.spans {
		for q := 0; q < 2; q++ {
			a, b := sp.a, min(sp.b, mid)
			if q == 1 {
				a, b = max(sp.a, mid), sp.b
			}
			if a >= b {
				continue
			}
			qi := q
			if sp.ty >= mid {
				qi += 2
			}
			t.qs = append(t.qs, mmQSpan{sp.ty, a, b, sp.off + a - sp.a, qi})
			for x := a; x < b; x++ {
				r := t.raw[sp.off+x-sp.a]
				t.qn[qi]++
				t.qt[qi] += r
				t.qtt[qi] += r * r
			}
		}
	}
	t.orderSpans()
	return t
}

// setColour stores the template's rgb planes in span order (see colourMatch).
func (t *champTpl) setColour(vc [3][]float32, d int) {
	for _, sp := range t.spans {
		for x := sp.a; x < sp.b; x++ {
			for c := range vc {
				t.trgb[c] = append(t.trgb[c], vc[c][sp.ty*d+x])
			}
		}
	}
}

// colourMatch compares the colour of the frame patch under t at top-left (x, y) with the
// template: uv is the mean correlation of the
// chroma planes (B-Y and R-Y; luminance is matched by the NCC scan) and sat the distance
// between the mean chromaticities (channel shares of the sum). A terrain look-alike of
// the right luminance pattern has the wrong colours.
func (t *champTpl) colourMatch(rgb []byte, x, y int) (uv, sat float32) {
	var n, su, sv, suu, svv, sut, svt, stu, stv, stuu, stvv float64
	var f, g [3]float64
	for _, sp := range t.spans {
		o := 3 * ((y+sp.ty)*mmFrameSize + x + sp.a)
		for i := 0; i < sp.b-sp.a; i++ {
			px := o + 3*i
			r, gg, b := float64(rgb[px]), float64(rgb[px+1]), float64(rgb[px+2])
			j := sp.off + i
			tr, tg, tb := float64(t.trgb[0][j]), float64(t.trgb[1][j]), float64(t.trgb[2][j])
			u, v := b-(0.299*r+0.587*gg+0.114*b), r-(0.299*r+0.587*gg+0.114*b)
			tu, tv := tb-(0.299*tr+0.587*tg+0.114*tb), tr-(0.299*tr+0.587*tg+0.114*tb)
			n++
			su += u
			sv += v
			suu += u * u
			svv += v * v
			sut += u * tu
			svt += v * tv
			stu += tu
			stv += tv
			stuu += tu * tu
			stvv += tv * tv
			f[0] += r
			f[1] += gg
			f[2] += b
			g[0] += tr
			g[1] += tg
			g[2] += tb
		}
	}
	if n == 0 {
		return 0, 1
	}
	var cu, cv float64
	if vf, vt := suu-su*su/n, stuu-stu*stu/n; vf > 1e-6 && vt > 1e-6 {
		cu = (sut - su*stu/n) / math.Sqrt(vf*vt)
	}
	if vf, vt := svv-sv*sv/n, stvv-stv*stv/n; vf > 1e-6 && vt > 1e-6 {
		cv = (svt - sv*stv/n) / math.Sqrt(vf*vt)
	}
	a := chromaticity(f[0]/n, f[1]/n, f[2]/n)
	b := chromaticity(g[0]/n, g[1]/n, g[2]/n)
	dr, dg, db := float64(a[0]-b[0]), float64(a[1]-b[1]), float64(a[2]-b[2])
	return float32((cu + cv) / 2), float32(math.Sqrt(dr*dr + dg*dg + db*db))
}

// chromaticity is the colour (r, g, b) as shares of the channel sum.
func chromaticity(r, g, b float64) [3]float32 {
	tot := r + g + b + 1
	return [3]float32{float32(r / tot), float32(g / tot), float32(b / tot)}
}

// mmLeadEnergy is the share of a template's energy that its lead spans carry.
const mmLeadEnergy = 0.5

// orderSpans puts the highest-energy spans first and records the split scan prunes with:
// the first nlead spans hold at least mmLeadEnergy of the template's squared norm.
func (t *champTpl) orderSpans() {
	type spanE struct {
		sp mmSpan
		e  float64
	}
	all := make([]spanE, len(t.spans))
	var total float64
	for i, sp := range t.spans {
		for _, z := range t.tv[sp.off : sp.off+sp.b-sp.a] {
			all[i].e += float64(z * z)
		}
		all[i].sp = sp
		total += all[i].e
	}
	sort.SliceStable(all, func(a, b int) bool { return all[a].e > all[b].e })
	var acc float64
	for i, se := range all {
		t.spans[i] = se.sp
		if acc < mmLeadEnergy*total {
			acc += se.e
			t.nlead = i + 1
			t.leadN += float64(se.sp.b - se.sp.a)
			for _, z := range t.tv[se.sp.off : se.sp.off+se.sp.b-se.sp.a] {
				t.leadSum += float64(z)
			}
		}
	}
	t.tailNorm = math.Sqrt(math.Max(total-acc, 0))
}

type mmHit struct {
	x, y  int // template top-left
	score float32
}

var mmNoHit = mmHit{-1, -1, -2}

// mmVerify vets a candidate match (top-left x, y, NCC score s) found by a scan; the scan
// only keeps candidates it accepts.
type mmVerify func(x, y int, s float32) bool

type mmWin struct{ x0, x1, y0, y1 int } // inclusive top-left search range

// mmFrameGray is a gray frame with row prefix sums of the value and its square, so a
// window's sum and sum of squares cost one subtraction per template row.
type mmFrameGray struct {
	g       []float32
	ps, pss []float64
}

const mmPrefixStride = mmFrameSize + 1

// set converts an mmFrameSize² rgb24 frame, reusing the buffers.
func (m *mmFrameGray) set(rgb []byte) {
	if m.g == nil {
		m.g = make([]float32, mmFrameSize*mmFrameSize)
		m.ps = make([]float64, mmPrefixStride*mmFrameSize)
		m.pss = make([]float64, mmPrefixStride*mmFrameSize)
	}
	for y := 0; y < mmFrameSize; y++ {
		var a, b float64
		src := rgb[3*y*mmFrameSize : 3*(y+1)*mmFrameSize]
		row := m.g[y*mmFrameSize : (y+1)*mmFrameSize]
		ps := m.ps[y*mmPrefixStride : (y+1)*mmPrefixStride]
		pss := m.pss[y*mmPrefixStride : (y+1)*mmPrefixStride]
		for x := range row {
			p := src[3*x : 3*x+3 : 3*x+3]
			f := 0.299*float32(p[0]) + 0.587*float32(p[1]) + 0.114*float32(p[2])
			row[x] = f
			v := float64(f)
			a += v
			b += v * v
			ps[x+1], pss[x+1] = a, b
		}
	}
}

// scan returns the best NCC match of t over the window that scores at least floor (mmNoHit
// when none does), skipping positions whose icon centre lies within d*0.6 of an excluded
// centre. The lead spans are correlated first; a window whose score cannot reach the best
// so far (or floor), by the Cauchy-Schwarz bound on the remaining spans, is dropped
// without the rest of its dot product.
func (t *champTpl) scan(m *mmFrameGray, w mmWin, excl []mmHit, floor float32, ok mmVerify) mmHit {
	return t.scanStep(m, w, excl, floor, ok, 1)
}

// scanStep is scan over every step-th position of the window.
func (t *champTpl) scanStep(m *mmFrameGray, w mmWin, excl []mmHit, floor float32, ok mmVerify, step int) mmHit {
	best := mmNoHit
	ex := float64(t.d) * mmExcl
	n := float64(t.n)
	lead, rest := t.spans[:t.nlead], t.spans[t.nlead:]
	for y := w.y0; y <= w.y1; y += step {
		for x := w.x0; x <= w.x1; x += step {
			if len(excl) > 0 && nearAny(x, y, excl, ex) {
				continue
			}
			var sl, ssl float64
			for i := range lead {
				sp := &lead[i]
				r := (y+sp.ty)*mmPrefixStride + x
				sl += m.ps[r+sp.b] - m.ps[r+sp.a]
				ssl += m.pss[r+sp.b] - m.pss[r+sp.a]
			}
			s, ss := sl, ssl
			for i := range rest {
				sp := &rest[i]
				r := (y+sp.ty)*mmPrefixStride + x
				s += m.ps[r+sp.b] - m.ps[r+sp.a]
				ss += m.pss[r+sp.b] - m.pss[r+sp.a]
			}
			v := ss - s*s/n
			if float32(v) < t.n*mmVarFloor {
				continue
			}
			den := math.Sqrt(v) * float64(t.norm)
			mean := s / n
			dl := float64(dotSpans(m.g, y, x, t.tv, lead))
			tail := math.Sqrt(math.Max(ss-ssl-2*mean*(s-sl)+(n-t.leadN)*mean*mean, 0)) * t.tailNorm
			if (dl-mean*t.leadSum+tail)/den < float64(max(best.score, floor))-1e-4 {
				continue
			}
			sc := float32((dl + float64(dotSpans(m.g, y, x, t.tv, rest))) / den)
			if sc > best.score && sc >= floor && (ok == nil || ok(x, y, sc)) {
				best = mmHit{x, y, sc}
			}
		}
	}
	return best
}

// scanPeaks returns up to n matches of t over the window, best first, that score at least
// floor and lie at least d*mmExcl apart: every second position is scored (a coarse hit is
// within mmCoarseSlack of the best score near it), the best distinct ones are refined over
// the pixels around them. Windows whose score cannot reach the floor, by the
// Cauchy-Schwarz bound on the spans after the lead ones, are dropped without their full
// dot product.
func (t *champTpl) scanPeaks(m *mmFrameGray, w mmWin, n int, floor float32) []mmHit {
	cut := float64(floor - mmCoarseSlack)
	nn := float64(t.n)
	lead, rest := t.spans[:t.nlead], t.spans[t.nlead:]
	var cand []mmHit
	for y := w.y0; y <= w.y1; y += 2 {
		for x := w.x0; x <= w.x1; x += 2 {
			var sl, ssl float64
			for i := range lead {
				sp := &lead[i]
				r := (y+sp.ty)*mmPrefixStride + x
				sl += m.ps[r+sp.b] - m.ps[r+sp.a]
				ssl += m.pss[r+sp.b] - m.pss[r+sp.a]
			}
			s, ss := sl, ssl
			for i := range rest {
				sp := &rest[i]
				r := (y+sp.ty)*mmPrefixStride + x
				s += m.ps[r+sp.b] - m.ps[r+sp.a]
				ss += m.pss[r+sp.b] - m.pss[r+sp.a]
			}
			v := ss - s*s/nn
			if float32(v) < t.n*mmVarFloor {
				continue
			}
			den := math.Sqrt(v) * float64(t.norm)
			mean := s / nn
			dl := float64(dotSpans(m.g, y, x, t.tv, lead))
			tail := math.Sqrt(math.Max(ss-ssl-2*mean*(s-sl)+(nn-t.leadN)*mean*mean, 0)) * t.tailNorm
			if (dl-mean*t.leadSum+tail)/den < cut-1e-4 {
				continue
			}
			if sc := float32((dl + float64(dotSpans(m.g, y, x, t.tv, rest))) / den); float64(sc) >= cut {
				cand = append(cand, mmHit{x, y, sc})
			}
		}
	}
	sort.Slice(cand, func(a, b int) bool { return cand[a].score > cand[b].score })
	ex := float64(t.d) * mmExcl
	var out []mmHit
	for _, c := range cand {
		if len(out) == n {
			break
		}
		if nearAny(c.x, c.y, out, ex) {
			continue
		}
		h := t.scan(m, mmWin{max(c.x-1, w.x0), min(c.x+1, w.x1), max(c.y-1, w.y0), min(c.y+1, w.y1)}, nil, floor, nil)
		if h.score >= floor && !nearAny(h.x, h.y, out, ex) {
			out = append(out, h)
		}
	}
	return out
}

// scanMasked is scan for an icon partly covered by other icons: the NCC over the template
// pixels that mask (an mmFrameSize² map of the pixels other icons cover) leaves free, at
// least minValid of them. It returns the best match and the share of the template it was
// scored on.
func (t *champTpl) scanMasked(m *mmFrameGray, w mmWin, mask []bool, minValid float32) (mmHit, float32) {
	best, bestShare := mmNoHit, float32(0)
	for y := w.y0; y <= w.y1; y++ {
		for x := w.x0; x <= w.x1; x++ {
			var n, sx, sxx, st, stt, sxt float64
			for i := range t.spans {
				sp := &t.spans[i]
				o := (y+sp.ty)*mmFrameSize + x + sp.a
				for j := 0; j < sp.b-sp.a; j++ {
					if mask[o+j] {
						continue
					}
					v, tv := float64(m.g[o+j]), float64(t.raw[sp.off+j])
					n++
					sx += v
					sxx += v * v
					st += tv
					stt += tv * tv
					sxt += v * tv
				}
			}
			if float32(n) < minValid*t.n {
				continue
			}
			vx, vt := sxx-sx*sx/n, stt-st*st/n
			if vx < n*mmVarFloor || vt <= 0 {
				continue
			}
			if sc := float32((sxt - sx*st/n) / math.Sqrt(vx*vt)); sc > best.score {
				best, bestShare = mmHit{x, y, sc}, float32(n)/t.n
			}
		}
	}
	return best, bestShare
}

// dotSpans correlates the template values tv with the frame at (x, y) over spans.
func dotSpans(g []float32, y, x int, tv []float32, spans []mmSpan) float32 {
	var s0, s1, s2, s3 float32
	for i := range spans {
		sp := &spans[i]
		n := sp.b - sp.a
		o := (y+sp.ty)*mmFrameSize + x + sp.a
		row := g[o : o+n]
		t := tv[sp.off : sp.off+n]
		j := 0
		for ; j+4 <= n; j += 4 {
			s0 += row[j] * t[j]
			s1 += row[j+1] * t[j+1]
			s2 += row[j+2] * t[j+2]
			s3 += row[j+3] * t[j+3]
		}
		for ; j < n; j++ {
			s0 += row[j] * t[j]
		}
	}
	return s0 + s1 + s2 + s3
}

// scanTrim is scan with a trimmed score: besides the full NCC it scores every
// three-quadrant subset of the template (one quadrant treated as covered by an overlay or
// another icon) and returns max(full, best subset - mmTrimPenalty) as the match score.
func (t *champTpl) scanTrim(m *mmFrameGray, w mmWin, excl []mmHit, ok mmVerify) mmHit {
	best := mmNoHit
	ex := float64(t.d) * mmExcl
	var st, stt float64
	for q := 0; q < 4; q++ {
		st += float64(t.qt[q])
		stt += float64(t.qtt[q])
	}
	ncc := func(n, p, pp, tt, ttt, pt float64) float32 {
		vp, vt := pp-p*p/n, ttt-tt*tt/n
		if vp < n*mmVarFloor || vt <= 0 {
			return -2
		}
		return float32((pt - p*tt/n) / math.Sqrt(vp*vt))
	}
	n := float64(t.n)
	for y := w.y0; y <= w.y1; y++ {
		for x := w.x0; x <= w.x1; x++ {
			if len(excl) > 0 && nearAny(x, y, excl, ex) {
				continue
			}
			var sp, spp, spt [4]float64
			for i := range t.qs {
				s := &t.qs[i]
				r := (y+s.ty)*mmPrefixStride + x
				sp[s.q] += m.ps[r+s.b] - m.ps[r+s.a]
				spp[s.q] += m.pss[r+s.b] - m.pss[r+s.a]
				k := s.b - s.a
				o := (y+s.ty)*mmFrameSize + x + s.a
				row := m.g[o : o+k]
				tv := t.raw[s.off : s.off+k]
				var c0, c1 float32
				j := 0
				for ; j+2 <= k; j += 2 {
					c0 += row[j] * tv[j]
					c1 += row[j+1] * tv[j+1]
				}
				for ; j < k; j++ {
					c0 += row[j] * tv[j]
				}
				spt[s.q] += float64(c0 + c1)
			}
			tp := sp[0] + sp[1] + sp[2] + sp[3]
			tpp := spp[0] + spp[1] + spp[2] + spp[3]
			tpt := spt[0] + spt[1] + spt[2] + spt[3]
			sc := ncc(n, tp, tpp, st, stt, tpt)
			for q := 0; q < 4; q++ {
				if v := ncc(n-float64(t.qn[q]), tp-sp[q], tpp-spp[q], st-float64(t.qt[q]), stt-float64(t.qtt[q]), tpt-spt[q]) - mmTrimPenalty; v > sc {
					sc = v
				}
			}
			if sc > best.score && (ok == nil || ok(x, y, sc)) {
				best = mmHit{x, y, sc}
			}
		}
	}
	return best
}

func nearAny(x, y int, hits []mmHit, r float64) bool {
	for _, h := range hits {
		if math.Hypot(float64(x-h.x), float64(y-h.y)) < r {
			return true
		}
	}
	return false
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// plausible reports whether the match of tp at (x, y) scoring s is a champion and not
// terrain that merely has the same luminance pattern. A sure match (mmSureScore) needs no
// ring: of the whole-map candidates of champions found elsewhere on the frame (LPL g1),
// 18 of 4,400 that scored this much were false, against some 25,000 of the weaker ones; and
// the ring of a champion icon is faint in some broadcasts (LPL: a thin dark ring that hardly
// leans toward the team colour) or hidden under another icon. It still must carry the
// champion's colours: score + mmColourWeight*(uv - mmSatWeight*sat) >= mmSureColour (see
// colourMatch). A weaker match must have a ring leaning toward the team colour (ringLean)
// and convincing colours (mmColourMin).
func (t *mmTracker) plausible(tp *champTpl, x, y int, s float32) bool {
	if s >= mmSureScore {
		return t.colourScore(tp, x, y, s) >= mmSureColour
	}
	if ringLean(t.rgb, x, y, tp.d, tp.ref.Team) < mmRingLean {
		return false
	}
	return t.colourScore(tp, x, y, s) >= mmColourMin
}

// colourScore is the match score plus the weighted colour agreement of the patch at (x, y)
// with the portrait.
func (t *mmTracker) colourScore(tp *champTpl, x, y int, s float32) float32 {
	uv, sat := tp.colourMatch(t.rgb, x, y)
	return s + mmColourWeight*(uv-mmSatWeight*sat)
}

var mmRingCache sync.Map // d -> []int32 (dx, dy pairs of the ring pixels)

// ringLean is the mean over the ring pixels of the icon at top-left (x, y) of how far the
// team's colour channel (blue for team 100, red for 200) exceeds both other channels
// (0 where it does not). The ring of a champion icon is saturated in its team's colour;
// terrain is olive, teal or grey and never leans like that.
func ringLean(rgb []byte, x, y, d, team int) float64 {
	var pix []int32
	if v, ok := mmRingCache.Load(d); ok {
		pix = v.([]int32)
	} else {
		c := float64(d-1) / 2
		for dy := 0; dy < d; dy++ {
			for dx := 0; dx < d; dx++ {
				r := math.Hypot(float64(dx)-c, float64(dy)-c) / (float64(d) / 2)
				if r >= 0.8 && r <= 1.0 {
					pix = append(pix, int32(dx), int32(dy))
				}
			}
		}
		mmRingCache.Store(d, pix)
	}
	var sum int
	for i := 0; i < len(pix); i += 2 {
		o := 3 * ((y+int(pix[i+1]))*mmFrameSize + x + int(pix[i]))
		r, g, b := int(rgb[o]), int(rgb[o+1]), int(rgb[o+2])
		l := b - max(r, g)
		if team == 200 {
			l = r - max(g, b)
		}
		sum += max(l, 0)
	}
	return float64(sum) / float64(len(pix)/2)
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
