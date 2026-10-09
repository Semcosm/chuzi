package slotwindows

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"
)

// SessionBrokerPipeName is a build-time constant. There is deliberately no
// option, request field, or environment override for the broker endpoint.
const SessionBrokerPipeName = `\\.\pipe\chuzi-session-bootstrap-v1`

const SessionBrokerProtocolVersion = 1

type SessionBrokerOperation string

const (
	SessionBrokerStart SessionBrokerOperation = "start"
	SessionBrokerStop  SessionBrokerOperation = "stop"
)

type SessionBrokerCode string

const (
	SessionBrokerOK                 SessionBrokerCode = "ok"
	SessionBrokerInvalidVersion     SessionBrokerCode = "invalid_version"
	SessionBrokerInvalidRequest     SessionBrokerCode = "invalid_request"
	SessionBrokerUnknownOperation   SessionBrokerCode = "unknown_operation"
	SessionBrokerUnauthorized       SessionBrokerCode = "unauthorized"
	SessionBrokerOwnershipMismatch  SessionBrokerCode = "ownership_mismatch"
	SessionBrokerOwnershipUnknown   SessionBrokerCode = "ownership_unknown"
	SessionBrokerStaleGeneration    SessionBrokerCode = "stale_generation"
	SessionBrokerAlreadyStarted     SessionBrokerCode = "already_started"
	SessionBrokerSessionUnavailable SessionBrokerCode = "session_unavailable"
	SessionBrokerSessionChanged     SessionBrokerCode = "session_changed"
	SessionBrokerStopTimeout        SessionBrokerCode = "stop_timeout"
)

var (
	ErrSessionBrokerInvalidVersion   = errors.New("slotwindows: session broker invalid version")
	ErrSessionBrokerInvalidRequest   = errors.New("slotwindows: session broker invalid request")
	ErrSessionBrokerUnknownOperation = errors.New("slotwindows: session broker unknown operation")
	ErrSessionBrokerOwnership        = errors.New("slotwindows: session broker ownership mismatch")
	ErrSessionBrokerUnknownOwnership = errors.New("slotwindows: session broker ownership unknown")
	ErrSessionBrokerStaleGeneration  = errors.New("slotwindows: session broker stale generation")
	ErrSessionBrokerAlreadyStarted   = errors.New("slotwindows: session broker already started")
	ErrSessionBrokerStopTimeout      = errors.New("slotwindows: session broker stop timeout")
)

