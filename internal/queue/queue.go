// Package queue provides deterministic request scheduling over the durable
// store. It owns timing, retry, and lease orchestration; account business
// states remain owned by internal/account and are changed through store APIs.
package queue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/internal/store"
)

var (
	ErrInvalidConfig     = errors.New("queue: invalid configuration")
	ErrRunnerUnavailable = errors.New("queue: runner is unavailable")
)

// Clock and IDGenerator are injected so scheduling can be replayed in tests.
type Clock func() time.Time
type IDGenerator func(kind string) string

// Work is the lease-bound unit handed to a session runner.
type Work struct {
	Request store.Request
	Lease   account.Lease
}

// Result is the runner's redacted runtime fact. The runner must not choose a
// business state or return credentials/page contents.
type Result struct {
	Succeeded bool
	Failure   account.FailureClass
}

// Runner is implemented by the later Session Runner. A fake implementation is
// sufficient for this phase.
type Runner interface {
	Run(context.Context, Work) (Result, error)
}

// Config controls one scheduler instance.
type Config struct {
	Owner                string
	LeaseTTL             time.Duration
	RunTimeout           time.Duration
	MaxGlobalConcurrency int
	RetryPolicy          account.RetryPolicy
	Clock                Clock
	NewID                IDGenerator
}

// Scheduler claims and processes at most one request per RunOnce call. A
// caller can invoke it from a worker loop or use it in deterministic tests.
type Scheduler struct {
	store  *store.Store
	runner Runner
	config Config
}

// New validates scheduler dependencies and returns a ready scheduler.
func New(database *store.Store, runner Runner, config Config) (*Scheduler, error) {
	if database == nil || runner == nil || strings.TrimSpace(config.Owner) == "" ||
		config.LeaseTTL <= 0 || config.RunTimeout <= 0 || config.MaxGlobalConcurrency < 1 ||
		config.Clock == nil || config.NewID == nil {
		return nil, ErrInvalidConfig
	}
	if _, err := config.RetryPolicy.Decide(account.Failure{Class: account.TransientFailure, Attempt: 1}); err != nil {
		return nil, err
	}
	return &Scheduler{store: database, runner: runner, config: config}, nil
}

// Outcome describes one scheduling pass.
type Outcome struct {
	Idle      bool
	Request   store.Request
	Retried   bool
	Succeeded bool
}

// RunOnce recovers expired work, expires requests past their deadline, claims
// one ready request, and drives its fake/session runner to a durable result.
func (s *Scheduler) RunOnce(ctx context.Context) (Outcome, error) {
	if s == nil || ctx == nil {
		return Outcome{}, ErrInvalidConfig
	}
	if err := ctx.Err(); err != nil {
		return Outcome{}, err
	}
	now := s.config.Clock()
	if now.IsZero() {
		return Outcome{}, ErrInvalidConfig
	}
	if err := s.recoverExpired(now); err != nil {
		return Outcome{}, err
	}
	if err := s.expireQueued(now); err != nil {
		return Outcome{}, err
	}
	claim, err := s.store.ClaimNext(now, s.config.NewID("lease"), s.config.Owner,
		s.config.LeaseTTL, s.config.NewID("claim"), s.config.Owner,
		"queue claim", store.QueueOptions{MaxGlobalConcurrency: s.config.MaxGlobalConcurrency})
	if errors.Is(err, store.ErrQueueEmpty) || errors.Is(err, store.ErrQueueCapacity) {
		return Outcome{Idle: true}, nil
	}
	if err != nil {
		return Outcome{}, err
	}

	state := claim.Transition.State
	startEvent := account.Event{
		EventID:   s.config.NewID("start"),
		AccountID: claim.Request.AccountID,
		RequestID: claim.Request.RequestID,
		// ClaimNext committed QUEUED -> STARTING. Keep the expected phase
		// explicit so a concurrent cancellation cannot be reinterpreted as a
		// valid start transition.
		From:             account.Starting,
		ExpectedRevision: state.Revision,
		To:               account.LoggingIn,
		Reason:           "session runner started",
		Actor:            s.config.Owner,
		OccurredAt:       now,
	}
	if _, err := s.store.ApplyEvent(startEvent); err != nil {
		cleanup := account.Event{
			EventID:          s.config.NewID("start-failure"),
			AccountID:        claim.Request.AccountID,
			RequestID:        claim.Request.RequestID,
			From:             account.Starting,
			ExpectedRevision: claim.Transition.State.Revision,
			To:               account.Cancelled,
			Reason:           "session runner start failed",
			Actor:            s.config.Owner,
			OccurredAt:       now,
		}
		_, _ = s.store.CancelRequestOwned(cleanup, claim.Lease, true)
		_ = s.store.ReleaseLease(claim.Request.AccountID, claim.Lease.LeaseID, claim.Lease.Owner)
		return Outcome{}, err
	}

	runnerContext := ctx
	stopRunnerContext := func() {}
	if s.config.RunTimeout > 0 {
		runnerContext, stopRunnerContext = context.WithTimeout(ctx, s.config.RunTimeout)
	}
	runnerResult, runnerErr := s.runner.Run(runnerContext, Work{Request: claim.Request, Lease: claim.Lease})
	runnerContextErr := runnerContext.Err()
	stopRunnerContext()
	finishedAt := s.config.Clock()
	if finishedAt.IsZero() {
		return Outcome{}, ErrInvalidConfig
	}
	if runnerErr != nil || runnerContextErr != nil {
		runnerResult = Result{Failure: account.TransientFailure}
	}
	if runnerResult.Succeeded {
		return s.finishSuccess(finishedAt, claim.Request, claim.Lease, Outcome{Request: claim.Request})
	}
	if !validFailure(runnerResult.Failure) {
		runnerResult.Failure = account.UnknownFailure
	}
	return s.finishFailure(finishedAt, claim.Request, claim.Lease, false, runnerResult.Failure)
}

