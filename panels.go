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
	mm    bool // m: broadcast minimap panel (the video pipeline runs while hidden)
	video bool // v: broadcast video sync (pipeline + synced playout)
	// replay is the playback state of a replayed game (nil: live): the header says REPLAY, the footer shows the position.
	replay *replayState
	embed  bool // drawn inside the app's pane: the app's hint bar lists the keys
}

// mmView is what the minimap panel shows: the draw mode, champions and a status line.
// Lives is the feed's word on who is dead at the shown instant; the scoreboard rows read
// it too, so board and map agree.
type mmView struct {
	Mode         string // mmModeKitty, mmModeBlocks or mmModeText
	Markers      []mmMarker
	Icons        []mmIcon
	Ver          string // Data Dragon version of the icons
	CellW, CellH int    // terminal cell size in pixels (kitty)
	Status       string
	Lives        mmLives
}

// ---------------- gold-diff series ----------------
type goldPt struct {
	gameMin int
	diff    int // blueGold - redGold
}

// ---------------- skill order ----------------
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

// runePage: keystone + 3 primary runes + secondary tree + 2 secondary runes (full names).
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

// items view: champion + FULL item names + skill (one focus, wide).
func itemsCols(budget int) (nameW, champW, skillW, itemsW int) {
	nameW, champW, skillW = 12, 8, 6
	itemsW = budget - nameW - champW - skillW - 3
	if itemsW < 20 {
		itemsW = 20
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
		C(padR("PLAYER", nameW), cDim), S(" "),
		C(padR("CHAMP", champW), cDim), S(" "),
		C(padR("ITEMS", itemsW), cDim), S(" "),
		C(padL("SKILL", skillW), cDim),
	}
}

// runes view: champion + FULL rune page (keystone + primary path + secondary) + skill.
func runesPlayerRow(name, champ string, dp detailsParticipant, tc string, budget int) line {
	nameW, champW, skillW := 12, 8, 6
	rW := budget - nameW - champW - skillW - 3
	if rW < 20 {
		rW = 20
	}
	return line{
		C(padR(name, nameW), tc), S(" "),
		C(padR(champ, champW), cDim), S(" "),
		C(padR(trim(runePage(dp), rW), rW), cPurple), S(" "),
		C(padL(skillPriority(dp.Abilities), skillW), cCyan),
	}
}
func runesPlayerHeader(budget int) line {
	nameW, champW, skillW := 12, 8, 6
	rW := budget - nameW - champW - skillW - 3
	if rW < 20 {
		rW = 20
	}
	return line{
		C(padR("PLAYER", nameW), cDim), S(" "),
		C(padR("CHAMP", champW), cDim), S(" "),
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
	l = append(l, C("GOLD", cBold), S(" "))
	cur := series[len(series)-1].diff
	tail := fmt.Sprintf("  %+dk", cur/1000)
	barW := width - 5 - len(tail)
	if barW < 10 {
		barW = 10
	}
	step := float64(len(series)) / float64(barW)
	if step < 1 {
		step = 1
	}
	for i := 0; i < barW; i++ {
		idx := int(float64(i) * step)
		if idx >= len(series) {
			idx = len(series) - 1
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
		return " ★SOUL"
	}
	return ""
}

// objMapPanel returns bordered lines summarizing map control.
func objMapPanel(b, r teamStats, t int) []line {
	W := t - 4
	var out []line
	// towers tug bar
	out = append(out, borderedL(statBarLine("TOWER", b.Towers, r.Towers,
		fmt.Sprintf("%d", b.Towers), fmt.Sprintf("%d", r.Towers), W), t))
	// dragons: elements + soul
	be := strings.Join(mapSlice(b.Dragons, elemChar), "·")
	re := strings.Join(mapSlice(r.Dragons, elemChar), "·")
	dl := line{C("DRAGON", cBold), S("  "),
		C(fmt.Sprintf("%d %s%s", len(b.Dragons), be, soulStr(b.Dragons)), cCyan),
		S("   "), C(fmt.Sprintf("%d %s%s", len(r.Dragons), re, soulStr(r.Dragons)), cRed)}
	out = append(out, borderedL(dl, t))
	// baron/inhib + gold lead
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
	bl := line{C("BARON", cBold), S(" "), C(fmt.Sprintf("%d", b.Barons), cCyan),
		S(" ─ "), C(fmt.Sprintf("%d", r.Barons), cRed),
		S("    "), C("INHIB", cBold), S(" "), C(fmt.Sprintf("%d", b.Inhibitors), cCyan),
		S(" ─ "), C(fmt.Sprintf("%d", r.Inhibitors), cRed),
		S("    "), C("GOLD ", cBold), C(leadStr, lc)}
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
