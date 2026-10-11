// Package sessionruntime contains the platform-neutral contract for one
// managed Windows execution-slot session. Platform adapters, persistence,
// and service assembly live in their existing packages.
package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	APIVersion              = "chuzi.session/v1"
	BrokerProtocolVersion   = 1
	BrokerPipeName          = "\\\\.\\pipe\\chuzi-session-bootstrap-v1"
	OwnershipJournalVersion = 1
)

var (
	ErrInvalidArgument    = errors.New("sessionruntime: invalid argument")
	ErrUnsupported        = errors.New("sessionruntime: unsupported")
	ErrUnauthorized       = errors.New("sessionruntime: unauthorized")
	ErrOwnershipUnknown   = errors.New("sessionruntime: ownership unknown")
	ErrOwnershipMismatch  = errors.New("sessionruntime: ownership mismatch")
	ErrStaleGeneration    = errors.New("sessionruntime: stale generation")
	ErrSessionUnavailable = errors.New("sessionruntime: session unavailable")
	ErrSessionChanged     = errors.New("sessionruntime: session changed")
	ErrDeadline           = errors.New("sessionruntime: deadline exceeded")
	ErrCancelled          = errors.New("sessionruntime: cancelled")
	ErrConflict           = errors.New("sessionruntime: conflict")
	ErrAlreadyStarted     = errors.New("sessionruntime: already started")
	ErrCleanup            = errors.New("sessionruntime: cleanup failed")
	ErrCapabilityDenied   = errors.New("sessionruntime: capability denied")
	ErrSecretInvalid      = errors.New("sessionruntime: invalid secret lease")
)

// Code is the stable, redacted classification exposed by the session API.
type Code string

const (
	CodeInvalidArgument    Code = "invalid_argument"
	CodeUnsupported        Code = "unsupported"
	CodeUnauthorized       Code = "unauthorized"
	CodeOwnershipUnknown   Code = "ownership_unknown"
	CodeOwnershipMismatch  Code = "ownership_mismatch"
	CodeStaleGeneration    Code = "stale_generation"
	CodeSessionUnavailable Code = "session_unavailable"
	CodeSessionChanged     Code = "session_changed"
	CodeDeadline           Code = "deadline_exceeded"
	CodeCancelled          Code = "cancelled"
	CodeConflict           Code = "conflict"
	CodeAlreadyStarted     Code = "already_started"
	CodeCleanup            Code = "cleanup_failed"
	CodeStaleReadiness     Code = "readiness_stale"
)

// Error is safe to serialize to Core/UI. Cause is deliberately private.
type Error struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
	cause   error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return "chuzi session: " + string(e.Code)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// Retryable exposes the stable retry policy without exposing a provider
// error. Ownership and identity failures remain non-retryable until an
// explicit recovery operation changes the durable fence.
func (e *Error) Retryable() bool {
	if e == nil {
		return false
	}
	switch e.Code {
	case CodeSessionUnavailable, CodeDeadline, CodeStaleReadiness, CodeConflict:
		return true
	default:
		return false
	}
}

func NewError(code Code, message string) *Error { return &Error{Code: code, Message: message} }

func CodeOf(err error) Code {
	if err == nil {
		return ""
	}
	var classified *Error
	if errors.As(err, &classified) && classified != nil && classified.Code != "" {
		return classified.Code
	}
	switch {
	case errors.Is(err, ErrInvalidArgument):
		return CodeInvalidArgument
	case errors.Is(err, ErrUnsupported):
		return CodeUnsupported
	case errors.Is(err, ErrUnauthorized), errors.Is(err, ErrCapabilityDenied):
		return CodeUnauthorized
	case errors.Is(err, ErrOwnershipUnknown):
		return CodeOwnershipUnknown
	case errors.Is(err, ErrOwnershipMismatch):
		return CodeOwnershipMismatch
	case errors.Is(err, ErrStaleGeneration):
		return CodeStaleGeneration
	case errors.Is(err, ErrSessionUnavailable):
		return CodeSessionUnavailable
	case errors.Is(err, ErrSessionChanged):
		return CodeSessionChanged
	case errors.Is(err, ErrDeadline):
		return CodeDeadline
	case errors.Is(err, ErrCancelled):
		return CodeCancelled
	case errors.Is(err, ErrConflict):
		return CodeConflict
	case errors.Is(err, ErrAlreadyStarted):
		return CodeAlreadyStarted
	case errors.Is(err, ErrCleanup):
		return CodeCleanup
	case errors.Is(err, ErrSecretInvalid):
		return CodeInvalidArgument
	default:
		return CodeSessionUnavailable
	}
}

