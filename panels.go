package main

import (
	"fmt"
	"math"
	"strings"
)

// ---------------- details feed types (item/rune/skill) ----------------

type detailsResp struct {
	Frames []detailsFrame `json:"frames"`
}
type detailsFrame struct {
	RFC460Timestamp string               `json:"rfc460Timestamp"`
	Participants    []detailsParticipant `json:"participants"`
}
type detailsParticipant struct {
	ParticipantID int   `json:"participantId"`
	Items         []int `json:"items"`
	PerkMetadata  struct {
		StyleID    int   `json:"styleId"`
		SubStyleID int   `json:"subStyleId"`
		Perks      []int `json:"perks"`
	} `json:"perkMetadata"`
	Abilities []string `json:"abilities"`
}

type detMap map[int]detailsParticipant

func itemName(id int) string {
	if n, ok := bundledItems[id]; ok && n != "" {
		return n
	}
	return fmt.Sprintf("#%d", id)
}

func runeName(id int) string {
	if n, ok := bundledRunes[id]; ok && n != "" {
		return n
	}
	return fmt.Sprintf("#%d", id)
}

func detFrom(dr detailsResp) detMap {
	m := detMap{}
	if len(dr.Frames) == 0 {
		return m
	}
	for _, p := range dr.Frames[len(dr.Frames)-1].Participants {
		m[p.ParticipantID] = p
	}
	return m
}

// ---------------- toggle state ----------------

type toggles struct {
	view  int  // b cycles: 0 stats, 1 items, 2 runes
	spark bool // s: gold-diff sparkline
	omap  bool // o: objective map panel
	// replay is the playback state of a replayed game (nil: live): the header says REPLAY, the footer shows the position.
	replay *replayState
	embed  bool // drawn inside the app's pane: the app's hint bar lists the keys
}

// boardView is what the feed says at the shown instant besides the frame: who is dead
// and until when, and the objective timers.
type boardView struct {
	Lives champLives
	Obj   objView
}

// ---------------- gold-diff series ----------------

type goldPt struct {
	gameMin int
	diff    int // blueGold - redGold
}

// ---------------- skill order ----------------

// skillPriority returns the basic abilities (R excluded) in the order they were first
// levelled, joined by ">".
func skillPriority(ab []string) string {
	seen := []string{}
	for _, a := range ab {
		if a == "R" {
			continue
		}
		dup := false
		for _, s := range seen {
			if s == a {
				dup = true
				break
			}
		}
		if !dup {
			seen = append(seen, a)
		}
	}
	return strings.Join(seen, ">")
}

// ---------------- build view rows ----------------

var runeTrees = map[int]string{
	8000: "Precision", 8100: "Domination", 8200: "Sorcery", 8300: "Inspiration", 8400: "Resolve",
}

// runePage formats keystone + 3 primary runes + secondary tree + 2 secondary runes (full names).
func runePage(dp detailsParticipant) string {
	pm := dp.PerkMetadata
	if len(pm.Perks) == 0 {
		return "#?"
	}
	end := 4
	if end > len(pm.Perks) {
		end = len(pm.Perks)
	}
	var prim []string
	for _, id := range pm.Perks[:end] {
		prim = append(prim, runeName(id))
	}
	out := strings.Join(prim, " · ")
	if len(pm.Perks) >= 6 {
		var sec []string
		for _, id := range pm.Perks[4:6] {
			sec = append(sec, runeName(id))
		}
		tree := runeTrees[pm.SubStyleID]
		if tree == "" {
			tree = "?"
		}
		out += "  /  " + tree + ": " + strings.Join(sec, " · ")
	}
	return out
}

