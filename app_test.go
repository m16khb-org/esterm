package main

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
)

// ---- fakes ----

type appFakeBoard struct {
	replay bool
	closed *int
	keys   []key
}

func (b *appFakeBoard) Draw(now time.Time, w, h int, tg *toggles, mode string) *canvas {
	c := newCanvas(w, h)
	c.text(0, 0, line{S(fmt.Sprintf("board %dx%d mode=%s video=%v", w, h, mode, tg.video))}, w)
	c.mm = &mmSlot{Row: 2, Col: 3, Cols: 10, Rows: 5}
	return c
}

func (b *appFakeBoard) Key(k key) bool {
	b.keys = append(b.keys, k)
	return k.Kind == keySpace
}

func (b *appFakeBoard) Hints() []hint {
	if !b.replay {
		return nil
	}
	return []hint{{"space", "pause"}, {"←/→", "seek"}, {"+/-", "speed"}}
}

func (b *appFakeBoard) Close() { *b.closed++ }

func appEvent(id, state, a, b string, aw, bw int, start time.Time) event {
	ev := event{State: state, Type: "match", StartTime: start.UTC().Format(time.RFC3339), BlockName: "Finals"}
	ev.League.ID, ev.League.Slug, ev.League.Name = "L1", "worlds", "Worlds"
	res := func(w int) *struct {
		Outcome  *string `json:"outcome"`
		GameWins int     `json:"gameWins"`
	} {
		return &struct {
			Outcome  *string `json:"outcome"`
			GameWins int     `json:"gameWins"`
		}{GameWins: w}
	}
	ev.Match = &match{ID: id, Teams: []teamResult{{Code: a, Name: a + " Team", Result: res(aw)}, {Code: b, Name: b + " Team", Result: res(bw)}}}
	for i := 0; i < 3; i++ {
		st := "unstarted"
		switch {
		case state == "completed" || (state == "inProgress" && i == 0):
			st = "completed"
		case state == "inProgress" && i == 1:
			st = "inProgress"
		}
		ev.Match.Games = append(ev.Match.Games, game{ID: fmt.Sprintf("%s-g%d", id, i+1), State: st})
	}
	return ev
}

type appRig struct {
	t         *testing.T
	a         *app
	opened    []string // "live:<match>" / "replay:<match>:<game>"
	closed    int
	pageCalls []string
	boards    []*appFakeBoard
}

func newAppRig(t *testing.T) *appRig {
	t.Helper()
	r := &appRig{t: t}
	now := time.Now()
	old := appEvent("m-old", "completed", "AAA", "BBB", 2, 0, now.Add(-72*time.Hour))
	older := appEvent("m-older", "completed", "CCC", "DDD", 1, 2, now.Add(-96*time.Hour))
	events := []event{
		old,
		appEvent("m-done", "completed", "MVK", "FUR", 3, 0, now.Add(-24*time.Hour)),
		appEvent("m-live", "inProgress", "T1", "G2", 1, 0, now.Add(-time.Hour)),
		appEvent("m-next", "unstarted", "KC", "TBD", 0, 0, now.Add(24*time.Hour)),
	}
	byID := map[string]event{}
	for _, ev := range append(events, old, older) {
		byID[ev.Match.ID] = ev
	}
	deps := appDeps{
		leagues: func() ([]leagueRow, error) {
			return orderLeagues([]leagueRow{{"L9", "zzz", "Zed"}, {"L2", "lck", "LCK"}, {"L1", "worlds", "Worlds"}, {"L0", "msi", "MSI"}}), nil
		},
		league: func(lid string) (leaguePage, error) {
			if lid != "L1" {
				return leaguePage{}, nil
			}
			return leaguePage{Events: events, Older: "old-token", Newer: ""}, nil
		},
		page: func(lid, token string) (leaguePage, error) {
			r.pageCalls = append(r.pageCalls, lid+":"+token)
			if token != "old-token" {
				return leaguePage{}, errors.New("bad token")
			}
			return leaguePage{Events: []event{older}, Older: ""}, nil
		},
		details: func(id string) (replayEventResp, error) {
			var er replayEventResp
			for i, g := range byID[id].Match.Games {
				er.Data.Event.Match.Games = append(er.Data.Event.Match.Games, replayGameJSON{Number: i + 1, State: g.State})
			}
			er.Data.Event.Match.Games[0].Vods = make([]struct {
				Parameter      string `json:"parameter"`
				Locale         string `json:"locale"`
				Provider       string `json:"provider"`
				Offset         int    `json:"offset"`
				FirstFrameTime string `json:"firstFrameTime"`
				StartMillis    int64  `json:"startMillis"`
				EndMillis      int64  `json:"endMillis"`
			}, 1)
			return er, nil
		},
		openLive: func(ev event, o appOptions) (board, error) {
			r.opened = append(r.opened, "live:"+ev.Match.ID)
			b := &appFakeBoard{closed: &r.closed}
			r.boards = append(r.boards, b)
			return b, nil
		},
		openReplay: func(ev event, g int, o appOptions) (board, error) {
			r.opened = append(r.opened, fmt.Sprintf("replay:%s:%d", ev.Match.ID, g))
			b := &appFakeBoard{replay: true, closed: &r.closed}
			r.boards = append(r.boards, b)
			return b, nil
		},
	}
	r.a = newApp(deps, defaultAppOptions())
	r.a.inline = true
	r.a.Resize(160, 50)
	r.a.start()
	r.a.Render(time.Now()) // lists are built while drawing
	return r
}

