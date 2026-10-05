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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/Semcosm/chuzi/internal/environment"
	"github.com/Semcosm/chuzi/internal/protocol"
	"golang.org/x/sys/windows"
)

var (
	ErrRuntimeConfig        = errors.New("slotagent: invalid runtime configuration")
	ErrRuntimeStart         = errors.New("slotagent: runtime start failed")
	ErrRuntimeStop          = errors.New("slotagent: runtime stop failed")
	errWorkerProcessExit    = errors.New("slotagent: worker process exited")
	errWorkerProcessDLLInit = errors.New("slotagent: worker process DLL initialization failed")
)

const processStatusDLLInitFailed uint32 = 0xC0000142

// RuntimeConfig is fixed when the agent starts. Requests select only the
// closed JobKind enum and an account identifier used to derive the profile.
type RuntimeConfig struct {
	RuntimeRoot        string
	PackageRoot        string
	EnvironmentID      string
	EnvironmentVersion string
	ManifestDigest     string
	Signer             string
	PackageGeneration  uint64
	WorkerEntrypoint   string
	AdapterEntrypoint  string
	WorkerRuntimeRoot  string
	WorkerCommand      string
	WorkerScript       string
	AdapterScript      string
	WindowsDesktop     string
	ProfileRoot        string
	WorkDir            string
	BrowserArgs        []string
}

type processLauncher struct{ config RuntimeConfig }

