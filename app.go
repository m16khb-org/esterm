package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// The interactive app (`esterm` / `esterm lol`): lazygit-style panes for leagues, matches
// and games, a main pane that previews the selection or plays a live/replay board, a
// hint bar and popups. app holds the whole UI state; everything it reads from the network
// or starts goes through appDeps so the state machine runs hermetically in tests.

// ---------------- options ----------------

type appOptions struct {
	Mode     string        // minimap drawing: auto|kitty|blocks|text
	Video    bool          // broadcast video sync (minimap panel on a board)
	Poll     time.Duration // live feed poll interval
	HideLeft bool          // hide the left column while a board is focused
}

var (
	appModes = []string{mmModeAuto, mmModeKitty, mmModeBlocks, mmModeText}
	appPolls = []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second}
)

func defaultAppOptions() appOptions {
	return appOptions{Mode: mmModeAuto, Video: true, Poll: appPolls[0]}
}

// ---------------- data ----------------

type leagueRow struct{ ID, Slug, Name string }

// leaguePage is one schedule response: the events and the tokens of the pages around it.
type leaguePage struct {
	Events       []event
	Older, Newer string
}

// leagueData is everything shown for one league.
type leagueData struct {
	all          map[string]event
	live         []event
	upcoming     []event
	recent       []event
	older, newer string
	loaded, busy bool
	err          string
}

// add merges events (later data replaces earlier for the same match) and rebuilds the
// three tab lists: upcoming oldest first, recent newest first.
func (d *leagueData) add(evs []event) {
	if d.all == nil {
		d.all = map[string]event{}
	}
	for _, ev := range evs {
		if ev.Type == "match" && ev.Match != nil {
			d.all[eventID(ev)] = ev
		}
	}
	d.live, d.upcoming, d.recent = nil, nil, nil
	for _, ev := range d.all {
		switch ev.State {
		case "inProgress":
			d.live = append(d.live, ev)
		case "completed":
			d.recent = append(d.recent, ev)
		default:
			d.upcoming = append(d.upcoming, ev)
		}
	}
	byTime := func(s []event, desc bool) {
		sort.Slice(s, func(i, j int) bool {
			a, b := parseTime(s[i].StartTime), parseTime(s[j].StartTime)
			if a.Equal(b) {
				return eventID(s[i]) < eventID(s[j])
			}
			return a.Before(b) != desc
		})
	}
	byTime(d.live, false)
	byTime(d.upcoming, false)
	byTime(d.recent, true)
}

type gameRow struct {
	No    int
	State string
	Vods  int
	Known bool // Vods is known (the match details arrived)
}

// ---------------- boards ----------------

// board is what the main pane plays: a live relay or a replay.
type board interface {
	// Draw renders the board into a canvas of exactly w×h cells; its kitty slot is
	// relative to the canvas.
	Draw(now time.Time, w, h int, tg *toggles, mode string) *canvas
	// Key applies a board-specific key (playback) and reports whether it was one.
	Key(k key) bool
	// Hints lists the board-specific keys for the hint bar and the help popup.
	Hints() []hint
	Close()
}

type liveBoard struct {
	fd     *feed
	mm     *minimap
	pl     *relayPlayout
	cancel context.CancelFunc
	poll   time.Duration
}

func newLiveBoard(lid, matchID string, poll time.Duration) *liveBoard {
	fd := newFeed(lid, poll)
	fd.matchID = matchID
	fd.FetchOnce()
	mm := newMinimap(minimapCfg{})
	mm.SetLives(fd.LivesAt)
	ctx, cancel := context.WithCancel(context.Background())
	go fd.Run(ctx)
	return &liveBoard{fd: fd, mm: mm, pl: &relayPlayout{fd: fd, mm: mm}, cancel: cancel, poll: poll}
}

func (b *liveBoard) Draw(now time.Time, w, h int, tg *toggles, mode string) *canvas {
	b.pl.hidePanel = !tg.mm
	b.fd.SetDetailsView(tg.view != 0)
	s, view := b.pl.Tick(now, tg.video)
	view.Mode = mode
	view.CellW, view.CellH = termCellPx()
	return buildCanvas(s.Ev, s.W, s.Det, s.Game, now, w, h, b.poll, *tg, s.Series, view)
}

func (b *liveBoard) Key(key) bool { return false }

func (b *liveBoard) Hints() []hint { return nil }

func (b *liveBoard) Close() {
	b.cancel()
	b.mm.Close(mmCloseWait)
}

type replayBoard struct{ rp *replay }

func (b replayBoard) Draw(now time.Time, w, h int, tg *toggles, mode string) *canvas {
	b.rp.SetDetailsView(tg.view != 0)
	st := b.rp.State()
	t := *tg
	t.replay = &st
	s, view := b.rp.Tick(now, t.video, t.mm)
	view.Mode = mode
	view.CellW, view.CellH = termCellPx()
	c := buildCanvas(b.rp.Event(), s.W, s.Det, s.Game, now, w, h-1, 0, t, s.Series, view)
	return withPlayerBar(c, replayBar(st, w, false))
}

func (b replayBoard) Key(k key) bool {
	switch {
	case k.Kind == keyRight:
		b.rp.Seek(replaySeekStep)
	case k.Kind == keyLeft:
		b.rp.Seek(-replaySeekStep)
	case k.Kind == keySpace:
		b.rp.TogglePause()
	case k.Kind == keyRune && (k.Rune == '+' || k.Rune == '='):
		b.rp.CycleSpeed(1)
	case k.Kind == keyRune && (k.Rune == '-' || k.Rune == '_'):
		b.rp.CycleSpeed(-1)
	default:
		return false
	}
	return true
}

