package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
)

// ---------------- shared types for event details / standings ----------------
type streamInfo struct {
	Provider  string `json:"provider"`
	Locale    string `json:"locale"`
	Parameter string `json:"parameter"`
	MediaLocale struct {
		EnglishName string `json:"englishName"`
	} `json:"mediaLocale"`
}

type eventDetailsResp struct {
	Data struct {
		Event struct {
			ID         string       `json:"id"`
			Tournament struct{ ID string `json:"id"` } `json:"tournament"`
			Streams    []streamInfo `json:"streams"`
		} `json:"event"`
	} `json:"data"`
}

type bracketTeam struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Result *struct {
		Outcome  string `json:"outcome"`
		GameWins int    `json:"gameWins"`
	} `json:"result"`
}
type bracketMatch struct {
	State string        `json:"state"`
	Teams []bracketTeam `json:"teams"`
}
type standingsResp struct {
	Data struct {
		Standings []struct {
			Stages []struct {
				Name     string `json:"name"`
				Sections []struct {
					Name     string          `json:"name"`
					Matches  []bracketMatch `json:"matches"`
					Rankings []struct {
						Teams []bracketTeam `json:"teams"`
					} `json:"rankings"`
				} `json:"sections"`
			} `json:"stages"`
		} `json:"standings"`
	} `json:"data"`
}

// findEventID returns the id of the live event for a league (or next upcoming).
// eventID returns the match id (schedule events expose id only at match.id).
func eventID(e event) string {
	if e.Match != nil && e.Match.ID != "" {
		return e.Match.ID
	}
	return e.ID
}

func findEventID(lid string) string {
	var lr scheduleResp
	url := gwBase + "/getLive?hl=en-US"
	if lid != "" {
		url += "&leagueId=" + lid
	}
	if getJSON(url, &lr) == nil {
		for _, e := range lr.Data.Schedule.Events {
			if e.State == "inProgress" {
				return eventID(e)
			}
		}
		if len(lr.Data.Schedule.Events) > 0 {
			return eventID(lr.Data.Schedule.Events[0])
		}
	}
	var sr scheduleResp
	url2 := gwBase + "/getSchedule?hl=en-US"
	if lid != "" {
		url2 += "&leagueId=" + lid
	}
	getJSON(url2, &sr)
	for _, e := range sr.Data.Schedule.Events {
		if e.State != "completed" {
			return eventID(e)
		}
	}
	if len(sr.Data.Schedule.Events) > 0 {
		return eventID(sr.Data.Schedule.Events[0])
	}
	return ""
}

func streamURL(provider, param string) string {
	switch strings.ToLower(provider) {
	case "youtube":
		return "https://www.youtube.com/watch?v=" + param
	case "twitch":
		return "https://www.twitch.tv/" + param
	case "afreecatv":
		return "https://play.afreecatv.com/" + param
	default:
		if strings.Contains(param, "://") {
			return param
		}
		return param
	}
}

func cmdStreams(args []string) {
	fs := flag.NewFlagSet("streams", flag.ExitOnError)
	league := fs.String("l", "", "league slug")
	fs.Parse(args)
	lid, ok := leagueID(*league)
	if !ok {
		fmt.Fprintln(os.Stderr, "unknown league:", *league)
		return
	}
	eid := findEventID(lid)
	if eid == "" {
		fmt.Println("  no event found — try: lole schedule")
		return
	}
	var ed eventDetailsResp
	if err := getJSON(gwBase+"/getEventDetails?hl=en-US&id="+eid, &ed); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return
	}
	streams := ed.Data.Event.Streams
	if len(streams) == 0 {
		fmt.Println("  no broadcast streams for this event")
		return
	}
	// prefer Korean, then English, then rest alphabetical
	sort.SliceStable(streams, func(i, j int) bool {
		rank := func(l string) int {
			switch {
			case strings.HasPrefix(l, "ko"):
				return 0
			case strings.HasPrefix(l, "en"):
				return 1
			default:
				return 2
			}
		}
		ri, rj := rank(streams[i].Locale), rank(streams[j].Locale)
		if ri != rj {
			return ri < rj
		}
		return streams[i].Locale < streams[j].Locale
	})
	fmt.Printf("  %-26s %-9s %s\n", "LANGUAGE", "PROVIDER", "URL")
	fmt.Println("  " + strings.Repeat("-", 70))
	for _, s := range streams {
		name := s.MediaLocale.EnglishName
		if name == "" {
			name = s.Locale
		}
		fmt.Printf("  %-26s %-9s %s\n", name, s.Provider, streamURL(s.Provider, s.Parameter))
	}
}

func cmdStandings(args []string) {
	fs := flag.NewFlagSet("standings", flag.ExitOnError)
	league := fs.String("l", "", "league slug")
	fs.Parse(args)
	lid, ok := leagueID(*league)
	if !ok {
		fmt.Fprintln(os.Stderr, "unknown league:", *league)
		return
	}
	eid := findEventID(lid)
	if eid == "" {
		fmt.Println("  no event found")
		return
	}
	var ed eventDetailsResp
	if err := getJSON(gwBase+"/getEventDetails?hl=en-US&id="+eid, &ed); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return
	}
	tid := ed.Data.Event.Tournament.ID
	if tid == "" {
		fmt.Println("  no tournament found for this event")
		return
	}
	var sr standingsResp
	if err := getJSON(gwBase+"/getStandings?hl=en-US&tournamentId="+tid, &sr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return
	}
	if len(sr.Data.Standings) == 0 {
		fmt.Println("  no standings available")
		return
	}
	for _, st := range sr.Data.Standings[0].Stages {
		fmt.Printf("\n  ▸ %s\n", strings.ToUpper(st.Name))
		for _, sec := range st.Sections {
			if len(sec.Matches) == 0 && len(sec.Rankings) == 0 {
				continue
			}
			if sec.Name != "" && sec.Name != st.Name {
				fmt.Printf("    %s\n", sec.Name)
			}
			for _, m := range sec.Matches {
				codeA, codeB := "TBD", "TBD"
				winA, winB := 0, 0
				if len(m.Teams) > 0 && m.Teams[0].Code != "" {
					codeA = m.Teams[0].Code
				}
				if len(m.Teams) > 1 && m.Teams[1].Code != "" {
					codeB = m.Teams[1].Code
				}
				if len(m.Teams) > 0 && m.Teams[0].Result != nil {
					winA = m.Teams[0].Result.GameWins
				}
				if len(m.Teams) > 1 && m.Teams[1].Result != nil {
					winB = m.Teams[1].Result.GameWins
				}
				marker := " "
				if m.State == "inProgress" {
					marker = "🔴"
				} else if m.State == "unstarted" {
					marker = "·"
				} else if m.State == "completed" {
					marker = "✓"
				}
				fmt.Printf("    %s %-5s vs %-5s  %d-%d\n", marker, codeA, codeB, winA, winB)
			}
			for _, rk := range sec.Rankings {
				for _, t := range rk.Teams {
					fmt.Printf("      %-6s %s\n", t.Code, t.Name)
				}
			}
		}
	}
}