func NewProcessLauncher(config RuntimeConfig) (JobLauncher, error) {
	if strings.TrimSpace(config.WorkerCommand) == "" || !filepath.IsAbs(config.WorkerCommand) || !strings.EqualFold(filepath.Base(config.WorkerCommand), "node.exe") || !filepath.IsAbs(config.WorkerScript) || (config.AdapterScript != "" && !filepath.IsAbs(config.AdapterScript)) || !managedDesktopPattern.MatchString(config.WindowsDesktop) || !filepath.IsAbs(config.ProfileRoot) || !filepath.IsAbs(config.WorkDir) {
		return nil, ErrRuntimeConfig
	}
	if len(config.BrowserArgs) > 32 {
		return nil, ErrRuntimeConfig
	}
	if config.WorkerRuntimeRoot == "" {
		config.WorkerRuntimeRoot = config.RuntimeRoot
	}
	if config.PackageRoot == "" {
		config.PackageRoot = config.RuntimeRoot
	}
	if config.WorkerEntrypoint == "" {
		config.WorkerEntrypoint = environment.HeadlessEntrypointName
	}
	if !filepath.IsAbs(config.PackageRoot) || config.PackageGeneration == 0 || config.EnvironmentID == "" || config.EnvironmentVersion == "" || config.ManifestDigest == "" || config.Signer == "" || (config.WorkerEntrypoint != environment.WorkerEntrypointName && config.WorkerEntrypoint != environment.HeadlessEntrypointName) || (config.AdapterScript == "" || config.AdapterEntrypoint != environment.AdapterBridgeEntrypointName) || !runtimePathContained(config.PackageRoot, config.WorkerScript) || !runtimePathContained(config.PackageRoot, config.AdapterScript) {
		return nil, ErrRuntimeConfig
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

func (l *processLauncher) Start(ctx context.Context, request Request) (job Job, startErr error) {
	stage := "validation"
	var nativeErr error
	defer func() {
		if startErr != nil {
			writeRuntimeStartDiagnostic(stage, startErr, nativeErr)
		}
	}()
	if l == nil || ctx == nil || request.Command != StartJob || request.Validate() != nil {
		return nil, ErrRuntimeConfig
	}
	stage = "context"
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stage = "profile_path"
	digest := sha256.Sum256([]byte(request.AccountID))
	profile := filepath.Join(l.config.ProfileRoot, hex.EncodeToString(digest[:]))
	relative, relativeErr := filepath.Rel(l.config.ProfileRoot, profile)
	if relativeErr != nil {
		stage = "profile_path_relation"
		nativeErr = relativeErr
		return nil, ErrRuntimeConfig
	}
	if relative == "." {
		stage = "profile_path_same"
		return nil, ErrRuntimeConfig
	}
	if strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		stage = "profile_path_escape"
		return nil, ErrRuntimeConfig
	}
	if filepath.IsAbs(relative) {
		stage = "profile_path_absolute"
		return nil, ErrRuntimeConfig
	}
	stage = "profile_root_leaf"
	if err := inspectDirectoryNoReparseLeaf(l.config.ProfileRoot); err != nil {
		nativeErr = err
		return nil, ErrRuntimeConfig
	}
	stage = "package_root_leaf"
	if err := inspectDirectoryNoReparseLeaf(l.config.PackageRoot); err != nil {
		nativeErr = err
		return nil, ErrRuntimeConfig
	}
	stage = "profile_directory_leaf"
	if err := inspectDirectoryNoReparseLeaf(profile); err != nil {
		nativeErr = err
		return nil, ErrRuntimeConfig
	}
	stage = "worker_script"
	script := l.config.WorkerScript
	if request.JobKind == Adapter {
		if l.config.AdapterEntrypoint != environment.AdapterBridgeEntrypointName || l.config.AdapterScript == "" {
			return nil, ErrRuntimeConfig
		}
		script = l.config.AdapterScript
	} else if request.JobKind != BrowserWorker {
		return nil, ErrRuntimeConfig
	}
	if err := inspectRegularNoReparseLeaf(script); err != nil {
		nativeErr = err
		return nil, ErrRuntimeConfig
	}
	stage = "worker_script_read"
	if err := inspectWorkerScriptReadable(script); err != nil {
		nativeErr = err
		return nil, ErrRuntimeConfig
	}
	if !runtimePathContained(l.config.PackageRoot, script) {
		return nil, ErrRuntimeConfig
	}
	if err := inspectRegularNoReparseLeaf(l.config.WorkerCommand); err != nil {
		nativeErr = err
		return nil, ErrRuntimeConfig
	}
	stage = "work_directory"
	if err := inspectDirectoryNoReparseLeaf(l.config.WorkDir); err != nil {
		nativeErr = err
		return nil, ErrRuntimeConfig
	}
	// CreateProcess receives the executable separately, but the command line
	// still must carry the same argv[0] that a normal Node invocation would
	// provide. Node uses that value while initializing its Windows runtime.
	args := workerCommandArgs(l.config.WorkerCommand, script, l.config.BrowserArgs)
	stage = "job_create"
	jobHandle, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		nativeErr = err
		return nil, ErrRuntimeStart
	}
	stage = "job_configure"
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = 0x00002000 // JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(jobHandle, 9, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		nativeErr = err
		_ = windows.CloseHandle(jobHandle)
		return nil, ErrRuntimeStart
	}
	stage = "pipes"
	stdinRead, stdinWrite, stdoutRead, stdoutWrite, stderrRead, stderrWrite, err := processPipes()
	if err != nil {
		nativeErr = err
		_ = windows.CloseHandle(jobHandle)
		return nil, ErrRuntimeStart
	}
	stage = "command_line_encode"
	command, err := windows.UTF16FromString(windows.ComposeCommandLine(args))
	if err != nil {
		nativeErr = err
		closeProcessPipes(stdinRead, stdinWrite, stdoutRead, stdoutWrite, stderrRead, stderrWrite)
		_ = windows.CloseHandle(jobHandle)
		return nil, ErrRuntimeStart
	}
	stage = "application_encode"
	application, err := windows.UTF16PtrFromString(l.config.WorkerCommand)
	if err != nil {
		nativeErr = err
		closeProcessPipes(stdinRead, stdinWrite, stdoutRead, stdoutWrite, stderrRead, stderrWrite)
		_ = windows.CloseHandle(jobHandle)
		return nil, ErrRuntimeStart
	}
	stage = "working_directory_encode"
	working, err := windows.UTF16PtrFromString(l.config.WorkDir)
	if err != nil {
		nativeErr = err
		closeProcessPipes(stdinRead, stdinWrite, stdoutRead, stdoutWrite, stderrRead, stderrWrite)
		_ = windows.CloseHandle(jobHandle)
		return nil, ErrRuntimeStart
	}
	stage = "desktop_encode"
	if _, err := windows.UTF16PtrFromString(l.config.WindowsDesktop); err != nil {
		nativeErr = err
		closeProcessPipes(stdinRead, stdinWrite, stdoutRead, stdoutWrite, stderrRead, stderrWrite)
		_ = windows.CloseHandle(jobHandle)
		return nil, ErrRuntimeStart
	}
	stage = "environment_encode"
	env, err := encodeEnvironment(controlledRuntimeEnv(profile, l.config.WindowsDesktop))
	if err != nil {
		nativeErr = err
		closeProcessPipes(stdinRead, stdinWrite, stdoutRead, stdoutWrite, stderrRead, stderrWrite)
		_ = windows.CloseHandle(jobHandle)
		return nil, ErrRuntimeStart
	}
	stage = "create_process"
	startup := windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{})), Flags: windows.STARTF_USESTDHANDLES, StdInput: stdinRead, StdOutput: stdoutWrite, StdErr: stderrWrite}
	var info windows.ProcessInformation
	// Keep the Node control worker on the session's verified default desktop.
	// Headed browser processes are launched separately by the fixed browser
	// launcher on the service-derived desktop from CHUZI_WINDOWS_DESKTOP.
	if err := windows.CreateProcess(application, &command[0], nil, nil, true, windows.CREATE_SUSPENDED|windows.CREATE_UNICODE_ENVIRONMENT|windows.CREATE_BREAKAWAY_FROM_JOB, env, working, &startup, &info); err != nil {
		nativeErr = err
		closeProcessPipes(stdinRead, stdinWrite, stdoutRead, stdoutWrite, stderrRead, stderrWrite)
		_ = windows.CloseHandle(jobHandle)
		return nil, ErrRuntimeStart
	}
	_ = windows.CloseHandle(stdinRead)
	_ = windows.CloseHandle(stdoutWrite)
	_ = windows.CloseHandle(stderrWrite)
	stderr := os.NewFile(uintptr(stderrRead), "worker-stderr")
	go drainWorkerStderr(stderr)
	stage = "assign_job"
	if err := windows.AssignProcessToJobObject(jobHandle, info.Process); err != nil {
		nativeErr = err
		_ = windows.TerminateProcess(info.Process, 1)
		_ = windows.CloseHandle(info.Thread)
		_ = windows.CloseHandle(info.Process)
		_ = windows.CloseHandle(stdinWrite)
		_ = windows.CloseHandle(stdoutRead)
		_ = windows.CloseHandle(jobHandle)
		return nil, ErrRuntimeStart
	}
	stage = "resume_thread"
	if _, err := windows.ResumeThread(info.Thread); err != nil {
		nativeErr = err
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
	processJob := &processJob{process: info.Process, thread: info.Thread, stdin: stdin, stdout: stdout, messages: make(chan protocol.Envelope, 16), done: make(chan struct{}), readDone: make(chan struct{}), job: jobHandle}
	go processJob.readLoop(stdout)
	go processJob.waitLoop()
	return processJob, nil
}