func kr(rs ...rune) []key {
	var ks []key
	for _, r := range rs {
		switch r {
		case ' ':
			ks = append(ks, key{Kind: keySpace})
		case '\r':
			ks = append(ks, key{Kind: keyEnter})
		case 0x1b:
			ks = append(ks, key{Kind: keyEsc})
		case '\t':
			ks = append(ks, key{Kind: keyTab})
		default:
			ks = append(ks, key{Kind: keyRune, Rune: r})
		}
	}
	return ks
}

func (r *appRig) press(ks ...key) (quit bool) {
	for _, k := range ks {
		quit = r.a.Key(k)
		r.a.Render(time.Now())
	}
	return quit
}

func (r *appRig) typ(s string) { r.press(kr([]rune(s)...)...) }

func (r *appRig) tabTo(name string) {
	r.a.focus = fMatches
	for i := 0; i < 3 && appTabs[r.a.tab] != name; i++ {
		r.typ("]")
	}
}

// pick selects the matches-tab entry with the given match id.
func (r *appRig) pick(id string) {
	r.a.focus = fMatches
	for i, ev := range r.a.curEvents() {
		if ev.Match.ID == id {
			r.a.mv.Sel = i
			r.a.sync()
			return
		}
	}
	r.t.Fatalf("match %s is not in the %s tab", id, appTabs[r.a.tab])
}

// ---- tests ----

func TestOrderLeaguesPopularFirst(t *testing.T) {
	rows := []leagueRow{{Slug: "zzz"}, {Slug: "lcs"}, {Slug: "lec"}, {Slug: "aaa"}, {Slug: "worlds"}, {Slug: "lck"}, {Slug: "lpl"}, {Slug: "msi"}}
	var got []string
	for _, r := range orderLeagues(rows) {
		got = append(got, r.Slug)
	}
	want := "worlds msi lck lpl lec lcs zzz aaa"
	if strings.Join(got, " ") != want {
		t.Errorf("order = %v, want %s", got, want)
	}
}

func TestAppFocusMoves(t *testing.T) {
	r := newAppRig(t)
	a := r.a
	if a.focus != fLeagues || a.leagues[0].Slug != "worlds" {
		t.Fatalf("start: focus %v, first league %v", a.focus, a.leagues)
	}
	r.press(key{Kind: keyTab})
	if a.focus != fMatches {
		t.Errorf("tab: focus = %v", a.focus)
	}
	r.press(key{Kind: keyBackTab}, key{Kind: keyBackTab})
	if a.focus != fMain {
		t.Errorf("shift-tab twice from matches: focus = %v, want main", a.focus)
	}
	for in, want := range map[string]appFocus{"1": fLeagues, "2": fMatches, "3": fGames, "0": fMain} {
		r.typ(in)
		if a.focus != want {
			t.Errorf("%q: focus = %v, want %v", in, a.focus, want)
		}
	}
	r.typ("1")
	r.typ("l")
	r.press(key{Kind: keyRight}, key{Kind: keyRight})
	if a.focus != fMain {
		t.Errorf("l, right, right from leagues: focus = %v, want main", a.focus)
	}
	r.typ("h")
	r.press(key{Kind: keyLeft})
	if a.focus != fMatches {
		t.Errorf("h then left from main: focus = %v, want matches", a.focus)
	}
	if !r.press(kr('q')...) {
		t.Error("q does not quit")
	}
}