func (b replayBoard) Hints() []hint {
	return []hint{{"space", "pause"}, {"←/→", "seek"}, {"+/-", "speed"}}
}

func (b replayBoard) Close() { b.rp.Close() }

// ---------------- dependencies ----------------

type appDeps struct {
	leagues    func() ([]leagueRow, error)
	league     func(lid string) (leaguePage, error) // first page around now, live matches included
	page       func(lid, token string) (leaguePage, error)
	details    func(matchID string) (replayEventResp, error)
	openLive   func(ev event, o appOptions) (board, error)
	openReplay func(ev event, game int, o appOptions) (board, error)
}

// appPopular lists the leagues shown first, in this order.
var appPopular = []string{"worlds", "msi", "lck", "lpl", "lec", "lcs"}

func realAppDeps() appDeps {
	return appDeps{
		leagues: func() ([]leagueRow, error) {
			var lr leaguesResp
			if err := getJSON(gwBase+"/getLeagues?hl=en-US", &lr); err != nil {
				return nil, err
			}
			var rows []leagueRow
			for _, l := range lr.Data.Leagues {
				rows = append(rows, leagueRow{ID: l.ID, Slug: l.Slug, Name: l.Name})
			}
			return orderLeagues(rows), nil
		},
		league: func(lid string) (leaguePage, error) {
			p, err := fetchSchedulePage(lid, "")
			if err != nil {
				return p, err
			}
			var live scheduleResp
			if getJSON(gwBase+"/getLive?hl=en-US&leagueId="+lid, &live) == nil {
				p.Events = append(p.Events, live.Data.Schedule.Events...)
			}
			return p, nil
		},
		page: fetchSchedulePage,
		details: func(id string) (replayEventResp, error) {
			var er replayEventResp
			err := getJSON(gwBase+"/getEventDetails?hl=en-US&id="+id, &er)
			return er, err
		},
		openLive: func(ev event, o appOptions) (board, error) {
			return newLiveBoard(ev.League.ID, ev.Match.ID, o.Poll), nil
		},
		openReplay: func(ev event, game int, o appOptions) (board, error) {
			rp, err := newReplay(context.Background(), replayOpts{MatchID: ev.Match.ID, Game: game, Poll: o.Poll, Block: ev.BlockName})
			if err != nil {
				return nil, err
			}
			return replayBoard{rp}, nil
		},
	}
}

func fetchSchedulePage(lid, token string) (leaguePage, error) {
	url := gwBase + "/getSchedule?hl=en-US&leagueId=" + lid
	if token != "" {
		url += "&pageToken=" + token
	}
	var sr scheduleResp
	if err := getJSON(url, &sr); err != nil {
		return leaguePage{}, err
	}
	s := sr.Data.Schedule
	return leaguePage{Events: s.Events, Older: s.Pages.Older, Newer: s.Pages.Newer}, nil
}

// orderLeagues puts the popular leagues first (appPopular order), then the rest as given.
func orderLeagues(rows []leagueRow) []leagueRow {
	var out, rest []leagueRow
	for _, slug := range appPopular {
		for _, r := range rows {
			if r.Slug == slug {
				out = append(out, r)
			}
		}
	}
	for _, r := range rows {
		pop := false
		for _, slug := range appPopular {
			pop = pop || r.Slug == slug
		}
		if !pop {
			rest = append(rest, r)
		}
	}
	return append(out, rest...)
}

// ---------------- state ----------------

type appFocus int

const (
	fLeagues appFocus = iota
	fMatches
	fGames
	fMain
)

var appTabs = [3]string{"Live", "Upcoming", "Recent"}

type app struct {
	deps appDeps
	opt  appOptions
	// autoMode is what "auto" drawing resolved to at start (the terminal probe must run
	// before the key reader starts).
	autoMode string

	W, H  int
	focus appFocus
	zoom  bool

	leagues  []leagueRow
	lerr     string
	lv       listView
	tab      int
	tabSet   bool // the user picked a tab: no automatic choice
	data     map[string]*leagueData
	mv, gv   listView
	det      map[string]replayEventResp
	detBusy  map[string]bool
	gamesFor string // match whose games the games pane shows

	board      board
	boardTitle string
	loading    string // what the main pane is opening ("" = nothing)
	boardSeq   int
	reopen     func() // reopens the running board (options that need a restart)
	tg         toggles

	help bool
	menu *menu
	msg  string

	msgs    chan func(*app) // results of background work, applied by the main loop
	done    chan struct{}
	inline  bool // tests: background work runs inline
	closing sync.WaitGroup
}

func newApp(deps appDeps, opt appOptions) *app {
	return &app{
		deps: deps, opt: opt, autoMode: mmModeBlocks, W: 120, H: 40,
		data: map[string]*leagueData{}, det: map[string]replayEventResp{}, detBusy: map[string]bool{},
		msgs: make(chan func(*app), 16), done: make(chan struct{}),
	}
}

// async runs work off the main loop and applies its result on it.
func (a *app) async(work func() func(*app)) {
	if a.inline {
		if f := work(); f != nil {
			f(a)
		}
		return
	}
	go func() {
		if f := work(); f != nil {
			select {
			case a.msgs <- f:
			case <-a.done:
			}
		}
	}()
}

