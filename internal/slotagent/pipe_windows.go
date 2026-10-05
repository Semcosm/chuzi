//go:build windows

package slotagent

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"time"

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
	response := Response{OK: true, AgentVersion: h.Version, SessionState: state}
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
// The pipe ACL permits the local service and the owning interactive user.
func ServeNamedPipe(ctx context.Context, path string, server *Server) error {
	if ctx == nil || server == nil || path != "\\\\.\\pipe\\chuzi-slot-"+server.slotID {
		return ErrInvalidMessage
	}
	descriptor := "D:P(A;;GA;;;SY)(A;;GA;;;OW)"
	if serviceSID := os.Getenv("CHUZI_AGENT_PIPE_SERVICE_SID"); serviceSID != "" {
		sid, sidErr := windows.StringToSid(serviceSID)
		if sidErr != nil {
			return ErrInvalidMessage
		}
		descriptor += "(A;;GA;;;" + sid.String() + ")"
	}
	if userSID := os.Getenv("CHUZI_AGENT_PIPE_USER_SID"); userSID != "" {
		sid, sidErr := windows.StringToSid(userSID)
		if sidErr != nil {
			return ErrInvalidMessage
		}
		descriptor += "(A;;GA;;;" + sid.String() + ")"
	}
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
		go serveConn(serveCtx, conn, server, func() {
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
	if ctx == nil || path == "" {
		return nil, ErrInvalidMessage
	}
	for {
		conn, err := winio.DialPipeContext(ctx, path)
		if err == nil {
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

func serveConn(ctx context.Context, conn net.Conn, server *Server, stop func()) {
	defer conn.Close()
	workerHandler, _ := server.handler.(WorkerHandler)
	workerSeen := false
	defer func() {
		if workerHandler != nil && workerSeen {
			workerHandler.DisconnectJobs(context.Background())
		}
	}()
	for {
		frame, err := ReadFrame(conn)
		if errors.Is(err, io.EOF) || ctx.Err() != nil {
			return
		}
		if err != nil {
			_ = WriteFrame(conn, Frame{Kind: "response", Response: &Response{OK: false, Failure: "invalid_frame"}})
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
			if err := WriteFrame(conn, Frame{Kind: "response", Response: &response}); err != nil {
				return
			}
			if frame.Request.Command == Shutdown {
				stop()
				return
			}
		case "worker_request":
			workerSeen = true
			if workerHandler == nil {
				_ = WriteFrame(conn, Frame{Kind: "response", Response: &Response{OK: false, Failure: "unsupported_frame"}})
				return
			}
			response, handleErr := server.HandleWorker(ctx, frame)
			if handleErr != nil {
				response = Frame{Kind: "response", Response: &Response{OK: false, Failure: failureClass(handleErr), RequestID: frame.RequestID, Owner: frame.Owner, SlotID: frame.SlotID, LeaseID: frame.LeaseID, EnvironmentGeneration: frame.EnvironmentGeneration}}
			} else {
				response.Kind = "worker_event"
			}
			if err := WriteFrame(conn, response); err != nil {
				return
			}
		default:
			_ = WriteFrame(conn, Frame{Kind: "response", Response: &Response{OK: false, Failure: "unsupported_frame"}})
			return
		}
	}
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