func TestAppLeagueSelectionLoadsMatchesAndPicksTab(t *testing.T) {
	r := newAppRig(t)
	a := r.a
	if appTabs[a.tab] != "Live" || len(a.curEvents()) != 1 || a.curEvents()[0].Match.ID != "m-live" {
		t.Errorf("worlds opens on its live match, tab=%s events=%d", appTabs[a.tab], len(a.curEvents()))
	}
	r.typ("j") // MSI: loads empty, the tab stays
	if a.selLeague().Slug != "msi" || len(a.curEvents()) != 0 {
		t.Errorf("msi: %v events", len(a.curEvents()))
	}
	r.typ("k")
	if len(a.curEvents()) != 1 {
		t.Errorf("back on worlds: %d live events", len(a.curEvents()))
	}
}

func TestAppTabsAndPaging(t *testing.T) {
	r := newAppRig(t)
	a := r.a
	r.press(key{Kind: keyTab}) // matches
	r.typ("]")
	if appTabs[a.tab] != "Upcoming" || len(a.curEvents()) != 1 {
		t.Errorf("] -> %s with %d events", appTabs[a.tab], len(a.curEvents()))
	}
	r.typ("]")
	if appTabs[a.tab] != "Recent" || len(a.curEvents()) != 2 {
		t.Fatalf("] -> %s with %d events", appTabs[a.tab], len(a.curEvents()))
	}
	r.typ("]")
	if appTabs[a.tab] != "Live" {
		t.Errorf("] wraps to Live, got %s", appTabs[a.tab])
	}
	r.typ("[")
	if appTabs[a.tab] != "Recent" {
		t.Errorf("[ from Live wraps to Recent, got %s", appTabs[a.tab])
	}
	// Recent is newest first; PgDn on the last row pages older results in.
	if got := a.curEvents()[0].Match.ID; got != "m-done" {
		t.Errorf("recent[0] = %s, want the newest (m-done)", got)
	}
	r.press(key{Kind: keyPgDn})
	if len(r.pageCalls) != 0 {
		t.Errorf("PgDn before the end loaded a page: %v", r.pageCalls)
	}
	a.mv.Sel = len(a.curEvents()) - 1
	r.press(key{Kind: keyPgDn})
	if len(r.pageCalls) != 1 || r.pageCalls[0] != "L1:old-token" {
		t.Fatalf("page calls = %v", r.pageCalls)
	}
	if evs := a.curEvents(); len(evs) != 3 || evs[2].Match.ID != "m-older" {
		t.Errorf("after paging: %d events, last %v", len(evs), evs[len(evs)-1].Match.ID)
	}
	a.mv.Sel = len(a.curEvents()) - 1
	r.press(key{Kind: keyPgDn})
	if len(r.pageCalls) != 1 || a.msg != "no more matches" {
		t.Errorf("no older token left: calls %v msg %q", r.pageCalls, a.msg)
	}
}

