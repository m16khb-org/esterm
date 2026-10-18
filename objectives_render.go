package main

// The objective markers of the minimap panel: the Dragon pit and the Baron pit with their
// countdown. Kitty draws the game's own icons (objectives_kitty.go); the block and text
// modes draw the letter D / E / B on the pit with the countdown in the cells below it.

// objPitXY are the pits as fractions of the map (x right, y down), from the game-unit
// positions Dragon (9866, 4414) and Baron (5007, 10471) through mmGameXY's inverse.
var objPitXY = [2][2]float64{{0.6662, 0.6997}, {0.3421, 0.2986}}

// mmObj is one objective marker of the minimap panel.
type mmObj struct {
	Icon   string // minimap icon name (cdMinimapIcons)
	Letter rune   // D, E or B: the marker of the block and text modes, the placeholder of an icon that is still loading
	X, Y   float64
	State  objState
	Text   string // the countdown "3:12" while waiting, else empty
}

// objMarkers returns the dragon pit and the baron pit markers of a view; nil without a
// shown frame.
func objMarkers(o objView) []mmObj {
	if !o.Valid {
		return nil
	}
	pit := func(i int, icon string, letter rune, t objTimer) mmObj {
		m := mmObj{Icon: icon, Letter: letter, X: objPitXY[i][0], Y: objPitXY[i][1], State: t.State}
		if t.State == objWaiting {
			m.Text = objClock(t.Left)
		}
		return m
	}
	dragon, letter := "dragon", 'D'
	switch d := o.Dragon.Drake; {
	case d == objElderName:
		dragon, letter = "dragon_elder", 'E'
	case d != "":
		dragon = "dragon_" + d
	}
	return []mmObj{pit(0, dragon, letter, o.Dragon), pit(1, "baron", 'B', o.Baron)}
}

// objCell is one terminal cell of an objective marker in the block and text modes.
type objCell struct {
	x, y   int
	r      rune
	state  objState
	letter bool // the pit's letter, else a countdown character
}

// placeObjectives returns the cells of the markers on a cols x rows map: the letter on the
// pit's cell and the countdown centred below it (above when the pit is on the last row).
func placeObjectives(objs []mmObj, cols, rows int) []objCell {
	var out []objCell
	for _, m := range objs {
		x := min(max(int(m.X*float64(cols)), 0), cols-1)
		y := min(max(int(m.Y*float64(rows)), 0), rows-1)
		out = append(out, objCell{x, y, m.Letter, m.State, true})
		n := len(m.Text)
		if n == 0 || n > cols {
			continue
		}
		ty := y + 1
		if ty >= rows {
			ty = y - 1
		}
		if ty < 0 {
			continue
		}
		x0 := min(max(x-(n-1)/2, 0), cols-n)
		for j, ch := range m.Text {
			out = append(out, objCell{x0 + j, ty, ch, m.State, false})
		}
	}
	return out
}

// objTaken is the set of cells the markers use, which champions keep clear.
func objTaken(cells []objCell) map[[2]int]bool {
	taken := make(map[[2]int]bool, len(cells))
	for _, c := range cells {
		taken[[2]int{c.x, c.y}] = true
	}
	return taken
}

// objStyle is the text style of an objective cell: the letter is green and bold while the
// monster is up, yellow while it counts down and dim when its time is unknown; countdown
// characters are yellow.
func objStyle(c objCell) string {
	switch {
	case !c.letter:
		return cYel
	case c.state == objUp:
		return cBold + cGreen
	case c.state == objWaiting:
		return cBold + cYel
	}
	return cDim
}
