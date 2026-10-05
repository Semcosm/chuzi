// Package slotagent defines the closed command protocol shared by the service
// and the Windows slot user agent. It deliberately carries no command text or
// executable and desktop paths.
package slotagent

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/Semcosm/chuzi/internal/protocol"
)

type Command string

// AgentVersion is the closed wire-safe identity returned by the Windows user
// agent during health checks. It must satisfy Response.Validate's identifier
// grammar because it crosses the authenticated protocol boundary.
const AgentVersion = "chuzi-user-agent-v1"

const (
	PrepareSlot Command = "prepare_slot"
	StartJob    Command = "start_job"
	CancelJob   Command = "cancel_job"
	StopJob     Command = "stop_job"
	Health      Command = "health"
	Shutdown    Command = "shutdown"
)

type JobKind string

const (
	BrowserWorker JobKind = "browser_worker"
	Adapter       JobKind = "adapter"
)

var (
	ErrInvalidMessage = errors.New("slotagent: invalid message")
	ErrStaleLease     = errors.New("slotagent: stale lease")
	ErrDuplicate      = errors.New("slotagent: duplicate request")
	ErrUnsupported    = errors.New("slotagent: unsupported command")
)

const maxSeenCommands = 4096

// Request is the complete wire shape. Optional fields are still closed enums
// and identifiers; arbitrary argv, executable paths and script text cannot be
// represented. LeaseID is a short-lived maintenance ticket for prepare/health
// and the durable slot lease for job operations.
type Request struct {
	CommandID             string  `json:"command_id"`
	RequestID             string  `json:"request_id"`
	Owner                 string  `json:"owner,omitempty"`
	AccountID             string  `json:"account_id,omitempty"`
	SlotID                string  `json:"slot_id"`
	LeaseID               string  `json:"lease_id"`
	EnvironmentGeneration uint64  `json:"environment_generation"`
	Auth                  string  `json:"auth,omitempty"`
	Command               Command `json:"command"`
	JobKind               JobKind `json:"job_kind,omitempty"`
}

type Response struct {
	CommandID             string `json:"command_id"`
	RequestID             string `json:"request_id"`
	Owner                 string `json:"owner,omitempty"`
	SlotID                string `json:"slot_id"`
	LeaseID               string `json:"lease_id"`
	EnvironmentGeneration uint64 `json:"environment_generation"`
	OK                    bool   `json:"ok"`
	Failure               string `json:"failure,omitempty"`
	AgentVersion          string `json:"agent_version,omitempty"`
	SessionState          string `json:"session_state,omitempty"`
}

func (r Response) Validate() error {
	if r.CommandID != "" && !safeID(r.CommandID, 128) ||
		r.RequestID != "" && !safeID(r.RequestID, 128) ||
		r.Owner != "" && !safeID(r.Owner, 160) ||
		r.SlotID != "" && !safeID(r.SlotID, 128) ||
		r.LeaseID != "" && !safeID(r.LeaseID, 160) ||
		r.EnvironmentGeneration == 0 && (r.CommandID != "" || r.RequestID != "" || r.SlotID != "" || r.LeaseID != "") ||
		r.Failure != "" && !safeID(r.Failure, 64) ||
		r.AgentVersion != "" && !safeID(r.AgentVersion, 64) {
		return ErrInvalidMessage
	}
	switch r.SessionState {
	case "", "ready", "disconnected", "logged_off", "unavailable":
		return nil
	default:
		return ErrInvalidMessage
	}
}

func (r Request) Validate() error {
	if !safeID(r.CommandID, 128) || !safeID(r.RequestID, 128) || !safeID(r.Owner, 160) || !safeID(r.SlotID, 128) || !safeID(r.LeaseID, 160) || r.EnvironmentGeneration == 0 || (r.Auth != "" && !safeID(r.Auth, 256)) {
		return ErrInvalidMessage
	}
	switch r.Command {
	case PrepareSlot, StartJob, CancelJob, StopJob, Health, Shutdown:
	default:
		return ErrUnsupported
	}
	if r.Command == StartJob {
		if (r.JobKind != BrowserWorker && r.JobKind != Adapter) || !safeID(r.AccountID, 128) {
			return ErrInvalidMessage
		}
	} else if r.Command == PrepareSlot {
		if r.JobKind != "" || !safeID(r.AccountID, 128) {
			return ErrInvalidMessage
		}
	} else if r.JobKind != "" || r.AccountID != "" {
		return ErrInvalidMessage
	}
	return nil
}

func safeID(value string, max int) bool {
	return value != "" && len(value) <= max && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\\/\r\n\t\x00")
}

// LeaseValidator compares every operation against the service's authoritative
// slot lease. The agent never decides account business state.
type LeaseValidator interface {
	ValidateAgentLease(context.Context, Request) error
}

// WorkerLeaseValidator is the optional service-side fence for worker frames.
// Keeping it separate from LeaseValidator prevents alternate worker handlers
// from bypassing the durable lease check at the protocol boundary.
type WorkerLeaseValidator interface {
	ValidateAgentWorkerLease(context.Context, Frame) error
}