func TestAppEnterRouting(t *testing.T) {
	r := newAppRig(t)
	a := r.a

	// live match -> live board
	r.pick("m-live")
	r.press(key{Kind: keyEnter})
	if len(r.opened) != 1 || r.opened[0] != "live:m-live" || a.board == nil || a.focus != fMain {
		t.Fatalf("enter on live: opened %v focus %v", r.opened, a.focus)
	}
	if !strings.Contains(a.boardTitle, "T1 vs G2") {
		t.Errorf("title = %q", a.boardTitle)
	}

	// finished match -> replay of game 1, the previous board is closed
	r.tabTo("Recent")
	r.pick("m-done")
	r.press(key{Kind: keyEnter})
	if r.opened[len(r.opened)-1] != "replay:m-done:1" || r.closed != 1 {
		t.Errorf("enter on completed: opened %v closed %d", r.opened, r.closed)
	}

	// a game of the finished match
	r.pick("m-done")
	r.typ("3")
	r.typ("j")
	r.press(key{Kind: keyEnter})
	if r.opened[len(r.opened)-1] != "replay:m-done:2" {
		t.Errorf("enter on game 2: opened %v", r.opened)
	}

	// live match, finished game 1 of it -> replay; its running game 2 -> live; game 3 -> message
	r.tabTo("Live")
	r.pick("m-live")
	r.typ("3")
	r.press(key{Kind: keyEnter})
	if got := r.opened[len(r.opened)-1]; got != "replay:m-live:1" {
		t.Errorf("finished game of a live match: %s", got)
	}
	r.pick("m-live")
	r.typ("3j")
	r.press(key{Kind: keyEnter})
	if got := r.opened[len(r.opened)-1]; got != "live:m-live" {
		t.Errorf("running game: %s", got)
	}
	n := len(r.opened)
	r.pick("m-live")
	r.typ("3jj")
	r.press(key{Kind: keyEnter})
	if len(r.opened) != n || !strings.Contains(a.msg, "game 3 has not started") {
		t.Errorf("unplayed game: opened %v msg %q", r.opened[n:], a.msg)
	}

	// upcoming match: nothing opens, a message says when it starts
	r.press(key{Kind: keyEsc}, key{Kind: keyEsc})
	r.tabTo("Upcoming")
	r.pick("m-next")
	n = len(r.opened)
	r.press(key{Kind: keyEnter})
	if len(r.opened) != n || !strings.HasPrefix(a.msg, "not started yet") || !strings.Contains(a.msg, "KST") {
		t.Errorf("upcoming: opened %v msg %q", r.opened[n:], a.msg)
	}
}

func TestAppOpenFailureReturnsToLists(t *testing.T) {
	r := newAppRig(t)
	a := r.a
	a.deps.openReplay = func(event, int, appOptions) (board, error) { return nil, errors.New("no livestats") }
	r.tabTo("Recent")
	r.pick("m-done")
	r.press(key{Kind: keyEnter})
	if a.board != nil || a.loading != "" || a.focus != fMatches || !strings.Contains(a.msg, "no livestats") {
		t.Errorf("board %v loading %q focus %v msg %q", a.board, a.loading, a.focus, a.msg)
	}
}

func TestAppBoardKeysOnlyWhenMainFocused(t *testing.T) {
	r := newAppRig(t)
	a := r.a
	r.tabTo("Recent")
	r.pick("m-done")
	r.press(key{Kind: keyEnter})
	b := r.boards[0]
	r.press(key{Kind: keySpace}, key{Kind: keyRight})
	if len(b.keys) != 2 {
		t.Fatalf("board saw %v", b.keys)
	}
	r.typ("v") // a board toggle: the app, not the fake board's Key, handles it
	if a.tg.video {
		t.Error("v did not toggle the video")
	}
	r.typ("1") // lists focused: j moves the leagues, the board sees nothing
	r.typ("j")
	if len(b.keys) != 4 || a.selLeague().Slug != "msi" { // space, right, v and the "1" that moved the focus away

		t.Errorf("lists focused: board saw %d keys, league %s", len(b.keys), a.selLeague().Slug)
	}
}

func TestAppEscBack(t *testing.T) {
	r := newAppRig(t)
	a := r.a
	r.pick("m-live")
	r.press(key{Kind: keyEnter})
	r.typ("f")
	if !a.zoom || a.focus != fMain {
		t.Fatalf("f: zoom %v focus %v", a.zoom, a.focus)
	}
	if lay := a.layout(); lay.main.W != a.W || !lay.leagues.empty() {
		t.Errorf("zoom layout: %+v", lay)
	}
	r.press(key{Kind: keyEsc})
	if a.zoom || a.focus != fMain || a.board == nil {
		t.Errorf("esc 1: zoom %v focus %v", a.zoom, a.focus)
	}
	r.press(key{Kind: keyEsc})
	if a.focus != fMatches || a.board == nil {
		t.Errorf("esc 2: focus %v board %v", a.focus, a.board)
	}
	r.press(key{Kind: keyEsc})
	if a.board != nil || r.closed != 1 {
		t.Errorf("esc 3: board %v closed %d", a.board, r.closed)
	}
	r.press(key{Kind: keyEsc})
	if a.focus != fMatches {
		t.Errorf("esc with nothing to leave moved the focus to %v", a.focus)
	}
}

func TestAppHelpPopup(t *testing.T) {
	r := newAppRig(t)
	r.typ("?")
	if !r.a.help {
		t.Fatal("? does not open the help")
	}
	text := appPlain(r.a.Render(time.Now()))
	for _, want := range []string{"Keybindings", "tab/S-tab", "quit", "enter"} {
		if !strings.Contains(text, want) {
			t.Errorf("help popup lacks %q", want)
		}
	}
	r.typ("x")
	if r.a.help {
		t.Error("a key does not close the help")
	}
}

