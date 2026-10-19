package main

import (
	"math/rand"
	"testing"
)

// synthPlate draws a nameplate window of a "name" (a sequence of glyph ids): a noisy dark
// background and a bright 6x9 pattern per glyph, centred and moved by shift columns.
func synthPlate(name []int, shift, noise int, rng *rand.Rand) []uint8 {
	p := make([]uint8, mvPlateW*mvPlateH)
	for i := range p {
		p[i] = uint8(40 + rng.Intn(noise+1))
	}
	x0 := (mvPlateW-7*len(name))/2 + shift
	for k, id := range name {
		g := rand.New(rand.NewSource(int64(id)*7919 + 1))
		for r := 0; r < 9; r++ {
			for c := 0; c < 6; c++ {
				if g.Intn(2) == 0 {
					p[(6+r)*mvPlateW+x0+k*7+c] = 230
				}
			}
		}
	}
	return p
}

var (
	plateA = []int{1, 2, 3, 4, 5, 6, 7} // "HLE Zeus"
	plateB = []int{1, 2, 3, 4, 5, 8, 9} // "HLE Zeka": two glyphs apart from A
	plateC = []int{11, 12, 13, 14}
	plateS = []int{21, 22, 23, 24, 25, 26} // a status text
)

func teamChamps(levels map[int]int, hp float64) []mvChamp {
	var cs []mvChamp
	for pid := 1; pid <= 5; pid++ {
		cs = append(cs, mvChamp{PID: pid, Team: 100, Level: levels[pid], HPFrac: hp, Alive: true})
	}
	return cs
}

func plateBar(x float64, lvl int, hp float64, name []int, rng *rand.Rand) mvBar {
	return mvBar{Team: 0, X: x, Y: 400, HPFrac: hp, Level: lvl, LevelC: 1, Conf: 1, Plate: synthPlate(name, rng.Intn(3)-1, 20, rng)}
}

func TestMvPlateOfBounds(t *testing.T) {
	lum := make([]uint8, 400*100)
	for i := range lum {
		lum[i] = 7
	}
	if p := mvPlateOf(lum, 400, 100, 100, 50); len(p) != mvPlateW*mvPlateH || p[0] != 7 {
		t.Fatalf("plate inside the image: %d px, first %d", len(p), p[0])
	}
	if mvPlateOf(lum, 400, 100, 100, 10) != nil {
		t.Error("a plate that starts above the image was cut")
	}
	if mvPlateOf(lum, 400, 100, 600, 50) != nil {
		t.Error("a plate right of the image was cut")
	}
}

func TestMvPlatesSimilarNamesStayApart(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	var b mvPlates
	for i := 0; i < 20; i++ {
		for _, name := range [][]int{plateA, plateB} {
			bar := plateBar(700, 9, 0.6, name, rng)
			b.observe(i*10, 0, bar, 0, 0, b.match(0, bar.Plate), nil)
		}
	}
	if n := len(b.teams[0]); n != 2 {
		t.Fatalf("two names two glyphs apart made %d clusters, want 2", n)
	}
	// the same name again, moved a little and on a noisier background, joins its own
	bar := plateBar(700, 9, 0.6, plateA, rng)
	bar.Plate = synthPlate(plateA, 2, 40, rng)
	m := b.match(0, bar.Plate)
	if m.corr < mvPlateSame {
		t.Errorf("own name correlates %.2f, want >= %.2f", m.corr, mvPlateSame)
	}
	b2 := plateBar(700, 9, 0.6, plateB, rng)
	if m2 := b.match(0, b2.Plate); m2.cluster == m.cluster {
		t.Error("two names joined the same cluster")
	}
}

// Votes tie clusters to champions where the level (or health, or position) differs, and a
// status text that comes with whatever level never earns a champion.
func TestMvPlatesIdentityFromVotes(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	var b mvPlates
	for i := 0; i < 60; i++ {
		// champions 1 and 2 climb together, 2 always one level behind; 3 far ahead
		l1 := 6 + i/10
		levels := map[int]int{1: l1, 2: l1 - 1, 3: l1 + 3, 4: 3, 5: 4}
		cs := teamChamps(levels, 0.6)
		for j := range cs {
			cs[j].HPFrac = 0.2 + 0.15*float64(j) // distinct health
		}
		for _, o := range []struct {
			pid  int
			name []int
		}{{1, plateA}, {2, plateC}} {
			bar := plateBar(700, levels[o.pid], cs[o.pid-1].HPFrac, o.name, rng)
			b.observe(i*mvPlateVoteGap, 0, bar, 0, 0, b.match(0, bar.Plate), cs)
		}
		// status text over champion 3, 4 or 5, whichever
		pid := 3 + i%3
		bar := plateBar(900, levels[pid], cs[pid-1].HPFrac, plateS, rng)
		b.observe(i*mvPlateVoteGap, 0, bar, 0, 0, b.match(0, bar.Plate), cs)
	}
	want := map[int]bool{1: true, 2: true, 0: true}
	if len(b.teams[0]) != 3 {
		t.Fatalf("%d clusters, want 3 (two names, one status)", len(b.teams[0]))
	}
	got := map[int]int{}
	for _, c := range b.teams[0] {
		got[c.identity()]++
		if !want[c.identity()] {
			t.Errorf("cluster identified as champion %d", c.identity())
		}
	}
	if got[1] != 1 || got[2] != 1 || got[0] != 1 {
		t.Errorf("identities %v, want champions 1 and 2 once each and the status text unidentified", got)
	}
}

