//go:build windows

package slotagent

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/Semcosm/chuzi/internal/protocol"
	"golang.org/x/sys/windows"
)

var (
	ErrRuntimeConfig = errors.New("slotagent: invalid runtime configuration")
	ErrRuntimeStart  = errors.New("slotagent: runtime start failed")
	ErrRuntimeStop   = errors.New("slotagent: runtime stop failed")
)

// RuntimeConfig is fixed when the agent starts. Requests select only the
// closed JobKind enum and an account identifier used to derive the profile.
type RuntimeConfig struct {
	RuntimeRoot       string
	WorkerRuntimeRoot string
	WorkerCommand     string
	WorkerScript      string
	AdapterScript     string
	ProfileRoot       string
	WorkDir           string
	BrowserArgs       []string
}

type processLauncher struct{ config RuntimeConfig }

func NewProcessLauncher(config RuntimeConfig) (JobLauncher, error) {
	if strings.TrimSpace(config.WorkerCommand) == "" || !filepath.IsAbs(config.WorkerCommand) || !strings.EqualFold(filepath.Base(config.WorkerCommand), "node.exe") || !filepath.IsAbs(config.WorkerScript) || (config.AdapterScript != "" && !filepath.IsAbs(config.AdapterScript)) || !filepath.IsAbs(config.ProfileRoot) || !filepath.IsAbs(config.WorkDir) {
		return nil, ErrRuntimeConfig
	}
	if len(config.BrowserArgs) > 32 {
		return nil, ErrRuntimeConfig
	}
	if config.WorkerRuntimeRoot == "" {
		config.WorkerRuntimeRoot = config.RuntimeRoot
	}
	if config.RuntimeRoot != "" {
		if !filepath.IsAbs(config.RuntimeRoot) || !runtimePathContained(config.RuntimeRoot, config.WorkerScript) || (config.AdapterScript != "" && !runtimePathContained(config.RuntimeRoot, config.AdapterScript)) {
			return nil, ErrRuntimeConfig
		}
	}
	if config.WorkerRuntimeRoot != "" && (!filepath.IsAbs(config.WorkerRuntimeRoot) || !runtimePathContained(config.WorkerRuntimeRoot, config.WorkerCommand)) {
		return nil, ErrRuntimeConfig
	}
	args := append([]string(nil), config.BrowserArgs...)
	for _, arg := range args {
		if arg == "" || strings.ContainsAny(arg, "\x00\r\n") {
			return nil, ErrRuntimeConfig
		}
	}
	config.BrowserArgs = args
	return &processLauncher{config: config}, nil
}

