package main

import (
	"context"
	"errors"
	"image"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// hangNetwork replaces the HTTP client by one whose requests block until the test ends and
// isolates the Data Dragon caches, so nothing touches the real network or disk cache.
func hangNetwork(t *testing.T) *atomic.Int32 {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var calls atomic.Int32
	release := make(chan struct{})
	old := httpClient
	httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
		return nil, errors.New("network down")
	})}
	ddLatestMu.Lock()
	oldLatest := ddLatest
	ddLatest = "9.9.9"
	ddLatestMu.Unlock()
	ddImgMu.Lock()
	oldMemo, oldVers := ddImgMemo, ddVers
	ddImgMemo, ddVers = map[string]image.Image{}, nil
	ddImgMu.Unlock()
	t.Cleanup(func() {
		close(release)
		waitDDIdle(t) // no background load may still read httpClient
		httpClient = old
		ddLatestMu.Lock()
		ddLatest = oldLatest
		ddLatestMu.Unlock()
		ddImgMu.Lock()
		ddImgMemo, ddVers = oldMemo, oldVers
		ddImgMu.Unlock()
	})
	return &calls
}

// waitDDIdle blocks until every background image load has returned.
func waitDDIdle(t *testing.T) {
	t.Helper()
	idle := func() bool {
		ddImgMu.Lock()
		n := len(ddFlights)
		ddImgMu.Unlock()
		busy := func(a *ddAsync) bool {
			a.mu.Lock()
			defer a.mu.Unlock()
			return a.loading
		}
		if n > 0 || busy(&mmBg.load) {
			return false
		}
		mmIconMemo.Lock()
		defer mmIconMemo.Unlock()
		for _, a := range mmIconMemo.m {
			if busy(a) {
				return false
			}
		}
		return true
	}
	for end := time.Now().Add(3 * time.Second); !idle(); {
		if time.Now().After(end) {
			t.Fatal("background loads did not finish")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// The render path asks for icons and the map every tick: with a hanging network those
// requests must neither block the caller nor pile up.
func TestRenderNeverBlocksOnNetwork(t *testing.T) {
	calls := hangNetwork(t)
	mmBg.Lock()
	mmBg.img, mmBg.cache = nil, nil
	mmBg.Unlock()
	mmBg.load = ddAsync{}
	mmIconMemo.Lock()
	mmIconMemo.m = nil
	mmIconMemo.Unlock()

	view := demoMinimap()
	view.Ver = "9.9.9"
	view.CellW, view.CellH = 8, 16
	view.Mode = mmModeKitty
	done := make(chan struct{})
	go func() {
		defer close(done)
		var kit kittyOut
		now := time.Now()
		for i := 0; i < 20; i++ {
			cv := demoCanvasAt(120, 40, now.Add(time.Duration(i)*500*time.Millisecond), 2*time.Second, detailsResp{}, toggles{mm: true}, nil, view)
			kit.Frame(cv.mm, i == 0)
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("rendering blocked on a hanging network")
	}
	time.Sleep(100 * time.Millisecond)
	// one load for the map plus at most one per distinct champion icon
	if n := calls.Load(); n > 12 {
		t.Errorf("render ticks started %d requests, want a bounded number", n)
	}
}

func TestDDImageSharesOneFlightWithoutHoldingLock(t *testing.T) {
	calls := hangNetwork(t)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ddImage(ctx, "9.9.9", "img/map/map11.png")
		}()
	}
	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	peeked := make(chan struct{})
	go func() { // would deadlock if the memo lock were held across the HTTP request
		ddPeek("9.9.9", "img/map/map11.png")
		close(peeked)
	}()
	select {
	case <-peeked:
	case <-time.After(time.Second):
		t.Fatal("ddImgMu is held while a download is in flight")
	}
	cancel()
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Errorf("8 concurrent callers made %d requests, want 1", n)
	}
}

func TestDDAsyncRetriesWithBackoff(t *testing.T) {
	var a ddAsync
	var loads atomic.Int32
	fail := func(context.Context) (image.Image, error) {
		loads.Add(1)
		return nil, errors.New("offline")
	}
	for i := 0; i < 50; i++ {
		a.get(fail)
		time.Sleep(time.Millisecond)
	}
	if n := loads.Load(); n != 1 {
		t.Fatalf("%d loads in a burst of ticks, want 1 (backoff)", n)
	}
	a.mu.Lock()
	a.next = time.Time{} // backoff elapsed
	a.mu.Unlock()
	ok := image.NewRGBA(image.Rect(0, 0, 1, 1))
	var got image.Image
	for i := 0; i < 200 && got == nil; i++ {
		got = a.get(func(context.Context) (image.Image, error) { return ok, nil })
		time.Sleep(time.Millisecond)
	}
	if got != image.Image(ok) {
		t.Fatal("a failure must not be cached forever: the retry should deliver the image")
	}
}

func TestDDVersionEviction(t *testing.T) {
	hangNetwork(t)
	ddImgMu.Lock()
	for _, v := range []string{"1.0.1", "1.0.2", "1.0.3"} {
		ddImgMemo[v+"/x.png"] = image.NewRGBA(image.Rect(0, 0, 1, 1))
		ddRememberVersion(v)
	}
	_, old := ddImgMemo["1.0.1/x.png"]
	_, cur := ddImgMemo["1.0.3/x.png"]
	ddImgMu.Unlock()
	if old || !cur {
		t.Errorf("memo keeps old=%v current=%v, want old evicted and current kept", old, cur)
	}
}

func TestDDPruneDiskKeepsOnlyListedVersions(t *testing.T) {
	hangNetwork(t)
	root := ddCacheRoot()
	for _, v := range []string{"1.0.1", "1.0.2", "1.0.3"} {
		if err := os.MkdirAll(filepath.Join(root, v, "img"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ddPruneDisk([]string{"1.0.3", "1.0.2"})
	for v, want := range map[string]bool{"1.0.1": false, "1.0.2": true, "1.0.3": true} {
		if _, err := os.Stat(filepath.Join(root, v)); (err == nil) != want {
			t.Errorf("version %s present=%v, want %v", v, err == nil, want)
		}
	}
}