func TestAppOptionsMenu(t *testing.T) {
	r := newAppRig(t)
	a := r.a
	r.pick("m-live")
	r.press(key{Kind: keyEnter})
	r.typ(",")
	if a.menu == nil {
		t.Fatal(", does not open the options")
	}
	text := appPlain(a.Render(time.Now()))
	for _, want := range []string{"Options", "Minimap drawing", "Broadcast video sync", "Poll interval", "Left column"} {
		if !strings.Contains(text, want) {
			t.Errorf("options menu lacks %q", want)
		}
	}
	if c := a.Render(time.Now()); c.mm != nil {
		t.Error("the kitty slot stays while a popup covers it")
	}

	r.press(key{Kind: keyEnter}) // Minimap drawing: auto -> kitty
	if a.opt.Mode != mmModeKitty || a.resolveMode() != mmModeKitty {
		t.Errorf("mode = %s", a.opt.Mode)
	}
	r.press(key{Kind: keyLeft}, key{Kind: keyLeft}) // back to auto, then text
	if a.opt.Mode != mmModeText {
		t.Errorf("left twice: mode = %s, want text", a.opt.Mode)
	}
	r.press(key{Kind: keyDown}, key{Kind: keyEnter}) // video off
	if a.opt.Video || a.tg.video || a.tg.mm {
		t.Errorf("video option %v, board video %v mm %v", a.opt.Video, a.tg.video, a.tg.mm)
	}
	r.press(key{Kind: keyDown}, key{Kind: keyEnter}) // poll 2s -> 5s restarts the live board
	if a.opt.Poll != 5*time.Second {
		t.Errorf("poll = %v", a.opt.Poll)
	}
	if len(r.opened) != 2 || r.closed != 1 {
		t.Errorf("poll change did not restart the live board: opened %v closed %d", r.opened, r.closed)
	}
	if a.menu == nil || a.focus != fMain {
		t.Errorf("menu %v focus %v after the restart", a.menu, a.focus)
	}
	r.press(key{Kind: keyDown}, key{Kind: keyEnter}) // left column hidden
	if !a.opt.HideLeft || !a.layout().leagues.empty() {
		t.Errorf("left column option: %v layout %+v", a.opt.HideLeft, a.layout())
	}
	r.press(key{Kind: keyEsc})
	if a.menu != nil {
		t.Error("esc does not close the options")
	}
	r.typ("q")
	// the options are reachable with a list focused and from the board alike
	r.typ(",")
	if a.menu == nil {
		t.Error(", is not reachable from the board")
	}
}

func TestAppVideoOptionAppliesToNewBoards(t *testing.T) {
	r := newAppRig(t)
	a := r.a
	r.typ(",")
	r.press(key{Kind: keyDown}, key{Kind: keyEnter}, key{Kind: keyEsc})
	r.pick("m-live")
	r.press(key{Kind: keyEnter})
	if a.tg.video || a.tg.mm || !a.tg.embed {
		t.Errorf("toggles %+v with the video option off", a.tg)
	}
}

var appAnsi = regexp.MustCompile("\x1b\\[[0-9;]*m")

func appPlain(c *canvas) string { return appAnsi.ReplaceAllString(canvasString(c), "") }

func TestAppLayoutRowsAreExactlyW(t *testing.T) {
	sizes := [][2]int{{160, 50}, {120, 40}, {119, 30}, {100, 30}, {80, 24}, {79, 30}, {60, 24}, {100, 23}, {40, 12}, {200, 60}}
	for _, boardOpen := range []bool{false, true} {
		for _, sz := range sizes {
			for _, focus := range []appFocus{fLeagues, fMain} {
				r := newAppRig(t)
				a := r.a
				a.Resize(sz[0], sz[1])
				if boardOpen {
					r.pick("m-live")
					r.press(key{Kind: keyEnter})
				}
				a.focus = focus
				c := a.Render(time.Now())
				rows := strings.Split(strings.TrimSuffix(appPlain(c), "\n"), "\n")
				if len(rows) != sz[1] {
					t.Errorf("%v board=%v focus=%v: %d rows, want %d", sz, boardOpen, focus, len(rows), sz[1])
				}
				for i, row := range rows {
					if n := runeLen(row); n != sz[0] {
						t.Errorf("%v board=%v focus=%v: row %d is %d cells wide: %q", sz, boardOpen, focus, i, n, row)
						break
					}
				}
			}
		}
	}
}