type OperationState string

const (
	StateRequested    OperationState = "requested"
	StateProvisioning OperationState = "provisioning"
	StateStopping     OperationState = "stopping"
	StateCancelling   OperationState = "cancelling"
	StateRetrying     OperationState = "retrying"
	StateReady        OperationState = "ready"
	StateStopped      OperationState = "stopped"
	StateFailed       OperationState = "failed"
	StateCancelled    OperationState = "cancelled"
)

func (s OperationState) Terminal() bool {
	return s == StateReady || s == StateStopped || s == StateFailed || s == StateCancelled
}

type RuntimePhase string

const (
	PhaseEnvironment RuntimePhase = "environment"
	PhaseHost        RuntimePhase = "host_preflight"
	PhaseProfile     RuntimePhase = "profile"
	PhaseLogin       RuntimePhase = "login"
	PhaseWTS         RuntimePhase = "wts"
	PhaseDesktop     RuntimePhase = "desktop"
	PhaseShell       RuntimePhase = "shell"
	PhaseAgent       RuntimePhase = "agent"
	PhaseWorker      RuntimePhase = "worker"
	PhaseStopping    RuntimePhase = "stopping"
	PhaseStopped     RuntimePhase = "stopped"
)

type ReadinessLayer string

const (
	LayerEnvironment ReadinessLayer = "environment"
	LayerHost        ReadinessLayer = "host_preflight"
	LayerProfile     ReadinessLayer = "profile"
	LayerWTS         ReadinessLayer = "wts"
	LayerShell       ReadinessLayer = "shell"
	LayerAgent       ReadinessLayer = "agent"
	LayerWorker      ReadinessLayer = "worker"
)

type ReadinessState string

const (
	ReadinessUnknown ReadinessState = "unknown"
	ReadinessPending ReadinessState = "pending"
	ReadinessReady   ReadinessState = "ready"
	ReadinessFailed  ReadinessState = "failed"
	ReadinessStale   ReadinessState = "stale"
)

// ReadinessCheck is a time-bounded fact, never a permanent boolean.
type ReadinessCheck struct {
	Layer      ReadinessLayer `json:"layer"`
	State      ReadinessState `json:"state"`
	Generation uint64         `json:"generation,omitempty"`
	SessionID  uint32         `json:"session_id,omitempty"`
	ObservedAt time.Time      `json:"observed_at,omitempty"`
	FreshUntil time.Time      `json:"fresh_until,omitempty"`
	Failure    Code           `json:"failure,omitempty"`
}

func (c ReadinessCheck) Fresh(now time.Time) bool {
	return c.State == ReadinessReady && !c.ObservedAt.IsZero() && !c.FreshUntil.IsZero() && !now.Before(c.ObservedAt) && now.Before(c.FreshUntil)
}

// Identity is derived by the service from a durable slot record. It contains
// no username, password, endpoint, executable, Profile path, or command text.
type Identity struct {
	PoolID     string `json:"pool_id"`
	SlotID     string `json:"slot_id"`
	Ordinal    int    `json:"ordinal"`
	Generation uint64 `json:"generation"`
	SID        string `json:"sid"`
	Owner      string `json:"owner"`
}

