//go:build windows

package slotagent

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"github.com/Semcosm/chuzi/internal/protocol"
	"golang.org/x/sys/windows"
)

var (
	ErrAgentStopped = errors.New("slotagent: agent stopped")
	ErrAgentAuth    = errors.New("slotagent: agent authentication failed")
)

type pipeDialError struct{ class string }

func (e pipeDialError) Error() string { return "slotagent: named pipe dial failed" }
func (e pipeDialError) Unwrap() error { return ErrAgentStopped }

// PipeDialFailureClass exposes only a bounded diagnostic category. Callers
// must not depend on raw winio or Win32 errors crossing the agent boundary.
func PipeDialFailureClass(err error) string {
	var dialErr pipeDialError
	if errors.As(err, &dialErr) {
		return dialErr.class
	}
	return ""
}

func classifyPipeDialError(err error) string {
	switch {
	case errors.Is(err, windows.ERROR_ACCESS_DENIED):
		return "access_denied"
	case errors.Is(err, windows.ERROR_FILE_NOT_FOUND), errors.Is(err, windows.ERROR_PATH_NOT_FOUND):
		return "pipe_missing"
	case errors.Is(err, windows.ERROR_PIPE_BUSY):
		return "pipe_busy"
	case errors.Is(err, windows.ERROR_SEM_TIMEOUT), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "unknown"
	}
}

const agentServiceSID = "S-1-5-18"

const (
	agentReadTimeout  = 15 * time.Second
	agentWriteTimeout = 10 * time.Second
)

// PipeConfig is the service-derived identity handoff for one agent pipe. The
// service SID is fixed to LocalSystem; only the exact service image path is
// supplied by the already-authenticated service process.
type PipeConfig struct {
	ServiceSID                string
	ExpectedServiceExecutable string
}

func (c PipeConfig) validate() error {
	if c.ServiceSID != "" && c.ServiceSID != agentServiceSID {
		return ErrInvalidMessage
	}
	path := strings.TrimSpace(c.ExpectedServiceExecutable)
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
		return ErrInvalidMessage
	}
	return nil
}

