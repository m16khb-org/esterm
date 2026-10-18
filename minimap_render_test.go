package main

import (
	"strings"
	"testing"
)

func TestMinimapLinesFullWidth(t *testing.T) {
	for _, w := range []int{40, 60, 80, 120} {
		for _, h := range []int{4, 12, 40} {
			ls := renderMinimap([]mmMarker{{Team: 100, Label: "T", X: 0.5, Y: 0.5}}, "MINIMAP · a very long status string that must be trimmed to fit", w, h, mmDots{})
			if len(ls) > h {
				t.Errorf("t=%d h=%d: %d lines exceed h", w, h, len(ls))
			}
			for i, l := range ls {
				if l.vis() != w {
					t.Errorf("t=%d h=%d line %d: vis %d", w, h, i, l.vis())
				}
			}
		}
	}
}

func TestPlaceMarkersCorners(t *testing.T) {
	p := placeMarkers([]mmMarker{{Team: 100, Label: "T"}, {Team: 200, Label: "B", X: 1, Y: 1}}, 30, 15)
	if p[0].x != 0 || p[0].y != 0 {
		t.Errorf("(0,0) -> %d,%d", p[0].x, p[0].y)
	}
	if p[1].x != 29 || p[1].y != 14 {
		t.Errorf("(1,1) -> %d,%d (label stays in the last cell)", p[1].x, p[1].y)
	}
}

func TestPlaceMarkersCollisions(t *testing.T) {
	ms := []mmMarker{{Team: 100, Label: "T", X: 0.5, Y: 0.5}, {Team: 100, Label: "J", X: 0.5, Y: 0.5}, {Team: 200, Label: "B", X: 0.5, Y: 0.5}, {Team: 100, Label: "M", X: 0.5, Y: 0.5}}
	a := placeMarkers(ms, 30, 15)
	rev := []mmMarker{ms[3], ms[2], ms[1], ms[0]}
	b := placeMarkers(rev, 30, 15)
	seen := map[[2]int]bool{}
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("order-dependent placement: %v vs %v", a[i], b[i])
		}
		c := [2]int{a[i].x, a[i].y}
		if seen[c] {
			t.Errorf("overlap at %v", c)
		}
		seen[c] = true
	}
}

func TestMMLabels(t *testing.T) {
	refs := []champRef{
		{1, 100, "Olaf", "top"}, {2, 100, "Vi", "Jungle"}, {3, 100, "Cassiopeia", "mid"},
		{4, 100, "Ashe", "bottom"}, {5, 100, "Seraphine", "support"}, {6, 200, "Jax", ""},
	}
	got := mmLabels(refs)
	want := map[int]string{1: "T", 2: "J", 3: "M", 4: "B", 5: "S", 6: "J"}
	for pid, w := range want {
		if got[pid] != w {
			t.Errorf("pid %d: got %q want %q", pid, got[pid], w)
		}
	}
	if mmRoleLabel("adc", "Ashe") != "B" || mmRoleLabel("", "") != "?" {
		t.Error("adc must map to B, empty role and champion to ?")
	}
}

func TestPlaceMarkersUseTheCellContainingThePosition(t *testing.T) {
	for _, c := range []struct {
		x, y float64
		cx   int
		cy   int
	}{{0.5, 0.5, 15, 7}, {0.99, 0.99, 29, 14}, {0.3, 0.2, 9, 3}, {-0.2, 2, 0, 14}} {
		p := placeMarkers([]mmMarker{{Team: 100, Label: "T", X: c.x, Y: c.y}}, 30, 15)
		if p[0].x != c.cx || p[0].y != c.cy {
			t.Errorf("(%v,%v) -> %d,%d, want cell %d,%d", c.x, c.y, p[0].x, p[0].y, c.cx, c.cy)
		}
	}
}

