// Package request exposes the control-plane request operations used by later
// adapters. It validates caller input and delegates durable state changes to
// the store; it does not decide account transitions itself.
package request

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/internal/store"
)

var (
	ErrInvalidInput = errors.New("request: invalid input")
	ErrNotAllowed   = errors.New("request: operation is not allowed")
)

// IDGenerator supplies stable IDs for requests and domain events. Production
// adapters can provide their own generator; tests use a deterministic one.
type IDGenerator func(kind string) string

// Clock supplies the current time to keep request behavior replayable.
type Clock func() time.Time

// Service implements request submission, lookup, and cancellation.
type Service struct {
	store        *store.Store
	clock        Clock
	newID        IDGenerator
	defaultActor string
}

// New constructs a request service with explicit time and ID dependencies.
func New(database *store.Store, clock Clock, newID IDGenerator, defaultActor string) (*Service, error) {
	if database == nil || clock == nil || newID == nil || strings.TrimSpace(defaultActor) == "" {
		return nil, ErrInvalidInput
	}
	return &Service{store: database, clock: clock, newID: newID, defaultActor: defaultActor}, nil
}

// SubmitInput describes one login request. RequestID is supplied by the
// caller so retries can preserve a stable public correlation ID.
type SubmitInput struct {
	RequestID          string
	AccountID          string
	IdempotencyKey     string
	NotificationRoomID string
	Actor              string
	Deadline           time.Time
}

// Submit creates and queues a request atomically. The returned boolean is true
// when the idempotency key resolved to an existing request.
func (s *Service) Submit(input SubmitInput) (store.Request, bool, error) {
	if s == nil || strings.TrimSpace(input.RequestID) == "" ||
		strings.TrimSpace(input.AccountID) == "" || strings.TrimSpace(input.IdempotencyKey) == "" {
		return store.Request{}, false, ErrInvalidInput
	}
	now := s.clock()
	if now.IsZero() {
		return store.Request{}, false, ErrInvalidInput
	}
	request, err := store.NewRequest(input.RequestID, input.AccountID, input.IdempotencyKey, now)
	if err != nil {
		return store.Request{}, false, err
	}
	request.NotificationRoomID = input.NotificationRoomID
	if !input.Deadline.IsZero() {
		if input.Deadline.Before(now) {
			return store.Request{}, false, fmt.Errorf("%w: deadline is in the past", ErrInvalidInput)
		}
		request.Deadline = input.Deadline
	}
	if err := request.Validate(); err != nil {
		return store.Request{}, false, err
	}
	actor := strings.TrimSpace(input.Actor)
	if actor == "" {
		actor = s.defaultActor
	}
	event := account.Event{
		EventID:    s.newID("queue"),
		AccountID:  input.AccountID,
		RequestID:  input.RequestID,
		From:       account.NoRequest,
		To:         account.Queued,
		Reason:     "request submitted",
		Actor:      actor,
		OccurredAt: now,
	}
	result, idempotent, err := s.store.SubmitRequest(request, event)
	if err != nil {
		return store.Request{}, false, err
	}
	return result, idempotent, nil
}

// Status returns a validated durable request projection.
func (s *Service) Status(requestID string) (store.Request, error) {
	if s == nil || strings.TrimSpace(requestID) == "" {
		return store.Request{}, ErrInvalidInput
	}
	return s.store.GetRequest(requestID)
}

// Cancel transitions an active request to CANCELLED and releases a lease in
// the same storage transaction.
func (s *Service) Cancel(requestID, actor, reason string) (store.Request, error) {
	if s == nil || strings.TrimSpace(requestID) == "" {
		return store.Request{}, ErrInvalidInput
	}
	if strings.TrimSpace(actor) == "" {
		actor = s.defaultActor
	}
	if strings.TrimSpace(reason) == "" {
		reason = "request cancelled"
	}
	now := s.clock()
	if now.IsZero() {
		return store.Request{}, ErrInvalidInput
	}
	request, err := s.store.GetRequest(requestID)
	if err != nil {
		return store.Request{}, err
	}
	state, err := s.store.GetAccount(request.AccountID)
	if err != nil {
		return store.Request{}, err
	}
	lease, hasLease, err := s.store.GetLease(request.AccountID)
	if err != nil {
		return store.Request{}, err
	}
	event := account.Event{
		EventID:          s.newID("cancel"),
		AccountID:        request.AccountID,
		RequestID:        requestID,
		From:             state.Status,
		ExpectedRevision: state.Revision,
		To:               account.Cancelled,
		Reason:           reason,
		Actor:            actor,
		OccurredAt:       now,
	}
	if hasLease {
		if _, err := s.store.CancelRequestOwned(event, lease, false); err != nil {
			return store.Request{}, err
		}
	} else if _, err := s.store.CancelRequest(event); err != nil {
		return store.Request{}, err
	}
	return s.store.GetRequest(requestID)
}
