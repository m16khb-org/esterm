//go:build windows

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"
)

// Child processes on Windows: each runs in a job object that kills everything in it when
// its last handle closes, so ffmpeg and yt-dlp (whose pip launcher starts python) go with
// esterm however it ends. The main view comes over a named pipe: Windows children inherit
// no descriptors beyond the standard three.

var (
	procCreateJobObjectW         = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject  = kernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject = kernel32.NewProc("AssignProcessToJobObject")
	procTerminateJobObject       = kernel32.NewProc("TerminateJobObject")
	procCreateNamedPipeW         = kernel32.NewProc("CreateNamedPipeW")
	procConnectNamedPipe         = kernel32.NewProc("ConnectNamedPipe")
)

const (
	jobObjectExtendedLimitInformation = 9
	jobLimitKillOnJobClose            = 0x2000
	processSetQuota                   = 0x0100
	processTerminate                  = 0x0001
	pipeAccessInbound                 = 0x1
)

type jobBasicLimit struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type jobExtendedLimit struct {
	Basic                                    jobBasicLimit
	IO                                       [6]uint64
	ProcessMemoryLimit, JobMemoryLimit       uintptr
	PeakProcessMemoryUsed, PeakJobMemoryUsed uintptr
}

// newKillJob creates a job object whose processes are killed when it is closed.
func newKillJob() (syscall.Handle, error) {
	h, _, e := procCreateJobObjectW.Call(0, 0)
	if h == 0 {
		return 0, e
	}
	info := jobExtendedLimit{Basic: jobBasicLimit{LimitFlags: jobLimitKillOnJobClose}}
	if r, _, e := procSetInformationJobObject.Call(h, jobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info)); r == 0 {
		syscall.CloseHandle(syscall.Handle(h))
		return 0, e
	}
	return syscall.Handle(h), nil
}

// mmStart starts cmd in a job of its own and kills the job when ctx ends. The returned
// stop func optionally kills the job, then reaps the process and closes the job, which
// kills whatever it started that is still running.
func mmStart(ctx context.Context, cmd *exec.Cmd) (stop func(kill bool) error, err error) {
	job, err := newKillJob()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		syscall.CloseHandle(job)
		return nil, err
	}
	if p, err := syscall.OpenProcess(processSetQuota|processTerminate, false, uint32(cmd.Process.Pid)); err == nil {
		procAssignProcessToJobObject.Call(uintptr(job), uintptr(p))
		syscall.CloseHandle(p)
	}
	kill := func() {
		procTerminateJobObject.Call(uintptr(job), 1)
		cmd.Process.Kill()
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			kill()
		case <-done:
		}
	}()
	return func(k bool) error {
		if k {
			kill()
		}
		err := cmd.Wait()
		close(done)
		syscall.CloseHandle(job)
		return err
	}, nil
}

var mmPipeSeq atomic.Int64

// mmMainPipe is the second output of ffmpeg, the main view: a named pipe that ffmpeg opens
// as its output file and esterm reads.
type mmMainPipe struct {
	target    string // the pipe's name, which ffmpeg writes to
	h         syscall.Handle
	connect   sync.Once
	connected atomic.Bool
	close     sync.Once
}

func newMMMainPipe() (*mmMainPipe, error) {
	name := fmt.Sprintf(`\\.\pipe\esterm-main-%d-%d`, os.Getpid(), mmPipeSeq.Add(1))
	n, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	h, _, e := procCreateNamedPipeW.Call(uintptr(unsafe.Pointer(n)), pipeAccessInbound, 0, 1, 0, mmMainBytes, 0, 0)
	if syscall.Handle(h) == syscall.InvalidHandle {
		return nil, e
	}
	return &mmMainPipe{target: name, h: syscall.Handle(h)}, nil
}

func (p *mmMainPipe) attach(cmd *exec.Cmd) {}
func (p *mmMainPipe) started()             {}

// Read waits for ffmpeg to open the pipe, then reads it; its end is io.EOF.
func (p *mmMainPipe) Read(b []byte) (int, error) {
	p.connect.Do(func() {
		procConnectNamedPipe.Call(uintptr(p.h), 0) // ERROR_PIPE_CONNECTED when ffmpeg opened it first: as good
		p.connected.Store(true)
	})
	var n uint32
	if err := syscall.ReadFile(p.h, b, &n, nil); err != nil {
		return int(n), io.EOF
	}
	return int(n), nil
}

// Close frees the pipe. A reader still waiting for ffmpeg (which failed before it opened
// the pipe) is let go by opening the pipe here.
func (p *mmMainPipe) Close() error {
	p.close.Do(func() {
		if !p.connected.Load() {
			if n, err := syscall.UTF16PtrFromString(p.target); err == nil {
				if c, err := syscall.CreateFile(n, syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_EXISTING, 0, 0); err == nil {
					syscall.CloseHandle(c)
				}
			}
		}
		syscall.CloseHandle(p.h)
	})
	return nil
}