// IdentityPublic is safe for Core/UI projections and intentionally omits SID.
type IdentityPublic struct {
	PoolID     string `json:"pool_id"`
	SlotID     string `json:"slot_id"`
	Ordinal    int    `json:"ordinal"`
	Generation uint64 `json:"generation"`
}

func (i Identity) Public() IdentityPublic {
	return IdentityPublic{PoolID: i.PoolID, SlotID: i.SlotID, Ordinal: i.Ordinal, Generation: i.Generation}
}

func (i Identity) Validate() error {
	if !validID(i.PoolID) || !validID(i.SlotID) || i.Ordinal < 1 || i.Ordinal > 256 || i.Generation == 0 || !validSID(i.SID) || !validID(i.Owner) {
		return ErrInvalidArgument
	}
	return nil
}

type Session struct {
	ID         uint32    `json:"session_id"`
	SID        string    `json:"sid"`
	State      string    `json:"state"`
	ObservedAt time.Time `json:"observed_at"`
}

func (s Session) ValidateActive(identity Identity) error {
	if err := identity.Validate(); err != nil || s.ID == 0 || s.State != "active" || s.SID != identity.SID || s.ObservedAt.IsZero() {
		return ErrSessionChanged
	}
	return nil
}

// Status is the current runtime projection, separate from operation state.
type Status struct {
	Phase       RuntimePhase     `json:"phase"`
	Session     Session          `json:"session"`
	AgentReady  bool             `json:"agent_ready"`
	WorkerReady bool             `json:"worker_ready"`
	Generation  uint64           `json:"generation"`
	Checks      []ReadinessCheck `json:"checks,omitempty"`
	UpdatedAt   time.Time        `json:"updated_at"`
}

type StartRequest struct {
	PoolID           string    `json:"pool_id,omitempty"`
	SlotID           string    `json:"slot_id,omitempty"`
	Actor            string    `json:"actor"`
	IdempotencyKey   string    `json:"idempotency_key"`
	ExpectedRevision uint64    `json:"expected_revision,omitempty"`
	Attempt          int       `json:"attempt,omitempty"`
	Deadline         time.Time `json:"deadline,omitempty"`
}

type StopRequest struct {
	PoolID           string    `json:"pool_id,omitempty"`
	SlotID           string    `json:"slot_id,omitempty"`
	Actor            string    `json:"actor"`
	IdempotencyKey   string    `json:"idempotency_key"`
	ExpectedRevision uint64    `json:"expected_revision,omitempty"`
	Generation       uint64    `json:"generation"`
	Deadline         time.Time `json:"deadline,omitempty"`
}

type CancelRequest struct {
	OperationID    string    `json:"operation_id"`
	Actor          string    `json:"actor"`
	IdempotencyKey string    `json:"idempotency_key"`
	Deadline       time.Time `json:"deadline,omitempty"`
}

type RetryRequest struct {
	OperationID    string    `json:"operation_id"`
	Actor          string    `json:"actor"`
	IdempotencyKey string    `json:"idempotency_key"`
	Attempt        int       `json:"attempt"`
	Deadline       time.Time `json:"deadline,omitempty"`
}