// start loads the leagues.
func (a *app) start() {
	a.async(func() func(*app) {
		rows, err := a.deps.leagues()
		return func(a *app) {
			if err != nil {
				a.lerr = err.Error()
				return
			}
			a.leagues = rows
			a.sync()
		}
	})
}

func (a *app) resolveMode() string {
	if a.opt.Mode == mmModeAuto {
		return a.autoMode
	}
	return a.opt.Mode
}

// ---------------- selection ----------------

func (a *app) selLeague() *leagueRow {
	if a.lv.Sel < 0 || a.lv.Sel >= len(a.leagues) {
		return nil
	}
	return &a.leagues[a.lv.Sel]
}

func (a *app) curData() *leagueData {
	if l := a.selLeague(); l != nil {
		return a.data[l.ID]
	}
	return nil
}

func (a *app) curEvents() []event {
	d := a.curData()
	if d == nil {
		return nil
	}
	switch a.tab {
	case 0:
		return d.live
	case 1:
		return d.upcoming
	}
	return d.recent
}

func (a *app) selEvent() *event {
	evs := a.curEvents()
	if a.mv.Sel < 0 || a.mv.Sel >= len(evs) {
		return nil
	}
	return &evs[a.mv.Sel]
}

func (a *app) games() []gameRow {
	ev := a.selEvent()
	if ev == nil || ev.Match == nil {
		return nil
	}
	var rows []gameRow
	if d, ok := a.det[ev.Match.ID]; ok && len(d.Data.Event.Match.Games) > 0 {
		for _, g := range d.Data.Event.Match.Games {
			rows = append(rows, gameRow{No: g.Number, State: g.State, Vods: len(g.Vods), Known: true})
		}
		return rows
	}
	for i, g := range ev.Match.Games {
		rows = append(rows, gameRow{No: i + 1, State: g.State})
	}
	return rows
}

// sync reacts to a changed selection: it loads the selected league and the selected
// match's details when they are missing, resets the games pane for a new match, and
// picks a tab for a league loaded for the first time.
func (a *app) sync() {
	if l := a.selLeague(); l != nil {
		d := a.data[l.ID]
		if d == nil {
			d = &leagueData{}
			a.data[l.ID] = d
		}
		if !d.loaded && !d.busy {
			d.busy = true
			lid := l.ID
			a.async(func() func(*app) {
				p, err := a.deps.league(lid)
				return func(a *app) { a.leagueLoaded(lid, p, err, false) }
			})
		}
	}
	id := ""
	if ev := a.selEvent(); ev != nil {
		id = ev.Match.ID
		if _, ok := a.det[id]; !ok && !a.detBusy[id] {
			a.detBusy[id] = true
			a.async(func() func(*app) {
				er, _ := a.deps.details(id) // a failed fetch leaves er empty
				return func(a *app) {
					delete(a.detBusy, id)
					a.det[id] = er // empty after a failure: the games pane keeps the schedule rows
				}
			})
		}
	}
	if id != a.gamesFor {
		a.gamesFor = id
		a.gv = listView{}
	}
}

func (a *app) leagueLoaded(lid string, p leaguePage, err error, more bool) {
	d := a.data[lid]
	if d == nil {
		return
	}
	d.busy = false
	if err != nil {
		d.err, d.loaded = err.Error(), true // r reloads
		return
	}
	d.err = ""
	d.add(p.Events)
	if !more {
		d.loaded, d.older, d.newer = true, p.Older, p.Newer
	}
	if l := a.selLeague(); l != nil && l.ID == lid && !a.tabSet && !more {
		a.tab = 2
		if len(d.live) > 0 {
			a.tab = 0
		} else if len(d.upcoming) > 0 {
			a.tab = 1
		}
		a.mv = listView{}
	}
	a.sync()
}

// loadMore fetches the next page in the direction the tab scrolls: older for Recent,
// newer for Upcoming.
func (a *app) loadMore() {
	l, d := a.selLeague(), a.curData()
	if l == nil || d == nil || d.busy || a.tab == 0 {
		return
	}
	token, older := d.newer, false
	if a.tab == 2 {
		token, older = d.older, true
	}
	if token == "" {
		a.msg = "no more matches"
		return
	}
	d.busy = true
	lid := l.ID
	a.async(func() func(*app) {
		p, err := a.deps.page(lid, token)
		return func(a *app) {
			if err == nil {
				if older {
					a.data[lid].older = p.Older
				} else {
					a.data[lid].newer = p.Newer
				}
			}
			a.leagueLoaded(lid, p, err, true)
		}
	})
}

// ---------------- boards ----------------

func (a *app) closeBoard() {
	a.boardSeq++
	a.loading = ""
	a.reopen = nil
	if a.board != nil {
		a.reap(a.board)
		a.board = nil
	}
	a.zoom = false
}

// reap closes a board in the background; quitting waits for all of them.
func (a *app) reap(b board) {
	if a.inline {
		b.Close()
		return
	}
	a.closing.Add(1)
	go func() {
		defer a.closing.Done()
		b.Close()
	}()
}

func (a *app) matchTitle(ev event) string {
	l, r := "TBD", "TBD"
	if ev.Match != nil && len(ev.Match.Teams) >= 2 {
		l, r = codeOr(ev.Match.Teams[0].Code, "TBD"), codeOr(ev.Match.Teams[1].Code, "TBD")
	}
	return l + " vs " + r
}