func runtimePathContained(root, value string) bool {
	rel, err := filepath.Rel(strings.ToLower(filepath.Clean(root)), strings.ToLower(filepath.Clean(value)))
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func workerCommandArgs(executable, script string, browserArgs []string) []string {
	args := make([]string, 0, len(browserArgs)+3)
	args = append(args, executable, script, "--stdio")
	return append(args, browserArgs...)
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

// The service validates the complete path chain before granting a managed
// profile or runtime. The agent runs as that managed user, which may not have
// read-attributes access to unrelated parent directories, so it rechecks only
// the authorized leaf before opening it.
func inspectRegularNoReparseLeaf(path string) error {
	attrs, err := fileAttributes(path)
	if err != nil {
		return err
	}
	if attrs&0x400 != 0 || attrs&0x10 != 0 {
		return ErrRuntimeConfig
	}
	return nil
}

// Node performs a realpath/lstat walk before loading the entrypoint. Confirm
// the same target-user access through Go first so a denied parent or leaf is
// reported at the worker boundary instead of surfacing only as a Node loader
// failure after process creation.
func inspectWorkerScriptReadable(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrRuntimeConfig
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	return file.Close()
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

func inspectDirectoryNoReparseLeaf(path string) error {
	attrs, err := fileAttributes(path)
	if err != nil {
		return err
	}
	if attrs&0x400 != 0 || attrs&0x10 == 0 {
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
			if errors.Is(err, io.EOF) {
				writeRuntimeStartDiagnostic("worker_output_eof", ErrAgentStopped, nil)
			} else {
				writeRuntimeStartDiagnostic("worker_output_parse", ErrInvalidMessage, nil)
			}
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
	var code uint32
	if err := windows.GetExitCodeProcess(j.process, &code); err == nil {
		exitErr := errWorkerProcessExit
		if code == processStatusDLLInitFailed {
			exitErr = errWorkerProcessDLLInit
		}
		writeRuntimeStartDiagnostic("worker_process_exit", exitErr, nil)
	}
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
func controlledRuntimeEnv(profile string, desktop ...string) []string {
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
	values = replaceRuntimeEnv(values, "CHUZI_SESSION_PROFILE_DIR", profile)
	if len(desktop) > 0 && desktop[0] != "" {
		values = replaceRuntimeEnv(values, "CHUZI_WINDOWS_DESKTOP", desktop[0])
	}
	return values
}

func processPipes() (windows.Handle, windows.Handle, windows.Handle, windows.Handle, windows.Handle, windows.Handle, error) {
	var stdinRead, stdinWrite, stdoutRead, stdoutWrite windows.Handle
	security := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	if err := windows.CreatePipe(&stdinRead, &stdinWrite, &security, 0); err != nil {
		return 0, 0, 0, 0, 0, 0, err
	}
	if err := windows.SetHandleInformation(stdinWrite, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		closeProcessPipes(stdinRead, stdinWrite, 0, 0)
		return 0, 0, 0, 0, 0, 0, err
	}
	if err := windows.CreatePipe(&stdoutRead, &stdoutWrite, &security, 0); err != nil {
		closeProcessPipes(stdinRead, stdinWrite, 0, 0)
		return 0, 0, 0, 0, 0, 0, err
	}
	if err := windows.SetHandleInformation(stdoutRead, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		closeProcessPipes(stdinRead, stdinWrite, stdoutRead, stdoutWrite)
		return 0, 0, 0, 0, 0, 0, err
	}
	var stderrRead, stderrWrite windows.Handle
	if err := windows.CreatePipe(&stderrRead, &stderrWrite, &security, 0); err != nil {
		closeProcessPipes(stdinRead, stdinWrite, stdoutRead, stdoutWrite)
		return 0, 0, 0, 0, 0, 0, err
	}
	if err := windows.SetHandleInformation(stderrRead, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		closeProcessPipes(stdinRead, stdinWrite, stdoutRead, stdoutWrite, stderrRead, stderrWrite)
		return 0, 0, 0, 0, 0, 0, err
	}
	return stdinRead, stdinWrite, stdoutRead, stdoutWrite, stderrRead, stderrWrite, nil
}

func drainWorkerStderr(stderr io.ReadCloser) {
	if stderr == nil {
		return
	}
	data, _ := io.ReadAll(io.LimitReader(stderr, 4097))
	_ = stderr.Close()
	if len(data) == 0 {
		return
	}
	writeWorkerStderrDiagnostic(data)
}

func writeWorkerStderrDiagnostic(data []byte) {
	path := strings.TrimSpace(os.Getenv("CHUZI_AGENT_STARTUP_DIAGNOSTICS"))
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
		return
	}
	truncated := len(data) > 4096
	if truncated {
		data = data[:4096]
	}
	digest := sha256.Sum256(data)
	runtimeStartDiagnosticsMu.Lock()
	defer runtimeStartDiagnosticsMu.Unlock()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.WriteString("WORKER_STDERR_BYTES=" + strconv.Itoa(len(data)) + "\n")
	_, _ = file.WriteString("WORKER_STDERR_TRUNCATED=" + strconv.FormatBool(truncated) + "\n")
	_, _ = file.WriteString("WORKER_STDERR_CLASS=" + workerStderrClass(data) + "\n")
	_, _ = file.WriteString("WORKER_STDERR_SHA256=" + hex.EncodeToString(digest[:]) + "\n")
	_, _ = file.WriteString("WORKER_STDERR_SUMMARY=" + workerStderrSummary(data) + "\n")
	_, _ = file.WriteString("WORKER_STDERR_END=1\n")
}

func workerStderrSummary(data []byte) string {
	var summary strings.Builder
	spacePending := false
	for _, value := range data {
		if value < 0x20 || value > 0x7e {
			if summary.Len() > 0 {
				spacePending = true
			}
			continue
		}
		if spacePending {
			summary.WriteByte(' ')
			spacePending = false
		}
		summary.WriteByte(value)
	}
	value := workerWindowsPathPattern.ReplaceAllString(summary.String(), "<path>")
	value = strings.TrimSpace(value)
	if len(value) > 512 {
		value = value[:512]
	}
	return value
}

func workerStderrClass(data []byte) string {
	text := strings.ToLower(string(data))
	switch {
	case strings.Contains(text, "cannot find module"):
		return "module_not_found"
	case strings.Contains(text, "syntaxerror"):
		return "syntax_error"
	case strings.Contains(text, "bad option"):
		return "bad_option"
	case strings.Contains(text, "permission denied"), strings.Contains(text, "access is denied"), strings.Contains(text, "operation not permitted"), strings.Contains(text, "eperm"):
		return "permission"
	case strings.Contains(text, "not recognized"), strings.Contains(text, "cannot execute"):
		return "command_error"
	default:
		return "other"
	}
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
	encoded := make([]uint16, 0, len(entries)*2+1)
	for _, entry := range entries {
		part, err := windows.UTF16FromString(entry)
		if err != nil || len(part) == 0 {
			return nil, ErrRuntimeStart
		}
		encoded = append(encoded, part[:len(part)-1]...)
		encoded = append(encoded, 0)
	}
	// The environment block is terminated by an additional NUL after the
	// per-entry separators.
	encoded = append(encoded, 0)
	return &encoded[0], nil
}

var runtimeStartDiagnosticsMu sync.Mutex

var workerWindowsPathPattern = regexp.MustCompile(`(?i)(?:[a-z]:[\\/]|\\\\)[^"'<>|()\r\n]*`)

func writeRuntimeStartDiagnostic(stage string, startErr, nativeErr error) {
	if stage == "" {
		return
	}
	path := strings.TrimSpace(os.Getenv("CHUZI_AGENT_STARTUP_DIAGNOSTICS"))
	// The target-user environment intentionally receives only CHUZI_AGENT_*
	// values, so the service-side smoke marker is not present here. The
	// diagnostics path itself is the opt-in marker; production launches do not
	// set it.
	if path == "" {
		if os.Getenv("CHUZI_RUN_WINDOWS_JOB_POOL_SMOKE") != "1" {
			return
		}
		// Client-side smoke diagnostics run in the SYSTEM test process, while
		// the agent-specific path exists only in the target user's environment.
		// Fall back to the existing service-owned redacted diagnostics file.
		if root := strings.TrimSpace(os.Getenv("CHUZI_WINDOWS_JOB_POOL_SMOKE_ROOT")); filepath.IsAbs(root) && !strings.ContainsAny(root, "\x00\r\n") {
			path = filepath.Join(root, "native-token-diagnostics.log")
		}
	}
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
		return
	}
	runtimeStartDiagnosticsMu.Lock()
	defer runtimeStartDiagnosticsMu.Unlock()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	class := runtimeStartErrorClass(startErr)
	if nativeErr != nil {
		class = runtimeStartErrorClass(nativeErr)
	}
	_, _ = file.WriteString("WORKER_START_STAGE=" + stage + "\n")
	_, _ = file.WriteString("WORKER_START_ERROR_CLASS=" + class + "\n")
	_, _ = file.WriteString("WORKER_START_END=1\n")
}

func runtimeStartErrorClass(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, windows.ERROR_DLL_INIT_FAILED):
		return "dll_init_failed"
	case errors.Is(err, errWorkerProcessDLLInit):
		return "dll_init_failed"
	case errors.Is(err, errWorkerProcessExit):
		return "worker_process_exit"
	case errors.Is(err, ErrInvalidMessage):
		return "invalid_message"
	case errors.Is(err, ErrAgentStopped):
		return "agent_stopped"
	case errors.Is(err, windows.ERROR_ACCESS_DENIED):
		return "access_denied"
	case errors.Is(err, windows.ERROR_FILE_NOT_FOUND):
		return "file_not_found"
	case errors.Is(err, windows.ERROR_PATH_NOT_FOUND):
		return "path_not_found"
	case errors.Is(err, windows.ERROR_INVALID_PARAMETER):
		return "invalid_parameter"
	case errors.Is(err, ErrRuntimeConfig):
		return "runtime_config"
	case errors.Is(err, ErrRuntimeStart):
		return "runtime_start"
	default:
		return "unknown"
	}
}

var _ JobLauncher = (*processLauncher)(nil)
var _ Job = (*processJob)(nil)
