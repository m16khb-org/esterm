package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// esterm — a tiny terminal client for esports, organized as `esterm <game> <command>`.
// The lol game uses the same unofficial lolesports endpoints that lolesports.com renders.
// All times are shown in KST (Asia/Seoul).

const (
	apiKey   = "0TvQnueqKa5mxJntVWt0w4LpLfEkrV1Ta8rQBb9Z"
	origin   = "https://lolesports.com"
	gwBase   = "https://esports-api.lolesports.com/persisted/gw"
	feedBase = "https://feed.lolesports.com/livestats/v1"
)

var kst = time.FixedZone("KST", 9*60*60)

// --------------------------- HTTP ---------------------------

var httpClient = &http.Client{Timeout: 8 * time.Second}

func getJSON(url string, v any) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("Origin", origin)
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil // empty feed (e.g. game just started)
	}
	return json.Unmarshal(body, v)
}

// --------------------------- types ---------------------------

type leaguesResp struct {
	Data struct {
		Leagues []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Slug string `json:"slug"`
		} `json:"leagues"`
	} `json:"data"`
}

type scheduleResp struct {
	Data struct {
		Schedule struct {
			Events []event `json:"events"`
			Pages  struct {
				Older string `json:"older"`
				Newer string `json:"newer"`
			} `json:"pages"`
		} `json:"schedule"`
	} `json:"data"`
}

type event struct {
	ID        string `json:"id"`
	StartTime string `json:"startTime"`
	State     string `json:"state"`
	Type      string `json:"type"`
	BlockName string `json:"blockName"`
	League    struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
	} `json:"league"`
	Match *match `json:"match"`
}

type match struct {
	ID    string       `json:"id"`
	Teams []teamResult `json:"teams"`
	Games []game       `json:"games"`
}

type teamResult struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Code   string `json:"code"`
	Result *struct {
		Outcome  *string `json:"outcome"`
		GameWins int     `json:"gameWins"`
	} `json:"result"`
}

type game struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

type windowResp struct {
	EsportsGameID  string `json:"esportsGameId"`
	EsportsMatchID string `json:"esportsMatchId"`
	GameMetadata   struct {
		PatchVersion     string   `json:"patchVersion"`
		BlueTeamMetadata teamMeta `json:"blueTeamMetadata"`
		RedTeamMetadata  teamMeta `json:"redTeamMetadata"`
	} `json:"gameMetadata"`
	Frames []frame `json:"frames"`
}

type teamMeta struct {
	EsportsTeamID  string `json:"esportsTeamId"`
	Participants   []struct {
		ParticipantID int    `json:"participantId"`
		SummonerName  string `json:"summonerName"`
		ChampionID    string `json:"championId"`
		Role          string `json:"role"`
	} `json:"participantMetadata"`
}

type frame struct {
	RFC460Timestamp string    `json:"rfc460Timestamp"`
	GameState       string    `json:"gameState"`
	BlueTeam        teamStats `json:"blueTeam"`
	RedTeam         teamStats `json:"redTeam"`
}

type teamStats struct {
	TotalGold    int `json:"totalGold"`
	Inhibitors   int `json:"inhibitors"`
	Towers       int `json:"towers"`
	Barons       int `json:"barons"`
	TotalKills   int `json:"totalKills"`
	Dragons      []string `json:"dragons"`
	Participants []participant `json:"participants"`
}

type participant struct {
	ParticipantID int `json:"participantId"`
	TotalGold     int `json:"totalGold"`
	Level         int `json:"level"`
	Kills         int `json:"kills"`
	Deaths        int `json:"deaths"`
	Assists       int `json:"assists"`
	CreepScore    int `json:"creepScore"`
	CurrentHealth int `json:"currentHealth"`
	MaxHealth     int `json:"maxHealth"`
}

// --------------------------- helpers ---------------------------

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// latestStartingTime returns a 10s-aligned timestamp ~40s ago, formatted for the live feed.
func latestStartingTime() string {
	t := time.Now().UTC().Add(-40 * time.Second)
	t = t.Add(-time.Duration(t.Second()%10) * time.Second)
	t = t.Truncate(time.Second)
	return t.Format("2006-01-02T15:04:05.000Z")
}

func leagueID(slug string) (string, bool) {
	if slug == "" {
		return "", true
	}
	var lr leaguesResp
	if err := getJSON(gwBase+"/getLeagues?hl=en-US", &lr); err != nil {
		return "", false
	}
	for _, l := range lr.Data.Leagues {
		if strings.EqualFold(l.Slug, slug) || strings.EqualFold(l.Name, slug) {
			return l.ID, true
		}
	}
	return "", false
}

