package main

import (
	"testing"
	"time"
)

func (r *appRig) click(x, y int) {
	r.a.Mouse(mouseEvent{X: x, Y: y, Btn: mouseLeft})
	r.a.Render(time.Now())
}

func TestMouseClickFocusesAndSelectsLists(t *testing.T) {
	r := newAppRig(t)
	a := r.a
	lay := a.layout()

	// leagues: inner row 1 is item 0, so row 3 is item 2
	if !mouseIn(lay.leagues, 5, 3) {
		t.Fatalf("leagues rect %v does not contain the test point", lay.leagues)
	}
	r.click(5, 3)
	if a.focus != fLeagues || a.lv.Sel != 2 {
		t.Fatalf("click league row 2: focus %v sel %d", a.focus, a.lv.Sel)
	}
	if a.selLeague() == nil || a.selLeague().Slug != "lck" {
		t.Fatalf("selected league = %v", a.selLeague())
	}

	// the top border focuses the pane but selects nothing
	before := a.lv.Sel
	r.click(5, lay.leagues.Y)
	if a.focus != fLeagues || a.lv.Sel != before {
		t.Fatalf("border click: focus %v sel %d", a.focus, a.lv.Sel)
	}

	// main pane
	main := lay.main
	r.click(main.X+2, main.Y+2)
	if a.focus != fMain {
		t.Fatalf("main click: focus %v", a.focus)
	}
}

func TestMouseClickTabsAndRows(t *testing.T) {
	r := newAppRig(t)
	a := r.a
	a.focus = fMatches
	a.Render(time.Now())
	lay := a.layout()
	in := innerRect(lay.matches)

	// Recent is the third tab: Live(4) + space + Upcoming(8) + space
	x := in.X + runeLen(appTabs[0]) + 1 + runeLen(appTabs[1]) + 1 + 1
	r.click(x, in.Y)
	if appTabs[a.tab] != "Recent" || a.focus != fMatches {
		t.Fatalf("tab click: tab %s focus %v", appTabs[a.tab], a.focus)
	}

	// first row of Recent = m-done; single click only selects
	first := in.Y + 1
	r.click(in.X+2, first+1)
	if a.mv.Sel != 1 {
		t.Fatalf("row click: sel %d", a.mv.Sel)
	}
	if len(r.opened) != 0 {
		t.Fatalf("single click opened %v", r.opened)
	}
}

func TestMouseDoubleClickOpensMatch(t *testing.T) {
	r := newAppRig(t)
	a := r.a
	a.focus = fMatches
	a.setTab(2)
	a.mv.Sel = 0
	a.Render(time.Now())
	lay := a.layout()
	in := innerRect(lay.matches)
	first := in.Y + 1

	r.click(in.X+2, first)
	r.click(in.X+2, first)
	if len(r.opened) != 1 || r.opened[0] != "replay:m-done:1" {
		t.Fatalf("double click opened %v", r.opened)
	}
	if a.focus != fMain || a.board == nil {
		t.Fatalf("after opening: focus %v board %v", a.focus, a.board)
	}
}

func TestMouseWheelMovesSelection(t *testing.T) {
	r := newAppRig(t)
	a := r.a
	a.setTab(2)
	a.mv.Sel = 0
	a.Render(time.Now())
	lay := a.layout()
	in := innerRect(lay.matches)
	y := in.Y + 1

	a.Mouse(mouseEvent{X: in.X + 2, Y: y, Btn: mouseWheelDown})
	a.Render(time.Now())
	if a.focus != fMatches || a.mv.Sel != 1 {
		t.Fatalf("wheel down: focus %v sel %d", a.focus, a.mv.Sel)
	}
	a.Mouse(mouseEvent{X: in.X + 2, Y: y, Btn: mouseWheelUp})
	a.Render(time.Now())
	if a.mv.Sel != 0 {
		t.Fatalf("wheel up: sel %d", a.mv.Sel)
	}
}

func TestMouseMenuClickCyclesAndCloses(t *testing.T) {
	old := currentLang
	defer func() { setLanguage(old) }()

	r := newAppRig(t)
	a := r.a
	r.press(key{Kind: keyRune, Rune: ','})
	if a.menu == nil {
		t.Fatal("menu did not open")
	}
	a.Render(time.Now())
	in := a.menu.inner(rect{0, 0, a.W, a.H})
	langRow := len(a.menu.Items) - 1

	r.click(in.X+2, in.Y+langRow)
	if a.menu != nil {
		t.Fatal("menu stayed open after a row click")
	}
	if currentLang != langKO {
		t.Fatalf("language click = %v, want 한국어", currentLang)
	}

	// a click outside closes without applying
	r.press(key{Kind: keyRune, Rune: ','})
	a.Render(time.Now())
	opt := a.opt
	a.Mouse(mouseEvent{X: 0, Y: 0, Btn: mouseLeft})
	if a.menu != nil {
		t.Fatal("outside click left the menu open")
	}
	if a.opt != opt {
		t.Fatalf("outside click changed the options to %+v", a.opt)
	}
}
