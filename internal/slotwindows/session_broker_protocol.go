package slotwindows

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// SessionBrokerPipeName is a build-time constant. There is deliberately no
// option, request field, or environment override for the broker endpoint.
const SessionBrokerPipeName = `\\.\pipe\chuzi-session-bootstrap-v1`

const SessionBrokerProtocolVersion = 1
const sessionBrokerCleanupTimeout = 10 * time.Second

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
		if r.Operation == SessionBrokerStop && r.State != "stopped" {
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
	SlotID     string `json:"slot_id"`
	Ordinal    int    `json:"ordinal"`
	Generation uint64 `json:"generation"`
	SID        string `json:"sid"`
	SessionID  uint32 `json:"session_id"`
	Owner      string `json:"owner"`
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

// SessionBrokerJournal is a separate, broker-owned persistence boundary. It
// stores only exact slot/SID/session/generation/owner facts; it never stores
// credentials, profile paths, endpoints, or provider data. A deployment may
// leave the journal nil for an explicitly volatile test broker.
type SessionBrokerJournal interface {
	Load(context.Context) (SessionBrokerJournalState, error)
	Save(context.Context, SessionBrokerJournalState) error
}

type SessionBrokerJournalState struct {
	Version    int                      `json:"version"`
	Records    []SessionBrokerOwnership `json:"records,omitempty"`
	Tombstones []SessionBrokerOwnership `json:"tombstones,omitempty"`
}

// SessionBroker holds ownership records created by this broker or recovered
// from its durable journal. Recovery still requires an authoritative adapter
// Find check before any record is used.
type SessionBroker struct {
	mu         sync.Mutex
	slotMu     sync.Mutex
	slotLocks  map[string]*sync.Mutex
	adapter    SessionBrokerAdapter
	journal    SessionBrokerJournal
	records    map[string]SessionBrokerOwnership
	tombstones map[string]SessionBrokerOwnership
}

func NewSessionBroker(adapter SessionBrokerAdapter) *SessionBroker {
	return &SessionBroker{adapter: adapter, slotLocks: make(map[string]*sync.Mutex), records: make(map[string]SessionBrokerOwnership), tombstones: make(map[string]SessionBrokerOwnership)}
}

func (b *SessionBroker) lockSlot(slotID string) func() {
	b.slotMu.Lock()
	if b.slotLocks == nil {
		b.slotLocks = make(map[string]*sync.Mutex)
	}
	lock := b.slotLocks[slotID]
	if lock == nil {
		lock = &sync.Mutex{}
		b.slotLocks[slotID] = lock
	}
	b.slotMu.Unlock()
	lock.Lock()
	return lock.Unlock
}

// NewSessionBrokerWithJournal constructs a broker whose ownership survives a
// process restart. Loading is fail-closed: malformed or unavailable state
// must prevent the broker from serving requests rather than weakening the
// ownership fence.
func NewSessionBrokerWithJournal(adapter SessionBrokerAdapter, journal SessionBrokerJournal) (*SessionBroker, error) {
	if adapter == nil || journal == nil {
		return nil, ErrSessionBrokerInvalidRequest
	}
	state, err := journal.Load(context.Background())
	if err != nil {
		return nil, err
	}
	if state.Version != 1 {
		return nil, ErrSessionBrokerInvalidRequest
	}
	broker := &SessionBroker{adapter: adapter, journal: journal, slotLocks: make(map[string]*sync.Mutex), records: make(map[string]SessionBrokerOwnership), tombstones: make(map[string]SessionBrokerOwnership)}
	for _, record := range state.Records {
		if err := validateJournalOwnership(record, true); err != nil {
			return nil, err
		}
		if _, exists := broker.records[record.SlotID]; exists {
			return nil, ErrSessionBrokerInvalidRequest
		}
		broker.records[record.SlotID] = record
	}
	for _, record := range state.Tombstones {
		if err := validateJournalOwnership(record, true); err != nil {
			return nil, err
		}
		if _, exists := broker.tombstones[record.SlotID]; exists {
			return nil, ErrSessionBrokerInvalidRequest
		}
		if _, active := broker.records[record.SlotID]; active {
			return nil, ErrSessionBrokerInvalidRequest
		}
		broker.tombstones[record.SlotID] = record
	}
	return broker, nil
}

func validateJournalOwnership(record SessionBrokerOwnership, requireSession bool) error {
	if record.SessionID == 0 && requireSession {
		return ErrSessionBrokerInvalidRequest
	}
	request := SessionBrokerRequest{Version: SessionBrokerProtocolVersion, Operation: SessionBrokerStart, SlotID: record.SlotID, Ordinal: record.Ordinal, Generation: record.Generation, SID: record.SID, Owner: record.Owner}
	return request.validate()
}

func (b *SessionBroker) persistLocked(ctx context.Context) error {
	if b.journal == nil {
		return nil
	}
	state := SessionBrokerJournalState{Version: 1, Records: make([]SessionBrokerOwnership, 0, len(b.records)), Tombstones: make([]SessionBrokerOwnership, 0, len(b.tombstones))}
	for _, record := range b.records {
		state.Records = append(state.Records, record)
	}
	for _, record := range b.tombstones {
		state.Tombstones = append(state.Tombstones, record)
	}
	sort.Slice(state.Records, func(i, j int) bool { return state.Records[i].SlotID < state.Records[j].SlotID })
	sort.Slice(state.Tombstones, func(i, j int) bool { return state.Tombstones[i].SlotID < state.Tombstones[j].SlotID })
	return b.journal.Save(ctx, state)
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
	if b == nil {
		response.Code = SessionBrokerSessionUnavailable
		return response
	}
	unlock := b.lockSlot(request.SlotID)
	defer unlock()

	b.mu.Lock()
	previous, hasPrevious := b.records[request.SlotID]
	b.mu.Unlock()
	if hasPrevious {
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
		// A durable ownership record whose exact session cannot be proven is
		// quarantined logically. Reporting AlreadyStarted would strand the
		// operation and could let a reused SID/session be mistaken for the
		// recorded runtime. Keep the record for operator/recovery handling.
		if err == nil && current.ID != 0 {
			response.Code = SessionBrokerSessionChanged
		} else {
			response.Code = SessionBrokerOwnershipUnknown
		}
		return response
	}
	b.mu.Lock()
	tombstone, hasTombstone := b.tombstones[request.SlotID]
	b.mu.Unlock()
	if hasTombstone && request.Generation <= tombstone.Generation {
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
			if cleanupErr := b.cleanupStartedSession(ownership); cleanupErr != nil {
				response.Code = SessionBrokerStopTimeout
				return response
			}
		}
		response.Code = SessionBrokerSessionUnavailable
		return response
	}
	if ctx.Err() != nil {
		ownership.SessionID = started.ID
		if err := b.cleanupStartedSession(ownership); err != nil {
			response.Code = SessionBrokerStopTimeout
		} else {
			response.Code = SessionBrokerSessionUnavailable
		}
		return response
	}
	current, err := b.adapter.Find(ctx, request.SID)
	if err != nil || current.ID != started.ID || current.State != "active" {
		ownership.SessionID = started.ID
		if cleanupErr := b.cleanupStartedSession(ownership); cleanupErr != nil {
			response.Code = SessionBrokerStopTimeout
			return response
		}
		response.Code = SessionBrokerSessionChanged
		return response
	}
	if ctx.Err() != nil {
		ownership.SessionID = started.ID
		if err := b.cleanupStartedSession(ownership); err != nil {
			response.Code = SessionBrokerStopTimeout
		} else {
			response.Code = SessionBrokerSessionUnavailable
		}
		return response
	}
	record := SessionBrokerOwnership{SlotID: request.SlotID, Ordinal: request.Ordinal, Generation: request.Generation, SID: request.SID, SessionID: current.ID, Owner: request.Owner}
	b.mu.Lock()
	b.records[request.SlotID] = record
	if err := b.persistLocked(ctx); err != nil {
		delete(b.records, request.SlotID)
		b.mu.Unlock()
		if cleanupErr := b.cleanupStartedSession(record); cleanupErr != nil {
			response.Code = SessionBrokerStopTimeout
			return response
		}
		response.Code = SessionBrokerSessionUnavailable
		return response
	}
	b.mu.Unlock()
	return sessionBrokerOK(response, current.ID, "active")
}