func gold(g int) string {
	if g >= 1000 {
		return fmt.Sprintf("%.1fk", float64(g)/1000)
	}
	return fmt.Sprintf("%d", g)
}

func kda(k, d, a int) string {
	return fmt.Sprintf("%d/%d/%d", k, d, a)
}

func hpBar(cur, max int) string {
	if max <= 0 {
		return "-"
	}
	frac := float64(cur) / float64(max)
	const blocks = 8
	filled := int(frac * blocks)
	if filled > blocks {
		filled = blocks
	}
	if filled < 0 {
		filled = 0
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", blocks-filled) + fmt.Sprintf(" %d", cur)
}

func clearScreen() {
	fmt.Print("\033[H\033[2J")
}

// --------------------------- commands ---------------------------

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "lol":
		runLoL(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown game: %s\n\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Println(`esterm — esports terminal client

Usage:
  esterm <game> <command> [flags]

Games:
  lol    League of Legends (https://lolesports.com endpoints)

Run 'esterm <game> help' for game commands.`)
}

func runLoL(args []string) {
	if len(args) < 1 {
		lolUsage()
		os.Exit(1)
	}
	switch args[0] {
	case "bench":
		cmdBench(args[1:])
	case "demo":
		cmdDemo(args[1:])
	case "relay":
		cmdRelay(args[1:])
	case "live":
		cmdLive(args[1:])
	case "streams":
		cmdStreams(args[1:])
	case "standings":
		cmdStandings(args[1:])
	case "schedule":
		cmdSchedule(args[1:])
	case "leagues":
		cmdLeagues(args[1:])
	case "-h", "--help", "help":
		lolUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown lol command: %s\n\n", args[0])
		lolUsage()
		os.Exit(1)
	}
}

func lolUsage() {
	fmt.Println(`esterm lol — League of Legends esports (https://lolesports.com endpoints)

Usage:
  esterm lol relay      [-l <league>] [-i Dur] [-1]   ASCII live broadcast (tick 1s · poll 10s)
  esterm lol live       [-l <league>] [-w]            live games + real-time board
  esterm lol streams    [-l <league>]                 broadcast stream links (where to watch)
  esterm lol standings  [-l <league>]                 tournament bracket / standings
  esterm lol schedule   [-l <league>] [-n N] [-a]     upcoming matches (-a = recent)
  esterm lol leagues                                  list leagues & slugs

Flags:
  -l <league>   filter by slug (msi, worlds, lck, lpl, lec, lcs, ...)
  -w            watch mode: refresh every 30s
  -n N          number of matches (default 15)
  -a            show recent/completed instead of upcoming

Examples:
  esterm lol live -l msi -w
  esterm lol schedule -l lck -n 10
  esterm lol leagues`)
}

func cmdLeagues(args []string) {
	var lr leaguesResp
	if err := getJSON(gwBase+"/getLeagues?hl=en-US", &lr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Printf("  %-22s %-26s %s\n", "SLUG", "NAME", "ID")
	fmt.Println("  " + strings.Repeat("-", 64))
	type row struct{ slug, name, id string }
	rows := make([]row, 0, len(lr.Data.Leagues))
	for _, l := range lr.Data.Leagues {
		rows = append(rows, row{l.Slug, l.Name, l.ID})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].slug < rows[j].slug })
	for _, r := range rows {
		fmt.Printf("  %-22s %-26s %s\n", r.slug, r.name, r.id)
	}
}

func cmdLive(args []string) {
	fs := flag.NewFlagSet("lol live", flag.ExitOnError)
	league := fs.String("l", "", "league slug")
	watch := fs.Bool("w", false, "refresh every 30s")
	fs.Parse(args)

	if *watch {
		for {
			clearScreen()
			renderLive(*league)
			fmt.Println("\n  ↻ refreshing in 30s — Ctrl+C to quit")
			time.Sleep(30 * time.Second)
		}
	}
	renderLive(*league)
}

func renderLive(leagueSlug string) {
	lid, ok := leagueID(leagueSlug)
	if !ok {
		fmt.Fprintln(os.Stderr, "unknown league:", leagueSlug)
		return
	}
	url := gwBase + "/getLive?hl=en-US"
	if lid != "" {
		url += "&leagueId=" + lid
	}
	var lr scheduleResp
	if err := getJSON(url, &lr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return
	}
	events := lr.Data.Schedule.Events
	if len(events) == 0 {
		fmt.Println("  ⌛ No live games right now.")
		fmt.Println("    try: esterm lol schedule")
		return
	}
	for i, ev := range events {
		if i > 0 {
			fmt.Println("  " + strings.Repeat("─", 62))
		}
		renderLiveEvent(ev)
	}
}

