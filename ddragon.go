package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
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

func ddGet(ctx context.Context, url string, v any) bool {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		drainBody(resp.Body)
		return false
	}
	body, _ := io.ReadAll(resp.Body)
	return json.Unmarshal(body, v) == nil
}

// resolveDDVersion picks the exact ddragon version for a game patch,
// matching the patch's major.minor against the published version list.
func resolveDDVersion(ctx context.Context, patch string) string {
	base := majorMinor(patch)
	if base == "" {
		return ""
	}
	var vers []string
	if ddGet(ctx, "https://ddragon.leagueoflegends.com/api/versions.json", &vers) && len(vers) > 0 {
		for i := len(vers) - 1; i >= 0; i-- {
			if strings.HasPrefix(vers[i], base+".") {
				return vers[i]
			}
		}
	}
	return base + ".1"
}

func ensureDDragon(patch string) {
	v := resolveDDVersion(context.Background(), patch)
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

var (
	ddImgMu    sync.Mutex
	ddImgMemo  = map[string]image.Image{}
	ddFlights  = map[string]*ddFlight{}
	ddVers     []string // versions with memoized images, most recent first (at most ddKeepVersions)
	ddLatestMu sync.Mutex
	ddLatest   string
)

// ddKeepVersions is how many Data Dragon versions stay in memory and on disk: the map is
// served from the newest release while icons follow the match patch, so two are in use.
const ddKeepVersions = 2

// ddFlight is one in-progress image load shared by every concurrent caller of its key.
type ddFlight struct {
	done chan struct{}
	img  image.Image
	err  error
}

// ddLatestVersion returns the newest Data Dragon version (memoized on success).
func ddLatestVersion(ctx context.Context) (string, error) {
	ddLatestMu.Lock()
	defer ddLatestMu.Unlock()
	if ddLatest != "" {
		return ddLatest, nil
	}
	var vers []string
	if !ddGet(ctx, "https://ddragon.leagueoflegends.com/api/versions.json", &vers) || len(vers) == 0 {
		return "", fmt.Errorf("ddragon: cannot load version list")
	}
	ddLatest = vers[0]
	return ddLatest, nil
}

// ddImage returns a Data Dragon PNG (e.g. "img/map/map11.png"), served from the
// in-memory memo, then the disk cache under UserCacheDir/esterm/ddragon, then the network.
// Concurrent calls for one key share a single load; no lock is held during I/O.
func ddImage(ctx context.Context, version, path string) (image.Image, error) {
	key := version + "/" + path
	for {
		ddImgMu.Lock()
		if img, ok := ddImgMemo[key]; ok {
			ddImgMu.Unlock()
			return img, nil
		}
		if f, ok := ddFlights[key]; ok {
			ddImgMu.Unlock()
			select {
			case <-f.done:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			if f.err != nil && ctx.Err() == nil && errors.Is(f.err, context.Canceled) {
				continue // the leader gave up, not this caller
			}
			return f.img, f.err
		}
		if err := ctx.Err(); err != nil { // a caller that already gave up starts no download
			ddImgMu.Unlock()
			return nil, err
		}
		f := &ddFlight{done: make(chan struct{})}
		ddFlights[key] = f
		ddImgMu.Unlock()

		var fetched bool
		f.img, fetched, f.err = ddLoadImage(ctx, key)

		ddImgMu.Lock()
		delete(ddFlights, key)
		var keep []string
		if f.err == nil {
			ddImgMemo[key] = f.img
			if fresh := ddRememberVersion(version); fresh && fetched {
				keep = append(keep, ddVers...)
			}
		}
		ddImgMu.Unlock()
		close(f.done)
		if keep != nil {
			ddPruneDisk(keep)
		}
		return f.img, f.err
	}
}

// ddRememberVersion marks version as the most recently used and evicts the memoized images
// of versions beyond ddKeepVersions. It reports whether version was newly added. Call with
// ddImgMu held.
func ddRememberVersion(version string) bool {
	if len(ddVers) > 0 && ddVers[0] == version {
		return false
	}
	fresh := true
	for i, v := range ddVers {
		if v == version {
			fresh = false
			ddVers = append(ddVers[:i], ddVers[i+1:]...)
			break
		}
	}
	ddVers = append([]string{version}, ddVers...)
	for len(ddVers) > ddKeepVersions {
		old := ddVers[len(ddVers)-1] + "/"
		ddVers = ddVers[:len(ddVers)-1]
		for k := range ddImgMemo {
			if strings.HasPrefix(k, old) {
				delete(ddImgMemo, k)
			}
		}
	}
	return fresh
}

func ddCacheRoot() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "esterm", "ddragon")
}

// ddPruneDisk deletes every cached version directory except the kept ones.
func ddPruneDisk(keep []string) {
	root := ddCacheRoot()
	if root == "" {
		return
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		return
	}
next:
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		for _, k := range keep {
			if e.Name() == k {
				continue next
			}
		}
		os.RemoveAll(filepath.Join(root, e.Name()))
	}
}