// itemsCols splits budget for the items and runes views: name + champion + the detail column
// + skill. The skill column (Q>W>E) never shrinks; the champion column and then the name
// give way first, and the detail column is always measured from the budget so no row can
// grow past it.
func itemsCols(budget int) (nameW, champW, skillW, itemsW int) {
	skillW = 6
	nameW = min(12, max(8, budget/4))
	champW = 8
	itemsW = budget - nameW - champW - skillW - 3
	if itemsW < 8 {
		champW = 0
		itemsW = budget - nameW - skillW - 3
	}
	if itemsW < 8 {
		nameW, itemsW = 6, max(0, budget-6-skillW-3)
	}
	return
}
func itemsPlayerRow(name, champ string, dp detailsParticipant, tc string, budget int) line {
	nameW, champW, skillW, itemsW := itemsCols(budget)
	var its []string
	for _, id := range dp.Items {
		its = append(its, itemName(id))
	}
	items := trim(strings.Join(its, " · "), itemsW)
	// champW is 0 when the champion column had to go; the empty column keeps the row budget wide.
	return line{
		C(padR(name, nameW), tc), S(" "),
		C(padR(champ, champW), cDim), S(" "),
		S(padR(items, itemsW)), S(" "),
		C(padL(skillPriority(dp.Abilities), skillW), cCyan),
	}
}
func itemsPlayerHeader(budget int) line {
	nameW, champW, skillW, itemsW := itemsCols(budget)
	return line{
		C(padR(tr("player"), nameW), cDim), S(" "),
		C(padR(tr("champion"), champW), cDim), S(" "),
		C(padR("ITEMS", itemsW), cDim), S(" "),
		C(padL("SKILL", skillW), cDim),
	}
}

// runesPlayerRow renders the runes view row: name + champion + FULL rune page (keystone +
// primary path + secondary) + skill, on the same column split as the items view.
func runesPlayerRow(name, champ string, dp detailsParticipant, tc string, budget int) line {
	nameW, champW, skillW, rW := itemsCols(budget)
	return line{
		C(padR(name, nameW), tc), S(" "),
		C(padR(champ, champW), cDim), S(" "),
		C(padR(trim(runePage(dp), rW), rW), cPurple), S(" "),
		C(padL(skillPriority(dp.Abilities), skillW), cCyan),
	}
}
func runesPlayerHeader(budget int) line {
	nameW, champW, skillW, rW := itemsCols(budget)
	return line{
		C(padR(tr("player"), nameW), cDim), S(" "),
		C(padR(tr("champion"), champW), cDim), S(" "),
		C(padR("RUNES", rW), cDim), S(" "),
		C(padL("SKILL", skillW), cDim),
	}
}

// ---------------- sparkline (gold diff over time) ----------------

func sparkline(series []goldPt, width int) line {
	if len(series) < 2 {
		return line{C("gold trend · collecting…", cDim)}
	}
	maxAbs := 1
	for _, p := range series {
		a := p.diff
		if a < 0 {
			a = -a
		}
		if a > maxAbs {
			maxAbs = a
		}
	}
	ch := []rune("▁▂▃▄▅▆▇█")
	var l line
	l = append(l, C(tr("gold"), cBold), S(" "))
	cur := series[len(series)-1].diff
	tail := fmt.Sprintf("  %+dk", cur/1000)
	if cur > -1000 && cur < 1000 { // the integer form would print "+0k" for every sub-1k lead
		tail = fmt.Sprintf("  %+.1fk", math.Trunc(float64(cur)/100)/10)
	}
	barW := width - 5 - runeLen(tail)
	if barW < 10 {
		barW = 10
	}
	// Each bar cell samples the series at its own position, so the curve spans the whole bar
	// instead of repeating the newest sample past the end of the series.
	last := len(series) - 1
	for i := 0; i < barW; i++ {
		idx := 0
		if barW > 1 {
			idx = (i*last + (barW-1)/2) / (barW - 1)
		}
		d := series[idx].diff
		lvl := int(math.Round(math.Abs(float64(d)) / float64(maxAbs) * 7))
		if lvl > 7 {
			lvl = 7
		}
		col := cCyan
		if d < 0 {
			col = cRed
		} else if d == 0 {
			col = cGray
		}
		l = append(l, C(string(ch[lvl]), col))
	}
	lc := cCyan
	if cur < 0 {
		lc = cRed
	}
	l = append(l, C(tail, lc))
	return l
}

// ---------------- objective map panel ----------------

func elemChar(e string) string {
	if e == "" {
		return "?"
	}
	return strings.ToUpper(e[:1])
}
func soulStr(dragons []string) string {
	if len(dragons) >= 4 {
		return " ★" + strings.ToUpper(tr("soul"))
	}
	return ""
}

