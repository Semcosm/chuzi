// Package request exposes the control-plane request operations used by later
// adapters. It validates caller input and delegates durable state changes to
// the store; it does not decide account transitions itself.
package request

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/internal/observability"
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

// Config controls optional request-service behavior. Rate limiting applies
// only to new submissions; Status and Cancel remain read/control operations.
type Config struct {
	RateLimit    RateLimitConfig
	Sink         observability.Sink
	Capabilities CapabilityRevoker
}

type CapabilityRevoker interface {
	RevokeRequest(context.Context, string) error
}

// StorePort is the durable request capability consumed by this package. It
// keeps request orchestration independent from the concrete bbolt store.
type StorePort interface {
	SubmitRequest(store.Request, account.Event) (store.Request, bool, error)
	GetRequest(string) (store.Request, error)
	GetAccount(string) (account.Snapshot, error)
	GetLease(string) (account.Lease, bool, error)
	CancelRequest(account.Event) (account.TransitionResult, error)
	CancelRequestOwned(account.Event, account.Lease, bool) (account.TransitionResult, error)
}

var _ StorePort = (*store.Store)(nil)

// Service implements request submission, lookup, and cancellation.
type Service struct {
	store        StorePort
	clock        Clock
	newID        IDGenerator
	defaultActor string
	limiter      *rateLimiter
	sink         observability.Sink
	capabilities CapabilityRevoker
}

// New constructs a request service with explicit time and ID dependencies.
func New(database StorePort, clock Clock, newID IDGenerator, defaultActor string) (*Service, error) {
	return NewWithConfig(database, clock, newID, defaultActor, Config{})
}

// NewWithConfig constructs a request service with explicit rate-limit and
// observability dependencies. Config keeps the legacy New signature stable.
func NewWithConfig(database StorePort, clock Clock, newID IDGenerator, defaultActor string, config Config) (*Service, error) {
	if database == nil || clock == nil || newID == nil || strings.TrimSpace(defaultActor) == "" {
		return nil, ErrInvalidInput
	}
	limiter, err := newRateLimiter(config.RateLimit)
	if err != nil {
		return nil, err
	}
	if config.Sink == nil {
		config.Sink = observability.NopSink{}
	}
	return &Service{store: database, clock: clock, newID: newID, defaultActor: defaultActor, limiter: limiter, sink: config.Sink, capabilities: config.Capabilities}, nil
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
	input.Actor = actor
	reservation, reserveErr := s.reserveSubmission(input, request, now)
	if reserveErr != nil {
		return store.Request{}, false, reserveErr
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
		reservation.rollback()
		return store.Request{}, false, err
	}
	if idempotent {
		reservation.rollback()
	}
	return result, idempotent, nil
}

type idempotencyReader interface {
	GetRequestByIdempotencyKey(string) (store.Request, error)
}

func (s *Service) reserveSubmission(input SubmitInput, request store.Request, now time.Time) (*rateLimitReservation, error) {
	if reader, ok := s.store.(idempotencyReader); ok {
		if _, err := reader.GetRequestByIdempotencyKey(input.IdempotencyKey); err == nil {
			// Let the durable transaction decide whether this is an identical
			// retry or an idempotency conflict without consuming a quota.
			return nil, nil
		}
	}
	reservation, err := s.limiter.reserve(input, now)
	if err != nil {
		s.sink.Record(observability.Event{At: now, Component: "request", Operation: "submit", Outcome: "denied", ErrorClass: "rate_limited", RequestID: observability.RedactIdentifier(request.RequestID)})
		return nil, ErrRateLimited
	}
	return reservation, nil
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
	if s.capabilities != nil {
		if err := s.capabilities.RevokeRequest(context.Background(), requestID); err != nil {
			return store.Request{}, err
		}
	}
	return s.store.GetRequest(requestID)
}