func (l *processLauncher) Start(ctx context.Context, request Request) (Job, error) {
	if l == nil || ctx == nil || request.Command != StartJob || request.Validate() != nil {
		return nil, ErrRuntimeConfig
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(request.AccountID))
	profile := filepath.Join(l.config.ProfileRoot, hex.EncodeToString(digest[:]))
	if relative, err := filepath.Rel(l.config.ProfileRoot, profile); err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return nil, ErrRuntimeConfig
	}
	if err := inspectDirectoryNoReparseChain(l.config.ProfileRoot); err != nil {
		return nil, ErrRuntimeConfig
	}
	if err := inspectDirectoryNoReparseChain(profile); err != nil {
		return nil, ErrRuntimeConfig
	}
	script := l.config.WorkerScript
	if request.JobKind == Adapter && l.config.AdapterScript != "" {
		script = l.config.AdapterScript
	}
	if err := inspectRegularNoReparse(script); err != nil {
		return nil, ErrRuntimeConfig
	}
	if err := inspectRegularNoReparse(l.config.WorkerCommand); err != nil {
		return nil, ErrRuntimeConfig
	}
	if err := inspectDirectoryNoReparseChain(l.config.WorkDir); err != nil {
		return nil, ErrRuntimeConfig
	}
	args := []string{script, "--stdio"}
	args = append(args, l.config.BrowserArgs...)
	jobHandle, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, ErrRuntimeStart
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = 0x00002000 // JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(jobHandle, 9, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		_ = windows.CloseHandle(jobHandle)
		return nil, ErrRuntimeStart
	}
	stdinRead, stdinWrite, stdoutRead, stdoutWrite, err := processPipes()
	if err != nil {
		_ = windows.CloseHandle(jobHandle)
		return nil, ErrRuntimeStart
	}
	command, err := windows.UTF16FromString(windows.ComposeCommandLine(args))
	if err != nil {
		closeProcessPipes(stdinRead, stdinWrite, stdoutRead, stdoutWrite)
		_ = windows.CloseHandle(jobHandle)
		return nil, ErrRuntimeStart
	}
	application, err := windows.UTF16PtrFromString(l.config.WorkerCommand)
	if err != nil {
		closeProcessPipes(stdinRead, stdinWrite, stdoutRead, stdoutWrite)
		_ = windows.CloseHandle(jobHandle)
		return nil, ErrRuntimeStart
	}
	working, err := windows.UTF16PtrFromString(l.config.WorkDir)
	if err != nil {
		closeProcessPipes(stdinRead, stdinWrite, stdoutRead, stdoutWrite)
		_ = windows.CloseHandle(jobHandle)
		return nil, ErrRuntimeStart
	}
	env, err := encodeEnvironment(controlledRuntimeEnv(profile))
	if err != nil {
		closeProcessPipes(stdinRead, stdinWrite, stdoutRead, stdoutWrite)
		_ = windows.CloseHandle(jobHandle)
		return nil, ErrRuntimeStart
	}
	startup := windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{})), Flags: windows.STARTF_USESTDHANDLES, StdInput: stdinRead, StdOutput: stdoutWrite, StdErr: stdoutWrite}
	var info windows.ProcessInformation
	if err := windows.CreateProcess(application, &command[0], nil, nil, true, windows.CREATE_SUSPENDED|windows.CREATE_UNICODE_ENVIRONMENT|windows.CREATE_NO_WINDOW, env, working, &startup, &info); err != nil {
		closeProcessPipes(stdinRead, stdinWrite, stdoutRead, stdoutWrite)
		_ = windows.CloseHandle(jobHandle)
		return nil, ErrRuntimeStart
	}
	_ = windows.CloseHandle(stdinRead)
	_ = windows.CloseHandle(stdoutWrite)
	if err := windows.AssignProcessToJobObject(jobHandle, info.Process); err != nil {
		_ = windows.TerminateProcess(info.Process, 1)
		_ = windows.CloseHandle(info.Thread)
		_ = windows.CloseHandle(info.Process)
		_ = windows.CloseHandle(stdinWrite)
		_ = windows.CloseHandle(stdoutRead)
		_ = windows.CloseHandle(jobHandle)
		return nil, ErrRuntimeStart
	}
	if _, err := windows.ResumeThread(info.Thread); err != nil {
		_ = windows.TerminateJobObject(jobHandle, 1)
		_ = windows.CloseHandle(info.Thread)
		_ = windows.CloseHandle(info.Process)
		_ = windows.CloseHandle(stdinWrite)
		_ = windows.CloseHandle(stdoutRead)
		_ = windows.CloseHandle(jobHandle)
		return nil, ErrRuntimeStart
	}
	stdin := os.NewFile(uintptr(stdinWrite), "worker-stdin")
	stdout := os.NewFile(uintptr(stdoutRead), "worker-stdout")
	job := &processJob{process: info.Process, thread: info.Thread, stdin: stdin, stdout: stdout, messages: make(chan protocol.Envelope, 16), done: make(chan struct{}), readDone: make(chan struct{}), job: jobHandle}
	go job.readLoop(stdout)
	go job.waitLoop()
	return job, nil
}