func TestAppLeftColumnWidths(t *testing.T) {
	cases := []struct{ w, h, left int }{
		{200, 50, 44}, // 30% = 60 -> max 44
		{160, 50, 44}, // 48 -> 44
		{130, 40, 39}, // 30% of 130
		{120, 40, 36},
		{119, 40, 26},
		{100, 30, 26},
		{80, 24, 26},
	}
	for _, c := range cases {
		a := newAppRig(t).a
		a.Resize(c.w, c.h)
		lay := a.layout()
		if lay.leagues.W != c.left || lay.main.X != c.left || lay.main.W != c.w-c.left {
			t.Errorf("%dx%d: left %d, main %+v, want left %d", c.w, c.h, lay.leagues.W, lay.main, c.left)
		}
		if lay.leagues.H+lay.matches.H+lay.games.H != c.h-1 {
			t.Errorf("%dx%d: left column heights %d+%d+%d != %d", c.w, c.h, lay.leagues.H, lay.matches.H, lay.games.H, c.h-1)
		}
	}
	// 30% of 120 is below the minimum 28 only for narrower terminals: check the floor directly.
	if got := min(max(120*30/100, 28), 44); got != 36 {
		t.Errorf("clamp = %d", got)
	}
}

func TestAppSmallTerminalHidesListsWhileBoardHasFocus(t *testing.T) {
	for _, sz := range [][2]int{{70, 30}, {100, 20}} {
		r := newAppRig(t)
		a := r.a
		a.Resize(sz[0], sz[1])
		r.pick("m-live")
		r.press(key{Kind: keyEnter}) // opens the board and focuses it
		lay := a.layout()
		if !lay.leagues.empty() || lay.main.W != sz[0] {
			t.Errorf("%v: board focused, layout %+v", sz, lay)
		}
		r.press(key{Kind: keyEsc}) // lists reachable with esc
		lay = a.layout()
		if lay.leagues.empty() || !lay.main.empty() || lay.leagues.W != sz[0] {
			t.Errorf("%v: after esc, layout %+v", sz, lay)
		}
	}
	// a wide terminal keeps the lists next to the board
	r := newAppRig(t)
	r.pick("m-live")
	r.press(key{Kind: keyEnter})
	if lay := r.a.layout(); lay.leagues.empty() || lay.main.empty() {
		t.Errorf("wide: %+v", lay)
	}
}

func TestAppKittySlotIsOffsetByThePaneOrigin(t *testing.T) {
	r := newAppRig(t)
	a := r.a
	r.pick("m-live")
	r.press(key{Kind: keyEnter})
	lay := a.layout()
	c := a.Render(time.Now())
	if c.mm == nil {
		t.Fatal("the board's slot was dropped")
	}
	// the fake board puts the slot at row 2, col 3 of its canvas; the pane's inner rect starts one cell in
	if c.mm.Row != lay.main.Y+1+2 || c.mm.Col != lay.main.X+1+3 {
		t.Errorf("slot at %d,%d, want %d,%d", c.mm.Row, c.mm.Col, lay.main.Y+3, lay.main.X+4)
	}
	if c.mm.Cols != 10 || c.mm.Rows != 5 {
		t.Errorf("slot size changed: %+v", c.mm)
	}
	text := appPlain(c)
	if want := fmt.Sprintf("board %dx%d", lay.main.W-2, lay.main.H-2); !strings.Contains(text, want) {
		t.Errorf("the board is not drawn at the pane's inner size (%s)", want)
	}
	// Leaving the board or opening a popup removes the images; closing the popup brings them back.
	r.typ("?")
	if a.Render(time.Now()).mm != nil {
		t.Error("slot kept under the help popup")
	}
	r.typ("x")
	if a.Render(time.Now()).mm == nil {
		t.Error("slot not restored after the popup")
	}
	r.press(key{Kind: keyEsc}, key{Kind: keyEsc})
	if a.Render(time.Now()).mm != nil {
		t.Error("slot stays after the board closed")
	}
}