type Handler interface {
	HandleAgentCommand(context.Context, Request) (Response, error)
}

// WorkerHandler is the optional bridge for the versioned worker protocol.
// Implementations receive only the closed lifecycle envelopes; runtime
// configuration remains service-derived and is never carried on the wire.
type WorkerHandler interface {
	HandleWorkerFrame(context.Context, Frame) (Frame, error)
	DisconnectJobs(context.Context)
}

type Server struct {
	slotID     string
	generation uint64
	leases     LeaseValidator
	handler    Handler
	mu         sync.Mutex
	seen       map[string]struct{}
	seenOrder  []string
}

func NewServer(slotID string, generation uint64, leases LeaseValidator, handler Handler) (*Server, error) {
	if !safeID(slotID, 128) || generation == 0 || leases == nil || handler == nil {
		return nil, ErrInvalidMessage
	}
	return &Server{slotID: slotID, generation: generation, leases: leases, handler: handler, seen: make(map[string]struct{}, maxSeenCommands), seenOrder: make([]string, 0, maxSeenCommands)}, nil
}

func (s *Server) Handle(ctx context.Context, request Request) (Response, error) {
	if s == nil || ctx == nil || request.Validate() != nil || request.SlotID != s.slotID || request.EnvironmentGeneration != s.generation {
		return Response{}, ErrInvalidMessage
	}
	if err := s.leases.ValidateAgentLease(ctx, request); err != nil {
		if handler, ok := s.handler.(WorkerHandler); ok {
			handler.DisconnectJobs(ctx)
		}
		return Response{}, ErrStaleLease
	}
	s.mu.Lock()
	if _, exists := s.seen[request.CommandID]; exists {
		s.mu.Unlock()
		return Response{}, ErrDuplicate
	}
	// Command IDs are unique within the service, so retaining only the recent
	// bounded window prevents a long-lived agent from exhausting its protocol
	// state while still rejecting retries and immediate duplicates.
	if len(s.seenOrder) >= maxSeenCommands {
		oldest := s.seenOrder[0]
		delete(s.seen, oldest)
		s.seenOrder = s.seenOrder[1:]
	}
	s.seen[request.CommandID] = struct{}{}
	s.seenOrder = append(s.seenOrder, request.CommandID)
	s.mu.Unlock()
	response, err := s.handler.HandleAgentCommand(ctx, request)
	if err != nil {
		return Response{}, err
	}
	response.CommandID = request.CommandID
	response.RequestID = request.RequestID
	response.Owner = request.Owner
	response.SlotID = s.slotID
	response.LeaseID = request.LeaseID
	response.EnvironmentGeneration = s.generation
	return response, nil
}

// HandleWorker forwards one approved worker envelope to the active job.
func (s *Server) HandleWorker(ctx context.Context, frame Frame) (Frame, error) {
	if s == nil || ctx == nil || frame.Validate() != nil || frame.Kind != "worker_request" || frame.SlotID != s.slotID || frame.EnvironmentGeneration != s.generation {
		return Frame{}, ErrInvalidMessage
	}
	handler, ok := s.handler.(WorkerHandler)
	if !ok {
		return Frame{}, ErrUnsupported
	}
	if validator, ok := s.leases.(WorkerLeaseValidator); ok {
		if err := validator.ValidateAgentWorkerLease(ctx, frame); err != nil {
			if errors.Is(err, ErrStaleLease) {
				handler.DisconnectJobs(ctx)
			}
			return Frame{}, err
		}
	}
	response, err := handler.HandleWorkerFrame(ctx, frame)
	if errors.Is(err, ErrStaleLease) {
		// A worker frame can arrive after the durable slot lease expires and
		// before the control command that normally disconnects the job. Stop
		// the residual runtime at the protocol boundary as soon as the fence
		// is observed.
		handler.DisconnectJobs(ctx)
	}
	return response, err
}

// Frame multiplexes the closed control commands with the versioned browser
// worker protocol. Worker frames must contain only approved lifecycle fields.
type Frame struct {
	Kind                  string             `json:"kind"`
	Request               *Request           `json:"request,omitempty"`
	Response              *Response          `json:"response,omitempty"`
	SlotID                string             `json:"slot_id,omitempty"`
	RequestID             string             `json:"request_id,omitempty"`
	Owner                 string             `json:"owner,omitempty"`
	LeaseID               string             `json:"lease_id,omitempty"`
	EnvironmentGeneration uint64             `json:"environment_generation,omitempty"`
	Worker                *protocol.Envelope `json:"worker,omitempty"`
}