type Operation struct {
	OperationID string         `json:"operation_id"`
	PoolID      string         `json:"pool_id"`
	SlotID      string         `json:"slot_id"`
	Ordinal     int            `json:"ordinal"`
	State       OperationState `json:"state"`
	Phase       RuntimePhase   `json:"phase"`
	FailureCode Code           `json:"failure_code,omitempty"`
	Attempt     int            `json:"attempt"`
	Revision    uint64         `json:"revision"`
	Generation  uint64         `json:"generation"`
	RequestedAt time.Time      `json:"requested_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	Deadline    time.Time      `json:"deadline,omitempty"`
	CompletedAt time.Time      `json:"completed_at,omitempty"`
	Idempotent  bool           `json:"idempotent,omitempty"`
	Status      Status         `json:"status"`
}

// SessionAPI is the selected future control surface. Current Core implements
// StartSlotSession/GetSlotSessionOperation; other methods remain unsupported
// until their durable store operations land.
type SessionAPI interface {
	StartSlotSession(context.Context, StartRequest) (Operation, error)
	StopSlotSession(context.Context, StopRequest) (Operation, error)
	CancelSlotSession(context.Context, CancelRequest) (Operation, error)
	RetrySlotSession(context.Context, RetryRequest) (Operation, error)
	GetSlotSessionOperation(context.Context, string) (Operation, error)
	GetSlotReadiness(context.Context, string) (Status, error)
}

// SessionProvider is the credential-free broker boundary. A provider must
// establish a real WTS session; a process token is not a session substitute.
type SessionProvider interface {
	Start(context.Context, Identity) (Session, error)
	Stop(context.Context, Identity, Session) error
	Find(context.Context, string) (Session, error)
}

// HostFacts are redaction-safe preflight observations. Paths, certificates,
// account names, and native error text stay inside the host adapter.
type HostFacts struct {
	Edition     string    `json:"edition"`
	BuildClass  string    `json:"build_class"`
	Capacity    int       `json:"capacity"`
	RDPEnabled  bool      `json:"rdp_enabled"`
	NLARequired bool      `json:"nla_required"`
	CertTrusted bool      `json:"certificate_trusted"`
	ObservedAt  time.Time `json:"observed_at"`
}

type HostPreflight interface {
	Check(context.Context) (HostFacts, error)
}

// ProfileSpec contains only service-derived policy identifiers. The adapter
// derives the Profile from SID and must reject caller paths or shell text.
type ProfileSpec struct {
	Generation uint64 `json:"generation"`
	PolicyID   string `json:"policy_id"`
}

type ProfileProvider interface {
	Ensure(context.Context, Identity, ProfileSpec) error
	Remove(context.Context, Identity) error
}

type Desktop struct {
	SessionID  uint32    `json:"session_id"`
	Generation uint64    `json:"generation"`
	ReadyAt    time.Time `json:"ready_at"`
}

type DesktopProvider interface {
	Ensure(context.Context, Identity, Session) (Desktop, error)
	Stop(context.Context, Identity, Desktop) error
}

type AgentHealth struct {
	Ready      bool      `json:"ready"`
	Generation uint64    `json:"generation"`
	ObservedAt time.Time `json:"observed_at"`
}

type AgentProvider interface {
	Start(context.Context, Identity, Session, Desktop) error
	Stop(context.Context, Identity) error
	Health(context.Context, Identity) (AgentHealth, error)
}

type WorkerHealth struct {
	Ready      bool      `json:"ready"`
	Capability string    `json:"capability,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
}

type WorkerProvider interface {
	Start(context.Context, Identity) error
	Stop(context.Context, Identity) error
	Health(context.Context, Identity) (WorkerHealth, error)
}

// AccountProvider owns creation and deletion of the disposable local account.
// Recover rotates its local password; it never reads one from ordinary state.
type AccountProvider interface {
	Create(context.Context, Identity) (SecretLease, error)
	Recover(context.Context, Identity) (SecretLease, error)
	Delete(context.Context, Identity) error
}

// LoginAdapter is a restricted consumer of a SecretLease. Implementations are
// deployment-owned and must keep the secret in-process or an OS-authenticated
// secret channel; this interface is never JSON encoded.
type LoginAdapter interface {
	Login(context.Context, Identity, SecretLease) (Session, error)
	Logout(context.Context, Identity, Session) error
}

// SecretLease has no exported fields and no JSON representation of its value.
// The caller must Wipe it after Login/Recover use; WithPassword supplies a
// temporary copy and clears that copy before returning.
type SecretLease struct {
	username string
	password []uint16
}

func NewSecretLease(username string, password []uint16) (SecretLease, error) {
	if strings.TrimSpace(username) == "" || len(password) == 0 {
		return SecretLease{}, ErrSecretInvalid
	}
	copyPassword := append([]uint16(nil), password...)
	return SecretLease{username: username, password: copyPassword}, nil
}

