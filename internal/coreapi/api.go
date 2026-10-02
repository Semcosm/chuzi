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
	Severity string `json:"severity"`
	Category string `json:"category"`
	Summary  string `json:"summary"`
}

type DiagnosticStatus struct {
	ID        string    `json:"id"`
	State     string    `json:"state"`
	Attempts  int       `json:"attempts"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// DiagnosticsAPI is optional so older in-process embedders remain compatible.
type DiagnosticsAPI interface {
	SubmitDiagnosticReport(context.Context, DiagnosticReport) (DiagnosticStatus, error)
}

// JobPoolStatus is a redaction-safe execution capacity projection. It never
// contains Windows usernames, SIDs, Profile paths, endpoints, or commands.
type JobPoolStatus struct {
	PoolID             string `json:"pool_id"`
	EnvironmentID      string `json:"environment_id"`
	EnvironmentVersion string `json:"environment_version"`
	Desired            int    `json:"desired"`
	Ready              int    `json:"ready"`
	Leased             int    `json:"leased"`
	Quarantined        int    `json:"quarantined"`
	Draining           int    `json:"draining"`
	Provisioning       int    `json:"provisioning"`
	Retiring           int    `json:"retiring"`
	Unprovisioned      int    `json:"unprovisioned"`
	EffectiveCapacity  int    `json:"effective_capacity"`
}

type JobPoolStatusAPI interface {
	GetJobPoolStatus(context.Context, string) (JobPoolStatus, error)
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
