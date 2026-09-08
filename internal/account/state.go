// Package account contains the deterministic account domain state machine.
//
// It deliberately has no persistence, clock, browser, Matrix, or credential
// dependencies. Callers provide a snapshot and an event, and persist the
// returned snapshot and audit record in a later integration layer.
package account

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Status is the externally visible business state of an account.
type Status string

const (
	NoRequest      Status = "NO_REQUEST"
	Queued         Status = "QUEUED"
	Starting       Status = "STARTING"
	LoggingIn      Status = "LOGGING_IN"
	LoginSucceeded Status = "LOGIN_SUCCEEDED"
	LoginFailed    Status = "LOGIN_FAILED"
	Expired        Status = "EXPIRED"
	Cancelled      Status = "CANCELLED"
	Blocked        Status = "BLOCKED"
)

// Valid reports whether s is a state declared by the account contract.
func (s Status) Valid() bool {
	switch s {
	case NoRequest, Queued, Starting, LoggingIn, LoginSucceeded,
		LoginFailed, Expired, Cancelled, Blocked:
		return true
	default:
		return false
	}
}

// IsTerminal reports whether no further business transition is allowed.
func (s Status) IsTerminal() bool {
	return s == Cancelled || s == Blocked
}

var (
	ErrInvalidSnapshot   = errors.New("account: invalid snapshot")
	ErrInvalidEvent      = errors.New("account: invalid event")
	ErrInvalidTransition = errors.New("account: invalid transition")
	ErrEventConflict     = errors.New("account: event id conflict")
	ErrAccountMismatch   = errors.New("account: account id mismatch")
	ErrRequestMismatch   = errors.New("account: request id mismatch")
	ErrStaleEvent        = errors.New("account: stale event")
	ErrRevisionOverflow  = errors.New("account: revision overflow")
)

// Event is a domain fact that requests one business-state transition.
type Event struct {
	EventID          string    `json:"event_id"`
	AccountID        string    `json:"account_id"`
	RequestID        string    `json:"request_id"`
	From             Status    `json:"from"`
	ExpectedRevision uint64    `json:"expected_revision"`
	To               Status    `json:"to"`
	Reason           string    `json:"reason"`
	Actor            string    `json:"actor"`
	OccurredAt       time.Time `json:"occurred_at"`
}

func (e Event) validFields() error {
	if !nonEmpty(e.EventID) || !nonEmpty(e.AccountID) || !nonEmpty(e.RequestID) ||
		!nonEmpty(e.Reason) || !nonEmpty(e.Actor) {
		return ErrInvalidEvent
	}
	if !e.From.Valid() || !e.To.Valid() {
		return ErrInvalidEvent
	}
	if e.OccurredAt.IsZero() {
		return ErrInvalidEvent
	}
	return nil
}

func (e Event) equal(other Event) bool {
	return e.EventID == other.EventID &&
		e.AccountID == other.AccountID &&
		e.RequestID == other.RequestID &&
		e.From == other.From &&
		e.ExpectedRevision == other.ExpectedRevision &&
		e.To == other.To &&
		e.Reason == other.Reason &&
		e.Actor == other.Actor &&
		e.OccurredAt.Equal(other.OccurredAt)
}

// AuditRecord is the immutable record produced by an accepted event.
type AuditRecord struct {
	Event    Event  `json:"event"`
	Revision uint64 `json:"revision"`
}

// Snapshot is the complete in-memory state required by Apply. AppliedEvents
// is retained here so duplicate events can be recognized before persistence is
// introduced. Callers should treat the map as immutable and use Clone when
// constructing a modified value.
type Snapshot struct {
	AccountID     string                 `json:"account_id"`
	Status        Status                 `json:"status"`
	RequestID     string                 `json:"request_id,omitempty"`
	Revision      uint64                 `json:"revision"`
	AppliedEvents map[string]AuditRecord `json:"applied_events,omitempty"`
}

// NewSnapshot creates an account with no active request.
func NewSnapshot(accountID string) (Snapshot, error) {
	if !nonEmpty(accountID) {
		return Snapshot{}, fmt.Errorf("%w: account id is required", ErrInvalidSnapshot)
	}
	return Snapshot{
		AccountID:     accountID,
		Status:        NoRequest,
		AppliedEvents: make(map[string]AuditRecord),
	}, nil
}

// Clone returns a snapshot whose event index can be changed independently.
func (s Snapshot) Clone() Snapshot {
	clone := s
	clone.AppliedEvents = make(map[string]AuditRecord, len(s.AppliedEvents))
	for id, record := range s.AppliedEvents {
		clone.AppliedEvents[id] = record
	}
	return clone
}

