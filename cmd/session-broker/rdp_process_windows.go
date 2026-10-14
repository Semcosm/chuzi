//go:build windows

package main

import (
	"github.com/Semcosm/chuzi/internal/slotwindows"
	"golang.org/x/sys/windows"
	"sync"
	"time"
	"unsafe"
)

// Start suspended, assign the kill-on-close Job, then resume. A broker crash
// cannot leave an mstsc child that later logs into a recycled target.
type rdpProcess struct {
	mu      sync.Mutex
	process windows.Handle
	job     windows.Handle
	done    chan struct{}
}

func startRDPProcess(executable, profile string) (*rdpProcess, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, slotwindows.ErrSessionUnavailable
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		windows.CloseHandle(job)
		return nil, slotwindows.ErrSessionUnavailable
	}
	app, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		windows.CloseHandle(job)
		return nil, slotwindows.ErrSessionUnavailable
	}
	command, err := windows.UTF16FromString(windows.ComposeCommandLine([]string{executable, profile}))
	if err != nil {
		windows.CloseHandle(job)
		return nil, slotwindows.ErrSessionUnavailable
	}
	startup := windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{}))}
	var info windows.ProcessInformation
	if err := windows.CreateProcess(app, &command[0], nil, nil, false, windows.CREATE_SUSPENDED, nil, nil, &startup, &info); err != nil {
		windows.CloseHandle(job)
		return nil, slotwindows.ErrSessionUnavailable
	}
	defer windows.CloseHandle(info.Thread)
	if err := windows.AssignProcessToJobObject(job, info.Process); err != nil {
		windows.TerminateProcess(info.Process, 1)
		windows.CloseHandle(info.Process)
		windows.CloseHandle(job)
		return nil, slotwindows.ErrSessionUnavailable
	}
	if _, err := windows.ResumeThread(info.Thread); err != nil {
		windows.TerminateJobObject(job, 1)
		windows.CloseHandle(info.Process)
		windows.CloseHandle(job)
		return nil, slotwindows.ErrSessionUnavailable
	}
	process := &rdpProcess{process: info.Process, job: job, done: make(chan struct{})}
	go func() { windows.WaitForSingleObject(info.Process, windows.INFINITE); close(process.done) }()
	return process, nil
}

func (p *rdpProcess) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.job == 0 {
		return nil
	}
	if err := windows.TerminateJobObject(p.job, 1); err != nil {
		return slotwindows.ErrSessionUnavailable
	}
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		return slotwindows.ErrSessionUnavailable
	}
	if p.process != 0 {
		if err := windows.CloseHandle(p.process); err != nil {
			return slotwindows.ErrSessionUnavailable
		}
		p.process = 0
	}
	if err := windows.CloseHandle(p.job); err != nil {
		return slotwindows.ErrSessionUnavailable
	}
	p.job = 0
	return nil
}