var (
	managedSIDPattern = regexp.MustCompile(`^S-1-[0-9]+(?:-[0-9]+)+$`)
	ownerPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,159}$`)
)

// SessionBrokerRequest is the complete wire request. Credentials are absent
// by design: a deployment-owned SessionLoginAdapter receives a short-lived
// in-memory buffer in-process, while the fixed broker protocol carries only
// ownership metadata.
type SessionBrokerRequest struct {
	Version    int                    `json:"version"`
	Operation  SessionBrokerOperation `json:"operation"`
	SlotID     string                 `json:"slot_id"`
	Ordinal    int                    `json:"ordinal"`
	Generation uint64                 `json:"generation"`
	SID        string                 `json:"sid"`
	Owner      string                 `json:"owner"`
	SessionID  uint32                 `json:"session_id,omitempty"`
}

type SessionBrokerResponse struct {
	Version   int                    `json:"version"`
	Operation SessionBrokerOperation `json:"operation"`
	Code      SessionBrokerCode      `json:"code"`
	SessionID uint32                 `json:"session_id,omitempty"`
	State     string                 `json:"state,omitempty"`
}

func (r SessionBrokerRequest) validate() error {
	switch {
	case r.Version != SessionBrokerProtocolVersion:
		return ErrSessionBrokerInvalidVersion
	case r.Operation != SessionBrokerStart && r.Operation != SessionBrokerStop:
		return ErrSessionBrokerUnknownOperation
	case !slotIDPattern.MatchString(r.SlotID) || r.Ordinal < 1 || r.Ordinal > 256 || r.Generation == 0 || !managedSIDPattern.MatchString(r.SID) || !ownerPattern.MatchString(r.Owner):
		return ErrSessionBrokerInvalidRequest
	case r.Operation == SessionBrokerStart && r.SessionID != 0:
		return ErrSessionBrokerInvalidRequest
	case r.Operation == SessionBrokerStop && r.SessionID == 0:
		return ErrSessionBrokerInvalidRequest
	}
	return nil
}

func decodeJSONLine[T any](data []byte, value *T) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return ErrSessionBrokerInvalidRequest
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ErrSessionBrokerInvalidRequest
	}
	return nil
}

func DecodeSessionBrokerRequest(data []byte) (SessionBrokerRequest, error) {
	var request SessionBrokerRequest
	if err := decodeJSONLine(data, &request); err != nil {
		return SessionBrokerRequest{}, err
	}
	if err := request.validate(); err != nil {
		return SessionBrokerRequest{}, err
	}
	return request, nil
}

func EncodeSessionBrokerRequest(request SessionBrokerRequest) ([]byte, error) {
	if err := request.validate(); err != nil {
		return nil, err
	}
	return json.Marshal(request)
}

func (r SessionBrokerResponse) validate() error {
	if r.Version != SessionBrokerProtocolVersion || (r.Operation != SessionBrokerStart && r.Operation != SessionBrokerStop) || r.Code == "" {
		return ErrSessionBrokerInvalidRequest
	}
	switch r.Code {
	case SessionBrokerOK, SessionBrokerInvalidVersion, SessionBrokerInvalidRequest, SessionBrokerUnknownOperation, SessionBrokerUnauthorized, SessionBrokerOwnershipMismatch, SessionBrokerOwnershipUnknown, SessionBrokerStaleGeneration, SessionBrokerAlreadyStarted, SessionBrokerSessionUnavailable, SessionBrokerSessionChanged, SessionBrokerStopTimeout:
	default:
		return ErrSessionBrokerInvalidRequest
	}
	if r.Code == SessionBrokerOK {
		if r.Operation == SessionBrokerStart && (r.SessionID == 0 || r.State != "active") {
			return ErrSessionBrokerInvalidRequest
		}
		if r.Operation == SessionBrokerStop && r.SessionID != 0 && r.State != "stopped" {
			return ErrSessionBrokerInvalidRequest
		}
	}
	return nil
}

func DecodeSessionBrokerResponse(data []byte) (SessionBrokerResponse, error) {
	var response SessionBrokerResponse
	if err := decodeJSONLine(data, &response); err != nil {
		return SessionBrokerResponse{}, err
	}
	if err := response.validate(); err != nil {
		return SessionBrokerResponse{}, err
	}
	return response, nil
}

func EncodeSessionBrokerResponse(response SessionBrokerResponse) ([]byte, error) {
	if err := response.validate(); err != nil {
		return nil, err
	}
	return json.Marshal(response)
}

// SessionBrokerACL describes the two identities a deployment-owned listener
// must allow. A listener must reject every other client before JSON parsing.
type SessionBrokerACL struct {
	ServiceSID string
	BrokerSID  string
}

func (a SessionBrokerACL) Validate() error {
	if !isPrincipalSID(a.ServiceSID) || !managedSIDPattern.MatchString(a.BrokerSID) || a.ServiceSID == a.BrokerSID || isBroadSID(a.ServiceSID) || isBroadSID(a.BrokerSID) {
		return ErrSessionBrokerInvalidRequest
	}
	return nil
}

func (a SessionBrokerACL) Allows(sid string) bool {
	return a.Validate() == nil && (sid == a.ServiceSID || sid == a.BrokerSID)
}

func isBroadSID(sid string) bool {
	switch sid {
	case "S-1-1-0", "S-1-5-7", "S-1-5-11", "S-1-5-32-545":
		return true
	default:
		return false
	}
}

func isPrincipalSID(sid string) bool {
	return managedSIDPattern.MatchString(sid) || sid == "S-1-5-18" || sid == "S-1-5-19" || sid == "S-1-5-20"
}

type SessionBrokerOwnership struct {
	SlotID     string
	Ordinal    int
	Generation uint64
	SID        string
	SessionID  uint32
	Owner      string
}

func (o SessionBrokerOwnership) sameRequest(r SessionBrokerRequest) bool {
	return o.SlotID == r.SlotID && o.Ordinal == r.Ordinal && o.Generation == r.Generation && o.SID == r.SID && o.Owner == r.Owner
}

// SessionBrokerAdapter is the deployment side of the protocol. Start and
// Stop must use a real RDP/Winlogon or equivalent controlled login service; a
// process token is not a WTS session. Find is the authoritative session check.
type SessionBrokerAdapter interface {
	Start(context.Context, SessionBrokerOwnership) (BootstrapSession, error)
	Stop(context.Context, SessionBrokerOwnership) error
	Find(context.Context, string) (BootstrapSession, error)
}

// SessionBroker holds only ownership it created during this process lifetime.
// It intentionally cannot adopt an active session after restart.
type SessionBroker struct {
	mu         sync.Mutex
	adapter    SessionBrokerAdapter
	records    map[string]SessionBrokerOwnership
	tombstones map[string]SessionBrokerOwnership
}

func NewSessionBroker(adapter SessionBrokerAdapter) *SessionBroker {
	return &SessionBroker{adapter: adapter, records: make(map[string]SessionBrokerOwnership), tombstones: make(map[string]SessionBrokerOwnership)}
}

func (b *SessionBroker) Handle(ctx context.Context, request SessionBrokerRequest) SessionBrokerResponse {
	response := SessionBrokerResponse{Version: SessionBrokerProtocolVersion, Operation: request.Operation}
	if err := request.validate(); err != nil {
		response.Code = sessionBrokerCode(err)
		return response
	}
	if ctx == nil || b == nil || b.adapter == nil {
		response.Code = SessionBrokerSessionUnavailable
		return response
	}
	switch request.Operation {
	case SessionBrokerStart:
		return b.start(ctx, request, response)
	case SessionBrokerStop:
		return b.stop(ctx, request, response)
	default:
		response.Code = SessionBrokerUnknownOperation
		return response
	}
}

func (b *SessionBroker) start(ctx context.Context, request SessionBrokerRequest, response SessionBrokerResponse) SessionBrokerResponse {
	b.mu.Lock()
	defer b.mu.Unlock()
	if previous, ok := b.records[request.SlotID]; ok {
		if previous.Generation > request.Generation {
			response.Code = SessionBrokerStaleGeneration
			return response
		}
		if !previous.sameRequest(request) {
			response.Code = SessionBrokerOwnershipMismatch
			return response
		}
		current, err := b.adapter.Find(ctx, previous.SID)
		if err == nil && current.ID == previous.SessionID && current.State == "active" {
			return sessionBrokerOK(response, current.ID, "active")
		}
		response.Code = SessionBrokerAlreadyStarted
		return response
	}
	if previous, ok := b.tombstones[request.SlotID]; ok && request.Generation <= previous.Generation {
		response.Code = SessionBrokerStaleGeneration
		return response
	}

	if current, err := b.adapter.Find(ctx, request.SID); current.ID != 0 || (err == nil && current.State != "") {
		response.Code = SessionBrokerOwnershipUnknown
		return response
	}
	ownership := SessionBrokerOwnership{SlotID: request.SlotID, Ordinal: request.Ordinal, Generation: request.Generation, SID: request.SID, Owner: request.Owner}
	started, err := b.adapter.Start(ctx, ownership)
	if err != nil || started.ID == 0 || started.State != "active" {
		if started.ID != 0 {
			ownership.SessionID = started.ID
			_ = b.adapter.Stop(ctx, ownership)
		}
		response.Code = SessionBrokerSessionUnavailable
		return response
	}
	current, err := b.adapter.Find(ctx, request.SID)
	if err != nil || current.ID != started.ID || current.State != "active" {
		ownership.SessionID = started.ID
		_ = b.adapter.Stop(ctx, ownership)
		response.Code = SessionBrokerSessionChanged
		return response
	}
	record := SessionBrokerOwnership{SlotID: request.SlotID, Ordinal: request.Ordinal, Generation: request.Generation, SID: request.SID, SessionID: current.ID, Owner: request.Owner}
	b.records[request.SlotID] = record
	return sessionBrokerOK(response, current.ID, "active")
}

func (b *SessionBroker) stop(ctx context.Context, request SessionBrokerRequest, response SessionBrokerResponse) SessionBrokerResponse {
	b.mu.Lock()
	defer b.mu.Unlock()
	record, ok := b.records[request.SlotID]
	tombstone, wasStopped := b.tombstones[request.SlotID]
	if !ok {
		if wasStopped && tombstone.sameRequest(request) && tombstone.SessionID == request.SessionID {
			return sessionBrokerOK(response, 0, "stopped")
		}
		response.Code = SessionBrokerOwnershipUnknown
		return response
	}
	if record.Generation > request.Generation {
		response.Code = SessionBrokerStaleGeneration
		return response
	}
	if !record.sameRequest(request) {
		response.Code = SessionBrokerOwnershipMismatch
		return response
	}
	if record.SessionID != request.SessionID {
		response.Code = SessionBrokerSessionChanged
		return response
	}
	if err := b.adapter.Stop(ctx, record); err != nil && !errors.Is(err, ErrSessionUnavailable) {
		response.Code = SessionBrokerSessionUnavailable
		return response
	}
	if err := waitBrokerSessionGone(ctx, b.adapter, record.SID); err != nil {
		response.Code = SessionBrokerStopTimeout
		return response
	}
	if current, present := b.records[request.SlotID]; present && current == record {
		delete(b.records, request.SlotID)
		b.tombstones[request.SlotID] = record
	}
	return sessionBrokerOK(response, 0, "stopped")
}

func waitBrokerSessionGone(ctx context.Context, adapter SessionBrokerAdapter, sid string) error {
	if ctx == nil {
		return ErrSessionBrokerStopTimeout
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
	}
	for {
		if _, err := adapter.Find(ctx, sid); errors.Is(err, ErrSessionUnavailable) {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func sessionBrokerOK(response SessionBrokerResponse, sessionID uint32, state string) SessionBrokerResponse {
	response.Code = SessionBrokerOK
	response.SessionID = sessionID
	response.State = state
	return response
}

func sessionBrokerCode(err error) SessionBrokerCode {
	switch {
	case errors.Is(err, ErrSessionBrokerInvalidVersion):
		return SessionBrokerInvalidVersion
	case errors.Is(err, ErrSessionBrokerUnknownOperation):
		return SessionBrokerUnknownOperation
	case errors.Is(err, ErrSessionBrokerOwnership):
		return SessionBrokerOwnershipMismatch
	case errors.Is(err, ErrSessionBrokerUnknownOwnership):
		return SessionBrokerOwnershipUnknown
	case errors.Is(err, ErrSessionBrokerStaleGeneration):
		return SessionBrokerStaleGeneration
	case errors.Is(err, ErrSessionBrokerAlreadyStarted):
		return SessionBrokerAlreadyStarted
	default:
		return SessionBrokerInvalidRequest
	}
}