func (s *Scheduler) finishSuccess(now time.Time, request store.Request, lease account.Lease, outcome Outcome) (Outcome, error) {
	state, err := s.store.GetAccount(request.AccountID)
	if err != nil {
		return Outcome{}, err
	}
	event := account.Event{
		EventID:   s.config.NewID("success"),
		AccountID: request.AccountID,
		RequestID: request.RequestID,
		// A runner can only finish a request after the STARTING -> LOGGING_IN
		// event. A fixed From phase turns a concurrent cancellation/recovery
		// into a stale event instead of allowing a different transition.
		From:             account.LoggingIn,
		ExpectedRevision: state.Revision,
		To:               account.LoginSucceeded,
		Reason:           "session runner succeeded",
		Actor:            s.config.Owner,
		OccurredAt:       now,
	}
	if _, err := s.store.CompleteRequestOwned(event, lease); err != nil {
		return Outcome{}, err
	}
	outcome.Succeeded = true
	outcome.Request, err = s.store.GetRequest(request.RequestID)
	return outcome, err
}

func (s *Scheduler) finishFailure(now time.Time, request store.Request, lease account.Lease, allowExpired bool, class account.FailureClass) (Outcome, error) {
	state, err := s.store.GetAccount(request.AccountID)
	if err != nil {
		return Outcome{}, err
	}
	decision, err := s.config.RetryPolicy.Decide(account.Failure{Class: class, Attempt: request.Attempt})
	if err != nil {
		return Outcome{}, err
	}
	failureEvent := account.Event{
		EventID:   s.config.NewID("failure"),
		AccountID: request.AccountID,
		RequestID: request.RequestID,
		// The runner owns a LOGGING_IN lease. Do not derive From from a later
		// read, otherwise a concurrent state change could be overwritten as a
		// failure.
		From:             account.LoggingIn,
		ExpectedRevision: state.Revision,
		To:               account.LoginFailed,
		Reason:           failureReason(class),
		Actor:            s.config.Owner,
		OccurredAt:       now,
	}
	var retryEvent *account.Event
	if decision.Retry {
		event := account.Event{
			EventID:          s.config.NewID("retry"),
			AccountID:        request.AccountID,
			RequestID:        request.RequestID,
			From:             account.LoginFailed,
			ExpectedRevision: state.Revision + 1,
			To:               account.Queued,
			Reason:           "transient failure retry",
			Actor:            s.config.Owner,
			OccurredAt:       now,
		}
		retryEvent = &event
	}
	result, err := s.store.RecordFailureOwned(failureEvent, class, now.Add(decision.Delay), retryEvent, lease, allowExpired)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Request: result.Request, Retried: result.Retried != nil}, nil
}