func TestPlaceMarkersDeadAndStaleLabels(t *testing.T) {
	ms := []mmMarker{
		{Team: 100, Label: "J", X: 11.0 / 30, Y: 0.5, State: mmIconDead, Secs: 23},
		{Team: 100, Label: "T", X: 10.0 / 30, Y: 0.5, State: mmIconDead, Secs: 7},
		{Team: 200, Label: "M", X: 0.2, Y: 0.1, State: mmIconStale, Secs: 40},
		{Team: 200, Label: "B", X: 0.8, Y: 0.1, State: mmIconDead, Secs: 0},
		{Team: 200, Label: "S", X: 1, Y: 1, State: mmIconDead, Secs: 15},
		{Team: 200, Label: "A", X: 0.5, Y: 0.9},
	}
	got := placeMarkers(ms, 30, 15)
	byLabel := map[string]placed{}
	for _, p := range got {
		byLabel[strings.ToUpper(p.label[:1])] = p
	}
	want := map[string]string{
		"J": "j23", // its countdown fits right of it
		"T": "t",   // its neighbour sits in the next cell: a letter without digits
		"M": "M",   // stale: the letter as it is (dimmed by the renderer), no digits
		"B": "b..", // due
		"S": "s",   // at the edge there is no room for digits
		"A": "A",
	}
	for l, w := range want {
		if byLabel[l].label != w {
			t.Errorf("%s: label %q, want %q", l, byLabel[l].label, w)
		}
	}
	if byLabel["M"].state != mmIconStale || byLabel["J"].state != mmIconDead {
		t.Error("the state travels with the placement")
	}
	seen := map[[2]int]bool{}
	for _, p := range got {
		for i := range []rune(p.label) {
			c := [2]int{p.x + i, p.y}
			if seen[c] || p.x+i >= 30 {
				t.Errorf("%q overlaps another label or leaves the map at %v", p.label, c)
			}
			seen[c] = true
		}
	}
	rev := make([]mmMarker, len(ms))
	for i := range ms {
		rev[len(ms)-1-i] = ms[i]
	}
	again := placeMarkers(rev, 30, 15)
	for i := range got {
		if got[i] != again[i] {
			t.Errorf("order-dependent placement: %v vs %v", got[i], again[i])
		}
	}
}

func TestRenderMinimapBlocksDeadAndStaleMarkers(t *testing.T) {
	offlineMap(t)
	mk := []mmMarker{
		{Team: 100, Label: "T", X: 0.5, Y: 0.5, State: mmIconDead, Secs: 23},
		{Team: 200, Label: "J", X: 0.2, Y: 0.2, State: mmIconStale, Secs: 9},
		{Team: 200, Label: "M", X: 0.8, Y: 0.2},
	}
	ls := renderMinimapBlocks(mk, "s", 60, 20)
	var text strings.Builder
	styles := map[string]string{}
	for _, l := range ls {
		if l.vis() != 60 {
			t.Errorf("line width %d, want 60", l.vis())
		}
		text.WriteString(plain(l))
		for _, s := range l {
			styles[s.text] = s.color
		}
	}
	for _, want := range []string{"t23", "J", "M"} {
		if want == "t23" {
			if !strings.Contains(text.String(), "t23") {
				t.Errorf("dead champion: want the lower-case letter and its seconds in %q", text.String())
			}
			continue
		}
		if !strings.Contains(text.String(), want) {
			t.Errorf("missing %s", want)
		}
	}
	if !strings.HasPrefix(styles["M"], "\033[1;") || strings.HasPrefix(styles["J"], "\033[1;") || strings.HasPrefix(styles["t"], "\033[1;") {
		t.Errorf("a live letter is bold, a stale or dead one is not: M %q J %q t %q", styles["M"], styles["J"], styles["t"])
	}
	digit := styles["23"]
	if digit == "" {
		digit = styles["2"]
	}
	if digit == styles["t"] || !strings.Contains(digit, "220;220;220") {
		t.Errorf("a countdown digit is light grey, not the team colour: %q", digit)
	}
}

func TestRenderMinimapTextDeadAndStaleMarkers(t *testing.T) {
	mk := []mmMarker{
		{Team: 100, Label: "T", X: 0.5, Y: 0.5, State: mmIconDead, Secs: 23},
		{Team: 200, Label: "J", X: 0.2, Y: 0.2, State: mmIconStale, Secs: 9},
		{Team: 200, Label: "M", X: 0.8, Y: 0.2},
		{Team: 200, Label: "B", X: 1, Y: 0.9, State: mmIconDead, Secs: 31}, // no room for digits
	}
	for _, w := range []int{40, 60, 100} {
		var text strings.Builder
		styles := map[string]string{}
		for i, l := range renderMinimap(mk, "s", w, 20, mmDots{}) {
			if l.vis() != w {
				t.Errorf("t=%d line %d width %d", w, i, l.vis())
			}
			text.WriteString(plain(l))
			for _, s := range l {
				styles[s.text] = s.color
			}
		}
		if !strings.Contains(text.String(), "t23") || !strings.Contains(text.String(), "J") || !strings.Contains(text.String(), "b") || strings.Contains(text.String(), "b3") {
			t.Errorf("t=%d: dead and stale labels missing in %q", w, text.String())
		}
		if styles["t"] != cDim+cCyan || styles["J"] != cDim+cRed || styles["M"] != cBold+cRed || styles["23"] != cDim {
			t.Errorf("t=%d styles: t %q J %q M %q 23 %q", w, styles["t"], styles["J"], styles["M"], styles["23"])
		}
	}
}