func renderLiveEvent(ev event) {
	if ev.Match == nil {
		fmt.Printf("  LIVE  %s — %s\n", ev.League.Name, or(ev.BlockName, "Match"))
		return
	}
	codes := map[string]string{}
	for _, t := range ev.Match.Teams {
		codes[t.ID] = codeOr(t.Code, t.Name)
	}
	left, right := "TBD", "TBD"
	score := ""
	if len(ev.Match.Teams) >= 2 {
		left, right = codes[ev.Match.Teams[0].ID], codes[ev.Match.Teams[1].ID]
		if left == "" {
			left = "TBD"
		}
		if right == "" {
			right = "TBD"
		}
		if ev.Match.Teams[0].Result != nil && ev.Match.Teams[1].Result != nil {
			score = fmt.Sprintf("  series %s %d - %d %s",
				left, ev.Match.Teams[0].Result.GameWins,
				ev.Match.Teams[1].Result.GameWins, right)
		}
	}
	fmt.Printf("  🔴 LIVE  %s — %s vs %s  [%s]%s\n",
		strings.ToUpper(ev.League.Slug), left, right, or(ev.BlockName, "Match"), score)

	var liveGame *game
	gameNo := 0
	for i := range ev.Match.Games {
		if ev.Match.Games[i].State == "inProgress" {
			liveGame = &ev.Match.Games[i]
		}
		if ev.Match.Games[i].State != "unstarted" {
			gameNo++
		}
	}
	if liveGame == nil {
		fmt.Println("  (no single game currently in progress)")
		return
	}
	fmt.Printf("  Game %d\n", gameNo)

	var w windowResp
	if err := getJSON(feedBase+"/window/"+liveGame.ID+"?startingTime="+latestStartingTime(), &w); err != nil {
		fmt.Fprintln(os.Stderr, "  window error:", err)
		return
	}
	renderWindow(w, codes)
}

func renderWindow(w windowResp, codes map[string]string) {
	if len(w.Frames) == 0 {
		fmt.Println("  (waiting for in-game data...)")
		return
	}
	f := w.Frames[len(w.Frames)-1]
	bm := w.GameMetadata.BlueTeamMetadata
	rm := w.GameMetadata.RedTeamMetadata
	blueCode := codes[bm.EsportsTeamID]
	redCode := codes[rm.EsportsTeamID]

	fmt.Printf("  patch %s | %s | snap %s KST\n\n",
		w.GameMetadata.PatchVersion, f.GameState,
		parseTime(f.RFC460Timestamp).In(kst).Format("15:04:05"))

	b, r := f.BlueTeam, f.RedTeam
	fmt.Printf("  %-10s %6s %9s %7s %6s %6s %5s\n", "", "KILLS", "GOLD", "DRAG", "BARON", "TOWER", "INHIB")
	fmt.Printf("  %-10s %6d %9s %7d %6d %6d %5d\n", blueCode, b.TotalKills, gold(b.TotalGold), len(b.Dragons), b.Barons, b.Towers, b.Inhibitors)
	fmt.Printf("  %-10s %6d %9s %7d %6d %6d %5d\n", redCode, r.TotalKills, gold(r.TotalGold), len(r.Dragons), r.Barons, r.Towers, r.Inhibitors)

	fmt.Println()
	renderParticipants(bm, rm, b, r)
}

func renderParticipants(bm, rm teamMeta, b, r teamStats) {
	bm2 := mapMeta(bm)
	rm2 := mapMeta(rm)
	fmt.Printf("  %-24s %-12s %9s %5s %9s %s\n", "PLAYER", "CHAMPION", "K/D/A", "CS", "GOLD", "HP")
	for _, p := range b.Participants {
		m := bm2[p.ParticipantID]
		fmt.Printf("  %-24s %-12s %9s %5d %9s %s\n",
			trunc(m.SummonerName, 24), m.ChampionID, kda(p.Kills, p.Deaths, p.Assists),
			p.CreepScore, gold(p.TotalGold), hpBar(p.CurrentHealth, p.MaxHealth))
	}
	for _, p := range r.Participants {
		m := rm2[p.ParticipantID]
		fmt.Printf("  %-24s %-12s %9s %5d %9s %s\n",
			trunc(m.SummonerName, 24), m.ChampionID, kda(p.Kills, p.Deaths, p.Assists),
			p.CreepScore, gold(p.TotalGold), hpBar(p.CurrentHealth, p.MaxHealth))
	}
}

