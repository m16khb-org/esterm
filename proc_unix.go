//go:build !windows

package main

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// mmStart starts cmd in its own process group and kills the whole group when ctx ends.
// The returned stop func optionally kills the group, then reaps the process.
func mmStart(ctx context.Context, cmd *exec.Cmd) (stop func(kill bool) error, err error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Parent-death watchdog: the child's group gets a background reader on a pipe whose write
	// end only lolterm holds. When lolterm dies for any reason (even SIGKILL) the kernel closes
	// it, the reader sees EOF and kills the whole group. Watcher stdio is /dev/null so it
	// never holds the child's stdout/stderr pipes open.
	pr, pw, perr := os.Pipe()
	if perr != nil {
		return nil, perr
	}
	cmd.Args = append([]string{"/bin/sh", "-c", `( read _ ; kill -KILL 0 ) <&3 >/dev/null 2>&1 & exec "$@" 3<&-`, "sh", cmd.Path}, cmd.Args[1:]...)
	cmd.Path = "/bin/sh"
	cmd.ExtraFiles = append([]*os.File{pr}, cmd.ExtraFiles...)
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return nil, err
	}
	pr.Close()
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		case <-done:
		}
	}()
	return func(kill bool) error {
		if kill {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		err := cmd.Wait()
		pw.Close()
		close(done)
		return err
	}, nil
}

// mmMainFD is the descriptor the child writes the main view to: ExtraFiles[1], [0] being
// the watchdog's (fd 3, see mmStart).
const mmMainFD = 4

// mmMainPipe is the second output of ffmpeg, the main view: an inherited pipe.
type mmMainPipe struct {
	target string // the ffmpeg output that writes to it
	r, w   *os.File
}

func newMMMainPipe() (*mmMainPipe, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	return &mmMainPipe{target: "pipe:" + strconv.Itoa(mmMainFD), r: r, w: w}, nil
}

// attach hands the write end to cmd, before mmStart.
func (p *mmMainPipe) attach(cmd *exec.Cmd) { cmd.ExtraFiles = append(cmd.ExtraFiles, p.w) }

// started drops our write end once the child holds it, so the reader sees its end.
func (p *mmMainPipe) started() { p.w.Close() }

func (p *mmMainPipe) Read(b []byte) (int, error) { return p.r.Read(b) }

func (p *mmMainPipe) Close() error {
	p.w.Close()
	return p.r.Close()
}