func ValidateWorkerEnvelope(message protocol.Envelope) error {
	if message.Protocol != protocol.Version || !safeID(message.ID, 128) || message.Error != "" {
		return ErrInvalidMessage
	}
	switch message.Type {
	case protocol.Hello, protocol.HelloAck, protocol.Ping, protocol.Pong, protocol.Shutdown, protocol.ShutdownAck, protocol.SessionStart, protocol.SessionStarted, protocol.SessionSuccess, protocol.SessionFailure, protocol.SessionCancel, protocol.SessionCancelled:
	default:
		return ErrUnsupported
	}
	if len(message.Payload) > 16 {
		return ErrInvalidMessage
	}
	for key, value := range message.Payload {
		if !workerPayloadKeyAllowed(message.Type, key) || !safeWorkerText(value, 512) {
			return ErrInvalidMessage
		}
	}
	return validateWorkerPayload(message.Type, message.Payload)
}

// Worker payloads are deliberately closed by message type. The worker
// process is untrusted runtime code, so a generic string map must not become
// a covert path for profiles, credentials, executable arguments, or host data.
func workerPayloadKeyAllowed(messageType, key string) bool {
	allowed := map[string]map[string]struct{}{
		protocol.Hello:            {"service": {}, "version": {}},
		protocol.HelloAck:         {"service": {}, "browserRuntime": {}, "capabilities": {}},
		protocol.SessionStart:     {"session_id": {}, "account_id": {}, "request_id": {}, "mode": {}},
		protocol.SessionStarted:   {"session_id": {}, "runtime": {}, "session_handle": {}, "cdp_host": {}, "cdp_port": {}, "browser_product": {}, "protocol_version": {}},
		protocol.SessionSuccess:   {"session_id": {}},
		protocol.SessionFailure:   {"session_id": {}, "failure": {}, "reason": {}},
		protocol.SessionCancel:    {"session_id": {}},
		protocol.SessionCancelled: {"session_id": {}, "already_stopped": {}},
	}
	_, ok := allowed[messageType][key]
	return ok
}

func safeWorkerText(value string, max int) bool {
	return len(value) <= max && !strings.ContainsAny(value, "\x00\r\n")
}

func validateWorkerPayload(messageType string, payload map[string]string) error {
	for _, key := range []string{"session_id", "account_id", "request_id", "service", "version", "browser_product", "protocol_version", "session_handle"} {
		if value, ok := payload[key]; ok && !safeID(value, 256) {
			return ErrInvalidMessage
		}
	}
	if value, ok := payload["service"]; ok && value != "chuzi-session-runner" && value != "chuzi-browser-worker" {
		return ErrInvalidMessage
	}
	if value, ok := payload["version"]; ok && value != protocol.Version {
		return ErrInvalidMessage
	}
	if value, ok := payload["mode"]; ok {
		switch value {
		case "deferred", "headless", "headed", "hold", "success", "failure", "crash", "probe", "adapter":
		default:
			return ErrInvalidMessage
		}
	}
	if value, ok := payload["runtime"]; ok && value != "headless-cdp" && value != "headed-cdp" && value != "deferred" {
		return ErrInvalidMessage
	}
	if value, ok := payload["browserRuntime"]; ok && value != "deferred" && value != "headless-cdp" && value != "headed-cdp" {
		return ErrInvalidMessage
	}
	if value, ok := payload["cdp_host"]; ok && value != "127.0.0.1" {
		return ErrInvalidMessage
	}
	if value, ok := payload["cdp_port"]; ok && (value == "" || len(value) > 5 || strings.Trim(value, "0123456789") != "") {
		return ErrInvalidMessage
	}
	if value, ok := payload["failure"]; ok {
		switch value {
		case "transient", "credential", "permission", "configuration", "runtime", "unknown":
		default:
			return ErrInvalidMessage
		}
	}
	if value, ok := payload["already_stopped"]; ok && value != "true" && value != "false" {
		return ErrInvalidMessage
	}
	if value, ok := payload["capabilities"]; ok {
		known := map[string]struct{}{
			"protocol.v1": {}, "browser-runtime.contract": {}, "browser-runtime.cdp": {},
			"browser-runtime.headless-cdp": {}, "browser-runtime.headed-cdp": {}, "session.cdp": {},
		}
		for _, capability := range strings.Split(value, ",") {
			if capability == "" || !safeID(capability, 128) {
				return ErrInvalidMessage
			}
			if _, ok := known[capability]; !ok {
				return ErrInvalidMessage
			}
		}
	}
	if value, ok := payload["browser_product"]; ok {
		switch value {
		case "Chrome", "Chromium", "FakeChromium", "Firefox", "HeadlessChrome", "Microsoft Edge", "unknown":
		default:
			return ErrInvalidMessage
		}
	}
	if value, ok := payload["reason"]; ok {
		// Reasons are user-visible diagnostics. Keep this to the fixed worker
		// vocabulary so a runtime cannot smuggle arbitrary error text across the
		// service boundary.
		switch value {
		case "browser runtime is deferred", "cancelled", "browser_command_unavailable", "browser_crashed", "cdp_endpoint_timeout", "cdp_endpoint_invalid", "browser_command_missing", "browser_mode_invalid", "windows_desktop_invalid", "windows_desktop_unsupported", "profile_path_invalid", "automation_not_configured", "browser_runtime_failed":
		default:
			return ErrInvalidMessage
		}
	}
	_ = messageType
	return nil
}
