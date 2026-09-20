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
