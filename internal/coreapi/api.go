// Package coreapi defines the transport-neutral public contract exposed by
// the CHUZI Core. It intentionally contains no persistence, browser, Matrix,
// credential, or UI types.
package coreapi

import (
	"context"
	"errors"
	"time"
)

const APIVersion = "chuzi.core/v1"

// Code is the stable, redacted error classification returned by Core APIs.
type Code string

const (
	CodeInvalidArgument Code = "invalid_argument"
	CodeNotFound        Code = "not_found"
	CodeConflict        Code = "conflict"
	CodeForbidden       Code = "forbidden"
	CodeUnavailable     Code = "unavailable"
	CodeCancelled       Code = "cancelled"
	CodeDeadline        Code = "deadline_exceeded"
	CodeRateLimited     Code = "rate_limited"
	CodeInternal        Code = "internal"
)

// Error never wraps the storage or adapter error. Callers can safely expose
// its code without leaking database paths, account data, or stack traces.
type Error struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return "chuzi core: " + string(e.Code)
}

func NewError(code Code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// CodeOf returns a stable classification. Unknown errors are deliberately
// treated as internal rather than exposing their text to API consumers.
func CodeOf(err error) Code {
	if err == nil {
		return ""
	}
	var classified *Error
	if errors.As(err, &classified) && classified != nil && classified.Code != "" {
		return classified.Code
	}
	return CodeInternal
}

// SubmitRequest is the caller-facing input for one durable request.
// NotificationRoomID is accepted only as an input and is never returned by
// the Core projections.
type SubmitRequest struct {
	RequestID          string    `json:"request_id"`
	AccountID          string    `json:"account_id"`
	IdempotencyKey     string    `json:"idempotency_key"`
	NotificationRoomID string    `json:"notification_room_id,omitempty"`
	Actor              string    `json:"actor,omitempty"`
	Deadline           time.Time `json:"deadline,omitempty"`
}

type CancelRequest struct {
	RequestID string `json:"request_id"`
	Actor     string `json:"actor,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// Request is a safe request projection. It omits idempotency keys, room IDs,
// raw actor data, and any browser or credential material.
type Request struct {
	RequestID   string    `json:"request_id"`
	Account     string    `json:"account"`
	State       string    `json:"state"`
	Attempt     int       `json:"attempt"`
	LastFailure string    `json:"last_failure,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	NotBefore   time.Time `json:"not_before,omitempty"`
	Deadline    time.Time `json:"deadline,omitempty"`
}

// RequestQuery bounds a read-only request list. State is an optional request
// business-state filter; offsets and limits are applied after filtering and
// before the redacted projections are returned. The current implementation
// scans the durable request set to preserve global creation-time ordering.
type RequestQuery struct {
	State  string `json:"state,omitempty"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// RequestListAPI is an additive capability. Keeping it optional preserves
// source compatibility for older in-process API implementations while the
// transport advertises the method for feature detection.
type RequestListAPI interface {
	ListRequests(context.Context, RequestQuery) ([]Request, error)
}

type Account struct {
	Account   string `json:"account"`
	State     string `json:"state"`
	RequestID string `json:"request_id,omitempty"`
	Revision  uint64 `json:"revision"`
}

// Result is the durable, redacted outcome projection. It represents the
// request state known to Core, not raw worker facts or page contents.
type Result struct {
	RequestID string    `json:"request_id"`
	State     string    `json:"state"`
	Outcome   string    `json:"outcome"`
	Failure   string    `json:"failure,omitempty"`
	Attempt   int       `json:"attempt"`
	UpdatedAt time.Time `json:"updated_at"`
}

type EventQuery struct {
	AccountID string    `json:"account_id,omitempty"`
	RequestID string    `json:"request_id,omitempty"`
	Since     time.Time `json:"since,omitempty"`
	Until     time.Time `json:"until,omitempty"`
	Limit     int       `json:"limit,omitempty"`
}

type Event struct {
	Kind      string    `json:"kind"`
	EventID   string    `json:"event_id"`
	At        time.Time `json:"at"`
	Account   string    `json:"account"`
	RequestID string    `json:"request_id,omitempty"`
	Operation string    `json:"operation"`
	From      string    `json:"from,omitempty"`
	To        string    `json:"to,omitempty"`
	Actor     string    `json:"actor,omitempty"`
	Version   uint64    `json:"version,omitempty"`
	Resource  string    `json:"resource,omitempty"`
}

type NotificationQuery struct {
	AccountID string    `json:"account_id,omitempty"`
	RequestID string    `json:"request_id,omitempty"`
	Since     time.Time `json:"since,omitempty"`
	Until     time.Time `json:"until,omitempty"`
	Offset    int       `json:"offset,omitempty"`
	Limit     int       `json:"limit,omitempty"`
}

type Notification struct {
	EventID     string    `json:"event_id"`
	Account     string    `json:"account"`
	RequestID   string    `json:"request_id"`
	State       string    `json:"state"`
	Failure     string    `json:"failure,omitempty"`
	Status      string    `json:"status"`
	Attempt     int       `json:"attempt"`
	OccurredAt  time.Time `json:"occurred_at"`
	DeliveredAt time.Time `json:"delivered_at,omitempty"`
}

// BrowserViewRequest requests one bounded, read-only image from an active
// browser session. It never accepts a URL, Profile path, or CDP endpoint.
type BrowserViewRequest struct {
	RequestID string `json:"request_id"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
}

// BrowserView is an ephemeral page projection. Data is base64-encoded by the
// JSON representation and is never persisted or included in audit events.
type BrowserView struct {
	RequestID   string    `json:"request_id"`
	ContentType string    `json:"content_type"`
	Width       int       `json:"width"`
	Height      int       `json:"height"`
	Data        string    `json:"data"`
	CapturedAt  time.Time `json:"captured_at"`
}

// BrowserViewAPI is optional so existing in-process test doubles and older
// embedders remain source-compatible while the transport advertises the
// additive method when the implementation supports it.
type BrowserViewAPI interface {
	GetBrowserView(context.Context, BrowserViewRequest) (BrowserView, error)
}

type RDPCapabilityRequest struct {
	RequestID string `json:"request_id"`
	Actor     string `json:"actor,omitempty"`
}

// RDPCapability is an opaque, short-lived bearer capability. It contains no
// endpoint, credentials, Profile path, or certificate information.
type RDPCapability struct {
	ID        string    `json:"id"`
	Token     string    `json:"token"`
	RequestID string    `json:"request_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

type RDPCapabilityAPI interface {
	IssueRDPCapability(context.Context, RDPCapabilityRequest) (RDPCapability, error)
}

// DiagnosticReport is a user-consented, redaction-safe support submission.
// The Core implementation collects only allow-listed local diagnostics.
type DiagnosticReport struct {
	Severity   string `json:"severity"`
	Category   string `json:"category"`
	Summary    string `json:"summary"`
	ErrorClass string `json:"error_class,omitempty"`
	Operation  string `json:"operation,omitempty"`
}

type DiagnosticStatus struct {
	ID        string    `json:"id"`
	State     string    `json:"state"`
	Attempts  int       `json:"attempts"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// DiagnosticSnapshotRequest asks Core for a fresh local support snapshot. It
// is read-only and never queues or uploads a report.
type DiagnosticSnapshotRequest struct {
	Severity   string `json:"severity"`
	Category   string `json:"category"`
	Summary    string `json:"summary"`
	ErrorClass string `json:"error_class,omitempty"`
	Operation  string `json:"operation,omitempty"`
}

type DiagnosticEvent struct {
	At         time.Time `json:"at,omitempty"`
	Component  string    `json:"component,omitempty"`
	Operation  string    `json:"operation,omitempty"`
	Outcome    string    `json:"outcome,omitempty"`
	RequestID  string    `json:"request_id,omitempty"`
	Resource   string    `json:"resource,omitempty"`
	ErrorClass string    `json:"error_class,omitempty"`
	DurationMS int64     `json:"duration_ms,omitempty"`
}

// DiagnosticCoreStatus records the launcher-visible lifecycle state without
// exposing a PID, endpoint, path, or process error text.
type DiagnosticCoreStatus struct {
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	Ready     bool   `json:"ready"`
	Status    string `json:"status"`
}

// DiagnosticSnapshot is the bounded, redaction-safe local artifact that a
// client may save for support. It contains no paths, credentials, or raw log
// text, and is intentionally separate from remote report submission.
type DiagnosticSnapshot struct {
	Schema            string               `json:"schema"`
	ID                string               `json:"id"`
	CreatedAt         time.Time            `json:"created_at"`
	Source            string               `json:"source"`
	Version           string               `json:"version"`
	Platform          string               `json:"platform"`
	Arch              string               `json:"arch"`
	Severity          string               `json:"severity"`
	Category          string               `json:"category"`
	Summary           string               `json:"summary"`
	ErrorClass        string               `json:"error_class,omitempty"`
	Operation         string               `json:"operation,omitempty"`
	EventCount        int                  `json:"event_count"`
	EventsTruncated   bool                 `json:"events_truncated,omitempty"`
	CaptureErrorClass string               `json:"capture_error_class,omitempty"`
	CoreStatus        DiagnosticCoreStatus `json:"core_status"`
	Events            []DiagnosticEvent    `json:"events,omitempty"`
}

// DiagnosticsAPI is optional so older in-process embedders remain compatible.
type DiagnosticsAPI interface {
	SubmitDiagnosticReport(context.Context, DiagnosticReport) (DiagnosticStatus, error)
}

// DiagnosticSnapshotAPI is additive so older in-process embedders can keep
// serving the existing Core contract while clients feature-detect local
// export support.
type DiagnosticSnapshotAPI interface {
	GetDiagnosticSnapshot(context.Context, DiagnosticSnapshotRequest) (DiagnosticSnapshot, error)
}

// JobPoolStatus is a redaction-safe execution capacity projection. It never
// contains Windows usernames, SIDs, Profile paths, endpoints, or commands.
type JobPoolStatus struct {
	ExecutionMode             string    `json:"execution_mode"`
	PoolID                    string    `json:"pool_id"`
	EnvironmentID             string    `json:"environment_id"`
	EnvironmentVersion        string    `json:"environment_version"`
	Desired                   int       `json:"desired"`
	Ready                     int       `json:"ready"`
	Leased                    int       `json:"leased"`
	Quarantined               int       `json:"quarantined"`
	Draining                  int       `json:"draining"`
	Provisioning              int       `json:"provisioning"`
	Retiring                  int       `json:"retiring"`
	Unprovisioned             int       `json:"unprovisioned"`
	EffectiveCapacity         int       `json:"effective_capacity"`
	MaxConcurrency            int       `json:"max_concurrency,omitempty"`
	DesiredState              string    `json:"desired_state"`
	Enabled                   bool      `json:"enabled"`
	EnvironmentReady          bool      `json:"environment_ready"`
	EnvironmentReadiness      string    `json:"environment_readiness"`
	ReconcileState            string    `json:"reconcile_state,omitempty"`
	OperationID               string    `json:"operation_id,omitempty"`
	LastFailureCode           string    `json:"last_failure_code,omitempty"`
	LastSuccessfulReconcileAt time.Time `json:"last_successful_reconcile_time,omitempty"`
	ConfigRevision            uint64    `json:"config_revision"`
}

type JobPoolStatusAPI interface {
	GetJobPoolStatus(context.Context, string) (JobPoolStatus, error)
}

type JobPoolConfig struct {
	PoolID             string    `json:"pool_id"`
	DesiredSlots       int       `json:"desired_slots"`
	MaxConcurrency     int       `json:"max_concurrency,omitempty"`
	EnvironmentID      string    `json:"environment_id"`
	EnvironmentVersion string    `json:"environment_version"`
	ManifestDigest     string    `json:"manifest_digest"`
	Signer             string    `json:"signer"`
	Capabilities       []string  `json:"capabilities,omitempty"`
	RequireTrusted     bool      `json:"require_trusted"`
	DesiredState       string    `json:"desired_state"`
	Enabled            bool      `json:"enabled"`
	ConfigRevision     uint64    `json:"config_revision"`
	UpdatedAt          time.Time `json:"updated_at"`
	UpdatedBy          string    `json:"updated_by,omitempty"`
}

type JobPool struct {
	Config JobPoolConfig `json:"config"`
	Status JobPoolStatus `json:"status"`
}

type JobPoolApplyRequest struct {
	Config           JobPoolConfig `json:"config"`
	ExpectedRevision uint64        `json:"expected_revision"`
	IdempotencyKey   string        `json:"idempotency_key"`
	Actor            string        `json:"actor"`
	RequestedAt      time.Time     `json:"requested_at,omitempty"`
}

type JobPoolScaleRequest struct {
	PoolID           string    `json:"pool_id"`
	DesiredSlots     int       `json:"desired_slots"`
	ExpectedRevision uint64    `json:"expected_revision"`
	IdempotencyKey   string    `json:"idempotency_key"`
	Actor            string    `json:"actor"`
	RequestedAt      time.Time `json:"requested_at,omitempty"`
}

type JobPoolActionRequest struct {
	PoolID           string    `json:"pool_id"`
	ExpectedRevision uint64    `json:"expected_revision"`
	IdempotencyKey   string    `json:"idempotency_key"`
	Actor            string    `json:"actor"`
	RequestedAt      time.Time `json:"requested_at,omitempty"`
}

// JobPoolDeleteRequest requests a safe, asynchronous pool removal. Core first
// drains the pool and only removes its durable configuration after every slot
// has been retired.
type JobPoolDeleteRequest struct {
	PoolID           string    `json:"pool_id"`
	ExpectedRevision uint64    `json:"expected_revision"`
	IdempotencyKey   string    `json:"idempotency_key"`
	Actor            string    `json:"actor"`
	RequestedAt      time.Time `json:"requested_at,omitempty"`
}

type JobPoolOperation struct {
	OperationID           string    `json:"operation_id"`
	PoolID                string    `json:"pool_id"`
	Operation             string    `json:"operation"`
	State                 string    `json:"state"`
	Actor                 string    `json:"actor,omitempty"`
	ConfigRevision        uint64    `json:"config_revision"`
	RequestedAt           time.Time `json:"requested_at"`
	UpdatedAt             time.Time `json:"updated_at"`
	CompletedAt           time.Time `json:"completed_at,omitempty"`
	Result                string    `json:"result,omitempty"`
	FailureCode           string    `json:"failure_code,omitempty"`
	EnvironmentGeneration uint64    `json:"environment_generation,omitempty"`
	LastSuccessfulAt      time.Time `json:"last_successful_at,omitempty"`
	Idempotent            bool      `json:"idempotent,omitempty"`
}

type JobPoolAPI interface {
	ListJobPools(context.Context) ([]JobPool, error)
	GetJobPool(context.Context, string) (JobPool, error)
	ApplyJobPool(context.Context, JobPoolApplyRequest) (JobPoolOperation, error)
	ScaleJobPool(context.Context, JobPoolScaleRequest) (JobPoolOperation, error)
	DrainJobPool(context.Context, JobPoolActionRequest) (JobPoolOperation, error)
	ResumeJobPool(context.Context, JobPoolActionRequest) (JobPoolOperation, error)
	DeleteJobPool(context.Context, JobPoolDeleteRequest) (JobPoolOperation, error)
	GetJobPoolOperation(context.Context, string) (JobPoolOperation, error)
}

// StartSlotSessionRequest asks the service to establish the session for one
// existing logical slot. Exactly one of PoolID and SlotID is accepted.
type StartSlotSessionRequest struct {
	PoolID           string `json:"pool_id,omitempty"`
	SlotID           string `json:"slot_id,omitempty"`
	Actor            string `json:"actor"`
	IdempotencyKey   string `json:"idempotency_key"`
	ExpectedRevision uint64 `json:"expected_revision,omitempty"`
}

type SlotSessionStatus struct {
	ExecutionMode         string `json:"execution_mode"`
	PoolID                string `json:"pool_id"`
	SlotID                string `json:"slot_id"`
	Ordinal               int    `json:"ordinal"`
	Status                string `json:"status"`
	EnvironmentGeneration uint64 `json:"environment_generation"`
	SessionState          string `json:"session_state"`
	AgentReady            bool   `json:"agent_ready"`
}

type SlotSessionOperation struct {
	OperationID           string            `json:"operation_id"`
	PoolID                string            `json:"pool_id"`
	SlotID                string            `json:"slot_id"`
	Ordinal               int               `json:"ordinal"`
	State                 string            `json:"state"`
	Actor                 string            `json:"actor,omitempty"`
	RequestedAt           time.Time         `json:"requested_at"`
	UpdatedAt             time.Time         `json:"updated_at"`
	CompletedAt           time.Time         `json:"completed_at,omitempty"`
	FailureCode           string            `json:"failure_code,omitempty"`
	EnvironmentGeneration uint64            `json:"environment_generation"`
	Status                SlotSessionStatus `json:"status"`
	Idempotent            bool              `json:"idempotent,omitempty"`
}

type SlotSessionAPI interface {
	StartSlotSession(context.Context, StartSlotSessionRequest) (SlotSessionOperation, error)
	GetSlotSessionOperation(context.Context, string) (SlotSessionOperation, error)
}

type Environment struct {
	EnvironmentID  string    `json:"environment_id"`
	Version        string    `json:"version"`
	Capabilities   []string  `json:"capabilities,omitempty"`
	ManifestDigest string    `json:"manifest_digest"`
	Signer         string    `json:"signer"`
	Installed      bool      `json:"installed"`
	Verified       bool      `json:"verified"`
	Trusted        bool      `json:"trusted"`
	Enabled        bool      `json:"enabled"`
	Healthy        bool      `json:"healthy"`
	Ready          bool      `json:"ready"`
	Generation     uint64    `json:"generation"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type EnvironmentOperationRequest struct {
	EnvironmentID    string    `json:"environment_id"`
	Version          string    `json:"version"`
	Operation        string    `json:"operation"`
	PackageRef       string    `json:"package_ref,omitempty"`
	ExpectedRevision uint64    `json:"expected_revision,omitempty"`
	IdempotencyKey   string    `json:"idempotency_key"`
	Actor            string    `json:"actor"`
	RequestedAt      time.Time `json:"requested_at,omitempty"`
}

type EnvironmentOperation struct {
	OperationID           string    `json:"operation_id"`
	EnvironmentID         string    `json:"environment_id"`
	Version               string    `json:"version"`
	Operation             string    `json:"operation"`
	State                 string    `json:"state"`
	FailureCode           string    `json:"failure_code,omitempty"`
	EnvironmentGeneration uint64    `json:"environment_generation,omitempty"`
	RequestedAt           time.Time `json:"requested_at"`
	UpdatedAt             time.Time `json:"updated_at"`
	Idempotent            bool      `json:"idempotent,omitempty"`
}

type EnvironmentAPI interface {
	ListEnvironments(context.Context) ([]Environment, error)
	EnvironmentOperation(context.Context, EnvironmentOperationRequest) (EnvironmentOperation, error)
	GetEnvironmentOperation(context.Context, string) (EnvironmentOperation, error)
}

// API is the stable Core contract. Implementations may use any local IPC or
// in-process transport as long as these DTOs and error codes remain stable.
type API interface {
	SubmitRequest(context.Context, SubmitRequest) (Request, bool, error)
	GetRequest(context.Context, string) (Request, error)
	GetAccount(context.Context, string) (Account, error)
	CancelRequest(context.Context, CancelRequest) (Request, error)
	GetResult(context.Context, string) (Result, error)
	ListEvents(context.Context, EventQuery) ([]Event, error)
	ListNotifications(context.Context, NotificationQuery) ([]Notification, error)
}
