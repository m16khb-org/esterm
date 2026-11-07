package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGetJSONReportsHTTPStatus(t *testing.T) {
	old := httpClient.Transport
	httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"httpStatus":400,"errorCode":"BAD_QUERY_PARAMETER","message":"disallowed window ahead of broadcast"}`
		return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: r}, nil
	})
	defer func() { httpClient.Transport = old }()

	var w windowResp
	err := getJSON("https://feed.example/window/g1", &w)
	if err == nil {
		t.Fatal("a 400 response must not look like an empty feed")
	}
	for _, want := range []string{"400", "BAD_QUERY_PARAMETER", "ahead of broadcast"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

func TestGetJSONAcceptsEmptyFeed(t *testing.T) {
	old := httpClient.Transport
	httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("  \n")), Header: http.Header{}, Request: r}, nil
	})
	defer func() { httpClient.Transport = old }()

	var w windowResp
	if err := getJSON("https://feed.example/window/g1", &w); err != nil {
		t.Fatalf("an empty window is still a successful response: %v", err)
	}
}