// open starts the board for a game (game <= 0: the match's first/live game): a live
// board for a running match, a replay board for a finished one.
func (a *app) open(ev event, game int) {
	if ev.Match == nil {
		return
	}
	switch ev.State {
	case "inProgress":
		if game > 0 {
			if g := a.gameState(ev, game); g == "completed" {
				a.openReplay(ev, game)
				return
			} else if g == "unstarted" {
				a.msg = fmt.Sprintf("game %d has not started", game)
				return
			}
		}
		a.openLive(ev)
	case "completed":
		a.openReplay(ev, max(game, 1))
	default:
		a.msg = "not started yet · starts " + parseTime(ev.StartTime).In(kst).Format("01-02 15:04") + " KST"
	}
}

func (a *app) gameState(ev event, no int) string {
	for _, g := range a.games() {
		if g.No == no {
			return g.State
		}
	}
	return ""
}

func (a *app) beginBoard(title, loading string, reopen func()) int {
	a.closeBoard()
	a.boardTitle, a.loading, a.reopen = title, loading, reopen
	a.focus = fMain
	a.tg = toggles{video: a.opt.Video, mm: a.opt.Video, embed: true}
	return a.boardSeq
}

func (a *app) openLive(ev event) {
	title := "Live · " + a.matchTitle(ev)
	seq := a.beginBoard(title, "opening live board…", func() { a.openLive(ev) })
	opt := a.opt
	a.async(func() func(*app) {
		b, err := a.deps.openLive(ev, opt)
		return func(a *app) { a.boardReady(seq, b, err) }
	})
}

func (a *app) openReplay(ev event, game int) {
	title := fmt.Sprintf("Replay · %s · G%d", a.matchTitle(ev), game)
	seq := a.beginBoard(title, fmt.Sprintf("loading game %d…", game), nil)
	opt := a.opt
	a.async(func() func(*app) {
		b, err := a.deps.openReplay(ev, game, opt)
		return func(a *app) { a.boardReady(seq, b, err) }
	})
}

func (a *app) boardReady(seq int, b board, err error) {
	if seq != a.boardSeq {
		if b != nil {
			a.reap(b)
		}
		return
	}
	a.loading = ""
	if err != nil {
		a.msg = "cannot open: " + err.Error()
		a.boardTitle, a.reopen = "", nil
		a.focus = fMatches
		return
	}
	a.board = b
}

// ---------------- options ----------------

func cycleIdx(i, n, dir int) int { return ((i+dir)%n + n) % n }

func (a *app) menuItems() []menuItem {
	onOff := func(b bool) string {
		if b {
			return "on"
		}
		return "off"
	}
	left := "shown"
	if a.opt.HideLeft {
		left = "hidden"
	}
	return []menuItem{
		{"Minimap drawing", a.opt.Mode, "auto · kitty (pixel icons) · blocks (half-blocks) · text (braille)"},
		{"Broadcast video sync", onOff(a.opt.Video), "decode the broadcast minimap (needs yt-dlp + ffmpeg)"},
		{"Poll interval", a.opt.Poll.String(), "how often the live feed is fetched"},
		{"Left column in board view", left, "hide the lists while a board has the focus"},
	}
}

func (a *app) openMenu() {
	a.menu = &menu{Title: "Options"}
	a.menu.Items = a.menuItems()
}

// cycleOption steps option i and applies it immediately.
func (a *app) cycleOption(i, dir int) {
	prev := a.opt
	switch i {
	case 0:
		k := 0
		for j, m := range appModes {
			if m == a.opt.Mode {
				k = j
			}
		}
		a.opt.Mode = appModes[cycleIdx(k, len(appModes), dir)]
	case 1:
		a.opt.Video = !a.opt.Video
	case 2:
		k := 0
		for j, p := range appPolls {
			if p == a.opt.Poll {
				k = j
			}
		}
		a.opt.Poll = appPolls[cycleIdx(k, len(appPolls), dir)]
	case 3:
		a.opt.HideLeft = !a.opt.HideLeft
	}
	if a.opt.Video != prev.Video && a.board != nil {
		a.tg.video, a.tg.mm = a.opt.Video, a.opt.Video
	}
	if a.opt.Poll != prev.Poll && a.reopen != nil {
		a.reopen() // the live feed's poll interval is fixed when it starts
	}
	a.menu.Items = a.menuItems()
}

// ---------------- keys ----------------

func isRune(k key, rs string) bool { return k.Kind == keyRune && strings.ContainsRune(rs, k.Rune) }

// Key applies one key and reports whether the app should quit.
func (a *app) Key(k key) (quit bool) {
	a.msg = ""
	if k.Kind == keyCtrlC {
		return true
	}
	switch {
	case a.menu != nil:
		a.menuKey(k)
		return false
	case a.help:
		a.help = false
		return false
	}
	if a.focus == fMain && a.board != nil && a.boardKey(k) {
		return false
	}
	switch {
	case isRune(k, "qQ"):
		return true
	case isRune(k, "?"):
		a.help = true
	case isRune(k, ","):
		a.openMenu()
	case k.Kind == keyEsc:
		a.back()
	case k.Kind == keyTab:
		a.focus = (a.focus + 1) % 4
	case k.Kind == keyBackTab:
		a.focus = (a.focus + 3) % 4
	case isRune(k, "1230"):
		a.focus = map[rune]appFocus{'1': fLeagues, '2': fMatches, '3': fGames, '0': fMain}[k.Rune]
	case isRune(k, "fF"):
		if a.board != nil {
			a.zoom, a.focus = !a.zoom, fMain
		}
	case k.Kind == keyEnter:
		a.enter()
	case a.focus == fMain:
		if isRune(k, "h") || k.Kind == keyLeft {
			a.focus = fGames
		}
	default:
		a.listKey(k)
	}
	a.sync()
	return false
}

