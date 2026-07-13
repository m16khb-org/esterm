package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// Data Dragon static-data resolver (item & rune names).
// Loaded lazily on first need; graceful fallback to IDs if unreachable.

var (
	ddMu      sync.Mutex
	ddItems   map[int]string
	ddRunes   map[int]string // perk id -> name (includes keystones + tree ids)
	ddVersion string
)

func majorMinor(patch string) string {
	p := strings.SplitN(patch, ".", 3)
	if len(p) < 2 {
		return ""
	}
	return p[0] + "." + p[1]
}

func ddGet(url string, v any) bool {
	resp, err := http.Get(url)
	if err != nil || resp == nil || resp.StatusCode != 200 {
		if resp != nil {
			resp.Body.Close()
		}
		return false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return json.Unmarshal(body, v) == nil
}

// resolveDDVersion picks the exact ddragon version for a game patch,
// matching the patch's major.minor against the published version list.
func resolveDDVersion(patch string) string {
	base := majorMinor(patch)
	if base == "" {
		return ""
	}
	var vers []string
	if ddGet("https://ddragon.leagueoflegends.com/api/versions.json", &vers) && len(vers) > 0 {
		for i := len(vers) - 1; i >= 0; i-- {
			if strings.HasPrefix(vers[i], base+".") {
				return vers[i]
			}
		}
	}
	return base + ".1"
}

func ensureDDragon(patch string) {
	v := resolveDDVersion(patch)
	if v == "" {
		return
	}
	ddMu.Lock()
	defer ddMu.Unlock()
	if ddVersion == v && ddItems != nil {
		return
	}
	if it := loadItems(v); it != nil {
		ddItems = it
	}
	if ru := loadRunes(v); ru != nil {
		ddRunes = ru
	}
	ddVersion = v
}

func loadItems(version string) map[int]string {
	var raw struct {
		Data map[string]struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	if !ddGet("https://ddragon.leagueoflegends.com/cdn/"+version+"/data/en_US/item.json", &raw) {
		return nil
	}
	m := map[int]string{}
	for id, it := range raw.Data {
		var n int
		fmt.Sscanf(id, "%d", &n)
		if n > 0 {
			m[n] = it.Name
		}
	}
	return m
}

func loadRunes(version string) map[int]string {
	var trees []struct {
		ID    int    `json:"id"`
		Name  string `json:"name"`
		Slots []struct {
			Runes []struct {
				ID   int    `json:"id"`
				Name string `json:"name"`
			} `json:"runes"`
		} `json:"slots"`
	}
	if !ddGet("https://ddragon.leagueoflegends.com/cdn/"+version+"/data/en_US/runesReforged.json", &trees) {
		return nil
	}
	m := map[int]string{}
	for _, t := range trees {
		m[t.ID] = t.Name
		for _, s := range t.Slots {
			for _, r := range s.Runes {
				m[r.ID] = r.Name
			}
		}
	}
	return m
}

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

// itemShort trims an item name to a compact form (drop trailing articles/suffixes).
func itemShort(id int, max int) string {
	n := itemName(id)
	// use the last word (usually the distinctive noun: Cleaver, Greaves, Hourglass)
	if f := strings.Fields(n); len(f) > 1 {
		n = f[len(f)-1]
	}
	return trim(n, max)
}