func mapMeta(m teamMeta) map[int]struct {
	SummonerName string
	ChampionID   string
	Role         string
} {
	out := make(map[int]struct {
		SummonerName string
		ChampionID   string
		Role         string
	}, len(m.Participants))
	for _, p := range m.Participants {
		out[p.ParticipantID] = struct {
			SummonerName string
			ChampionID   string
			Role         string
		}{p.SummonerName, p.ChampionID, p.Role}
	}
	return out
}

func cmdSchedule(args []string) {
	fs := flag.NewFlagSet("lol schedule", flag.ExitOnError)
	league := fs.String("l", "", "league slug")
	n := fs.Int("n", 15, "number of matches")
	past := fs.Bool("a", false, "show recent/completed")
	fs.Parse(args)

	lid, ok := leagueID(*league)
	if !ok {
		fmt.Fprintln(os.Stderr, "unknown league:", *league)
		return
	}
	url := gwBase + "/getSchedule?hl=en-US"
	if lid != "" {
		url += "&leagueId=" + lid
	}
	var sr scheduleResp
	if err := getJSON(url, &sr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return
	}
	now := time.Now()
	var shown []event
	for _, ev := range sr.Data.Schedule.Events {
		if ev.Type != "match" {
			continue
		}
		t := parseTime(ev.StartTime)
		if *past {
			if ev.State == "completed" || (!t.IsZero() && t.Before(now)) {
				shown = append(shown, ev)
			}
		} else {
			if ev.State != "completed" && !t.IsZero() && t.After(now.Add(-3*time.Hour)) {
				shown = append(shown, ev)
			}
		}
	}
	if len(shown) == 0 {
		fmt.Println("  (no matches found — try a different league: esterm lol leagues)")
		return
	}
	sort.SliceStable(shown, func(i, j int) bool {
		return parseTime(shown[i].StartTime).Before(parseTime(shown[j].StartTime))
	})
	if *past {
		// most recent first
		for i, j := 0, len(shown)-1; i < j; i, j = i+1, j-1 {
			shown[i], shown[j] = shown[j], shown[i]
		}
	}
	if len(shown) > *n {
		shown = shown[:*n]
	}

	title := "UPCOMING"
	if *past {
		title = "RECENT"
	}
	leagueLabel := "ALL LEAGUES"
	if *league != "" {
		leagueLabel = strings.ToUpper(*league)
	}
	fmt.Printf("  %s — %s\n", title, leagueLabel)
	fmt.Println("  " + strings.Repeat("─", 56))
	fmt.Printf("  %-5s %-5s %-12s %-16s %s\n", "DATE", "TIME", "LEAGUE", "MATCH", "SCORE")
	for _, ev := range shown {
		t := parseTime(ev.StartTime).In(kst)
		left, right := "TBD", "TBD"
		if ev.Match != nil && len(ev.Match.Teams) >= 2 {
			left = codeOr(ev.Match.Teams[0].Code, "")
			right = codeOr(ev.Match.Teams[1].Code, "")
			if left == "" {
				left = "TBD"
			}
			if right == "" {
				right = "TBD"
			}
		}
		matchStr := fmt.Sprintf("%-5s vs %-5s", left, right)
		score := ""
		if ev.Match != nil && len(ev.Match.Teams) >= 2 &&
			ev.Match.Teams[0].Result != nil && ev.Match.Teams[1].Result != nil {
			gw0 := ev.Match.Teams[0].Result.GameWins
			gw1 := ev.Match.Teams[1].Result.GameWins
			if ev.State == "completed" || gw0+gw1 > 0 {
				score = fmt.Sprintf("%d-%d", gw0, gw1)
				if ev.Match.Teams[0].Result.Outcome != nil && *ev.Match.Teams[0].Result.Outcome == "win" {
					matchStr = fmt.Sprintf("%-5s vs %-5s", left+"*", right)
				} else if ev.Match.Teams[1].Result.Outcome != nil && *ev.Match.Teams[1].Result.Outcome == "win" {
					matchStr = fmt.Sprintf("%-5s vs %-5s", left, right+"*")
				}
			}
		}
		if ev.State == "inProgress" {
			score = score + "  [LIVE]"
		}
		league := strings.ToUpper(ev.League.Slug)
		if len(league) > 12 {
			league = league[:12]
		}
		fmt.Printf("  %-5s %-5s %-12s %-16s %s\n",
			t.Format("01-02"), t.Format("15:04"), league, matchStr, score)
	}
}

// --------------------------- small utils ---------------------------

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func codeOr(code, name string) string {
	if code != "" {
		return code
	}
	return name
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func fmtStr(m *match) string {
	if m == nil {
		return ""
	}
	// number of games as best-of hint
	return fmt.Sprintf("Bo%d", max(1, len(m.Games)))
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
