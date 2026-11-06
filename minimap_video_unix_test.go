//go:build !windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Tests that run shell-script stand-ins for the tools or watch process groups.

// A page source without -ss may be a live stream, so a failing yt-dlp is retried with
// backoff instead of ending the pipeline.
func TestMMLoopRetriesPageSourceWhenResolveFails(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "yt-dlp")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho boom >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ESTERM_YTDLP", fake)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	attempts := 0
	status := func(s string) {
		if s == "resolving stream" {
			attempts++
			if attempts == 2 {
				cancel()
			}
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		mmLoop(ctx, mmVideoOpts{Src: "https://www.youtube.com/watch?v=x", FPS: 2}, true, false, status, make(chan mmFrame, 1))
	}()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("mmLoop did not stop")
	}
	if attempts < 2 {
		t.Fatalf("resolve attempted %d time(s); want a retry after the first failure", attempts)
	}
}

// A finished YouTube broadcast served as a live archive is not seekable by ffmpeg; the
// pipeline must say so instead of stalling until the read timeout.
func TestMMResolveRejectsALiveArchiveVOD(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "yt-dlp")
	script := "#!/bin/sh\nprintf 'False\\n1080\\nhttps://h/videoplayback?live=1&hang=1\\n'\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ESTERM_YTDLP", fake)
	_, live, _, err := mmResolve(context.Background(), "https://www.youtube.com/watch?v=x")
	if err == nil || live {
		t.Fatalf("got live=%v err=%v, want a seekability error", live, err)
	}
}

func TestMMLoopStopsOnALiveArchive(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "yt-dlp")
	script := "#!/bin/sh\nprintf 'False\\n1080\\nhttps://h/videoplayback?live=1&hang=1\\n'\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ESTERM_YTDLP", fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		mmLoop(ctx, mmVideoOpts{Src: "https://www.youtube.com/watch?v=x", FPS: 2}, true, false, func(string) {}, make(chan mmFrame, 1))
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("mmLoop kept retrying a live archive")
	}
}

// TestMmStartParentDeath re-execs the test binary as a parent that spawns a long sleeper via
// mmStart; after SIGKILLing that parent the sleeper's group must disappear.
func TestMmStartParentDeath(t *testing.T) {
	if os.Getenv("ESTERM_TEST_PARENT") == "1" {
		cmd := exec.Command("/bin/sleep", "120")
		if _, err := mmStart(context.Background(), cmd); err != nil {
			os.Exit(2)
		}
		fmt.Println(cmd.Process.Pid)
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	p := exec.Command(os.Args[0], "-test.run=TestMmStartParentDeath")
	p.Env = append(os.Environ(), "ESTERM_TEST_PARENT=1")
	out, err := p.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	var pid int
	if _, err := fmt.Fscan(out, &pid); err != nil || pid <= 0 {
		t.Fatalf("child pid: %v", err)
	}
	if syscall.Kill(-pid, 0) != nil {
		t.Fatal("child group not running")
	}
	p.Process.Kill()
	p.Wait()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(-pid, 0) != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	syscall.Kill(-pid, syscall.SIGKILL)
	t.Fatal("child group survived parent SIGKILL")
}
