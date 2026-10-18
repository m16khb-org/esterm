package main

import (
	"image"
	"math"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
)

// Champion detection on broadcast minimap frames: masked, zero-mean normalized
// cross-correlation (NCC) of grayscale champion icons, with a per-champion tracker.

const (
	mmTplSize      = 24   // default template side in px for mmFrameSize frames (Worlds)
	mmAcquire      = 0.60 // minimum NCC to acquire a champion that was not seen recently
	mmJumpScore    = 0.85 // minimum NCC of a full-scan match that may teleport a champion
	mmJumpMargin   = 0.04 // full-scan match must beat the local match by this much to teleport
	mmFixMargin    = 0.08 // ...or by this much to replace a weak local match within the motion gate
	mmJumpDist     = 40   // px; a full-scan match farther than this away counts as a teleport
	mmStepPx       = 8    // allowed movement per frame index step
	mmRevalPeriod  = 5    // each champion is re-validated by a full scan every mmRevalPeriod frames
	mmTieMargin    = 0.03 // scores closer than this are tie-broken by ring colour
	mmAccept       = 0.60 // minimum NCC to accept a match
	mmRecent       = 20   // frames during which a lost champion is searched near its last position
	mmWeakFrames   = 8    // consecutive sub-mmAcquire matches after which a track is dropped
	mmTrimPenalty  = 0.05 // discount on a three-quadrant match score (see scanTrim)
	mmHoldFrames   = 8    // frames a champion lost beside another icon stays shown at its last position
	mmGraceFrames  = 2    // frames any lost champion stays shown at its last position
	mmHoldDist     = 30   // px between icon centres within which a lost champion counts as covered
	mmVarFloor     = 144  // per-pixel luminance variance below which a window is considered flat
	mmSureScore    = 0.90 // matches scoring at least this need no ring check
	mmRingLean     = 4    // weaker matches need a ring leaning toward the team colour (ringLean)
	mmSureLean     = 3    // ...and sure ones at least this much
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

// scanFull is scan for a window of the whole frame: every second position first (an icon
// correlates over a few pixels, so a coarse hit is within mmCoarseSlack of the best score
// near it), then the pixels around the best of those.
func (t *champTpl) scanFull(m *mmFrameGray, w mmWin, excl []mmHit, floor float32, ok mmVerify) mmHit {
	var okCoarse mmVerify
	if ok != nil {
		okCoarse = func(x, y int, s float32) bool { return ok(x, y, s+mmCoarseSlack) }
	}
	c := t.scanStep(m, w, excl, floor-mmCoarseSlack, okCoarse, 2)
	if c.score < 0 {
		return mmNoHit
	}
	return t.scan(m, mmWin{max(c.x-1, w.x0), min(c.x+1, w.x1), max(c.y-1, w.y0), min(c.y+1, w.y1)}, excl, floor, ok)
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

type mmTrack struct {
	have    bool // visible on the last processed frame
	known   bool // x, y, lastIdx are valid (champion was seen since Reset)
	x, y    int  // template top-left of the last accepted match
	lastIdx int
	weak    int // consecutive accepted matches scoring below mmAcquire
}

// mmTracker follows each champion across consecutive minimap frames.
type mmTracker struct {
	tpls []champTpl
	trk  []mmTrack
	img  mmFrameGray
	rgb  []byte  // the frame Step is processing
	hits []mmHit // Step scratch, one per template
}

func newMMTracker(tpls []champTpl) *mmTracker {
	t := &mmTracker{tpls: tpls, trk: make([]mmTrack, len(tpls)), hits: make([]mmHit, len(tpls))}
	return t
}

// Reset forgets every champion position (call when the minimap disappears).
func (t *mmTracker) Reset() {
	for i := range t.trk {
		t.trk[i] = mmTrack{}
	}
}

// find locates champion k on the current gray frame. It scans locally around the last
// position (limited by the motion gate) while it was seen within mmRecent frames, and
// does a full scan when lost, when the local match is poor, or when due for re-validation.
// Champions not seen recently are only re-acquired by a strong (mmAcquire) full-scan match.
func (t *mmTracker) find(k, idx int, excl []mmHit) mmHit {
	maxXY := mmFrameSize - t.tpls[k].d
	tp, tr := &t.tpls[k], &t.trk[k]
	ok := t.verifier(tp)
	full := mmWin{0, maxXY, 0, maxXY}
	if !tr.known || idx-tr.lastIdx > mmRecent {
		if h := tp.scanFull(&t.img, full, excl, mmAcquire, ok); h.score >= mmAcquire {
			return h
		}
		return mmNoHit
	}
	gate := clampInt(mmStepPx*max(idx-tr.lastIdx, 1), mmStepPx, mmJumpDist)
	loc := tp.scanTrim(&t.img, mmWin{
		clampInt(tr.x-gate, 0, maxXY), clampInt(tr.x+gate, 0, maxXY),
		clampInt(tr.y-gate, 0, maxXY), clampInt(tr.y+gate, 0, maxXY),
	}, excl, ok)
	due := (idx+k)%mmRevalPeriod == 0
	if loc.score >= mmJumpScore && !due {
		return loc
	}
	floor := float32(mmAccept)
	if loc.score >= mmAccept {
		floor = loc.score + mmJumpMargin // a full-scan match closer than this to loc never replaces it
	}
	f := tp.scanFull(&t.img, full, excl, floor, ok)
	far := math.Hypot(float64(f.x-tr.x), float64(f.y-tr.y)) > mmJumpDist
	switch {
	case far && f.score >= mmJumpScore && f.score >= loc.score+mmJumpMargin:
		return f
	case !far && f.score >= mmAccept && f.score >= loc.score+mmFixMargin:
		return f
	case loc.score >= mmAccept:
		return loc
	case !far && f.score >= mmAccept:
		return f
	}
	return mmNoHit
}

// verifier returns the scan check of champion template tp: a candidate must have a ring
// in the champion's team colour and, unless its score alone is sure, portrait colours
// that go with the score (see plausible).
func (t *mmTracker) verifier(tp *champTpl) mmVerify {
	return func(x, y int, s float32) bool { return t.plausible(tp, x, y, s) }
}

// plausible reports whether the match of tp at (x, y) scoring s is a champion and not
// terrain that merely has the same luminance pattern. The ring must lean toward the team
// colour (ringLean). A match that is not sure by its score must also carry the champion's
// colours: score + mmColourWeight*(uv - mmSatWeight*sat) >= mmColourMin (see colourMatch),
// so a weak score needs convincing colours and a strong one hardly any.
func (t *mmTracker) plausible(tp *champTpl, x, y int, s float32) bool {
	lean := ringLean(t.rgb, x, y, tp.d, tp.ref.Team)
	if s >= mmSureScore {
		return lean >= mmSureLean
	}
	if lean < mmRingLean {
		return false
	}
	uv, sat := tp.colourMatch(t.rgb, x, y)
	return s+mmColourWeight*(uv-mmSatWeight*sat) >= mmColourMin
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

// ringTeam guesses the team colour of the icon ring at top-left (x, y): blue-dominant
// annulus -> 100, red-dominant -> 200.
func ringTeam(rgb []byte, x, y, d int) int {
	c := float64(d-1) / 2
	var diff float64
	for dy := 0; dy < d; dy++ {
		for dx := 0; dx < d; dx++ {
			r := math.Hypot(float64(dx)-c, float64(dy)-c) / (float64(d) / 2)
			if r < 0.8 || r > 1.0 {
				continue
			}
			i := 3 * ((y+dy)*mmFrameSize + x + dx)
			diff += float64(rgb[i+2]) - float64(rgb[i])
		}
	}
	if diff > 0 {
		return 100
	}
	return 200
}

// mmBatch is one mmParallel call: its indices are claimed through next by every goroutine
// that works on it.
type mmBatch struct {
	f    func(int)
	n    int32
	next atomic.Int32
	wg   sync.WaitGroup
}

func (b *mmBatch) run() {
	for {
		i := b.next.Add(1) - 1
		if i >= b.n {
			return
		}
		b.f(int(i))
		b.wg.Done()
	}
}

var mmPool struct {
	once    sync.Once
	helpers int
	jobs    chan *mmBatch
}

// mmParallel runs f(0..n-1) on the calling goroutine plus a fixed set of long-lived helper
// workers (none are started per call) and waits for all of them. A helper that is busy
// with another call simply does not join this one.
func mmParallel(n int, f func(int)) {
	mmPool.once.Do(func() {
		mmPool.helpers = min(runtime.GOMAXPROCS(0), 10) - 1
		mmPool.jobs = make(chan *mmBatch, mmPool.helpers)
		for w := 0; w < mmPool.helpers; w++ {
			go func() {
				for b := range mmPool.jobs {
					b.run()
				}
			}()
		}
	})
	b := &mmBatch{f: f, n: int32(n)}
	b.wg.Add(n)
	for i := 1; i < min(n, mmPool.helpers+1); i++ {
		select {
		case mmPool.jobs <- b:
		default:
		}
	}
	b.run()
	b.wg.Wait()
}

// Step processes one frame (mmFrameSize² rgb24) and returns one detection per template,
// in template order.
func (t *mmTracker) Step(idx int, rgb []byte) []mmDet {
	t.img.set(rgb)
	t.rgb = rgb
	n := len(t.tpls)
	hits := t.hits
	mmParallel(n, func(k int) { hits[k] = t.find(k, idx, nil) })

	// Greedy cross-champion suppression: higher score claims a spot; ring colour breaks near ties.
	order := make([]int, 0, n)
	for k, h := range hits {
		if h.score >= mmAccept {
			order = append(order, k)
		}
	}
	sort.SliceStable(order, func(a, b int) bool { return hits[order[a]].score > hits[order[b]].score })
	ex := float64(t.tpls[0].d) * mmExcl
	for i := 0; i+1 < len(order); i++ {
		a, b := order[i], order[i+1]
		ha, hb := hits[a], hits[b]
		if hb.score+mmTieMargin > ha.score && math.Hypot(float64(ha.x-hb.x), float64(ha.y-hb.y)) < ex &&
			ringTeam(rgb, ha.x, ha.y, t.tpls[a].d) != t.tpls[a].ref.Team && ringTeam(rgb, hb.x, hb.y, t.tpls[b].d) == t.tpls[b].ref.Team {
			order[i], order[i+1] = b, a
		}
	}
	var claimed []mmHit
	var losers []int
	for _, k := range order {
		if nearAny(hits[k].x, hits[k].y, claimed, ex) {
			losers = append(losers, k)
			continue
		}
		claimed = append(claimed, hits[k])
	}
	for _, k := range losers {
		hits[k] = t.find(k, idx, claimed)
		if hits[k].score >= mmAccept {
			claimed = append(claimed, hits[k])
		}
	}

	dets := make([]mmDet, n)
	for k, h := range hits {
		tr := &t.trk[k]
		d := mmDet{champRef: t.tpls[k].ref, Score: h.score}
		weak := 0
		if h.score < mmAcquire {
			weak = tr.weak + 1
		}
		switch {
		case h.score < mmAccept:
			tr.have = false
		case weak > mmWeakFrames:
			// a long run of weak matches is a terrain look-alike, not a champion
			*tr = mmTrack{}
		default:
			*tr = mmTrack{have: true, known: true, x: h.x, y: h.y, lastIdx: idx, weak: weak}
			d.Visible = true
			d.X = (float64(h.x) + float64(t.tpls[k].d)/2) / mmFrameSize
			d.Y = (float64(h.y) + float64(t.tpls[k].d)/2) / mmFrameSize
		}
		dets[k] = d
	}

	// Hold: a champion lost for a frame or two (overlays, pings) stays shown at its last
	// position; a longer hold applies when it was last seen under another visible icon.
	found := make([]mmDet, 0, n)
	for _, d := range dets {
		if d.Visible {
			found = append(found, d)
		}
	}
	for k := range dets {
		tr := &t.trk[k]
		if dets[k].Visible || !tr.known || idx-tr.lastIdx > mmHoldFrames {
			continue
		}
		cx, cy := float64(tr.x)+float64(t.tpls[k].d)/2, float64(tr.y)+float64(t.tpls[k].d)/2
		if idx-tr.lastIdx <= mmGraceFrames {
			dets[k].Visible = true
			dets[k].X, dets[k].Y = cx/mmFrameSize, cy/mmFrameSize
			continue
		}
		for _, f := range found {
			if math.Hypot(f.X*mmFrameSize-cx, f.Y*mmFrameSize-cy) <= mmHoldDist {
				dets[k].Visible = true
				dets[k].X, dets[k].Y = cx/mmFrameSize, cy/mmFrameSize
				break
			}
		}
	}
	return dets
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