// boardKey handles the keys a focused board owns; the rest falls through to the app.
func (a *app) boardKey(k key) bool {
	if a.loading != "" {
		return false
	}
	return a.board.Key(k) || boardKey(&a.tg, k, false)
}

// back is esc: leave a popup, then the zoom, then the board's focus, then the board.
func (a *app) back() {
	switch {
	case a.zoom:
		a.zoom = false
	case a.focus == fMain:
		a.focus = fMatches
	case a.board != nil || a.loading != "":
		a.closeBoard()
	}
}

func (a *app) menuKey(k key) {
	m := a.menu
	switch {
	case k.Kind == keyEsc || isRune(k, ",qQ"):
		a.menu = nil
	case k.Kind == keyUp || isRune(k, "k"):
		m.Move(-1)
	case k.Kind == keyDown || isRune(k, "j"):
		m.Move(1)
	case k.Kind == keyEnter || k.Kind == keySpace || k.Kind == keyRight || isRune(k, "l"):
		a.cycleOption(m.Sel, 1)
	case k.Kind == keyLeft || isRune(k, "h"):
		a.cycleOption(m.Sel, -1)
	}
}

func (a *app) enter() {
	switch a.focus {
	case fLeagues:
		a.focus = fMatches
	case fMatches, fMain:
		if a.focus == fMain && a.board != nil {
			return
		}
		if ev := a.selEvent(); ev != nil {
			game := a.gv.Sel + 1
			if ev.State == "inProgress" {
				game = 0 // the running game
			}
			a.open(*ev, game)
		} else {
			a.msg = "no match selected"
		}
	case fGames:
		if ev := a.selEvent(); ev != nil {
			g := a.games()
			if a.gv.Sel < len(g) {
				a.open(*ev, g[a.gv.Sel].No)
			}
		}
	}
}

func (a *app) listKey(k key) {
	var l *listView
	var h int
	lay := a.layout()
	switch a.focus {
	case fLeagues:
		l, h = &a.lv, lay.leagues.H-2
	case fMatches:
		l, h = &a.mv, lay.matches.H-3
	default:
		l, h = &a.gv, lay.games.H-2
	}
	switch a.focus {
	case fLeagues:
		l.Items = make([]listItem, len(a.leagues))
	case fMatches:
		l.Items = make([]listItem, len(a.curEvents()))
	default:
		l.Items = make([]listItem, len(a.games()))
	}
	prevSel := l.Sel
	switch {
	case k.Kind == keyUp || isRune(k, "k"):
		l.Move(-1)
	case k.Kind == keyDown || isRune(k, "j"):
		l.Move(1)
	case k.Kind == keyPgUp:
		l.Page(-1, h)
	case k.Kind == keyPgDn:
		l.Page(1, h)
		if a.focus == fMatches && prevSel == len(l.Items)-1 {
			a.loadMore()
		}
	case k.Kind == keyHome || isRune(k, "g"):
		l.Home()
	case k.Kind == keyEnd || isRune(k, "G"):
		l.End()
	case k.Kind == keyLeft || isRune(k, "h"):
		a.focus = max(a.focus-1, fLeagues)
	case k.Kind == keyRight || isRune(k, "l"):
		a.focus++
	case isRune(k, "r"):
		a.reload()
	case isRune(k, "[") && a.focus == fMatches:
		a.setTab(a.tab - 1)
	case isRune(k, "]") && a.focus == fMatches:
		a.setTab(a.tab + 1)
	}
	if l == &a.lv && l.Sel != prevSel {
		a.tab, a.mv = a.tabAfterLeague(), listView{}
	}
	if l == &a.mv && l.Sel != prevSel {
		a.gv = listView{}
	}
}

// tabAfterLeague keeps the user's tab; before any choice a league opens on its best tab.
func (a *app) tabAfterLeague() int {
	if d := a.curData(); d != nil && d.loaded && !a.tabSet {
		switch {
		case len(d.live) > 0:
			return 0
		case len(d.upcoming) > 0:
			return 1
		}
		return 2
	}
	return a.tab
}

// reload forgets the selected league's matches and the match details and fetches them again.
func (a *app) reload() {
	if l := a.selLeague(); l != nil {
		delete(a.data, l.ID)
	}
	a.det, a.gamesFor = map[string]replayEventResp{}, ""
	a.mv, a.gv = listView{}, listView{}
}

func (a *app) setTab(t int) {
	a.tab, a.tabSet = cycleIdx(t, len(appTabs), 0), true
	a.mv, a.gv = listView{}, listView{}
}

// ---------------- layout ----------------

type appLayout struct {
	leagues, matches, games, main rect
	hint                          rect
}

func (a *app) narrow() bool { return a.W < 80 || a.H < 24 }

// layout places the panes: a left column of three lists (30% of the width on wide
// terminals, 26 columns on mid-size ones) and the main pane; while a board has the focus
// the left column makes way for it (always when the terminal is small, zoomed, or the
// option says so), and a small terminal shows either the lists or the board.
func (a *app) layout() appLayout {
	W, H := a.W, a.H
	body := rect{0, 0, W, max(H-1, 0)}
	lay := appLayout{hint: rect{0, H - 1, W, 1}}
	hasBoard := a.board != nil || a.loading != ""
	switch {
	case a.focus == fMain && (a.narrow() || (hasBoard && (a.zoom || a.opt.HideLeft))):
		lay.main = body
		return lay
	case a.narrow():
		lay.leagues, lay.matches, lay.games = splitLeft(body)
		return lay
	}
	lw := 26
	if W >= 120 {
		lw = min(max(W*30/100, 28), 44)
	}
	left := rect{0, 0, lw, body.H}
	lay.leagues, lay.matches, lay.games = splitLeft(left)
	lay.main = rect{lw, 0, W - lw, body.H}
	return lay
}