func TestMvPlateCost(t *testing.T) {
	var b mvPlates
	c := &mvCluster{nv: mvPlateVoteN}
	b.teams[0] = []*mvCluster{c}
	cs := teamChamps(map[int]int{1: 5, 2: 5, 3: 5, 4: 5, 5: 5}, 0.5)
	m := mvPlateMatch{cluster: c, corr: 0.95}
	if b.cost(0, m, cs) != nil {
		t.Error("an unidentified cluster must say nothing")
	}
	c.votes[3] = 100
	got := b.cost(0, m, cs)
	for j, ch := range cs {
		want := mvPlateWeight
		if ch.PID == 3 {
			want = 0
		}
		if got[j] != want {
			t.Errorf("pid %d costs %.2f, want %.2f", ch.PID, got[j], want)
		}
	}
	if b.cost(0, mvPlateMatch{cluster: c, corr: mvPlateRamp0}, cs) != nil || b.cost(0, mvPlateMatch{corr: -1}, cs) != nil {
		t.Error("a poor match or none must say nothing")
	}
	mid := b.cost(0, mvPlateMatch{cluster: c, corr: (mvPlateRamp0 + mvPlateRamp1) / 2}, cs)
	if !(mid[0] > 0 && mid[0] < mvPlateWeight) {
		t.Errorf("a half sure plate costs %.2f", mid[0])
	}
}

// Two champions alike in level, health and place: once the plates are tied to them, a bar
// goes to the champion its plate belongs to, whatever the tracker believes.
func TestMvAssignUsesPlates(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	v := mvView{X0: 0.2, Y0: 0.3, X1: 0.6, Y1: 0.5, OK: true}
	var book mvPlates
	for i := 0; i < 80; i++ { // levels and health tell 1 and 2 apart
		l1 := 9 + i/20
		cs := []mvChamp{
			{PID: 1, Team: 100, Level: l1, HPFrac: 0.9 - 0.004*float64(i), Alive: true},
			{PID: 2, Team: 100, Level: l1 + 2, HPFrac: 0.4, Alive: true},
		}
		bars := []mvBar{
			plateBar(700, cs[0].Level, cs[0].HPFrac, plateA, rng),
			plateBar(1300, cs[1].Level, cs[1].HPFrac, plateC, rng),
		}
		mvAssign(bars, v, cs, i*mvPlateVoteGap, &book)
	}
	// now they share level and health and both were last seen in the middle
	cs := []mvChamp{
		{PID: 1, Team: 100, Level: 12, HPFrac: 0.6, Alive: true, HasPrev: true, PrevX: 0.4, PrevY: 0.42, PrevAge: 1},
		{PID: 2, Team: 100, Level: 12, HPFrac: 0.6, Alive: true, HasPrev: true, PrevX: 0.4, PrevY: 0.42, PrevAge: 1},
	}
	bars := []mvBar{plateBar(700, 12, 0.6, plateC, rng), plateBar(1300, 12, 0.6, plateA, rng)} // left is 2, right is 1
	x0, _, _ := mvToMap(bars[0], v)
	obs := mvAssign(bars, v, cs, 9999, &book)
	if len(obs) != 2 {
		t.Fatalf("%d observations, want 2: %+v", len(obs), obs)
	}
	for _, o := range obs {
		want := 1
		if o.X == x0 {
			want = 2
		}
		if o.PID != want {
			t.Errorf("bar at x=%.3f assigned to %d, want %d", o.X, o.PID, want)
		}
	}
	// without the book the same frame has no basis to choose
	for _, o := range mvAssign(bars, v, cs, 9999, nil) {
		if o.Conf > 0.2 {
			t.Errorf("level, health and place alone claim confidence %.2f for pid %d", o.Conf, o.PID)
		}
	}
}
