//go:build windows

package slotwindows

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	ErrProcessStart     = errors.New("slotwindows: process start failed")
	ErrProcessTerminate = errors.New("slotwindows: process tree termination failed")
)

var managedDesktopPattern = regexp.MustCompile(`^winsta0\\ChuziSlot[0-9a-f]{16}$`)

const (
	wtsActive                         = 0
	wtsDisconnected                   = 4
	createUnicodeEnvironment          = 0x00000400
	createSuspended                   = 0x00000004
	jobObjectLimitKillOnJobClose      = 0x00002000
	jobObjectExtendedLimitInformation = 9
	tokenAdjustDefault                = 0x0080
	tokenAdjustSessionID              = 0x0100
)

type Session struct {
	ID    uint32
	State string
}

// FindSession returns only an active session whose token SID exactly matches
// the managed slot identity. Session 0 is never returned.
func FindSession(expectedSID string) (Session, error) {
	if expectedSID == "" {
		return Session{}, ErrSessionIdentity
	}
	var raw *windows.WTS_SESSION_INFO
	var count uint32
	if err := windows.WTSEnumerateSessions(0, 0, 1, &raw, &count); err != nil {
		return Session{}, ErrSessionUnavailable
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(raw)))
	entries := unsafe.Slice(raw, count)
	var disconnected bool
	for _, entry := range entries {
		if entry.SessionID == 0 {
			continue
		}
		if entry.State != wtsActive && entry.State != wtsDisconnected {
			continue
		}
		var token windows.Token
		if err := windows.WTSQueryUserToken(entry.SessionID, &token); err != nil {
			continue
		}
		user, err := token.GetTokenUser()
		_ = token.Close()
		if err != nil || user.User.Sid.String() != expectedSID {
			continue
		}
		if entry.State == wtsDisconnected {
			disconnected = true
			continue
		}
		return Session{ID: entry.SessionID, State: "active"}, nil
	}
	if disconnected {
		return Session{State: "disconnected"}, ErrSessionDisconnected
	}
	return Session{State: "logged_off"}, ErrSessionUnavailable
}

// VerifySession catches disconnect, logoff and session-ID changes before a
// previously captured session is reused.
func VerifySession(expectedSID string, previousID uint32) (Session, error) {
	current, err := FindSession(expectedSID)
	if err != nil {
		return current, err
	}
	if previousID != 0 && current.ID != previousID {
		return current, ErrSessionChanged
	}
	return current, nil
}

// Process owns one process tree. Closing its job handle is a bounded tree-wide
// termination fence scoped to this slot. The service keeps this handle in
// memory only; JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE makes service shutdown (or
// restart) terminate an orphaned agent before the next reconciliation starts
// a replacement.
type Process struct {
	mu      sync.Mutex
	waitMu  sync.Mutex
	process windows.Handle
	thread  windows.Handle
	job     windows.Handle
	closed  bool
}