func normalizeAgentPath(value string) string {
	path := strings.TrimSpace(strings.ReplaceAll(value, "/", "\\"))
	path = strings.TrimPrefix(path, `\\?\`)
	return strings.ToLower(strings.TrimRight(filepath.Clean(path), `\`))
}

type agentCallerIdentity struct {
	SID           string
	SessionID     uint32
	Executable    string
	Authenticated bool
}

func validateAgentCallerIdentity(identity agentCallerIdentity, config PipeConfig) error {
	if !identity.Authenticated || identity.SID != agentServiceSID || identity.SessionID != 0 || identity.Executable == "" {
		return ErrAgentAuth
	}
	if !validAgentExecutablePath(identity.Executable, config.ExpectedServiceExecutable) {
		return ErrAgentAuth
	}
	return nil
}

func validAgentExecutablePath(actual, expected string) bool {
	if strings.TrimSpace(actual) == "" || strings.TrimSpace(expected) == "" {
		return false
	}
	return filepath.IsAbs(expected) && normalizeAgentPath(actual) == normalizeAgentPath(expected)
}

func authenticateAgentCaller(ctx context.Context, conn net.Conn, config PipeConfig) error {
	identity, err := authenticatedAgentCaller(ctx, conn)
	if err != nil {
		return ErrAgentAuth
	}
	return validateAgentCallerIdentity(identity, config)
}

var (
	agentAuthAdvapi32             = windows.NewLazySystemDLL("advapi32.dll")
	agentAuthKernel32             = windows.NewLazySystemDLL("kernel32.dll")
	agentAuthImpersonateNamedPipe = agentAuthAdvapi32.NewProc("ImpersonateNamedPipeClient")
	agentAuthGetClientProcessID   = agentAuthKernel32.NewProc("GetNamedPipeClientProcessId")
	agentAuthGetServerProcessID   = agentAuthKernel32.NewProc("GetNamedPipeServerProcessId")
)

type agentPipeHandleConn interface{ Fd() uintptr }

func authenticatedAgentCaller(ctx context.Context, conn net.Conn) (agentCallerIdentity, error) {
	if ctx == nil || conn == nil {
		return agentCallerIdentity{}, ErrAgentAuth
	}
	select {
	case <-ctx.Done():
		return agentCallerIdentity{}, ErrAgentAuth
	default:
	}
	pipe, ok := conn.(agentPipeHandleConn)
	if !ok || pipe.Fd() == 0 || pipe.Fd() == ^uintptr(0) {
		return agentCallerIdentity{}, ErrAgentAuth
	}
	handle := windows.Handle(pipe.Fd())
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := impersonateAgentPipeClient(handle); err != nil {
		return agentCallerIdentity{}, ErrAgentAuth
	}
	defer windows.RevertToSelf()
	var token windows.Token
	if err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &token); err != nil {
		return agentCallerIdentity{}, ErrAgentAuth
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return agentCallerIdentity{}, ErrAgentAuth
	}
	sid := user.User.Sid.String()
	sessionID, err := agentTokenUint32(token, windows.TokenSessionId)
	if err != nil {
		return agentCallerIdentity{}, ErrAgentAuth
	}
	pid, err := agentNamedPipeProcessID(handle, agentAuthGetClientProcessID)
	if err != nil || pid == 0 {
		return agentCallerIdentity{}, ErrAgentAuth
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return agentCallerIdentity{}, ErrAgentAuth
	}
	defer windows.CloseHandle(process)
	var processToken windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &processToken); err != nil {
		return agentCallerIdentity{}, ErrAgentAuth
	}
	defer processToken.Close()
	processUser, err := processToken.GetTokenUser()
	if err != nil || processUser == nil || processUser.User.Sid == nil || !processUser.User.Sid.Equals(user.User.Sid) {
		return agentCallerIdentity{}, ErrAgentAuth
	}
	processSession, err := agentTokenUint32(processToken, windows.TokenSessionId)
	if err != nil || processSession != sessionID {
		return agentCallerIdentity{}, ErrAgentAuth
	}
	executable, err := agentProcessImagePath(process)
	if err != nil || strings.TrimSpace(executable) == "" {
		return agentCallerIdentity{}, ErrAgentAuth
	}
	if currentPID, err := agentNamedPipeProcessID(handle, agentAuthGetClientProcessID); err != nil || currentPID != pid {
		return agentCallerIdentity{}, ErrAgentAuth
	}
	return agentCallerIdentity{SID: sid, SessionID: sessionID, Executable: executable, Authenticated: true}, nil
}

func validateAgentServer(conn net.Conn, expectedExecutable string) error {
	pipe, ok := conn.(agentPipeHandleConn)
	if !ok || pipe.Fd() == 0 || pipe.Fd() == ^uintptr(0) {
		return ErrAgentAuth
	}
	handle := windows.Handle(pipe.Fd())
	pid, err := agentNamedPipeProcessID(handle, agentAuthGetServerProcessID)
	if err != nil || pid == 0 {
		return ErrAgentAuth
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ErrAgentAuth
	}
	defer windows.CloseHandle(process)
	executable, err := agentProcessImagePath(process)
	if err != nil || !validAgentExecutablePath(executable, expectedExecutable) {
		return ErrAgentAuth
	}
	if currentPID, err := agentNamedPipeProcessID(handle, agentAuthGetServerProcessID); err != nil || currentPID != pid {
		return ErrAgentAuth
	}
	return nil
}

func impersonateAgentPipeClient(handle windows.Handle) error {
	r1, _, callErr := agentAuthImpersonateNamedPipe.Call(uintptr(handle))
	if r1 == 0 {
		if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
			return callErr
		}
		return windows.ERROR_ACCESS_DENIED
	}
	return nil
}

func agentNamedPipeProcessID(handle windows.Handle, proc *windows.LazyProc) (uint32, error) {
	var pid uint32
	r1, _, callErr := proc.Call(uintptr(handle), uintptr(unsafe.Pointer(&pid)))
	if r1 == 0 {
		if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
			return 0, callErr
		}
		return 0, windows.ERROR_INVALID_HANDLE
	}
	return pid, nil
}

func agentTokenUint32(token windows.Token, class uint32) (uint32, error) {
	var value uint32
	var returned uint32
	if err := windows.GetTokenInformation(token, class, (*byte)(unsafe.Pointer(&value)), uint32(unsafe.Sizeof(value)), &returned); err != nil {
		return 0, err
	}
	if returned < uint32(unsafe.Sizeof(value)) {
		return 0, windows.ERROR_INVALID_DATA
	}
	return value, nil
}

func agentProcessImagePath(process windows.Handle) (string, error) {
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &size); err != nil {
		return "", err
	}
	return syscall.UTF16ToString(buffer[:size]), nil
}

// LeaseState is the service-derived lease fence held by one user agent. The
// agent never extends this lease; it only compares commands with the latest
// values provided by the service.
type LeaseState struct {
	mu         sync.RWMutex
	SlotID     string
	Generation uint64
	LeaseID    string
	RequestID  string
	Owner      string
	AccountID  string
	Token      string
	Valid      bool
}

type leaseSnapshot struct {
	SlotID     string
	Generation uint64
	LeaseID    string
	RequestID  string
	Owner      string
	AccountID  string
	Token      string
	Valid      bool
}

func (s *LeaseState) snapshot() (leaseSnapshot, bool) {
	if s == nil {
		return leaseSnapshot{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return leaseSnapshot{SlotID: s.SlotID, Generation: s.Generation, LeaseID: s.LeaseID, RequestID: s.RequestID, Owner: s.Owner, AccountID: s.AccountID, Token: s.Token, Valid: s.Valid}, s.Valid
}

func (s *LeaseState) restore(snapshot leaseSnapshot) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.SlotID, s.Generation, s.LeaseID, s.RequestID, s.Owner, s.AccountID, s.Token, s.Valid = snapshot.SlotID, snapshot.Generation, snapshot.LeaseID, snapshot.RequestID, snapshot.Owner, snapshot.AccountID, snapshot.Token, snapshot.Valid
	s.mu.Unlock()
}

func (s *LeaseState) ValidateAgentLease(_ context.Context, request Request) error {
	if s == nil {
		return ErrAgentAuth
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.Valid || request.SlotID != s.SlotID || request.EnvironmentGeneration != s.Generation || (s.Owner != "" && request.Owner != "" && request.Owner != s.Owner) || (s.Token != "" && request.Auth != s.Token) {
		return ErrStaleLease
	}
	if request.Command == PrepareSlot {
		return nil
	}
	if request.LeaseID != s.LeaseID || request.RequestID != s.RequestID {
		return ErrStaleLease
	}
	if request.Command == StartJob && (request.RequestID != s.RequestID || request.AccountID != s.AccountID) {
		return ErrStaleLease
	}
	return nil
}

func (s *LeaseState) Update(slotID string, generation uint64, leaseID, requestID, accountID, token string) error {
	return s.UpdateOwned(slotID, generation, leaseID, requestID, "service", accountID, token)
}

func (s *LeaseState) UpdateOwned(slotID string, generation uint64, leaseID, requestID, owner, accountID, token string) error {
	if !safeID(slotID, 128) || generation == 0 || !safeID(leaseID, 160) || !safeID(requestID, 128) || !safeID(owner, 160) || !safeID(accountID, 128) || (token != "" && !safeID(token, 256)) {
		return ErrInvalidMessage
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.SlotID, s.Generation, s.LeaseID, s.RequestID, s.Owner, s.AccountID, s.Token, s.Valid = slotID, generation, leaseID, requestID, owner, accountID, token, true
	return nil
}

func (s *LeaseState) Invalidate() {
	if s != nil {
		s.mu.Lock()
		s.Valid = false
		s.mu.Unlock()
	}
}

func (s *LeaseState) validateWorkerFrame(frame Frame) error {
	if s == nil {
		return ErrAgentAuth
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.Valid || frame.SlotID != s.SlotID || frame.EnvironmentGeneration != s.Generation || frame.LeaseID != s.LeaseID || frame.RequestID != s.RequestID || (s.Owner != "" && frame.Owner != s.Owner) {
		return ErrStaleLease
	}
	return nil
}

// ValidateAgentWorkerLease exposes the same service-derived fence used by the
// concrete runtime handler to the protocol server before dispatch.
func (s *LeaseState) ValidateAgentWorkerLease(_ context.Context, frame Frame) error {
	return s.validateWorkerFrame(frame)
}

type RuntimeHandler struct {
	mu               sync.Mutex
	Version          string
	SessionState     string
	SessionStateFunc func() string
	Leases           *LeaseState
	Launcher         JobLauncher
	active           bool
	closed           bool
	jobs             map[string]Job
	bootstrap        leaseSnapshot
	bootstrapSet     bool
}

// JobLauncher starts only the fixed worker runtimes configured by the
// service. It receives no executable, shell, desktop, or profile path from a
// request.
type JobLauncher interface {
	Start(context.Context, Request) (Job, error)
}

// CapabilityProbe independently verifies that the fixed worker runtime is
// usable during an agent health check, without starting a user job.
type CapabilityProbe interface {
	Probe(context.Context) error
}

type Job interface {
	RoundTrip(context.Context, protocol.Envelope) (protocol.Envelope, error)
	Stop() error
}

func (h *RuntimeHandler) HandleAgentCommand(ctx context.Context, request Request) (Response, error) {
	if h == nil {
		return Response{}, ErrAgentStopped
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed && request.Command != Shutdown {
		return Response{}, ErrAgentStopped
	}
	state := h.SessionState
	if h.SessionStateFunc != nil {
		state = h.SessionStateFunc()
	}
	response := Response{OK: true, AgentVersion: h.Version, SessionState: state, WorkerReady: true}
	if request.Command == Health {
		if h.Launcher == nil {
			response.OK = false
			response.WorkerReady = false
			response.Failure = "worker_unavailable"
		} else if probe, ok := h.Launcher.(CapabilityProbe); ok {
			if err := probe.Probe(ctx); err != nil {
				response.OK = false
				response.WorkerReady = false
				response.Failure = "worker_unavailable"
			}
		} else {
			response.OK = false
			response.WorkerReady = false
			response.Failure = "worker_unavailable"
		}
	}
	switch request.Command {
	case PrepareSlot:
		if h.Leases == nil || request.Auth == "" {
			return Response{}, ErrAgentAuth
		}
		if !h.bootstrapSet {
			bootstrap, ok := h.Leases.snapshot()
			if !ok {
				return Response{}, ErrAgentAuth
			}
			h.bootstrap, h.bootstrapSet = bootstrap, true
		}
		var stopErr error
		for id, job := range h.jobs {
			stopErr = errors.Join(stopErr, job.Stop())
			delete(h.jobs, id)
		}
		h.active = false
		if h.bootstrapSet {
			h.Leases.restore(h.bootstrap)
		}
		if stopErr != nil {
			return Response{}, stopErr
		}
		if err := h.Leases.UpdateOwned(request.SlotID, request.EnvironmentGeneration, request.LeaseID, request.RequestID, request.Owner, request.AccountID, request.Auth); err != nil {
			return Response{}, err
		}
	case StartJob:
		if h.Launcher == nil {
			err := ErrUnsupported
			writeRuntimeStartDiagnostic("handler_no_launcher", err, nil)
			return Response{}, err
		}
		if h.jobs == nil {
			h.jobs = make(map[string]Job)
		}
		if _, exists := h.jobs[request.RequestID]; exists {
			err := ErrDuplicate
			writeRuntimeStartDiagnostic("handler_duplicate", err, nil)
			return Response{}, err
		}
		job, err := h.Launcher.Start(ctx, request)
		if err != nil {
			writeRuntimeStartDiagnostic("handler_launcher", err, nil)
			return Response{}, err
		}
		if job == nil {
			err := ErrAgentStopped
			writeRuntimeStartDiagnostic("handler_nil_job", err, nil)
			return Response{}, err
		}
		h.jobs[request.RequestID] = job
		h.active = true
	case CancelJob, StopJob:
		var stopErr error
		if job := h.jobs[request.RequestID]; job != nil {
			stopErr = job.Stop()
			delete(h.jobs, request.RequestID)
		}
		h.active = false
		if h.bootstrapSet {
			h.Leases.restore(h.bootstrap)
		}
		if stopErr != nil {
			return Response{}, stopErr
		}
	case Health:
	case Shutdown:
		var stopErr error
		for id, job := range h.jobs {
			stopErr = errors.Join(stopErr, job.Stop())
			delete(h.jobs, id)
		}
		h.active = false
		h.closed = true
		if stopErr != nil {
			return Response{}, stopErr
		}
	default:
		return Response{}, ErrUnsupported
	}
	if h.SessionState == "" {
		response.SessionState = "ready"
	}
	return response, nil
}

func (h *RuntimeHandler) HandleWorkerFrame(ctx context.Context, frame Frame) (Frame, error) {
	if h == nil || ctx == nil || frame.Worker == nil {
		return Frame{}, ErrInvalidMessage
	}
	if err := h.Leases.validateWorkerFrame(frame); err != nil {
		return Frame{}, err
	}
	h.mu.Lock()
	job := h.jobs[frame.RequestID]
	h.mu.Unlock()
	if job == nil {
		return Frame{}, ErrAgentStopped
	}
	response, err := job.RoundTrip(ctx, *frame.Worker)
	if err != nil {
		writeRuntimeStartDiagnostic("worker_roundtrip", err, nil)
		return Frame{}, err
	}
	return Frame{Kind: "worker_event", SlotID: frame.SlotID, RequestID: frame.RequestID, Owner: frame.Owner, LeaseID: frame.LeaseID, EnvironmentGeneration: frame.EnvironmentGeneration, Worker: &response}, nil
}

func (h *RuntimeHandler) DisconnectJobs(_ context.Context) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, job := range h.jobs {
		_ = job.Stop()
		delete(h.jobs, id)
	}
	h.active = false
	if h.bootstrapSet {
		h.Leases.restore(h.bootstrap)
	}
}

// ServeNamedPipe accepts only bounded JSONL frames on a service-derived pipe.
// The default handoff binds the caller to the fixed chuzi.exe beside the
// agent image. Tests or a service wrapper with an explicit trusted image use
// ServeNamedPipeWithConfig.
func ServeNamedPipe(ctx context.Context, path string, server *Server) error {
	executable, err := os.Executable()
	if err != nil {
		return ErrInvalidMessage
	}
	return ServeNamedPipeWithConfig(ctx, path, server, PipeConfig{
		ServiceSID:                agentServiceSID,
		ExpectedServiceExecutable: filepath.Join(filepath.Dir(filepath.Clean(executable)), "chuzi.exe"),
	})
}

// ServeNamedPipeWithConfig binds the agent endpoint to a fixed service SID
// and exact service image. The config is a service-derived handoff, never a
// request field or an environment-selected trustee list.
func ServeNamedPipeWithConfig(ctx context.Context, path string, server *Server, config PipeConfig) error {
	if ctx == nil || server == nil || path != "\\\\.\\pipe\\chuzi-slot-"+server.slotID {
		return ErrInvalidMessage
	}
	if config.ServiceSID == "" {
		config.ServiceSID = agentServiceSID
	}
	if err := config.validate(); err != nil {
		return err
	}
	if _, err := windows.StringToSid(config.ServiceSID); err != nil {
		return ErrInvalidMessage
	}
	descriptor := "D:P(A;;GA;;;SY)(A;;GA;;;OW)"
	listener, err := winio.ListenPipe(path, &winio.PipeConfig{SecurityDescriptor: descriptor, InputBufferSize: MaxFrameBytes, OutputBufferSize: MaxFrameBytes})
	if err != nil {
		return ErrAgentStopped
	}
	defer listener.Close()
	serveCtx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		<-serveCtx.Done()
		_ = listener.Close()
	}()
	for {
		if err := serveCtx.Err(); err != nil {
			return nil
		}
		conn, err := listener.Accept()
		if err != nil {
			if serveCtx.Err() != nil {
				return nil
			}
			continue
		}
		go serveConn(serveCtx, conn, server, config, func() {
			stop()
			_ = listener.Close()
		})
	}
}

type Client struct {
	conn net.Conn
	mu   sync.Mutex
}

func Dial(ctx context.Context, path string) (*Client, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, pipeDialError{class: "unauthorized"}
	}
	return DialWithExecutable(ctx, path, filepath.Join(filepath.Dir(filepath.Clean(executable)), "chuzi-user-agent.exe"))
}

// DialWithExecutable is used by native smoke tooling whose disposable agent
// is outside the service executable directory. Production callers use Dial,
// which derives the fixed installed image beside chuzi.exe.
func DialWithExecutable(ctx context.Context, path, expectedExecutable string) (*Client, error) {
	if ctx == nil || path == "" {
		return nil, ErrInvalidMessage
	}
	if !filepath.IsAbs(expectedExecutable) || filepath.Clean(expectedExecutable) != expectedExecutable || !strings.EqualFold(filepath.Base(expectedExecutable), "chuzi-user-agent.exe") {
		return nil, ErrInvalidMessage
	}
	for {
		conn, err := winio.DialPipeContext(ctx, path)
		if err == nil {
			if err := validateAgentServer(conn, expectedExecutable); err != nil {
				_ = conn.Close()
				return nil, pipeDialError{class: "unauthorized"}
			}
			return &Client{conn: conn}, nil
		}
		class := classifyPipeDialError(err)
		if class != "pipe_missing" {
			return nil, pipeDialError{class: class}
		}
		// A newly-started agent can reach named-pipe setup just after the
		// service's first dial. Retry only the bounded missing-pipe race; the
		// caller's context still enforces the startup/health deadline.
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, pipeDialError{class: classifyPipeDialError(ctx.Err())}
		case <-timer.C:
		}
	}
}

func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

func (c *Client) Call(ctx context.Context, request Request) (Response, error) {
	if c == nil || c.conn == nil || ctx == nil || request.Validate() != nil {
		writeRuntimeStartDiagnostic("client_request_validation", ErrInvalidMessage, nil)
		return Response{}, ErrInvalidMessage
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		deadline = time.Now().Add(10 * time.Second)
	}
	_ = c.conn.SetDeadline(deadline)
	if err := WriteFrame(c.conn, Frame{Kind: "command", Request: &request}); err != nil {
		writeRuntimeStartDiagnostic("client_write", ErrAgentStopped, nil)
		return Response{}, ErrAgentStopped
	}
	frame, err := ReadFrame(c.conn)
	if err != nil || frame.Kind != "response" || frame.Response == nil {
		writeRuntimeStartDiagnostic("client_read", ErrAgentStopped, nil)
		return Response{}, ErrAgentStopped
	}
	if frame.Response.Validate() != nil || frame.Response.CommandID != request.CommandID || frame.Response.RequestID != request.RequestID || frame.Response.Owner != request.Owner || frame.Response.SlotID != request.SlotID || frame.Response.LeaseID != request.LeaseID || frame.Response.EnvironmentGeneration != request.EnvironmentGeneration {
		writeRuntimeStartDiagnostic("client_response_validation", ErrInvalidMessage, nil)
		return Response{}, ErrInvalidMessage
	}
	if !frame.Response.OK {
		switch frame.Response.Failure {
		case "stale_lease":
			writeRuntimeStartDiagnostic("client_failure_stale_lease", ErrStaleLease, nil)
			return Response{}, ErrStaleLease
		case "duplicate":
			writeRuntimeStartDiagnostic("client_failure_duplicate", ErrDuplicate, nil)
			return Response{}, ErrDuplicate
		case "unsupported":
			writeRuntimeStartDiagnostic("client_failure_unsupported", ErrUnsupported, nil)
			return Response{}, ErrUnsupported
		default:
			writeRuntimeStartDiagnostic("client_failure_agent", ErrAgentStopped, nil)
			return Response{}, ErrAgentStopped
		}
	}
	return *frame.Response, nil
}

// WorkerCall forwards one versioned worker envelope through the authenticated
// agent connection. The envelope is validated before it crosses the pipe.
func (c *Client) WorkerCall(ctx context.Context, frame Frame) (Frame, error) {
	if c == nil || c.conn == nil || ctx == nil || frame.Kind != "worker_request" || frame.Validate() != nil {
		return Frame{}, ErrInvalidMessage
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		deadline = time.Now().Add(10 * time.Second)
	}
	_ = c.conn.SetDeadline(deadline)
	if err := WriteFrame(c.conn, frame); err != nil {
		writeRuntimeStartDiagnostic("client_worker_write", ErrAgentStopped, nil)
		return Frame{}, ErrAgentStopped
	}
	response, err := ReadFrame(c.conn)
	if err != nil {
		writeRuntimeStartDiagnostic("client_worker_read", ErrAgentStopped, nil)
		return Frame{}, ErrAgentStopped
	}
	if response.Kind == "response" && response.Response != nil && !response.Response.OK {
		switch response.Response.Failure {
		case "stale_lease":
			return Frame{}, ErrStaleLease
		case "unsupported":
			return Frame{}, ErrUnsupported
		default:
			writeRuntimeStartDiagnostic("client_worker_failure", ErrAgentStopped, nil)
			return Frame{}, ErrAgentStopped
		}
	}
	if response.Kind != "worker_event" || response.Worker == nil || response.SlotID != frame.SlotID || response.RequestID != frame.RequestID || response.Owner != frame.Owner || response.LeaseID != frame.LeaseID || response.EnvironmentGeneration != frame.EnvironmentGeneration {
		writeRuntimeStartDiagnostic("client_worker_response_validation", ErrInvalidMessage, nil)
		return Frame{}, ErrInvalidMessage
	}
	return response, nil
}

func serveConn(ctx context.Context, conn net.Conn, server *Server, config PipeConfig, stop func()) {
	defer conn.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	if authenticateAgentCaller(ctx, conn, config) != nil {
		return
	}
	workerHandler, _ := server.handler.(WorkerHandler)
	workerSeen := false
	defer func() {
		if workerHandler != nil && workerSeen {
			workerHandler.DisconnectJobs(context.Background())
		}
	}()
	for {
		_ = conn.SetReadDeadline(time.Now().Add(agentReadTimeout))
		frame, err := ReadFrame(conn)
		if errors.Is(err, io.EOF) || ctx.Err() != nil {
			return
		}
		if err != nil {
			_ = writeAgentFrame(conn, Frame{Kind: "response", Response: &Response{OK: false, Failure: "invalid_frame"}})
			return
		}
		switch frame.Kind {
		case "command":
			if frame.Request.Command == StartJob {
				workerSeen = true
			}
			response, handleErr := server.Handle(ctx, *frame.Request)
			if handleErr != nil {
				if frame.Request.Command == StartJob {
					writeRuntimeStartDiagnostic("server_handle", handleErr, nil)
				}
				response = Response{CommandID: frame.Request.CommandID, RequestID: frame.Request.RequestID, Owner: frame.Request.Owner, SlotID: frame.Request.SlotID, LeaseID: frame.Request.LeaseID, EnvironmentGeneration: frame.Request.EnvironmentGeneration, OK: false, Failure: failureClass(handleErr)}
			}
			if err := writeAgentFrame(conn, Frame{Kind: "response", Response: &response}); err != nil {
				return
			}
			if frame.Request.Command == Shutdown {
				stop()
				return
			}
		case "worker_request":
			workerSeen = true
			if workerHandler == nil {
				_ = writeAgentFrame(conn, Frame{Kind: "response", Response: &Response{OK: false, Failure: "unsupported_frame"}})
				return
			}
			response, handleErr := server.HandleWorker(ctx, frame)
			if handleErr != nil {
				response = Frame{Kind: "response", Response: &Response{OK: false, Failure: failureClass(handleErr), RequestID: frame.RequestID, Owner: frame.Owner, SlotID: frame.SlotID, LeaseID: frame.LeaseID, EnvironmentGeneration: frame.EnvironmentGeneration}}
			} else {
				response.Kind = "worker_event"
			}
			if err := writeAgentFrame(conn, response); err != nil {
				return
			}
		default:
			_ = writeAgentFrame(conn, Frame{Kind: "response", Response: &Response{OK: false, Failure: "unsupported_frame"}})
			return
		}
	}
}

func writeAgentFrame(conn net.Conn, frame Frame) error {
	if conn == nil {
		return ErrAgentStopped
	}
	_ = conn.SetWriteDeadline(time.Now().Add(agentWriteTimeout))
	defer func() { _ = conn.SetWriteDeadline(time.Time{}) }()
	return WriteFrame(conn, frame)
}

func failureClass(err error) string {
	switch {
	case errors.Is(err, ErrStaleLease):
		return "stale_lease"
	case errors.Is(err, ErrDuplicate):
		return "duplicate"
	case errors.Is(err, ErrUnsupported):
		return "unsupported"
	case errors.Is(err, ErrAgentStopped):
		return "agent_stopped"
	default:
		return "agent_failure"
	}
}