func (b *SessionBroker) stop(ctx context.Context, request SessionBrokerRequest, response SessionBrokerResponse) SessionBrokerResponse {
	if b == nil {
		response.Code = SessionBrokerSessionUnavailable
		return response
	}
	unlock := b.lockSlot(request.SlotID)
	defer unlock()

	b.mu.Lock()
	record, ok := b.records[request.SlotID]
	tombstone, wasStopped := b.tombstones[request.SlotID]
	b.mu.Unlock()
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
	observed, findErr := b.adapter.Find(ctx, record.SID)
	if findErr == nil {
		if observed.ID != record.SessionID || observed.State != "active" {
			response.Code = SessionBrokerSessionChanged
			return response
		}
	} else if !errors.Is(findErr, ErrSessionUnavailable) {
		response.Code = SessionBrokerSessionUnavailable
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
	b.mu.Lock()
	current, present := b.records[request.SlotID]
	if present && current == record {
		delete(b.records, request.SlotID)
		b.tombstones[request.SlotID] = record
		if err := b.persistLocked(ctx); err != nil {
			// Keep the record in memory so a retry can commit the durable
			// tombstone. The session is already absent, so returning success
			// here would make a broker restart report unknown ownership.
			delete(b.tombstones, request.SlotID)
			b.records[request.SlotID] = record
			b.mu.Unlock()
			response.Code = SessionBrokerSessionUnavailable
			return response
		}
	}
	b.mu.Unlock()
	return sessionBrokerOK(response, 0, "stopped")
}

func waitBrokerSessionGone(ctx context.Context, adapter SessionBrokerAdapter, sid string) error {
	if ctx == nil {
		return ErrSessionBrokerStopTimeout
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, sessionBrokerCleanupTimeout)
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

// cleanupStartedSession runs after the caller's context has been cancelled.
// The cancellation must not prevent the broker from fencing the exact session
// it just created, and success requires an authoritative absence check.
func (b *SessionBroker) cleanupStartedSession(ownership SessionBrokerOwnership) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), sessionBrokerCleanupTimeout)
	defer cancel()
	stopErr := b.adapter.Stop(cleanupCtx, ownership)
	waitErr := waitBrokerSessionGone(cleanupCtx, b.adapter, ownership.SID)
	if waitErr != nil {
		return waitErr
	}
	if stopErr != nil && !errors.Is(stopErr, ErrSessionUnavailable) {
		return stopErr
	}
	return nil
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
