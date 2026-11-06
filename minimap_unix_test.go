//go:build !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Tests that run a shell-script stand-in for ffmpeg.

func TestRunSourceGivesUpOnUnreadableStream(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ESTERM_FFMPEG", fake)
	m := newMinimap(minimapCfg{})
	m.lockTimeout = 100 * time.Millisecond
	m.gen = 1
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	if !m.runSource(ctx, 1, "/nonexistent/a.mp4", nil, nil, true) {
		t.Fatal("did not give up on a stream without a layout")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("gave up too late")
	}
	if !strings.Contains(m.srcNote, "trying next stream") {
		t.Fatalf("note %q", m.srcNote)
	}
	last, stop := context.WithTimeout(ctx, 500*time.Millisecond)
	defer stop()
	if m.runSource(last, 1, "/nonexistent/a.mp4", nil, nil, false) {
		t.Fatal("the last stream must not report a fallback")
	}
	if m.srcNote != "HUD unreadable" {
		t.Fatalf("note %q", m.srcNote)
	}
}
