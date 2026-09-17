// Package coretransport exposes the versioned local transport for Core API
// clients. The wire format is deliberately independent from the Go DTO
// implementation and carries only redaction-safe projections.
package coretransport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Semcosm/chuzi/internal/coreapi"
)

const (
	ProtocolVersion = coreapi.APIVersion
	DefaultMaxFrame = 1 << 20

	methodHello             = "hello"
	methodCancel            = "cancel"
	methodSubmitRequest     = "submit_request"
	methodGetRequest        = "get_request"
	methodGetAccount        = "get_account"
	methodCancelRequest     = "cancel_request"
	methodGetResult         = "get_result"
	methodListEvents        = "list_events"
	methodListNotifications = "list_notifications"
)

const (
	MethodHello             = methodHello
	MethodCancel            = methodCancel
	MethodSubmitRequest     = methodSubmitRequest
	MethodGetRequest        = methodGetRequest
	MethodGetAccount        = methodGetAccount
	MethodCancelRequest     = methodCancelRequest
	MethodGetResult         = methodGetResult
	MethodListEvents        = methodListEvents
	MethodListNotifications = methodListNotifications
)

var (
	ErrInvalidTransport   = errors.New("core transport: invalid request")
	ErrUnsupportedVersion = errors.New("core transport: unsupported protocol version")
	ErrFrameTooLarge      = errors.New("core transport: frame too large")
	ErrEndpointBusy       = errors.New("core transport: endpoint already in use")
	ErrNotReady           = errors.New("core transport: handshake required")
	ErrClientClosed       = errors.New("core transport: client closed")
)

// Envelope is the JSONL request/response envelope. Requests use method and
// params; responses use type=result/error and one of result/error.
type Envelope struct {
	Protocol string          `json:"protocol"`
	ID       string          `json:"id"`
	Method   string          `json:"method,omitempty"`
	Params   json.RawMessage `json:"params,omitempty"`
	Type     string          `json:"type,omitempty"`
	Result   json.RawMessage `json:"result,omitempty"`
	Error    *ErrorPayload   `json:"error,omitempty"`
}

type ErrorPayload struct {
	Code    coreapi.Code `json:"code"`
	Message string       `json:"message"`
}

type HelloParams struct {
	Version string `json:"version"`
}

type HelloResult struct {
	Version string   `json:"version"`
	Methods []string `json:"methods"`
}

type CancelParams struct {
	ID string `json:"id"`
}

type CancelResult struct {
	Cancelled bool `json:"cancelled"`
}

type SubmitResult struct {
	Request    coreapi.Request `json:"request"`
	Idempotent bool            `json:"idempotent"`
}

type EventsResult struct {
	Events []coreapi.Event `json:"events"`
}

type NotificationsResult struct {
	Notifications []coreapi.Notification `json:"notifications"`
}

func methodList() []string {
	return []string{methodHello, methodCancel, methodSubmitRequest, methodGetRequest, methodGetAccount,
		methodCancelRequest, methodGetResult, methodListEvents, methodListNotifications}
}

func marshalRequest(id, method string, params any) ([]byte, error) {
	if err := validateID(id); err != nil || !validMethod(method) {
		return nil, ErrInvalidTransport
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, ErrInvalidTransport
	}
	return json.Marshal(Envelope{Protocol: ProtocolVersion, ID: id, Method: method, Params: raw})
}

func decodeParams(raw json.RawMessage, dst any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return ErrInvalidTransport
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ErrInvalidTransport
	}
	return nil
}

func validateEnvelope(envelope Envelope) error {
	if envelope.Protocol == "" || len(envelope.Protocol) > 64 || strings.ContainsAny(envelope.Protocol, "\r\n \t") {
		return ErrInvalidTransport
	}
	if err := validateID(envelope.ID); err != nil {
		return err
	}
	if envelope.Method == "" || !validMethod(envelope.Method) {
		return ErrInvalidTransport
	}
	return nil
}

func validateID(id string) error {
	if id == "" || len(id) > 128 || strings.ContainsAny(id, "\r\n \t") {
		return ErrInvalidTransport
	}
	return nil
}

func validMethod(method string) bool {
	if method == "" || len(method) > 64 {
		return false
	}
	for _, r := range method {
		if (r < 'a' || r > 'z') && r != '_' {
			return false
		}
	}
	return true
}

func stableError(err error) *ErrorPayload {
	if err == nil {
		return nil
	}
	code := coreapi.CodeOf(err)
	if errors.Is(err, ErrUnsupportedVersion) {
		code = coreapi.CodeUnavailable
	} else if errors.Is(err, ErrNotReady) || errors.Is(err, ErrInvalidTransport) || errors.Is(err, ErrFrameTooLarge) {
		code = coreapi.CodeInvalidArgument
	}
	switch code {
	case coreapi.CodeInvalidArgument, coreapi.CodeNotFound, coreapi.CodeConflict,
		coreapi.CodeForbidden, coreapi.CodeUnavailable, coreapi.CodeCancelled,
		coreapi.CodeDeadline, coreapi.CodeInternal:
	default:
		code = coreapi.CodeInternal
	}
	message := "core operation failed"
	switch code {
	case coreapi.CodeInvalidArgument:
		message = "request is invalid"
	case coreapi.CodeNotFound:
		message = "resource was not found"
	case coreapi.CodeConflict:
		message = "request conflicts with current state"
	case coreapi.CodeForbidden:
		message = "operation is not allowed"
	case coreapi.CodeUnavailable:
		message = "core is temporarily unavailable"
	case coreapi.CodeCancelled:
		message = "operation was cancelled"
	case coreapi.CodeDeadline:
		message = "operation deadline exceeded"
	}
	return &ErrorPayload{Code: code, Message: message}
}

func errorFromPayload(payload *ErrorPayload) error {
	if payload == nil {
		return coreapi.NewError(coreapi.CodeInternal, "core operation failed")
	}
	// Reclassify at the client boundary as well. A newer server or malformed
	// peer must not make an unknown code part of the public v1 error contract.
	classified := stableError(coreapi.NewError(payload.Code, ""))
	return coreapi.NewError(classified.Code, classified.Message)
}

func response(id string, value any) Envelope {
	raw, _ := json.Marshal(value)
	return Envelope{Protocol: ProtocolVersion, ID: id, Type: "result", Result: raw}
}

func errorResponse(id string, err error) Envelope {
	return Envelope{Protocol: ProtocolVersion, ID: id, Type: "error", Error: stableError(err)}
}

func classifyContext(ctx context.Context) error {
	if ctx == nil {
		return coreapi.NewError(coreapi.CodeCancelled, "operation was cancelled")
	}
	switch ctx.Err() {
	case context.Canceled:
		return coreapi.NewError(coreapi.CodeCancelled, "operation was cancelled")
	case context.DeadlineExceeded:
		return coreapi.NewError(coreapi.CodeDeadline, "operation deadline exceeded")
	}
	return nil
}

func invalidMethodError(method string) error {
	return fmt.Errorf("%w: unknown method", ErrInvalidTransport)
}