// StartProcessAsSlotUser is intentionally limited to the packaged user agent;
// the service never exposes a general executable launcher to request callers.
func StartProcessAsSlotUser(expectedSID string, sessionID uint32, executable string, args []string, environment map[string]string, workingDirectory string, desktop string) (*Process, error) {
	if sessionID == 0 || executable == "" || !filepath.IsAbs(executable) || !strings.EqualFold(filepath.Base(executable), "chuzi-user-agent.exe") || workingDirectory == "" || !filepath.IsAbs(workingDirectory) || desktop == "" || len(args) == 0 || args[0] != executable {
		return nil, ErrProcessStart
	}
	if err := inspectNoReparseChain(executable); err != nil {
		return nil, ErrProcessStart
	}
	if err := inspectDirectoryNoReparseChain(workingDirectory); err != nil {
		return nil, ErrProcessStart
	}
	for key := range environment {
		if !strings.HasPrefix(key, "CHUZI_AGENT_") || strings.ContainsAny(key, "=\x00") {
			return nil, ErrProcessStart
		}
	}
	session, err := VerifySession(expectedSID, sessionID)
	if err != nil {
		return nil, err
	}
	var token windows.Token
	if err := windows.WTSQueryUserToken(session.ID, &token); err != nil {
		return nil, ErrSessionUnavailable
	}
	defer token.Close()
	var primary windows.Token
	if err := windows.DuplicateTokenEx(token, windows.MAXIMUM_ALLOWED|tokenAdjustDefault|tokenAdjustSessionID, nil, windows.SecurityImpersonation, windows.TokenPrimary, &primary); err != nil {
		return nil, ErrProcessStart
	}
	defer primary.Close()
	user, err := primary.GetTokenUser()
	if err != nil || user.User.Sid.String() != expectedSID {
		return nil, ErrSessionIdentity
	}
	command, err := windows.UTF16FromString(windows.ComposeCommandLine(args))
	if err != nil || len(command) == 0 {
		return nil, ErrProcessStart
	}
	application, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		return nil, ErrProcessStart
	}
	working, err := windows.UTF16PtrFromString(workingDirectory)
	if err != nil {
		return nil, ErrProcessStart
	}
	desktopUTF16, err := windows.UTF16PtrFromString(desktop)
	if err != nil {
		return nil, ErrProcessStart
	}
	env, cleanupEnv, err := tokenEnvironment(primary, environment)
	if err != nil {
		return nil, ErrProcessStart
	}
	defer cleanupEnv()
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, ErrProcessStart
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
	if _, err := windows.SetInformationJobObject(job, jobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		_ = windows.CloseHandle(job)
		return nil, ErrProcessStart
	}
	if err := ensureManagedDesktop(primary, desktop); err != nil {
		_ = windows.CloseHandle(job)
		return nil, ErrProcessStart
	}
	startup := windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{})), Desktop: desktopUTF16}
	var info windows.ProcessInformation
	if err := windows.CreateProcessAsUser(primary, application, &command[0], nil, nil, false, createUnicodeEnvironment|createSuspended, env, working, &startup, &info); err != nil {
		_ = windows.CloseHandle(job)
		return nil, ErrProcessStart
	}
	if err := windows.AssignProcessToJobObject(job, info.Process); err != nil {
		_ = windows.TerminateProcess(info.Process, 1)
		_ = windows.CloseHandle(info.Thread)
		_ = windows.CloseHandle(info.Process)
		_ = windows.CloseHandle(job)
		return nil, ErrProcessStart
	}
	if _, err := windows.ResumeThread(info.Thread); err != nil {
		_ = windows.TerminateJobObject(job, 1)
		_ = windows.CloseHandle(info.Thread)
		_ = windows.CloseHandle(info.Process)
		_ = windows.CloseHandle(job)
		return nil, ErrProcessStart
	}
	return &Process{process: info.Process, thread: info.Thread, job: job}, nil
}

// StartProcessAsSlotUserForSlot retains the older ordinal-only helper for
// internal compatibility. New service code must use the slot-ID helper below.
func StartProcessAsSlotUserForSlot(expectedSID string, sessionID uint32, ordinal int, executable string, args []string, environment map[string]string, workingDirectory string) (*Process, error) {
	if ordinal < 1 || ordinal > 9999 {
		return nil, ErrProcessStart
	}
	desktop, err := managedDesktopName(fmt.Sprintf("ordinal-%d", ordinal))
	if err != nil {
		return nil, ErrProcessStart
	}
	return StartProcessAsSlotUser(expectedSID, sessionID, executable, args, environment, workingDirectory, "winsta0\\"+desktop)
}

// StartProcessAsSlotUserForSlotID derives the desktop from the service-owned
// logical slot ID; callers cannot supply a desktop name.
func StartProcessAsSlotUserForSlotID(expectedSID string, sessionID uint32, slotID string, executable string, args []string, environment map[string]string, workingDirectory string) (*Process, error) {
	desktop, err := managedDesktopName(slotID)
	if err != nil {
		return nil, ErrProcessStart
	}
	return StartProcessAsSlotUser(expectedSID, sessionID, executable, args, environment, workingDirectory, "winsta0\\"+desktop)
}

func ensureSessionDesktop(expectedSID string, sessionID uint32, desktop string) error {
	if _, err := VerifySession(expectedSID, sessionID); err != nil {
		return err
	}
	return verifySessionDesktop(expectedSID, sessionID, desktop)
}