func (s SecretLease) Username() string { return s.username }

func (s SecretLease) WithPassword(fn func([]uint16) error) error {
	if fn == nil || len(s.password) == 0 {
		return ErrSecretInvalid
	}
	copyPassword := append([]uint16(nil), s.password...)
	defer clear(copyPassword)
	return fn(copyPassword)
}

func (s *SecretLease) Wipe() {
	if s == nil {
		return
	}
	clear(s.password)
	s.password = nil
	s.username = ""
}

func (s SecretLease) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }
func (s SecretLease) String() string               { return "secret-lease" }

type JournalPhase string

const (
	JournalIntent     JournalPhase = "intent"
	JournalCommitted  JournalPhase = "committed"
	JournalCheckpoint JournalPhase = "checkpoint"
	JournalStopped    JournalPhase = "stopped"
	JournalUnknown    JournalPhase = "unknown"
)

// OwnershipRecord is the durable OS-side fence. SID is retained for service
// recovery but never returned by Core/UI. Broker/helper processes never write it.
type OwnershipRecord struct {
	Version    int          `json:"version"`
	PoolID     string       `json:"pool_id"`
	SlotID     string       `json:"slot_id"`
	Ordinal    int          `json:"ordinal"`
	SID        string       `json:"sid"`
	Generation uint64       `json:"generation"`
	SessionID  uint32       `json:"session_id,omitempty"`
	Owner      string       `json:"owner"`
	Phase      JournalPhase `json:"phase"`
	Revision   uint64       `json:"revision"`
	UpdatedAt  time.Time    `json:"updated_at"`
}

func (r OwnershipRecord) Validate() error {
	if r.Version != OwnershipJournalVersion || !validID(r.PoolID) || !validID(r.SlotID) || r.Ordinal < 1 || r.Ordinal > 256 || !validSID(r.SID) || r.Generation == 0 || !validID(r.Owner) || r.Revision == 0 || r.UpdatedAt.IsZero() {
		return ErrInvalidArgument
	}
	switch r.Phase {
	case JournalIntent, JournalCommitted, JournalCheckpoint, JournalStopped, JournalUnknown:
		return nil
	default:
		return ErrInvalidArgument
	}
}

type OwnershipJournal interface {
	Begin(context.Context, OwnershipRecord) error
	Commit(context.Context, OwnershipRecord) error
	Checkpoint(context.Context, OwnershipRecord) error
	Load(context.Context, string) (OwnershipRecord, error)
	MarkStopped(context.Context, OwnershipRecord) error
}

