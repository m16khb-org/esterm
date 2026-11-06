package main

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// iconStub serves flat PNGs of the given sizes: circle (CommunityDragon, by champion id in
// lower case, an absent entry is a 404), square (Data Dragon), and fails every request when
// down is set. It counts the CommunityDragon requests.
func iconStub(t *testing.T, circles map[string]int, down bool, plain ...map[string]int) *atomic.Int32 {
	t.Helper()
	isolateCache(t)
	var cd atomic.Int32
	flat := func(n int) []byte {
		im := image.NewRGBA(image.Rect(0, 0, n, n))
		for i := range im.Pix {
			im.Pix[i] = 200
		}
		im.Set(0, 0, color.RGBA{1, 2, 3, 255})
		var b bytes.Buffer
		png.Encode(&b, im)
		return b.Bytes()
	}
	reply := func(code int, body []byte) (*http.Response, error) {
		return &http.Response{StatusCode: code, Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{}}, nil
	}
	old := httpClient
	httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		u := r.URL.String()
		if strings.Contains(u, "communitydragon") {
			cd.Add(1)
			if down {
				return nil, errors.New("network down")
			}
			for champ, n := range circles {
				if strings.HasSuffix(u, "/"+champ+"/hud/"+champ+"_circle_0.png") {
					return reply(200, flat(n))
				}
			}
			for _, m := range plain {
				for champ, n := range m {
					if strings.HasSuffix(u, "/"+champ+"/hud/"+champ+"_circle.png") {
						return reply(200, flat(n))
					}
				}
			}
			return reply(404, nil)
		}
		return reply(200, flat(4)) // Data Dragon square
	})}
	ddImgMu.Lock()
	oldMemo, oldVers := ddImgMemo, ddVers
	ddImgMemo, ddVers = map[string]image.Image{}, nil
	ddImgMu.Unlock()
	t.Cleanup(func() {
		httpClient = old
		ddImgMu.Lock()
		ddImgMemo, ddVers = oldMemo, oldVers
		ddImgMu.Unlock()
	})
	return &cd
}

func TestChampIconIsTheMinimapCircle(t *testing.T) {
	iconStub(t, map[string]int{"qiyana": 8}, false)
	circles := true
	img, err := mmChampIcon(context.Background(), "9.9.9", "Qiyana", &circles)
	if err != nil || img.Bounds().Dx() != 8 {
		t.Fatalf("got %v, %v: want the 8 px circle, not the 4 px Data Dragon square", img, err)
	}
}

func TestChampIconFallsBackToTheSquareForAnUnknownCircle(t *testing.T) {
	cd := iconStub(t, nil, false)
	circles := true
	img, err := mmChampIcon(context.Background(), "9.9.9", "Qiyana", &circles)
	if err != nil || img.Bounds().Dx() != 4 {
		t.Fatalf("got %v, %v: want the 4 px square", img, err)
	}
	if !circles {
		t.Fatal("a champion CommunityDragon has no folder for says nothing about its being reachable")
	}
	if _, err := mmChampIcon(context.Background(), "9.9.9", "Ahri", &circles); err != nil || cd.Load() != 4 {
		t.Fatalf("the next champion is still asked for: err %v, CommunityDragon requests %d, want 4", err, cd.Load())
	}
}

func TestIconImgFallsBackToTheCircleWhenDDragonIsDown(t *testing.T) {
	isolateCache(t)
	flat := func(n int) []byte {
		im := image.NewRGBA(image.Rect(0, 0, n, n))
		for i := range im.Pix {
			im.Pix[i] = 200
		}
		var b bytes.Buffer
		png.Encode(&b, im)
		return b.Bytes()
	}
	old := httpClient
	httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.String(), "communitydragon") {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(flat(8))), Header: http.Header{}}, nil
		}
		return nil, errors.New("ddragon down")
	})}
	ddImgMu.Lock()
	oldMemo, oldFlights := ddImgMemo, ddFlights
	ddImgMemo, ddFlights = map[string]image.Image{}, map[string]*ddFlight{}
	ddImgMu.Unlock()
	mmIconMemo.Lock()
	oldIcons := mmIconMemo.m
	mmIconMemo.m = nil
	mmIconMemo.Unlock()
	t.Cleanup(func() {
		httpClient = old
		ddImgMu.Lock()
		ddImgMemo, ddFlights = oldMemo, oldFlights
		ddImgMu.Unlock()
		mmIconMemo.Lock()
		mmIconMemo.m = oldIcons
		mmIconMemo.Unlock()
	})
	deadline := time.Now().Add(2 * time.Second)
	for {
		if img := mmIconImg("9.9.9", "Qiyana"); img != nil {
			if img.Bounds().Dx() != 8 {
				t.Fatalf("got %v: want the 8 px CommunityDragon circle", img.Bounds())
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the circle fallback never loaded")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestChampIconUsesTheUnnumberedCircle(t *testing.T) {
	iconStub(t, nil, false, map[string]int{"rumble": 8})
	circles := true
	img, err := mmChampIcon(context.Background(), "9.9.9", "Rumble", &circles)
	if err != nil || img.Bounds().Dx() != 8 || !circles {
		t.Fatalf("got %v, %v, circles %v: want the 8 px unnumbered circle", img, err, circles)
	}
}

func TestChampIconStopsAskingAnUnreachableCommunityDragon(t *testing.T) {
	cd := iconStub(t, nil, true)
	circles := true
	for _, c := range []string{"Qiyana", "Ahri", "Jhin"} {
		img, err := mmChampIcon(context.Background(), "9.9.9", c, &circles)
		if err != nil || img.Bounds().Dx() != 4 {
			t.Fatalf("%s: got %v, %v: want the 4 px square", c, img, err)
		}
	}
	if cd.Load() != 1 {
		t.Fatalf("%d CommunityDragon requests, want 1: a host that does not answer is not asked again", cd.Load())
	}
}
