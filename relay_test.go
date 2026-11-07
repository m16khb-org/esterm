package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestCanvasFitsTerminal(t *testing.T) {
	now := time.Now()
	for _, w := range []int{50, 80, 104, 200, 300} {
		for h := 12; h <= 60; h++ {
			for flags := 0; flags < 16; flags++ {
				tg := toggles{view: flags & 3, spark: flags&4 != 0, omap: flags&8 != 0}
				if c := demoCanvasAt(w, h, now, 2*time.Second, detailsResp{}, tg, nil, boardView{}); c.h > h {
					t.Fatalf("w=%d h=%d tg=%+v: canvas has %d rows", w, h, tg, c.h)
				}
			}
		}
	}
}

var escSeq = regexp.MustCompile(`^\x1b\[([0-9;]*)([A-Za-z])`)

// screen is a minimal terminal: cursor addressing, SGR accumulation, erase and printable runes.
type screen struct {
	cells map[[2]int]cell
	r, c  int
	sty   string
	w, h  int
}

func (s *screen) feed(out string) {
	for len(out) > 0 {
		if m := escSeq.FindStringSubmatch(out); m != nil {
			out = out[len(m[0]):]
			switch m[2] {
			case "m":
				if m[1] == "0" {
					s.sty = ""
				} else {
					s.sty += m[0]
				}
			case "H":
				p := strings.Split(m[1], ";")
				s.r, s.c = 0, 0
				if len(p) == 2 {
					s.r, _ = strconv.Atoi(p[0])
					s.c, _ = strconv.Atoi(p[1])
					s.r, s.c = s.r-1, s.c-1
				}
			case "J":
				s.cells = map[[2]int]cell{}
			case "K":
				for c := s.c; c < s.w; c++ {
					delete(s.cells, [2]int{s.r, c})
				}
			}
			continue
		}
		r := []rune(out)[0]
		out = out[len(string(r)):]
		s.cells[[2]int{s.r, s.c}] = cell{ch: r, sty: s.sty}
		s.c++
	}
}

func (s *screen) matches(t *testing.T, c *canvas, what string) {
	t.Helper()
	for r := 0; r < c.h; r++ {
		for x := 0; x < c.w; x++ {
			if got, want := s.cells[[2]int{r, x}], c.buf[r][x]; got != want {
				t.Fatalf("%s: cell (%d,%d) = %q %q, want %q %q", what, r, x, got.ch, got.sty, want.ch, want.sty)
			}
		}
	}
}

// Styles accumulate in the terminal, so the diff renderer must reset between differently
// styled cells; a full repaint and a diff chain must show the same screen.
func TestRenderDiffNoStyleLeak(t *testing.T) {
	now := time.Now()
	a := demoCanvasAt(104, 40, now, 2*time.Second, detailsResp{}, toggles{}, nil, boardView{})
	b := demoCanvasAt(104, 40, now.Add(time.Second), 2*time.Second, detailsResp{}, toggles{view: 1, spark: true, omap: true}, nil, boardView{})

	full := &screen{cells: map[[2]int]cell{}, w: 104, h: 40}
	full.feed(renderDiff(nil, b))
	full.matches(t, b, "full repaint")

	chain := &screen{cells: map[[2]int]cell{}, w: 104, h: 40}
	chain.feed(renderDiff(nil, a))
	chain.feed(renderDiff(a, b))
	chain.matches(t, b, "diff after full repaint")
}

func TestPlayoutHeaderNeverStepsBackBySmallCorrections(t *testing.T) {
	var p relayPlayout
	if g := p.holdGame(100 * time.Second); g != 100*time.Second {
		t.Fatal(g)
	}
	if g := p.holdGame(99 * time.Second); g != 100*time.Second {
		t.Fatalf("stepped back to %s", g)
	}
	if g := p.holdGame(60 * time.Second); g != 60*time.Second {
		t.Fatalf("a real resync must pass: %s", g)
	}
	if g := p.holdGame(-1); g != -1 || p.game != 60*time.Second {
		t.Fatalf("unknown clock must not disturb the high-water mark")
	}
}