func verifySessionDesktop(expectedSID string, sessionID uint32, desktop string) error {
	if !managedDesktopPattern.MatchString("winsta0\\" + desktop) {
		return ErrProcessStart
	}
	var token windows.Token
	if err := windows.WTSQueryUserToken(sessionID, &token); err != nil {
		return ErrSessionUnavailable
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil || user.User.Sid.String() != expectedSID {
		return ErrSessionIdentity
	}
	return ensureManagedDesktop(token, "winsta0\\"+desktop)
}

// ensureManagedDesktop creates or opens the service-derived desktop while
// impersonating the target slot token. Only a service-derived hashed slot
// desktop is valid.
func ensureManagedDesktop(token windows.Token, desktop string) error {
	if token == 0 || !managedDesktopPattern.MatchString(desktop) {
		return ErrProcessStart
	}
	name := desktop[strings.LastIndexByte(desktop, '\\')+1:]
	nameUTF16, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return ErrProcessStart
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.SetThreadToken(nil, token); err != nil {
		return ErrProcessStart
	}
	defer windows.RevertToSelf()
	create := windows.NewLazySystemDLL("user32.dll").NewProc("CreateDesktopW")
	close := windows.NewLazySystemDLL("user32.dll").NewProc("CloseDesktop")
	handle, _, _ := create.Call(uintptr(unsafe.Pointer(nameUTF16)), 0, 0, 0, 0x000F01FF, 0)
	if handle != 0 {
		_, _, _ = close.Call(handle)
		return nil
	}
	open := windows.NewLazySystemDLL("user32.dll").NewProc("OpenDesktopW")
	handle, _, _ = open.Call(uintptr(unsafe.Pointer(nameUTF16)), 0, 0, 0x000F01FF)
	if handle == 0 {
		return ErrProcessStart
	}
	_, _, _ = close.Call(handle)
	return nil
}

func (p *Process) Wait(timeout time.Duration) (uint32, error) {
	if p == nil || timeout < 0 {
		return 0, ErrProcessStart
	}
	p.waitMu.Lock()
	defer p.waitMu.Unlock()
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return 0, ErrProcessStart
	}
	process := p.process
	p.mu.Unlock()
	milliseconds := uint32(timeout / time.Millisecond)
	if milliseconds == 0 && timeout > 0 {
		milliseconds = 1
	}
	result, err := windows.WaitForSingleObject(process, milliseconds)
	if err != nil {
		return 0, ErrProcessStart
	}
	if result == uint32(windows.WAIT_TIMEOUT) {
		return 0, context.DeadlineExceeded
	}
	var code uint32
	if err := windows.GetExitCodeProcess(process, &code); err != nil {
		return 0, ErrProcessStart
	}
	return code, nil
}

func (p *Process) Terminate() error {
	if p == nil {
		return nil
	}
	p.waitMu.Lock()
	defer p.waitMu.Unlock()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	if result, err := windows.WaitForSingleObject(p.process, 0); err == nil && result == uint32(windows.WAIT_OBJECT_0) {
		return nil
	}
	if err := windows.TerminateJobObject(p.job, 1); err != nil {
		// Termination races with natural process exit are idempotent. Confirm
		// the process state before surfacing a real cleanup failure.
		if result, waitErr := windows.WaitForSingleObject(p.process, 0); waitErr == nil && result == uint32(windows.WAIT_OBJECT_0) {
			return nil
		}
		return ErrProcessTerminate
	}
	result, waitErr := windows.WaitForSingleObject(p.process, uint32((2*time.Second)/time.Millisecond))
	if waitErr != nil || result != uint32(windows.WAIT_OBJECT_0) {
		return ErrProcessTerminate
	}
	return nil
}

func (p *Process) Close() error {
	if p == nil {
		return nil
	}
	p.waitMu.Lock()
	defer p.waitMu.Unlock()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	_ = windows.CloseHandle(p.thread)
	_ = windows.CloseHandle(p.process)
	if err := windows.CloseHandle(p.job); err != nil {
		return ErrProcessTerminate
	}
	return nil
}

func tokenEnvironment(token windows.Token, overrides map[string]string) (*uint16, func(), error) {
	var block *uint16
	if err := windows.CreateEnvironmentBlock(&block, token, false); err != nil {
		return nil, func() {}, err
	}
	cleanup := func() { _ = windows.DestroyEnvironmentBlock(block) }
	values := make(map[string]string)
	for offset, limit := uintptr(0), uintptr(32767); offset < limit; {
		start := (*uint16)(unsafe.Add(unsafe.Pointer(block), offset*2))
		length := uintptr(0)
		for offset+length < limit && *(*uint16)(unsafe.Add(unsafe.Pointer(block), (offset+length)*2)) != 0 {
			length++
		}
		if length == 0 {
			break
		}
		entry := windows.UTF16ToString(unsafe.Slice(start, length))
		if index := strings.IndexByte(entry, '='); index > 0 {
			key := strings.ToUpper(entry[:index])
			values[key] = key + "=" + entry[index+1:]
		}
		offset += length + 1
	}
	for key, value := range overrides {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, '\x00') {
			cleanup()
			return nil, func() {}, ErrProcessStart
		}
		values[strings.ToUpper(key)] = key + "=" + value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	entries := make([]string, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, values[key])
	}
	encoded, err := windows.UTF16FromString(strings.Join(entries, "\x00") + "\x00")
	if err != nil {
		cleanup()
		return nil, func() {}, fmt.Errorf("%w", ErrProcessStart)
	}
	return &encoded[0], cleanup, nil
}