// splitLeft stacks leagues (30%), matches and games (25%) in r.
func splitLeft(r rect) (leagues, matches, games rect) {
	l, g := r.H*3/10, r.H/4
	if r.H < 12 {
		l, g = r.H/3, r.H/3
	}
	m := r.H - l - g
	return rect{r.X, r.Y, r.W, l}, rect{r.X, r.Y + l, r.W, m}, rect{r.X, r.Y + l + m, r.W, g}
}

// ---------------- rendering ----------------

func (a *app) Resize(w, h int) { a.W, a.H = max(w, 1), max(h, 1) }

// layoutSig changes whenever the pane geometry or the board's presence does, which needs
// a full repaint (stale kitty images, cells under a former popup).
func (a *app) layoutSig() string {
	return fmt.Sprint(a.layout(), a.board != nil, a.loading != "", a.help, a.menu != nil)
}

func (a *app) Render(now time.Time) *canvas {
	if a.W < minTermW || a.H < minTermH {
		return tooSmallCanvas(a.W, a.H, minTermW, minTermH)
	}
	c := newCanvas(a.W, a.H)
	lay := a.layout()
	a.drawLeague(c, lay.leagues)
	a.drawMatches(c, lay.matches)
	a.drawGames(c, lay.games)
	a.drawMain(c, lay.main, now)
	if a.msg != "" {
		c.text(a.H-1, 0, truncLine(line{C(a.msg, cYel)}, a.W), a.W)
	} else {
		ctx, fixed := a.hintParts()
		drawHintBar(c, a.H-1, a.W, ctx, fixed)
	}
	screen := rect{0, 0, a.W, a.H}
	switch {
	case a.menu != nil:
		a.menu.Draw(c, screen)
		c.mm = nil // the popup covers the images: kitty removes them until it closes
	case a.help:
		drawPopup(c, screen, "Keybindings", a.helpLines())
		c.mm = nil
	}
	return c
}

func (a *app) drawLeague(c *canvas, r rect) {
	if r.empty() {
		return
	}
	in := drawPane(c, r, pane{Num: 1, Title: "Leagues", Focused: a.focus == fLeagues})
	if len(a.leagues) == 0 {
		msg := "loading…"
		if a.lerr != "" {
			msg = "error: " + a.lerr
		}
		c.text(in.Y, in.X, truncLine(line{C(msg, cDim)}, in.W), in.W)
		return
	}
	a.lv.Items = a.lv.Items[:0]
	for _, l := range a.leagues {
		a.lv.Items = append(a.lv.Items, listItem{Line: line{S(l.Name)}})
	}
	a.lv.Draw(c, in, a.focus == fLeagues)
}

func (a *app) drawMatches(c *canvas, r rect) {
	if r.empty() {
		return
	}
	d := a.curData()
	footer := ""
	if evs := a.curEvents(); len(evs) > 0 {
		footer = fmt.Sprintf("%d of %d", a.mv.Sel+1, len(evs))
	}
	in := drawPane(c, r, pane{Num: 2, Title: "Matches", Footer: footer, Focused: a.focus == fMatches})
	if in.empty() {
		return
	}
	var tabs line
	for i, t := range appTabs {
		if i > 0 {
			tabs = append(tabs, S(" "))
		}
		if i == a.tab {
			tabs = append(tabs, C(t, cBold+cGreen))
		} else {
			tabs = append(tabs, C(t, cDim))
		}
	}
	c.text(in.Y, in.X, truncLine(tabs, in.W), in.W)
	body := rect{in.X, in.Y + 1, in.W, in.H - 1}
	evs := a.curEvents()
	if len(evs) == 0 {
		msg := "no " + strings.ToLower(appTabs[a.tab]) + " matches"
		switch {
		case d == nil || (!d.loaded && d.err == ""):
			msg = "loading…"
		case d.err != "":
			msg = "error: " + d.err
		}
		c.text(body.Y, body.X, truncLine(line{C(msg, cDim)}, body.W), body.W)
		return
	}
	a.mv.Items = a.mv.Items[:0]
	for _, ev := range evs {
		a.mv.Items = append(a.mv.Items, listItem{Line: matchLine(ev)})
	}
	a.mv.Draw(c, body, a.focus == fMatches)
}

func (a *app) drawGames(c *canvas, r rect) {
	if r.empty() {
		return
	}
	in := drawPane(c, r, pane{Num: 3, Title: "Games", Focused: a.focus == fGames})
	rows := a.games()
	if len(rows) == 0 {
		c.text(in.Y, in.X, truncLine(line{C("select a match", cDim)}, in.W), in.W)
		return
	}
	a.gv.Items = a.gv.Items[:0]
	for _, g := range rows {
		a.gv.Items = append(a.gv.Items, listItem{Line: gameLine(g)})
	}
	a.gv.Draw(c, in, a.focus == fGames)
}

