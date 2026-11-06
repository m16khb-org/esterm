package main

import "time"

// Mouse support follows lazygit's model: a click focuses the pane under the cursor and
// selects the row, a second click on the same row opens it, and the wheel moves the
// selection. The menu closes on a click outside it.

func mouseIn(r rect, x, y int) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

func innerRect(r rect) rect {
	return rect{r.X + 1, r.Y + 1, max(r.W-2, 0), max(r.H-2, 0)}
}

// listGeom is the clickable geometry of one of the left lists. first is the screen row
// of the list's first visible item; the tab row of the matches pane is above it.
type listGeom struct {
	focus appFocus
	list  *listView
	in    rect
	first int
	count int
}

func (a *app) listGeomAt(x, y int) (listGeom, bool) {
	lay := a.layout()
	var g listGeom
	switch {
	case mouseIn(lay.leagues, x, y):
		g = listGeom{focus: fLeagues, list: &a.lv, in: innerRect(lay.leagues), count: len(a.leagues)}
	case mouseIn(lay.matches, x, y):
		in := innerRect(lay.matches)
		g = listGeom{focus: fMatches, list: &a.mv, in: rect{in.X, in.Y + 1, in.W, max(in.H-1, 0)}, count: len(a.curEvents())}
	case mouseIn(lay.games, x, y):
		g = listGeom{focus: fGames, list: &a.gv, in: innerRect(lay.games), count: len(a.games())}
	default:
		return g, false
	}
	g.first = g.in.Y
	return g, true
}

// tabAt returns the tab index at x in the matches pane's tab row, or -1.
func tabAt(in rect, x int) int {
	col := in.X
	for i, t := range appTabs {
		w := runeLen(t)
		if col+w > in.X+in.W {
			break
		}
		if x >= col && x < col+w {
			return i
		}
		col += w + 1
	}
	return -1
}

func (a *app) Mouse(ev mouseEvent) {
	if ev.Release {
		return
	}
	a.msg = ""
	if a.menu != nil {
		a.mouseMenu(ev)
		return
	}
	if a.help {
		if ev.Btn == mouseLeft {
			a.help = false
		}
		return
	}
	switch ev.Btn {
	case mouseWheelUp, mouseWheelDown:
		a.mouseWheel(ev)
		return
	}
	if ev.Btn != mouseLeft {
		return
	}
	lay := a.layout()
	if mouseIn(lay.main, ev.X, ev.Y) {
		a.focus = fMain
		a.doubleMain()
		a.sync()
		return
	}
	g, ok := a.listGeomAt(ev.X, ev.Y)
	if !ok {
		return
	}
	a.focus = g.focus
	if g.focus == fMatches {
		tabRow := innerRect(lay.matches)
		if ev.Y == tabRow.Y {
			if i := tabAt(tabRow, ev.X); i >= 0 {
				a.setTab(i)
				a.mv = listView{}
			}
			a.sync()
			return
		}
	}
	if ev.Y >= g.first && ev.Y < g.first+g.in.H {
		idx := g.list.Top + ev.Y - g.first
		if idx >= 0 && idx < g.count {
			prev := g.list.Sel
			g.list.Sel = idx
			a.afterSelect(g.list, prev)
			a.sync()
			a.doubleList(g.focus, idx)
			return
		}
	}
	a.sync()
}

// afterSelect applies what a keyboard move in the same list would: a new league reloads
// the matches pane and resets its tab choice; a new match resets the games pane.
func (a *app) afterSelect(l *listView, prev int) {
	if l == &a.lv && l.Sel != prev {
		a.tab, a.mv = a.tabAfterLeague(), listView{}
	}
	if l == &a.mv && l.Sel != prev {
		a.gv = listView{}
	}
}

func (a *app) doubleList(focus appFocus, idx int) {
	now := time.Now()
	if !a.clickAt.IsZero() && a.clickFocus == focus && a.clickIdx == idx && now.Sub(a.clickAt) <= 500*time.Millisecond {
		a.clickAt = time.Time{}
		a.enter()
		return
	}
	a.clickAt, a.clickFocus, a.clickIdx = now, focus, idx
}

func (a *app) doubleMain() {
	now := time.Now()
	if !a.clickAt.IsZero() && a.clickFocus == fMain && now.Sub(a.clickAt) <= 500*time.Millisecond {
		a.clickAt = time.Time{}
		if a.board != nil {
			a.zoom = !a.zoom
		}
		return
	}
	a.clickAt, a.clickFocus, a.clickIdx = now, fMain, -1
}

func (a *app) mouseWheel(ev mouseEvent) {
	step := 3
	if ev.Btn == mouseWheelUp {
		step = -3
	}
	g, ok := a.listGeomAt(ev.X, ev.Y)
	if !ok {
		return
	}
	a.focus = g.focus
	prev := g.list.Sel
	g.list.Move(step)
	a.afterSelect(g.list, prev)
	a.sync()
}

func (a *app) mouseMenu(ev mouseEvent) {
	m := a.menu
	switch ev.Btn {
	case mouseWheelUp:
		m.Move(-1)
		return
	case mouseWheelDown:
		m.Move(1)
		return
	}
	if ev.Btn != mouseLeft {
		return
	}
	in := m.inner(rect{0, 0, a.W, a.H})
	if mouseIn(in, ev.X, ev.Y) && ev.Y-in.Y < len(m.Items) {
		m.Sel = ev.Y - in.Y
		a.cycleOption(m.Sel, 1)
		a.menu = nil
		return
	}
	a.menu = nil
}