func TestPlayerRowShowsRespawnCountdownInTheHPColumn(t *testing.T) {
	p := participant{ParticipantID: 3, Level: 9, Kills: 1, Deaths: 2, TotalGold: 9000, CurrentHealth: 0, MaxHealth: 2800}
	m := pmeta{"Name", "Champ"}
	for _, budget := range []int{24, 30, 40, 48, 60, 100} {
		c := pickCols(budget)
		alive := playerRow(p, m, c, cCyan, nil)
		for _, tc := range []struct {
			name string
			lf   champLife
			want string
		}{
			{"exact", champLife{Dead: true, Secs: 22.3, Exact: true}, "☠ 23s"},
			{"estimate", champLife{Dead: true, Secs: 22.3}, "☠ ~23s"},
			{"overdue", champLife{Dead: true}, "☠ ~0s"},
			{"three digits", champLife{Dead: true, Secs: 99.5}, "☠ ~100s"},
		} {
			row := playerRow(p, m, c, cCyan, champLives{3: tc.lf})
			if row.vis() != alive.vis() {
				t.Errorf("budget %d %s: row is %d cells wide, the bar row %d", budget, tc.name, row.vis(), alive.vis())
			}
			if has := strings.Contains(appPlainLine(row), tc.want); has != (c.hpbar > 0) {
				t.Errorf("budget %d %s: %q in row = %v with hpbar %d: %q", budget, tc.name, tc.want, has, c.hpbar, appPlainLine(row))
			}
		}
		if row := playerRow(p, m, c, cCyan, champLives{3: {}}); appPlainLine(row) != appPlainLine(alive) {
			t.Errorf("budget %d: a champion the feed does not call dead lost its bar: %q", budget, appPlainLine(row))
		}
	}
}

func TestBoardWidthDoesNotChangeWhileChampionsAreDead(t *testing.T) {
	now := time.Now()
	lives := champLives{1: {Dead: true, Secs: 22.3, Exact: true}, 2: {Dead: true, Secs: 4}, 3: {Dead: true, Secs: 80.2}}
	for _, w := range []int{50, 60, 80, 104, 140, 200} {
		plain := appPlain(demoCanvasAt(w, 40, now, 2*time.Second, detailsResp{}, toggles{}, nil, boardView{}))
		dead := appPlain(demoCanvasAt(w, 40, now, 2*time.Second, detailsResp{}, toggles{}, nil, boardView{Lives: lives}))
		a, b := strings.Split(plain, "\n"), strings.Split(dead, "\n")
		if len(a) != len(b) {
			t.Fatalf("w=%d: %d rows with the countdowns, %d without", w, len(b), len(a))
		}
		for i := range a {
			if utf8.RuneCountInString(a[i]) != utf8.RuneCountInString(b[i]) {
				t.Fatalf("w=%d row %d changed width:\n%q\n%q", w, i, a[i], b[i])
			}
		}
		if !strings.Contains(dead, "☠ 23s") || !strings.Contains(dead, "☠ ~4s") {
			t.Errorf("w=%d: the countdowns are missing:\n%s", w, dead)
		}
	}
}

// Every board row closes with its frame: whatever the width, view, panel toggle or
// language, a long line is cut instead of pushing the right border off the canvas. An
// embedded board is cropped out of the framed one, so its rows end one cell short of the
// border and must still end in the blank that precedes it.
func TestBoardRowsKeepTheirFrame(t *testing.T) {
	now := time.Now()
	old := currentLang
	defer func() { setLanguage(old) }()
	lives := champLives{6: {Dead: true, Secs: 23}}
	sizes := [][2]int{{40, 12}, {50, 12}, {60, 20}, {80, 24}, {100, 30}, {104, 30}, {120, 40}, {160, 50}, {220, 60}, {300, 85}}
	for _, lang := range []langID{langEN, langKO} {
		setLanguage(lang)
		for _, sz := range sizes {
			for _, embed := range []bool{false, true} {
				for vw := 0; vw < 3; vw++ {
					for flags := 0; flags < 4; flags++ {
						tg := toggles{view: vw, spark: flags&1 != 0, omap: flags&2 != 0, embed: embed}
						c := demoCanvasAt(sz[0], sz[1], now, 2*time.Second, detailsResp{}, tg, nil, boardView{Lives: lives})
						what := fmt.Sprintf("lang %d %dx%d embed=%v view=%d s=%v o=%v", lang, sz[0], sz[1], embed, vw, tg.spark, tg.omap)
						if c.w != sz[0] {
							t.Fatalf("%s: canvas is %d cells wide", what, c.w)
						}
						for y, row := range c.buf {
							got := row[c.w-1].ch
							want := ' '
							if !embed {
								want = '║'
								if y == 0 {
									want = '╗'
								} else if y == c.h-1 {
									want = '╝'
								}
							}
							if got != want {
								t.Fatalf("%s: row %d ends in %q, want %q", what, y, got, want)
							}
						}
					}
				}
			}
		}
	}
}