func runtimePathContained(root, value string) bool {
	rel, err := filepath.Rel(strings.ToLower(filepath.Clean(root)), strings.ToLower(filepath.Clean(value)))
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func inspectRegularNoReparse(path string) error {
	if err := inspectNoReparseChain(path); err != nil {
		return err
	}
	attrs, err := fileAttributes(path)
	if err != nil || attrs&0x10 != 0 {
		return ErrRuntimeConfig
	}
	return nil
}

func inspectDirectoryNoReparseChain(path string) error {
	if err := inspectNoReparseChain(path); err != nil {
		return err
	}
	attrs, err := fileAttributes(path)
	if err != nil || attrs&0x10 == 0 {
		return ErrRuntimeConfig
	}
	return nil
}

func inspectNoReparseChain(path string) error {
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	if volume == "" {
		return ErrRuntimeConfig
	}
	rest := strings.TrimLeft(strings.TrimPrefix(clean, volume), `\/`)
	current := volume + string(filepath.Separator)
	if _, err := fileAttributes(current); err != nil {
		return err
	}
	for _, component := range strings.FieldsFunc(rest, func(r rune) bool { return r == '\\' || r == '/' }) {
		current = filepath.Join(current, component)
		attrs, err := fileAttributes(current)
		if err != nil || attrs&0x400 != 0 {
			if err != nil {
				return err
			}
			return ErrRuntimeConfig
		}
		if current != clean && attrs&0x10 == 0 {
			return ErrRuntimeConfig
		}
	}
	return nil
}

func fileAttributes(path string) (uint32, error) {
	wide, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.GetFileAttributes(wide)
}

type processJob struct {
	process  windows.Handle
	thread   windows.Handle
	stdin    io.WriteCloser
	stdout   io.Closer
	writeMu  sync.Mutex
	stateMu  sync.Mutex
	closed   bool
	readErr  error
	done     chan struct{}
	readDone chan struct{}
	messages chan protocol.Envelope
	job      windows.Handle
}

func (j *processJob) readLoop(stdout io.Reader) {
	decoder := json.NewDecoder(bufio.NewReader(stdout))
	for {
		var message protocol.Envelope
		if err := decoder.Decode(&message); err != nil {
			j.stateMu.Lock()
			if !j.closed {
				j.readErr = ErrRuntimeStart
			}
			if j.readDone != nil {
				close(j.readDone)
			}
			j.stateMu.Unlock()
			return
		}
		if ValidateWorkerEnvelope(message) != nil {
			j.stateMu.Lock()
			j.readErr = ErrInvalidMessage
			if j.readDone != nil {
				close(j.readDone)
			}
			j.stateMu.Unlock()
			return
		}
		select {
		case j.messages <- message:
		case <-j.done:
			return
		}
	}
}

func (j *processJob) waitLoop() {
	_, _ = windows.WaitForSingleObject(j.process, windows.INFINITE)
	if j.stdout != nil {
		_ = j.stdout.Close()
	}
	close(j.done)
}

func (j *processJob) RoundTrip(ctx context.Context, request protocol.Envelope) (protocol.Envelope, error) {
	if j == nil || ctx == nil || ValidateWorkerEnvelope(request) != nil {
		return protocol.Envelope{}, ErrInvalidMessage
	}
	j.writeMu.Lock()
	j.stateMu.Lock()
	closed := j.closed
	readErr := j.readErr
	j.stateMu.Unlock()
	if readErr != nil {
		j.writeMu.Unlock()
		return protocol.Envelope{}, readErr
	}
	if closed {
		j.writeMu.Unlock()
		return protocol.Envelope{}, ErrAgentStopped
	}
	err := json.NewEncoder(j.stdin).Encode(request)
	j.writeMu.Unlock()
	if err != nil {
		return protocol.Envelope{}, ErrAgentStopped
	}
	for {
		select {
		case <-ctx.Done():
			return protocol.Envelope{}, ctx.Err()
		case message := <-j.messages:
			// Terminal session events can be emitted asynchronously while the
			// service is issuing a ping. Return the first approved event and let
			// the caller classify it by protocol type.
			return message, nil
		case <-j.readDone:
			j.stateMu.Lock()
			err := j.readErr
			j.stateMu.Unlock()
			if err != nil {
				return protocol.Envelope{}, err
			}
			return protocol.Envelope{}, ErrAgentStopped
		case <-j.done:
			return protocol.Envelope{}, ErrAgentStopped
		}
	}
}

func (j *processJob) Stop() error {
	if j == nil {
		return nil
	}
	j.writeMu.Lock()
	defer j.writeMu.Unlock()
	j.stateMu.Lock()
	if j.closed {
		j.stateMu.Unlock()
		return nil
	}
	j.closed = true
	j.stateMu.Unlock()
	var stopErr error
	running := true
	if result, err := windows.WaitForSingleObject(j.process, 0); err == nil && result == uint32(windows.WAIT_OBJECT_0) {
		running = false
	}
	if running {
		if err := windows.TerminateJobObject(j.job, 1); err != nil {
			if result, waitErr := windows.WaitForSingleObject(j.process, 0); waitErr != nil || result != uint32(windows.WAIT_OBJECT_0) {
				stopErr = errors.Join(stopErr, ErrRuntimeStop)
			}
		} else if result, err := windows.WaitForSingleObject(j.process, uint32((2*time.Second)/time.Millisecond)); err != nil || result == uint32(windows.WAIT_TIMEOUT) {
			stopErr = errors.Join(stopErr, ErrRuntimeStop)
		}
	}
	if j.stdin != nil {
		if err := j.stdin.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			stopErr = errors.Join(stopErr, ErrRuntimeStop)
		}
	}
	if j.stdout != nil {
		if err := j.stdout.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			stopErr = errors.Join(stopErr, ErrRuntimeStop)
		}
	}
	for _, handle := range []windows.Handle{j.thread, j.process, j.job} {
		if handle != 0 {
			if err := windows.CloseHandle(handle); err != nil {
				stopErr = errors.Join(stopErr, ErrRuntimeStop)
			}
		}
	}
	return stopErr
}