// HostOwner enforces one installed service owner for the fixed broker pipe and
// one bbolt writer. A second installation must fail closed until explicit
// upgrade/migration releases the record.
type HostOwnerRecord struct {
	Version      int       `json:"version"`
	OwnerID      string    `json:"owner_id"`
	ServiceSID   string    `json:"service_sid"`
	BrokerSID    string    `json:"broker_sid"`
	DatabaseHash string    `json:"database_hash"`
	PipeName     string    `json:"pipe_name"`
	LeaseID      string    `json:"lease_id"`
	ExpiresAt    time.Time `json:"expires_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (r HostOwnerRecord) Validate() error {
	if r.Version != OwnershipJournalVersion || !validID(r.OwnerID) || !validSID(r.ServiceSID) || !validSID(r.BrokerSID) || r.ServiceSID == r.BrokerSID || r.DatabaseHash == "" || r.PipeName != BrokerPipeName || !validID(r.LeaseID) || r.ExpiresAt.IsZero() || r.UpdatedAt.IsZero() {
		return ErrInvalidArgument
	}
	return nil
}

type HostOwner interface {
	Acquire(context.Context, HostOwnerRecord) error
	Renew(context.Context, string, time.Time) error
	Release(context.Context, string) error
	Inspect(context.Context) (HostOwnerRecord, error)
}

type CapabilityKind string

const (
	CapabilityLoginProof CapabilityKind = "login_proof"
	CapabilityWorkspace  CapabilityKind = "workspace"
)

type CapabilityRequest struct {
	Kind           CapabilityKind `json:"kind"`
	Actor          string         `json:"actor"`
	SlotID         string         `json:"slot_id"`
	Generation     uint64         `json:"generation"`
	Revision       uint64         `json:"revision"`
	IdempotencyKey string         `json:"idempotency_key"`
	Deadline       time.Time      `json:"deadline,omitempty"`
}

type Capability struct {
	ID         string         `json:"id"`
	Kind       CapabilityKind `json:"kind"`
	Generation uint64         `json:"generation"`
	Revision   uint64         `json:"revision"`
	ExpiresAt  time.Time      `json:"expires_at"`
}

type Authorizer interface {
	Authorize(context.Context, CapabilityRequest) (Capability, error)
}

// DenyAuthorizer is the required default. It never claims a login proof or
// workspace capability is ready.
type DenyAuthorizer struct{}

func (DenyAuthorizer) Authorize(context.Context, CapabilityRequest) (Capability, error) {
	return Capability{}, ErrCapabilityDenied
}

type Checkpoint struct {
	Name       string    `json:"name"`
	State      string    `json:"state"`
	ObservedAt time.Time `json:"observed_at"`
	Detail     string    `json:"detail,omitempty"`
}

// ProbeReport is the only result a login probe may emit. Details are
// classifications/hashes; raw SID, username, Profile path, password, and
// native error text are intentionally absent.
type ProbeReport struct {
	Schema         string       `json:"schema"`
	RunID          string       `json:"run_id"`
	LoginMethod    string       `json:"login_method"`
	PrivilegeClass string       `json:"privilege_class"`
	SIDHash        string       `json:"sid_hash,omitempty"`
	SessionID      uint32       `json:"session_id,omitempty"`
	OldSessionGone bool         `json:"old_session_gone"`
	ShellReady     bool         `json:"shell_ready"`
	AgentReady     bool         `json:"agent_ready"`
	WorkerReady    bool         `json:"worker_ready"`
	Checkpoints    []Checkpoint `json:"checkpoints"`
	NativePending  bool         `json:"native_pending"`
}

func (r ProbeReport) Validate() error {
	if r.Schema == "" || r.RunID == "" || r.LoginMethod == "" || r.PrivilegeClass == "" || len(r.Checkpoints) == 0 {
		return ErrInvalidArgument
	}
	return nil
}

var (
	idPattern  = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9._:-]{0,159}$")
	sidPattern = regexp.MustCompile("^S-1-[0-9]+(?:-[0-9]+)+$")
)

func validID(value string) bool  { return idPattern.MatchString(value) }
func validSID(value string) bool { return sidPattern.MatchString(value) }

// ValidateWire rejects accidental secret fields when a trusted adapter tests
// a JSON envelope before sending it over the fixed broker pipe.
func ValidateWire(data []byte) error {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return ErrInvalidArgument
	}
	if value == nil {
		return ErrInvalidArgument
	}
	var visit func(any) bool
	visit = func(current any) bool {
		switch item := current.(type) {
		case map[string]any:
			for key, child := range item {
				lower := strings.ToLower(key)
				if strings.Contains(lower, "password") || strings.Contains(lower, "secret") || strings.Contains(lower, "credential") || strings.Contains(lower, "endpoint") || strings.Contains(lower, "profile") {
					return false
				}
				if !visit(child) {
					return false
				}
			}
		case []any:
			for _, child := range item {
				if !visit(child) {
					return false
				}
			}
		}
		return true
	}
	if object, ok := value.(map[string]any); !ok || !visit(object) {
		return ErrInvalidArgument
	}
	return nil
}

func (r OwnershipRecord) String() string {
	return fmt.Sprintf("ownership{slot=%s,generation=%d,phase=%s}", r.SlotID, r.Generation, r.Phase)
}