// Validate checks the invariants required before applying an event.
func (s Snapshot) Validate() error {
	if !nonEmpty(s.AccountID) || !s.Status.Valid() {
		return ErrInvalidSnapshot
	}
	if s.Status == NoRequest {
		if s.RequestID != "" {
			return fmt.Errorf("%w: no-request snapshot has a request id", ErrInvalidSnapshot)
		}
	} else if !nonEmpty(s.RequestID) {
		return fmt.Errorf("%w: active or terminal snapshot needs a request id", ErrInvalidSnapshot)
	}
	if uint64(len(s.AppliedEvents)) != s.Revision {
		return fmt.Errorf("%w: revision %d does not match event count %d", ErrInvalidSnapshot, s.Revision, len(s.AppliedEvents))
	}

	records := make([]AuditRecord, 0, len(s.AppliedEvents))
	for eventID, record := range s.AppliedEvents {
		if eventID == "" || eventID != record.Event.EventID || record.Revision == 0 || record.Revision > s.Revision {
			return fmt.Errorf("%w: invalid audit index", ErrInvalidSnapshot)
		}
		if err := record.Event.validFields(); err != nil {
			return fmt.Errorf("%w: invalid audit event %q", ErrInvalidSnapshot, eventID)
		}
		if record.Event.AccountID != s.AccountID || !CanTransition(record.Event.From, record.Event.To) {
			return fmt.Errorf("%w: invalid audit transition %q", ErrInvalidSnapshot, eventID)
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].Revision < records[j].Revision
	})
	replayedStatus := NoRequest
	replayedRequestID := ""
	for index, record := range records {
		wantRevision := uint64(index + 1)
		if record.Revision != wantRevision {
			return fmt.Errorf("%w: missing audit revision %d", ErrInvalidSnapshot, wantRevision)
		}
		if record.Event.ExpectedRevision != record.Revision-1 {
			return fmt.Errorf("%w: audit revision %d has expected revision %d", ErrInvalidSnapshot, record.Revision, record.Event.ExpectedRevision)
		}
		if record.Event.From != replayedStatus {
			return fmt.Errorf("%w: audit revision %d starts at %q, replay is at %q", ErrInvalidSnapshot, record.Revision, record.Event.From, replayedStatus)
		}
		if replayedStatus == NoRequest {
			replayedRequestID = record.Event.RequestID
		} else if record.Event.RequestID != replayedRequestID {
			return fmt.Errorf("%w: audit revision %d changes request id", ErrInvalidSnapshot, record.Revision)
		}
		replayedStatus = record.Event.To
	}
	if replayedStatus != s.Status || replayedRequestID != s.RequestID {
		return fmt.Errorf("%w: audit replay ends at %q/%q, snapshot is %q/%q", ErrInvalidSnapshot, replayedStatus, replayedRequestID, s.Status, s.RequestID)
	}
	return nil
}

// CanTransition reports whether the state graph permits from -> to.
func CanTransition(from, to Status) bool {
	if !from.Valid() || !to.Valid() {
		return false
	}
	switch from {
	case NoRequest:
		return to == Queued || to == Blocked
	case Queued:
		return to == Starting || to == Cancelled || to == Blocked
	case Starting:
		return to == LoggingIn || to == Cancelled || to == Blocked
	case LoggingIn:
		return to == LoginSucceeded || to == LoginFailed || to == Cancelled || to == Blocked
	case LoginSucceeded:
		return to == Expired || to == Blocked
	case LoginFailed:
		return to == Queued || to == Blocked
	case Expired:
		return to == Queued || to == Blocked
	default:
		return false
	}
}

// TransitionResult contains the resulting snapshot and audit record. When
// Idempotent is true, State is unchanged and Audit is the record from the
// original application.
type TransitionResult struct {
	State      Snapshot
	Audit      AuditRecord
	Idempotent bool
}

// Apply validates and applies one event without reading a clock or external
// system. It is safe to call again with the returned state and the same event.
func Apply(state Snapshot, event Event) (TransitionResult, error) {
	if err := state.Validate(); err != nil {
		return TransitionResult{}, err
	}
	if err := event.validFields(); err != nil {
		return TransitionResult{}, err
	}
	if event.AccountID != state.AccountID {
		return TransitionResult{}, fmt.Errorf("%w: event account %q, state account %q", ErrAccountMismatch, event.AccountID, state.AccountID)
	}

	if previous, exists := state.AppliedEvents[event.EventID]; exists {
		if previous.Event.equal(event) {
			return TransitionResult{
				State:      state.Clone(),
				Audit:      previous,
				Idempotent: true,
			}, nil
		}
		return TransitionResult{}, fmt.Errorf("%w: %q", ErrEventConflict, event.EventID)
	}

	if event.ExpectedRevision != state.Revision {
		return TransitionResult{}, fmt.Errorf("%w: event revision %d, state revision %d", ErrStaleEvent, event.ExpectedRevision, state.Revision)
	}
	if event.From != state.Status {
		return TransitionResult{}, fmt.Errorf("%w: event from %q, state is %q", ErrStaleEvent, event.From, state.Status)
	}
	if !CanTransition(event.From, event.To) {
		return TransitionResult{}, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, event.From, event.To)
	}
	if state.Status == NoRequest {
		// The first request, or a system-created block, establishes the
		// correlation id for this account lifecycle.
	} else if event.RequestID != state.RequestID {
		return TransitionResult{}, fmt.Errorf("%w: event %q, state %q", ErrRequestMismatch, event.RequestID, state.RequestID)
	}
	if state.Revision == ^uint64(0) {
		return TransitionResult{}, ErrRevisionOverflow
	}

	next := state.Clone()
	next.Status = event.To
	if state.Status == NoRequest {
		next.RequestID = event.RequestID
	}
	next.Revision++
	audit := AuditRecord{Event: event, Revision: next.Revision}
	next.AppliedEvents[event.EventID] = audit
	return TransitionResult{State: next, Audit: audit}, nil
}

func nonEmpty(value string) bool {
	return strings.TrimSpace(value) != ""
}