func replaceRuntimeEnv(environment []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

// controlledRuntimeEnv deliberately does not inherit the agent environment.
// The agent receives its pipe token and lease fence through environment
// variables; passing those variables to a browser or adapter would violate
// the worker boundary.
func controlledRuntimeEnv(profile string) []string {
	allowed := []string{
		"ALLUSERSPROFILE", "APPDATA", "COMSPEC", "LOCALAPPDATA",
		"PATH", "PATHEXT", "PROGRAMDATA", "SYSTEMDRIVE",
		"SYSTEMROOT", "TEMP", "TMP", "USERPROFILE", "WINDIR",
	}
	values := make([]string, 0, len(allowed)+1)
	for _, key := range allowed {
		if value, ok := os.LookupEnv(key); ok && value != "" {
			values = append(values, key+"="+value)
		}
	}
	return replaceRuntimeEnv(values, "CHUZI_SESSION_PROFILE_DIR", profile)
}

func processPipes() (windows.Handle, windows.Handle, windows.Handle, windows.Handle, error) {
	var stdinRead, stdinWrite, stdoutRead, stdoutWrite windows.Handle
	security := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	if err := windows.CreatePipe(&stdinRead, &stdinWrite, &security, 0); err != nil {
		return 0, 0, 0, 0, err
	}
	if err := windows.SetHandleInformation(stdinWrite, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		closeProcessPipes(stdinRead, stdinWrite, 0, 0)
		return 0, 0, 0, 0, err
	}
	if err := windows.CreatePipe(&stdoutRead, &stdoutWrite, &security, 0); err != nil {
		closeProcessPipes(stdinRead, stdinWrite, 0, 0)
		return 0, 0, 0, 0, err
	}
	if err := windows.SetHandleInformation(stdoutRead, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		closeProcessPipes(stdinRead, stdinWrite, stdoutRead, stdoutWrite)
		return 0, 0, 0, 0, err
	}
	return stdinRead, stdinWrite, stdoutRead, stdoutWrite, nil
}

func closeProcessPipes(handles ...windows.Handle) {
	for _, handle := range handles {
		if handle != 0 {
			_ = windows.CloseHandle(handle)
		}
	}
}

func encodeEnvironment(values []string) (*uint16, error) {
	entries := append([]string(nil), values...)
	sort.SliceStable(entries, func(i, j int) bool {
		return strings.ToUpper(entries[i]) < strings.ToUpper(entries[j])
	})
	encoded, err := windows.UTF16FromString(strings.Join(entries, "\x00") + "\x00")
	if err != nil || len(encoded) == 0 {
		return nil, ErrRuntimeStart
	}
	return &encoded[0], nil
}

var _ JobLauncher = (*processLauncher)(nil)
var _ Job = (*processJob)(nil)