func TestAppPreviewAndHints(t *testing.T) {
	r := newAppRig(t)
	a := r.a
	r.tabTo("Recent")
	r.pick("m-done")
	text := appPlain(a.Render(time.Now()))
	for _, want := range []string{"MVK Team", "FUR Team", "Score  3 - 0", "KST", "replay game 1", "VODs   1 of 3"} {
		if !strings.Contains(text, want) {
			t.Errorf("preview lacks %q:\n%s", want, text)
		}
	}
	r.typ("3")
	if !strings.Contains(appPlain(a.Render(time.Now())), "VOD") {
		t.Error("games pane does not mention the VOD")
	}
	r.pick("m-done")
	hs := a.hints()
	if len(hs) == 0 || hs[len(hs)-1].Key != "q" {
		t.Errorf("hints = %v", hs)
	}
	r.press(key{Kind: keyEnter})
	var keys []string
	for _, h := range a.hints() {
		keys = append(keys, h.Key)
	}
	if got := strings.Join(keys, " "); !strings.Contains(got, "space") || !strings.Contains(got, "←/→") || !strings.Contains(got, "esc") {
		t.Errorf("replay board hints = %s", got)
	}
}

func TestMatchLine(t *testing.T) {
	now := time.Date(2026, 10, 18, 3, 0, 0, 0, time.UTC) // 12:00 KST
	win := "win"
	ev := appEvent("m", "completed", "MVK", "FUR", 3, 0, now)
	ev.Match.Teams[0].Result.Outcome = &win
	if got := appPlainLine(matchLine(ev)); got != "10-18 12:00 MVK 3-0 FUR" {
		t.Errorf("completed: %q", got)
	}
	if got := appPlainLine(matchLine(appEvent("m", "unstarted", "KC", "TBD", 0, 0, now))); got != "10-18 12:00 KC vs TBD" {
		t.Errorf("upcoming: %q", got)
	}
	if got := appPlainLine(matchLine(appEvent("m", "inProgress", "T1", "G2", 1, 0, now))); got != "LIVE 12:00 T1 1-0 G2" {
		t.Errorf("live: %q", got)
	}
}

func appPlainLine(l line) string {
	var b strings.Builder
	for _, s := range l {
		b.WriteString(s.text)
	}
	return b.String()
}

func TestBuildCanvasReplayHeaderAndFooter(t *testing.T) {
	start := time.Date(2026, 10, 18, 3, 5, 0, 0, time.UTC) // 12:05 KST
	ev := appEvent("m", "completed", "MVK", "FUR", 3, 0, start)
	ev.ID = "m"
	ev.Match.Teams[0].Result, ev.Match.Teams[1].Result = nil, nil
	st := replayState{Pos: 75 * time.Second, Total: 31*time.Minute + 5*time.Second, Speed: 2, Start: start}
	tg := toggles{replay: &st, embed: true}
	text := appPlain(buildCanvas(ev, windowResp{}, detailsResp{}, 75*time.Second, time.Now(), 100, 30, 0, tg, nil, mmView{}))
	for _, want := range []string{"REPLAY", "[-]", "WORLDS · Finals"} {
		if !strings.Contains(text, want) {
			t.Errorf("replay board lacks %q:\n%s", want, text)
		}
	}
	for _, bad := range []string{"LIVE", "live feed", "q quit", "╔", "║", "played"} {
		if strings.Contains(text, bad) {
			t.Errorf("embedded replay board still says %q:\n%s", bad, text)
		}
	}
	bar := appPlainLine(replayBar(st, 100, false))
	for _, want := range []string{"1:15 / 31:05", "2×"} {
		if !strings.Contains(bar, want) {
			t.Errorf("player bar %q lacks %q", bar, want)
		}
	}
	if strings.Contains(bar, "quit") || strings.Contains(bar, "space") {
		t.Errorf("embedded player bar repeats the key hints: %q", bar)
	}
	live := appPlain(buildCanvas(ev, windowResp{}, detailsResp{}, time.Minute, time.Now(), 100, 30, 2*time.Second, toggles{}, nil, mmView{}))
	if !strings.Contains(live, "LIVE") || !strings.Contains(live, "live feed") || !strings.Contains(live, "[0]") {
		t.Errorf("live board changed:\n%s", live)
	}
}