func (s *Scheduler) expireQueued(now time.Time) error {
	requests, err := s.store.ListRequests()
	if err != nil {
		return err
	}
	for _, request := range requests {
		if request.State != account.Queued || request.Deadline.IsZero() || now.Before(request.Deadline) {
			continue
		}
		state, err := s.store.GetAccount(request.AccountID)
		if err != nil {
			return err
		}
		if state.Status != account.Queued || state.RequestID != request.RequestID {
			// The request advanced after ListRequests; the stale deadline pass
			// must not cancel a running lifecycle.
			continue
		}
		event := account.Event{
			EventID:          s.config.NewID("deadline"),
			AccountID:        request.AccountID,
			RequestID:        request.RequestID,
			From:             account.Queued,
			ExpectedRevision: state.Revision,
			To:               account.Cancelled,
			Reason:           "request deadline reached",
			Actor:            s.config.Owner,
			OccurredAt:       now,
		}
		if _, err := s.store.CancelRequest(event); err != nil && !errors.Is(err, account.ErrStaleEvent) {
			return err
		}
	}
	return nil
}

func (s *Scheduler) recoverExpired(now time.Time) error {
	leases, err := s.store.ListLeases()
	if err != nil {
		return err
	}
	for _, record := range leases {
		if !record.Lease.Expired(now) {
			continue
		}
		request, err := s.requestForAccount(record.AccountID)
		if err != nil {
			return err
		}
		state, err := s.store.GetAccount(record.AccountID)
		if err != nil {
			return err
		}
		if state.RequestID != request.RequestID || state.Status != request.State {
			// The lease snapshot no longer describes the account lifecycle.
			// Recovery must not touch the newer state.
			continue
		}
		switch request.State {
		case account.LoggingIn:
			if _, err := s.finishFailure(now, request, record.Lease, true, account.TransientFailure); err != nil && !errors.Is(err, account.ErrStaleEvent) && !errors.Is(err, account.ErrLeaseNotOwned) && !errors.Is(err, store.ErrLeaseNotFound) {
				return err
			}
		case account.Starting:
			event := account.Event{
				EventID:          s.config.NewID("recovery"),
				AccountID:        record.AccountID,
				RequestID:        request.RequestID,
				From:             account.Starting,
				ExpectedRevision: state.Revision,
				To:               account.Cancelled,
				Reason:           "lease expired before session start",
				Actor:            s.config.Owner,
				OccurredAt:       now,
			}
			if _, err := s.store.CancelRequestOwned(event, record.Lease, true); err != nil &&
				!errors.Is(err, account.ErrStaleEvent) && !errors.Is(err, account.ErrLeaseNotOwned) &&
				!errors.Is(err, store.ErrLeaseNotFound) {
				return err
			}
		default:
			if err := s.store.ReleaseLease(record.AccountID, record.Lease.LeaseID, record.Lease.Owner); err != nil && !errors.Is(err, store.ErrLeaseNotFound) && !errors.Is(err, account.ErrLeaseNotOwned) {
				return err
			}
		}
	}
	return nil
}

func (s *Scheduler) requestForAccount(accountID string) (store.Request, error) {
	state, err := s.store.GetAccount(accountID)
	if err != nil {
		return store.Request{}, err
	}
	if state.RequestID == "" || state.Status == account.NoRequest {
		return store.Request{}, fmt.Errorf("%w: request for account %q", store.ErrRequestNotFound, accountID)
	}
	request, err := s.store.GetRequest(state.RequestID)
	if err != nil {
		return store.Request{}, err
	}
	if request.AccountID != accountID || request.State != state.Status {
		return store.Request{}, fmt.Errorf("%w: request for account %q does not match account state", store.ErrRequestStateMismatch, accountID)
	}
	return request, nil
}

func validFailure(class account.FailureClass) bool {
	switch class {
	case account.TransientFailure, account.CredentialFailure,
		account.PermissionFailure, account.ConfigurationFailure,
		account.UnknownFailure:
		return true
	default:
		return false
	}
}

func failureReason(class account.FailureClass) string {
	if !validFailure(class) {
		return "unknown failure"
	}
	return "session failure: " + string(class)
}