func (a *app) drawMain(c *canvas, r rect, now time.Time) {
	if r.empty() {
		return
	}
	title := "[0] Preview"
	if a.boardTitle != "" {
		title = "[0] " + a.boardTitle
	}
	in := drawPane(c, r, pane{Title: title, Focused: a.focus == fMain})
	if in.empty() {
		return
	}
	switch {
	case a.loading != "":
		c.text(in.Y, in.X+1, line{C(a.loading, cYel)}, in.W-1)
	case a.board != nil:
		if in.W < 50 {
			c.text(in.Y, in.X+1, truncLine(line{C("terminal too narrow for the board", cDim)}, in.W-1), in.W-1)
			return
		}
		bc := a.board.Draw(now, in.W, in.H, &a.tg, a.resolveMode())
		c.blit(bc, in)
		if s := bc.mm; s != nil {
			o := *s // the slot is board-relative; the terminal wants absolute cells
			o.Row += in.Y
			o.Col += in.X
			c.mm = &o
		}
	default:
		for i, l := range a.previewLines() {
			if i >= in.H {
				break
			}
			c.text(in.Y+i, in.X+1, truncLine(l, in.W-1), in.W-1)
		}
	}
}

// matchLine is one row of the matches list: KST time, teams, score or state.
func matchLine(ev event) line {
	t := parseTime(ev.StartTime).In(kst)
	l, r := "TBD", "TBD"
	var lw, rw string
	var lwin, rwin bool
	if m := ev.Match; m != nil && len(m.Teams) >= 2 {
		l, r = codeOr(m.Teams[0].Code, "TBD"), codeOr(m.Teams[1].Code, "TBD")
		if a, b := m.Teams[0].Result, m.Teams[1].Result; a != nil && b != nil {
			lw, rw = itoa(a.GameWins), itoa(b.GameWins)
			lwin = a.Outcome != nil && *a.Outcome == "win"
			rwin = b.Outcome != nil && *b.Outcome == "win"
		}
	}
	stamp := t.Format("01-02 15:04")
	if t.Year() != time.Now().In(kst).Year() {
		stamp = t.Format("06-01-02") + "   " // other year: date only, same column width
	}
	when := C(stamp, cGray)
	score := C("vs", cDim)
	if lw != "" && (ev.State != "unstarted") {
		score = S(lw + "-" + rw)
	}
	if ev.State == "inProgress" {
		when = C("LIVE "+t.Format("15:04"), cBold+cRed)
	}
	ls, rs := S(l), S(r)
	if lwin {
		ls = C(l, cBold+cGreen)
	}
	if rwin {
		rs = C(r, cBold+cGreen)
	}
	return line{when, S(" "), ls, S(" "), score, S(" "), rs}
}

func gameLine(g gameRow) line {
	var st seg
	switch g.State {
	case "completed":
		st = C("done", cGreen)
	case "inProgress":
		st = C("live", cBold+cRed)
	default:
		st = C("not played", cDim)
	}
	l := line{S(fmt.Sprintf("G%d ", g.No)), st}
	switch {
	case g.Vods > 0:
		l = append(l, C("  VOD", cCyan))
	case g.Known && g.State == "completed":
		l = append(l, C("  no VOD", cDim))
	}
	return l
}

// previewLines describes the selection and what enter will do.
func (a *app) previewLines() []line {
	ev := a.selEvent()
	if ev == nil || ev.Match == nil {
		l := a.selLeague()
		if l == nil {
			return []line{{C("esterm · League of Legends esports", cBold)}, nil, {C("pick a league on the left", cDim)}}
		}
		return []line{{C(l.Name, cBold)}, {C("league "+l.Slug, cDim)}, nil, {C("pick a match: 2 / enter", cDim)}}
	}
	m := ev.Match
	l, r := "TBD", "TBD"
	var lw, rw int
	if len(m.Teams) >= 2 {
		l, r = or(m.Teams[0].Name, "TBD"), or(m.Teams[1].Name, "TBD")
		if m.Teams[0].Result != nil && m.Teams[1].Result != nil {
			lw, rw = m.Teams[0].Result.GameWins, m.Teams[1].Result.GameWins
		}
	}
	bo := len(m.Games)
	if d, ok := a.det[m.ID]; ok && d.Data.Event.Match.Strategy.Count > 0 {
		bo = d.Data.Event.Match.Strategy.Count
	}
	out := []line{
		{C(l, cBold+cCyan), S("  vs  "), C(r, cBold+cRed)},
		{C(strings.ToUpper(ev.League.Slug), cBold), S(" · " + or(ev.BlockName, "Match") + fmt.Sprintf(" · BO%d", bo))},
		nil,
	}
	state := map[string]string{"inProgress": "LIVE", "completed": "completed", "unstarted": "upcoming"}[ev.State]
	if state == "" {
		state = ev.State
	}
	score := "—"
	if ev.State != "unstarted" {
		score = fmt.Sprintf("%d - %d", lw, rw)
	}
	out = append(out,
		line{C("Score  ", cGray), S(score + "  (" + state + ")")},
		line{C("Start  ", cGray), S(parseTime(ev.StartTime).In(kst).Format("2006-01-02 15:04") + " KST")},
	)
	if d, ok := a.det[m.ID]; ok && len(d.Data.Event.Match.Games) > 0 {
		vods, played := 0, 0
		for _, g := range d.Data.Event.Match.Games {
			if len(g.Vods) > 0 {
				vods++
			}
			if g.State == "completed" {
				played++
			}
		}
		if played > 0 {
			out = append(out, line{C("VODs   ", cGray), S(fmt.Sprintf("%d of %d played games have an official VOD", vods, played))})
		}
	}
	out = append(out, nil, line{C("enter ▸ ", cGreen), S(a.enterDoes(*ev))})
	return out
}