func TestReplayEventSeriesWinsOnlyAtTheFinalGame(t *testing.T) {
	var er replayEventResp
	m := &er.Data.Event.Match
	m.Teams = append(m.Teams, struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Code   string `json:"code"`
		Result *struct {
			GameWins int `json:"gameWins"`
		} `json:"result"`
	}{ID: "a", Code: "MVK", Result: &struct {
		GameWins int `json:"gameWins"`
	}{GameWins: 3}})
	m.Games = []replayGameJSON{{Number: 1, ID: "g1", State: "completed"}, {Number: 2, ID: "g2", State: "completed"}, {Number: 3, ID: "g3", State: "unplayed"}}
	ev, _, err := replayEvent("m", er, 1)
	if err != nil || ev.Match.Teams[0].Result != nil {
		t.Errorf("game 1 of 2 played: result %v err %v, want unknown", ev.Match.Teams[0].Result, err)
	}
	ev, _, _ = replayEvent("m", er, 2)
	if r := ev.Match.Teams[0].Result; r == nil || r.GameWins != 3 {
		t.Errorf("final played game: result %v, want 3 wins", r)
	}
}

func TestMatchLineShowsYearOnlyWhenItDiffers(t *testing.T) {
	now := time.Now().In(kst)
	cur := appPlainLine(matchLine(appEvent("a", "completed", "AAA", "BBB", 1, 0, now)))
	if strings.HasPrefix(cur, now.Format("06-")) {
		t.Errorf("current-year row carries a year: %q", cur)
	}
	old := now.AddDate(-1, 0, 0)
	if got := appPlainLine(matchLine(appEvent("b", "completed", "AAA", "BBB", 1, 0, old))); !strings.HasPrefix(got, old.Format("06-01-02")) {
		t.Errorf("other-year row lacks the year: %q", got)
	}
}

func TestAppTinyTerminalShowsNoticeAtRealSize(t *testing.T) {
	for _, sz := range [][2]int{{33, 9}, {10, 3}, {1, 1}, {39, 30}, {80, 9}} {
		r := newAppRig(t)
		r.a.Resize(sz[0], sz[1])
		c := r.a.Render(time.Now())
		if c.w != sz[0] || c.h != sz[1] {
			t.Errorf("%v: canvas is %dx%d, want the real size", sz, c.w, c.h)
		}
		if sz[0] >= 33 && !strings.Contains(appPlain(c), "terminal too small (need ≥ 40×10)") {
			t.Errorf("%v: no notice:\n%s", sz, appPlain(c))
		}
	}
	r := newAppRig(t)
	r.a.Resize(40, 10)
	if strings.Contains(appPlain(r.a.Render(time.Now())), "too small") {
		t.Error("the minimum size itself is shown the notice")
	}
}

func TestRenderDiffRepaintsOnAnySizeChange(t *testing.T) {
	a, b := newCanvas(80, 24), newCanvas(80, 30)
	if out := renderDiff(a, b); !strings.HasPrefix(out, "\x1b[H\x1b[2J") {
		t.Error("a height change is not a full repaint")
	}
	if out := renderDiff(a, newCanvas(80, 24)); out != "" {
		t.Errorf("identical frames emit %q", out)
	}
}

func TestAppHintBarKeepsHelpAndQuit(t *testing.T) {
	for _, w := range []int{40, 50, 70, 100, 160} {
		for _, open := range []bool{false, true} {
			r := newAppRig(t)
			r.a.Resize(w, 20)
			if open {
				r.pick("m-live")
				r.press(key{Kind: keyEnter})
			}
			bar := strings.TrimRight(strings.Split(appPlain(r.a.Render(time.Now())), "\n")[19], " ")
			if !strings.HasSuffix(bar, "? help  q quit") {
				t.Errorf("w=%d board=%v: bar %q does not end in the fixed hints", w, open, bar)
			}
		}
	}
	r := newAppRig(t)
	r.a.Resize(70, 20)
	r.press(key{Kind: keyEsc})
	r.typ("?")
	if bar := strings.Split(appPlain(r.a.Render(time.Now())), "\n")[19]; !strings.Contains(bar, "esc close") || strings.Contains(bar, "q quit") {
		t.Errorf("help popup bar = %q", bar)
	}
	r.press(key{Kind: keyEsc})
	r.typ(",")
	if bar := strings.Split(appPlain(r.a.Render(time.Now())), "\n")[19]; !strings.Contains(bar, "↑/↓ move  enter change  esc close") {
		t.Errorf("options popup bar = %q", bar)
	}
}