// objDragonLine is the dragons row: element letters and the soul badge, or - when the panel
// is too narrow for them - just the counts.
func objDragonLine(b, r []string, elements bool) line {
	bs, rs := fmt.Sprintf("%d", len(b)), fmt.Sprintf("%d", len(r))
	if elements {
		bs += " " + strings.Join(mapSlice(b, elemChar), "·")
		rs += " " + strings.Join(mapSlice(r, elemChar), "·")
	}
	bs += soulStr(b)
	rs += soulStr(r)
	return line{C(strings.ToUpper(tr("dragon")), cBold), S("  "), C(bs, cCyan), S("   "), C(rs, cRed)}
}

// objMapPanel returns bordered lines summarizing map control. Every row is fitted to the
// panel: the dragon elements and then the gold lead drop out whole rather than being cut.
func objMapPanel(b, r teamStats, t int) []line {
	W := t - 4
	var out []line
	// towers tug bar
	out = append(out, borderedL(statBarLine(tr("tower"), b.Towers, r.Towers,
		fmt.Sprintf("%d", b.Towers), fmt.Sprintf("%d", r.Towers), W), t))
	// dragons: elements + soul
	dl := objDragonLine(b.Dragons, r.Dragons, true)
	if dl.vis() > W {
		dl = objDragonLine(b.Dragons, r.Dragons, false)
	}
	out = append(out, borderedL(dl, t))
	// baron/inhib + gold lead
	bl := line{C(strings.ToUpper(tr("baron")), cBold), S(" "), C(fmt.Sprintf("%d", b.Barons), cCyan),
		S(" ─ "), C(fmt.Sprintf("%d", r.Barons), cRed),
		S("    "), C(tr("inhibitor"), cBold), S(" "), C(fmt.Sprintf("%d", b.Inhibitors), cCyan),
		S(" ─ "), C(fmt.Sprintf("%d", r.Inhibitors), cRed)}
	lead := r.TotalGold - b.TotalGold
	leadStr := "even"
	lc := cDim
	if lead > 0 {
		leadStr = "red +" + gold(lead)
		lc = cRed
	} else if lead < 0 {
		leadStr = "blue +" + gold(-lead)
		lc = cCyan
	}
	if gl := (line{S("    "), C(tr("gold"), cBold), S(" "), C(leadStr, lc)}); bl.vis()+gl.vis() <= W {
		bl = append(bl, gl...)
	}
	out = append(out, borderedL(bl, t))
	return out
}

func mapSlice(s []string, fn func(string) string) []string {
	out := make([]string, len(s))
	for i, v := range s {
		out[i] = fn(v)
	}
	return out
}

// syntheticDetails builds plausible item/rune/skill data for demo previews.
func syntheticDetails() detailsResp {
	tmpl := []struct {
		items []int
		perks []int
		ab    []string
		style int
		sub   int
	}{
		{[]int{6631, 3006, 3071, 3748, 3190, 2055}, []int{8008, 9111, 9104, 8014, 8299, 9105, 5005, 5008, 5008}, []string{"Q", "W", "E", "Q", "Q", "R", "Q", "W", "Q", "W", "R", "W", "W"}, 8000, 8200},
		{[]int{6653, 3158, 4628, 3135, 3157, 2055}, []int{8112, 8124, 8138, 8137, 8136, 8135, 5008, 5008, 5008}, []string{"W", "Q", "E", "W", "W", "R", "W", "Q", "W", "Q", "R", "Q", "Q"}, 8100, 8200},
		{[]int{6671, 3006, 3072, 3031, 3036, 2055}, []int{8229, 8214, 8234, 8237, 8429, 8451, 5008, 5008, 5001}, []string{"Q", "E", "Q", "W", "Q", "R", "Q", "E", "Q", "E", "R", "E", "E"}, 8200, 8400},
	}
	df := detailsFrame{}
	for i := 0; i < 10; i++ {
		tm := tmpl[i%len(tmpl)]
		dp := detailsParticipant{ParticipantID: i + 1, Items: tm.items, Abilities: tm.ab}
		dp.PerkMetadata.Perks = tm.perks
		dp.PerkMetadata.StyleID = tm.style
		dp.PerkMetadata.SubStyleID = tm.sub
		df.Participants = append(df.Participants, dp)
	}
	return detailsResp{Frames: []detailsFrame{df}}
}

func syntheticSeries() []goldPt {
	var s []goldPt
	for m := 0; m <= 30; m++ {
		d := int(1200*math.Sin(float64(m)/4.0)) + (m-15)*250
		s = append(s, goldPt{m, d * 4})
	}
	return s
}