func (a *app) enterDoes(ev event) string {
	g := max(a.gv.Sel+1, 1)
	switch ev.State {
	case "inProgress":
		return "open the live board (scoreboard + broadcast minimap)"
	case "completed":
		return fmt.Sprintf("replay game %d (scoreboard + VOD minimap, space ←/→ +/-)", g)
	}
	return "nothing yet: the match has not started"
}

// ---------------- hints and help ----------------

func (a *app) hints() []hint {
	ctx, fixed := a.hintParts()
	return append(ctx, fixed...)
}

// hintParts splits the key hints into the context ones (cut first on a narrow screen) and
// the fixed ones that always stay: back, help, quit. An open popup shows only its own keys.
func (a *app) hintParts() (ctx, fixed []hint) {
	switch {
	case a.menu != nil:
		return []hint{{"↑/↓", "move"}, {"enter", "change"}, {"esc", "close"}}, nil
	case a.help:
		return nil, []hint{{"esc", "close"}}
	}
	fixed = []hint{{"?", "help"}, {"q", "quit"}}
	if a.focus == fMain && a.board != nil {
		ctx = append(a.board.Hints(), hint{"f", "zoom"}, hint{",", "options"},
			hint{"b", "view"}, hint{"s", "spark"}, hint{"o", "obj"}, hint{"m", "map"}, hint{"v", "video"})
		return ctx, append([]hint{{"esc", "back"}}, fixed...)
	}
	switch a.focus {
	case fMatches:
		ctx = []hint{{"j/k", "move"}, {"enter", "open"}, {"[/]", "tab"}, {"PgDn", "more"}, {"tab", "pane"}, {",", "options"}}
	case fMain:
		ctx = []hint{{"enter", "open"}, {"tab", "pane"}, {",", "options"}}
	default:
		ctx = []hint{{"j/k", "move"}, {"enter", "open"}, {"tab", "pane"}, {",", "options"}}
		fixed = append([]hint{{"esc", "back"}}, fixed...)
	}
	return ctx, fixed
}

func (a *app) helpLines() []line {
	k := func(key, desc string) line { return line{C(padR(key, 12), cGreen), S(desc)} }
	out := []line{
		{C("Everywhere", cBold)},
		k("tab/S-tab", "next / previous pane"),
		k("1 2 3 0", "focus Leagues / Matches / Games / main pane"),
		k(",", "options (minimap drawing, video sync, poll, left column)"),
		k("?", "this help"),
		k("q  ctrl+c", "quit"),
		k("esc", "back: close popup, unzoom, leave the board, close the board"),
		nil,
		{C("Lists", cBold)},
		k("j/k ↑/↓", "move"),
		k("PgUp/PgDn", "page (PgDn at the end loads older / newer matches)"),
		k("g/G", "first / last"),
		k("h/l ←/→", "previous / next pane"),
		k("[ ]", "Live / Upcoming / Recent tab (Matches)"),
		k("r", "reload the league's matches"),
		k("enter", "open: live match → live board, finished → replay"),
	}
	if a.board != nil {
		out = append(out, nil, line{C("Board (main pane focused)", cBold)},
			k("b", "stats / items / runes"), k("s", "gold sparkline"), k("o", "objective map"),
			k("m", "minimap panel"), k("v", "broadcast video sync"), k("f", "zoom the board"))
		if len(a.board.Hints()) > 0 {
			out = append(out, k("space", "pause / resume"), k("←/→", "seek ±10 s"), k("+/-", "speed 1× 2× 4×"))
		}
	}
	return out
}

// ---------------- run ----------------

func cmdApp() {
	a := newApp(realAppDeps(), defaultAppOptions())
	a.runApp()
}

func (a *app) runApp() {
	// The kitty probe reads the terminal: it runs before the key reader starts.
	if m, err := mmResolveMode(mmModeAuto); err == nil {
		a.autoMode = m
	}
	var kit kittyOut
	keys, leave := enterFullscreen(func() { a.shutdown(&kit) })
	defer leave()
	defer a.shutdown(&kit)

	var prev *canvas
	sig := ""
	draw := func() {
		a.Resize(termSize())
		a.sync()
		if s := a.layoutSig(); s != sig {
			sig, prev = s, nil
		}
		c := a.Render(time.Now())
		fmt.Print(renderDiff(prev, c) + kit.Frame(c.mm, prev == nil || prev.w != c.w || prev.h != c.h))
		prev = c
	}
	a.start()
	draw()
	cadence := a.tickInterval()
	tick := time.NewTicker(cadence)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			draw()
		case f := <-a.msgs:
			f(a)
			draw()
		case k, ok := <-keys:
			if !ok || a.Key(k) {
				return
			}
			draw()
		}
		if d := a.tickInterval(); d != cadence {
			cadence = d
			tick.Reset(d)
		}
	}
}

// tickInterval is how often the screen is redrawn (see mmTickFor).
func (a *app) tickInterval() time.Duration {
	return mmTickFor(a.board != nil && a.tg.video && a.tg.mm)
}

// shutdown ends the boards (their children are reaped before it returns) and removes the
// kitty images; it is safe to call more than once.
func (a *app) shutdown(kit *kittyOut) {
	select {
	case <-a.done:
	default:
		close(a.done)
	}
	a.closeBoard()
	a.closing.Wait()
	fmt.Print(kit.Delete())
}