// ddLoadImage reads key from the disk cache or the network; fetched reports a network
// download that was written to the disk cache.
func ddLoadImage(ctx context.Context, key string) (img image.Image, fetched bool, err error) {
	var cache string
	if root := ddCacheRoot(); root != "" {
		cache = filepath.Join(root, filepath.FromSlash(key))
	}
	return loadCachedImage(ctx, cache, "https://ddragon.leagueoflegends.com/cdn/"+key)
}

// loadCachedImage returns the PNG at url from the disk cache file cache (empty: no cache)
// or else downloads it and writes the cache atomically.
func loadCachedImage(ctx context.Context, cache, url string) (img image.Image, fetched bool, err error) {
	if cache != "" {
		if f, err := os.Open(cache); err == nil {
			img, derr := png.Decode(f)
			f.Close()
			if derr == nil {
				return img, false, nil
			}
		}
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, false, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		drainBody(resp.Body)
		return nil, false, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, err
	}
	img, err = png.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	if cache != "" && os.MkdirAll(filepath.Dir(cache), 0o755) == nil {
		if tmp, err := os.CreateTemp(filepath.Dir(cache), ".tmp-*"); err == nil {
			_, werr := tmp.Write(body)
			cerr := tmp.Close()
			if werr != nil || cerr != nil || os.Rename(tmp.Name(), cache) != nil {
				os.Remove(tmp.Name())
			} else {
				fetched = true
			}
		}
	}
	return img, fetched, nil
}

// drainBody discards a bounded amount of an unread response body so the connection can be
// reused.
func drainBody(r io.Reader) { io.Copy(io.Discard, io.LimitReader(r, 64<<10)) }

func loadItems(version string) map[int]string {
	var raw struct {
		Data map[string]struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	if !ddGet(context.Background(), "https://ddragon.leagueoflegends.com/cdn/"+version+"/data/en_US/item.json", &raw) {
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
	if !ddGet(context.Background(), "https://ddragon.leagueoflegends.com/cdn/"+version+"/data/en_US/runesReforged.json", &trees) {
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

// ddPeek returns an already memoized Data Dragon image, or nil. It never loads.
func ddPeek(version, path string) image.Image {
	ddImgMu.Lock()
	defer ddImgMu.Unlock()
	return ddImgMemo[version+"/"+path]
}

// ddRetryMin and ddRetryMax bound the delay before a failed background load is retried.
const (
	ddRetryMin = 2 * time.Second
	ddRetryMax = 5 * time.Minute
)

// ddAsync loads one image in the background for callers that must not block (the render
// path): get returns nil until the load succeeded, runs at most one load at a time and
// retries after an exponentially growing delay when a load fails.
type ddAsync struct {
	mu      sync.Mutex
	img     image.Image
	loading bool
	next    time.Time
	fails   int
}

func (a *ddAsync) get(load func(context.Context) (image.Image, error)) image.Image {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.img != nil || a.loading || time.Now().Before(a.next) {
		return a.img
	}
	a.loading = true
	go func() {
		img, err := load(context.Background())
		a.mu.Lock()
		defer a.mu.Unlock()
		a.loading = false
		if err != nil || img == nil {
			a.fails++
			a.next = time.Now().Add(min(ddRetryMin<<uint(min(a.fails-1, 8)), ddRetryMax))
			return
		}
		a.img = img
	}()
	return nil
}

// cdMinimapIcons is where CommunityDragon serves the game's own minimap icons: the dragon
// pit icon per drake type ("dragon_infernal", "dragon_elder", ...), "dragon" for a drake
// whose type is not known yet, and "baron". They are tiny (64 px) and stable, so the newest
// ones are used whatever the game's patch and are kept on disk without a version.
const cdMinimapIcons = "https://raw.communitydragon.org/latest/game/assets/ux/minimap/icons/"

var objIconMemo struct {
	sync.Mutex
	m map[string]*ddAsync
}

// objIconImg returns the minimap icon called name (see cdMinimapIcons) or nil while it is
// not available yet. It never blocks: a missing icon is loaded in the background from the
// disk cache or the network (retried with backoff after a failure) and the next call picks
// it up.
func objIconImg(name string) image.Image {
	objIconMemo.Lock()
	a := objIconMemo.m[name]
	if a == nil {
		if objIconMemo.m == nil {
			objIconMemo.m = map[string]*ddAsync{}
		}
		a = &ddAsync{}
		objIconMemo.m[name] = a
	}
	objIconMemo.Unlock()
	return a.get(func(ctx context.Context) (image.Image, error) {
		var cache string
		if root := ddCacheRoot(); root != "" {
			cache = filepath.Join(filepath.Dir(root), "cdragon", "minimap", name+".png")
		}
		img, _, err := loadCachedImage(ctx, cache, cdMinimapIcons+name+".png")
		return img, err
	})
}
