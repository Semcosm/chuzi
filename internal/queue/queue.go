// Package queue provides deterministic request scheduling over the durable
// store. It owns timing, retry, and lease orchestration; account business
// states remain owned by internal/account and are changed through store APIs.
package queue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/internal/observability"
	"github.com/Semcosm/chuzi/internal/slot"
	"github.com/Semcosm/chuzi/internal/store"
)

var (
	ErrInvalidConfig     = errors.New("queue: invalid configuration")
	ErrRunnerUnavailable = errors.New("queue: runner is unavailable")
)

// Clock and IDGenerator are injected so scheduling can be replayed in tests.
type Clock func() time.Time
type IDGenerator func(kind string) string

// StorePort is the scheduler's durable capability set. Keeping it here makes
// the queue testable with a focused store fake and prevents it from depending
// on unrelated persistence operations.
type StorePort interface {
	ClaimNext(time.Time, string, string, time.Duration, string, string, string, store.QueueOptions) (store.Claim, error)
	ApplyEvent(account.Event) (account.TransitionResult, error)
	CancelRequestOwned(account.Event, account.Lease, bool) (account.TransitionResult, error)
	ReleaseLease(string, string, string) error
	GetRequest(string) (store.Request, error)
	GetAccount(string) (account.Snapshot, error)
	CompleteRequestOwned(account.Event, account.Lease) (account.TransitionResult, error)
	RecordFailureOwned(account.Event, account.FailureClass, time.Time, *account.Event, account.Lease, bool) (store.FailureResult, error)
	ListRequests() ([]store.Request, error)
	CancelRequest(account.Event) (account.TransitionResult, error)
	ListLeases() ([]store.LeaseRecord, error)
}

// SlotAwareClaimer atomically claims an account request and a matching
// execution slot. Concrete store implementations use one bbolt transaction;
// older focused test stores can continue using StorePort without slots.
type SlotAwareClaimer interface {
	ClaimNextWithSlot(time.Time, string, string, time.Duration, string, string, string, store.QueueOptions, string, slot.EnvironmentRequirement) (store.SlotClaim, error)
}

type SlotLeaseRecovery interface {
	RecoverExpiredSlotLeases(time.Time) error
}

type SlotLeaseLister interface {
	ListSlotLeases() ([]store.SlotLeaseRecord, error)
}

// SlotLeaseMetadataReader exposes the durable slot runtime marker used to
// decide whether an expired lease needs an agent fence. A missing marker is a
// logical slot and can be recovered directly; a persisted marker requires a
// successful platform stop confirmation before Store may remove the lease.
type SlotLeaseMetadataReader interface {
	GetSlot(string) (slot.Slot, error)
}

// SlotLeaseStopper fences any residual agent job before durable lease
// recovery removes the lease record.
type SlotLeaseStopper interface {
	StopSlotLease(context.Context, slot.Lease) error
}

type SlotLeaseFenceConfirmer interface {
	ConfirmSlotLeaseStopped(slot.Lease) error
}

// CapabilityRevoker invalidates ephemeral interactive capabilities at the
// same lifecycle boundaries that release account and slot leases.
type CapabilityRevoker interface {
	RevokeRequest(context.Context, string) error
	RevokeSlotLease(context.Context, string) error
}

// AccountLeaseRevoker is optional so focused scheduler fakes can retain the
// smaller capability surface while the production credential boundary fences
// capabilities when an account lease expires.
type AccountLeaseRevoker interface {
	RevokeAccountLease(context.Context, string) error
}

var _ StorePort = (*store.Store)(nil)

// Work is the lease-bound unit handed to a session runner.
type Work struct {
	Request               store.Request
	Lease                 account.Lease
	Slot                  slot.Slot
	SlotLease             slot.Lease
	SlotID                string
	EnvironmentGeneration uint64
	AgentHandle           string
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
	Sink                 observability.Sink
	SlotPoolID           string
	SlotRequirement      slot.EnvironmentRequirement
	RuntimeConfig        RuntimeConfigProvider
	Capabilities         CapabilityRevoker
	SlotLeaseStopper     SlotLeaseStopper
}

// RuntimeConfig is the scheduler-owned projection of control-plane settings.
// It is deliberately limited to scheduling inputs; callers cannot mutate the
// Scheduler's durable state through this callback.
type RuntimeConfig struct {
	SlotPoolID           string
	SlotRequirement      slot.EnvironmentRequirement
	MaxGlobalConcurrency int
}

// RuntimeConfigProvider supplies the current control-plane scheduling
// projection before each scheduling pass.
type RuntimeConfigProvider func() (RuntimeConfig, error)

func (s *Scheduler) revokeRequest(requestID string) error {
	if s == nil || s.config.Capabilities == nil || requestID == "" {
		return nil
	}
	return s.config.Capabilities.RevokeRequest(context.Background(), requestID)
}

func (s *Scheduler) revokeSlotLease(leaseID string) error {
	if s == nil || s.config.Capabilities == nil || leaseID == "" {
		return nil
	}
	return s.config.Capabilities.RevokeSlotLease(context.Background(), leaseID)
}

func (s *Scheduler) revokeAccountLease(leaseID string) error {
	if s == nil || s.config.Capabilities == nil || leaseID == "" {
		return nil
	}
	revoker, ok := s.config.Capabilities.(AccountLeaseRevoker)
	if !ok {
		return nil
	}
	return revoker.RevokeAccountLease(context.Background(), leaseID)
}

// Scheduler claims and processes at most one request per RunOnce call. A
// caller can invoke it from a worker loop or use it in deterministic tests.
type Scheduler struct {
	mu     sync.Mutex
	store  StorePort
	runner Runner
	config Config
}

// New validates scheduler dependencies and returns a ready scheduler.
func New(database StorePort, runner Runner, config Config) (*Scheduler, error) {
	if database == nil || runner == nil || strings.TrimSpace(config.Owner) == "" ||
		config.LeaseTTL <= 0 || config.RunTimeout <= 0 || config.MaxGlobalConcurrency < 1 ||
		config.Clock == nil || config.NewID == nil {
		return nil, ErrInvalidConfig
	}
	if _, err := config.RetryPolicy.Decide(account.Failure{Class: account.TransientFailure, Attempt: 1}); err != nil {
		return nil, err
	}
	if config.Sink == nil {
		config.Sink = observability.NopSink{}
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Outcome{}, err
	}
	if s.config.RuntimeConfig != nil {
		runtimeConfig, err := s.config.RuntimeConfig()
		if err != nil {
			return Outcome{}, err
		}
		if runtimeConfig.MaxGlobalConcurrency == 0 {
			runtimeConfig.MaxGlobalConcurrency = s.config.MaxGlobalConcurrency
		}
		if runtimeConfig.MaxGlobalConcurrency < 1 {
			return Outcome{}, ErrInvalidConfig
		}
		s.config.SlotPoolID = runtimeConfig.SlotPoolID
		s.config.SlotRequirement = runtimeConfig.SlotRequirement
		s.config.MaxGlobalConcurrency = runtimeConfig.MaxGlobalConcurrency
	}
	now := s.config.Clock()
	if now.IsZero() {
		return Outcome{}, ErrInvalidConfig
	}
	if err := s.recoverExpired(now); err != nil {
		s.record(now, "recovery", "failed", "", "store_error", 0)
		return Outcome{}, err
	}
	if err := s.expireQueued(now); err != nil {
		s.record(now, "deadline", "failed", "", "store_error", 0)
		return Outcome{}, err
	}
	if strings.TrimSpace(s.config.SlotPoolID) != "" {
		if err := s.recoverExpiredSlotLeases(now); err != nil {
			s.record(now, "slot-recovery", "failed", "", "store_error", 0)
			return Outcome{}, err
		}
	}
	claimID, leaseID := s.config.NewID("claim"), s.config.NewID("lease")
	var err error
	var claim store.Claim
	var slotClaim store.SlotClaim
	if strings.TrimSpace(s.config.SlotPoolID) != "" {
		aware, ok := s.store.(SlotAwareClaimer)
		if !ok {
			return Outcome{}, ErrRunnerUnavailable
		}
		slotClaim, err = aware.ClaimNextWithSlot(now, leaseID, s.config.Owner, s.config.LeaseTTL, claimID, s.config.Owner,
			"queue claim", store.QueueOptions{MaxGlobalConcurrency: s.config.MaxGlobalConcurrency}, s.config.SlotPoolID, s.config.SlotRequirement)
		if errors.Is(err, store.ErrQueueEmpty) || errors.Is(err, store.ErrQueueCapacity) || errors.Is(err, slot.ErrSlotUnavailable) {
			s.record(now, "claim", "idle", "", "slot_unavailable", 0)
			return Outcome{Idle: true}, nil
		}
		if err != nil {
			s.record(now, "claim", "failed", "", "store_error", 0)
			return Outcome{}, err
		}
		claim = slotClaim.Claim
	} else {
		claim, err = s.store.ClaimNext(now, leaseID, s.config.Owner,
			s.config.LeaseTTL, claimID, s.config.Owner,
			"queue claim", store.QueueOptions{MaxGlobalConcurrency: s.config.MaxGlobalConcurrency})
		if errors.Is(err, store.ErrQueueEmpty) || errors.Is(err, store.ErrQueueCapacity) {
			s.record(now, "claim", "idle", "", "", 0)
			return Outcome{Idle: true}, nil
		}
		if err != nil {
			s.record(now, "claim", "failed", "", "store_error", 0)
			return Outcome{}, err
		}
	}
	if errors.Is(err, store.ErrQueueEmpty) || errors.Is(err, store.ErrQueueCapacity) {
		s.record(now, "claim", "idle", "", "", 0)
		return Outcome{Idle: true}, nil
	}
	if err != nil {
		s.record(now, "claim", "failed", "", "store_error", 0)
		return Outcome{}, err
	}
	if workSlotID := slotClaim.SlotLease.SlotID; workSlotID != "" {
		s.config.Sink.Record(observability.Event{At: now, Component: "slot", Operation: "acquire", Outcome: "leased", RequestID: observability.RedactIdentifier(claim.Request.RequestID), Resource: observability.RedactIdentifier(workSlotID)})
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
		if slotClaim.SlotLease.LeaseID != "" {
			if release, ok := s.store.(interface {
				ReleaseSlotLease(string, string, string) error
			}); ok {
				_ = release.ReleaseSlotLease(slotClaim.SlotLease.SlotID, slotClaim.SlotLease.LeaseID, slotClaim.SlotLease.Owner)
			}
		}
		s.record(now, "start", "failed", claim.Request.RequestID, "transition_failed", 0)
		return Outcome{}, err
	}

	runnerContext := ctx
	stopRunnerContext := func() {}
	if s.config.RunTimeout > 0 {
		runnerContext, stopRunnerContext = context.WithTimeout(ctx, s.config.RunTimeout)
	}
	work := Work{Request: claim.Request, Lease: claim.Lease}
	if strings.TrimSpace(s.config.SlotPoolID) != "" {
		work.Slot, work.SlotLease = slotClaim.Slot, slotClaim.SlotLease
		work.SlotID, work.EnvironmentGeneration, work.AgentHandle = slotClaim.Slot.SlotID, slotClaim.Slot.EnvironmentGeneration, slotClaim.Slot.AgentHandle
	}
	runnerResult, runnerErr := s.runner.Run(runnerContext, work)
	runnerContextErr := runnerContext.Err()
	stopRunnerContext()
	finishedAt := s.config.Clock()
	if finishedAt.IsZero() {
		return Outcome{}, ErrInvalidConfig
	}
	duration := finishedAt.Sub(now)
	if duration < 0 {
		duration = 0
	}
	slotQuarantined, slotReleased := false, false
	slotCapabilityRevoked := false
	var currentRequest store.Request
	var currentRequestErr error
	if runnerErr != nil || runnerContextErr != nil {
		durableCancellation := false
		if runnerErr != nil {
			currentRequest, currentRequestErr = s.store.GetRequest(claim.Request.RequestID)
			if currentRequestErr == nil {
				durableCancellation = currentRequest.State == account.Cancelled
			}
		}
		quarantineSlot := runnerErr != nil || (runnerContextErr != nil && ctx.Err() == nil)
		if ctx.Err() != nil || durableCancellation {
			quarantineSlot = false
		}
		if work.SlotLease.LeaseID != "" && quarantineSlot {
			if quarantine, ok := s.store.(interface {
				QuarantineSlotLease(string, string, string, time.Time, string) error
			}); ok {
				revokeErr := s.revokeSlotLease(work.SlotLease.LeaseID)
				slotCapabilityRevoked = revokeErr == nil
				if revokeErr != nil {
					return Outcome{}, revokeErr
				}
				quarantineErr := quarantine.QuarantineSlotLease(work.SlotLease.SlotID, work.SlotLease.LeaseID, work.SlotLease.Owner, finishedAt, "agent failure")
				slotQuarantined = true
				if quarantineErr != nil {
					return Outcome{}, quarantineErr
				}
				s.config.Sink.Record(observability.Event{At: finishedAt, Component: "slot", Operation: "quarantine", Outcome: "quarantined", RequestID: observability.RedactIdentifier(claim.Request.RequestID), Resource: observability.RedactIdentifier(work.SlotLease.SlotID), ErrorClass: "agent_failure"})
			} else if release, ok := s.store.(interface {
				ReleaseSlotLease(string, string, string) error
			}); ok {
				revokeErr := s.revokeSlotLease(work.SlotLease.LeaseID)
				slotCapabilityRevoked = revokeErr == nil
				if revokeErr != nil {
					return Outcome{}, revokeErr
				}
				releaseErr := release.ReleaseSlotLease(work.SlotLease.SlotID, work.SlotLease.LeaseID, work.SlotLease.Owner)
				if releaseErr != nil {
					return Outcome{}, releaseErr
				}
				slotReleased = true
			}
		}
		if work.SlotLease.LeaseID != "" && !slotQuarantined && !slotReleased {
			if release, ok := s.store.(interface {
				ReleaseSlotLease(string, string, string) error
			}); ok {
				revokeErr := s.revokeSlotLease(work.SlotLease.LeaseID)
				slotCapabilityRevoked = revokeErr == nil
				if revokeErr != nil {
					return Outcome{}, revokeErr
				}
				releaseErr := release.ReleaseSlotLease(work.SlotLease.SlotID, work.SlotLease.LeaseID, work.SlotLease.Owner)
				if releaseErr != nil {
					return Outcome{}, releaseErr
				}
				slotReleased = true
				s.config.Sink.Record(observability.Event{At: finishedAt, Component: "slot", Operation: "release", Outcome: "released", RequestID: observability.RedactIdentifier(claim.Request.RequestID), Resource: observability.RedactIdentifier(work.SlotLease.SlotID)})
			}
		}
		// A request cancellation is durable and may race with the worker's
		// terminal event. Do not turn that expected lifecycle into LOGIN_FAILED
		// or a retry after the cancellation transaction released its lease.
		if currentRequest.RequestID == "" && currentRequestErr == nil {
			currentRequest, currentRequestErr = s.store.GetRequest(claim.Request.RequestID)
		}
		if currentRequestErr != nil {
			_ = s.revokeRequest(claim.Request.RequestID)
			s.record(finishedAt, "run", "failed", claim.Request.RequestID, "store_error", duration)
			return Outcome{}, currentRequestErr
		}
		if currentRequest.State == account.Cancelled {
			if err := s.revokeRequest(claim.Request.RequestID); err != nil {
				return Outcome{}, err
			}
			s.record(finishedAt, "run", "cancelled", claim.Request.RequestID, "", duration)
			return Outcome{Request: currentRequest}, nil
		}
		runnerResult = Result{Failure: account.TransientFailure}
	}
	if work.SlotLease.LeaseID != "" && !slotQuarantined && !slotReleased {
		if release, ok := s.store.(interface {
			ReleaseSlotLease(string, string, string) error
		}); ok {
			revokeErr := s.revokeSlotLease(work.SlotLease.LeaseID)
			slotCapabilityRevoked = revokeErr == nil
			if revokeErr != nil {
				return Outcome{}, revokeErr
			}
			releaseErr := release.ReleaseSlotLease(work.SlotLease.SlotID, work.SlotLease.LeaseID, work.SlotLease.Owner)
			if releaseErr != nil {
				return Outcome{}, releaseErr
			}
			s.config.Sink.Record(observability.Event{At: finishedAt, Component: "slot", Operation: "release", Outcome: "released", RequestID: observability.RedactIdentifier(claim.Request.RequestID), Resource: observability.RedactIdentifier(work.SlotLease.SlotID)})
		}
	}
	if work.SlotLease.LeaseID != "" && !slotCapabilityRevoked {
		if err := s.revokeSlotLease(work.SlotLease.LeaseID); err != nil {
			return Outcome{}, err
		}
	}
	// Capabilities are scoped to one running attempt. Revoke them before the
	// request is retried or reaches a terminal business transition.
	if err := s.revokeRequest(claim.Request.RequestID); err != nil {
		return Outcome{}, err
	}
	if runnerResult.Succeeded {
		outcome, err := s.finishSuccess(finishedAt, claim.Request, claim.Lease, Outcome{Request: claim.Request})
		if err != nil {
			if cancelled, ok := s.resolveCancellationRace(claim.Request.RequestID, err); ok {
				s.record(finishedAt, "run", "cancelled", claim.Request.RequestID, "", duration)
				return cancelled, nil
			}
			s.record(finishedAt, "run", "failed", claim.Request.RequestID, "transition_failed", duration)
		} else {
			s.record(finishedAt, "run", "succeeded", claim.Request.RequestID, "", duration)
		}
		return outcome, err
	}
	if !validFailure(runnerResult.Failure) {
		runnerResult.Failure = account.UnknownFailure
	}
	outcome, err := s.finishFailure(finishedAt, claim.Request, claim.Lease, false, runnerResult.Failure)
	if err != nil {
		if cancelled, ok := s.resolveCancellationRace(claim.Request.RequestID, err); ok {
			s.record(finishedAt, "run", "cancelled", claim.Request.RequestID, "", duration)
			return cancelled, nil
		}
		s.record(finishedAt, "run", "failed", claim.Request.RequestID, "transition_failed", duration)
	} else if outcome.Retried {
		s.record(finishedAt, "run", "retried", claim.Request.RequestID, string(runnerResult.Failure), duration)
	} else {
		s.record(finishedAt, "run", "failed", claim.Request.RequestID, string(runnerResult.Failure), duration)
	}
	return outcome, err
}

func (s *Scheduler) recoverExpiredSlotLeases(now time.Time) error {
	if s == nil {
		return ErrInvalidConfig
	}
	if lister, ok := s.store.(SlotLeaseLister); ok {
		leases, err := lister.ListSlotLeases()
		if err != nil {
			return err
		}
		for _, record := range leases {
			if record.Lease.Expired(now) {
				if err := s.fenceExpiredSlotLease(record.Lease); err != nil {
					return err
				}
				if err := s.revokeSlotLease(record.Lease.LeaseID); err != nil {
					return err
				}
			}
		}
	}
	if recovery, ok := s.store.(SlotLeaseRecovery); ok {
		return recovery.RecoverExpiredSlotLeases(now)
	}
	return nil
}

// fenceExpiredSlotLease establishes the platform stop-before-release fence.
// The durable AgentHandle is authoritative after restart; an absent stopper
// must never be treated as proof that a persisted Windows agent is gone.
func (s *Scheduler) fenceExpiredSlotLease(lease slot.Lease) error {
	if s == nil {
		return ErrInvalidConfig
	}
	requiresFence := true
	if reader, ok := s.store.(SlotLeaseMetadataReader); ok {
		value, err := reader.GetSlot(lease.SlotID)
		if err != nil {
			return err
		}
		requiresFence = value.AgentHandle != ""
	}
	if s.config.SlotLeaseStopper != nil {
		if err := s.config.SlotLeaseStopper.StopSlotLease(context.Background(), lease); err != nil {
			return err
		}
	} else if requiresFence {
		if s.config.SlotLeaseStopper == nil {
			return store.ErrSlotLeaseFenceRequired
		}
	}
	if !requiresFence {
		return nil
	}
	confirmer, ok := s.store.(SlotLeaseFenceConfirmer)
	if !ok {
		return store.ErrSlotLeaseFenceRequired
	}
	return confirmer.ConfirmSlotLeaseStopped(lease)
}

// resolveCancellationRace treats a stale terminal event as an expected
// cancellation outcome when the durable request has already moved to
// CANCELLED. The cancellation transaction owns the business result; the
// runner must not surface its losing terminal fact as an integration error.
func (s *Scheduler) resolveCancellationRace(requestID string, transitionErr error) (Outcome, bool) {
	if s == nil || !errors.Is(transitionErr, account.ErrStaleEvent) {
		return Outcome{}, false
	}
	current, err := s.store.GetRequest(requestID)
	if err != nil || current.State != account.Cancelled {
		return Outcome{}, false
	}
	return Outcome{Request: current}, true
}

func (s *Scheduler) record(at time.Time, operation, outcome, requestID, errorClass string, duration time.Duration) {
	if s == nil || s.config.Sink == nil {
		return
	}
	s.config.Sink.Record(observability.Event{At: at, Component: "queue", Operation: operation, Outcome: outcome, RequestID: observability.RedactIdentifier(requestID), ErrorClass: errorClass, Duration: duration})
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
		if err := s.revokeRequest(request.RequestID); err != nil {
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
		if err := s.revokeAccountLease(record.Lease.LeaseID); err != nil {
			return err
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
			if err := s.revokeRequest(request.RequestID); err != nil {
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
		if err := s.recoverOrphanedSlotLease(now, request, record.Lease.Owner); err != nil {
			return err
		}
	}
	return nil
}

func (s *Scheduler) recoverOrphanedSlotLease(now time.Time, request store.Request, owner string) error {
	lister, ok := s.store.(SlotLeaseLister)
	if !ok || strings.TrimSpace(s.config.SlotPoolID) == "" {
		return nil
	}
	leases, err := lister.ListSlotLeases()
	if err != nil {
		return err
	}
	for _, record := range leases {
		if record.Lease.RequestID != request.RequestID || record.Lease.AccountID != request.AccountID || record.Lease.Owner != owner {
			continue
		}
		if quarantine, ok := s.store.(interface {
			QuarantineSlotLease(string, string, string, time.Time, string) error
		}); ok {
			if err := s.fenceExpiredSlotLease(record.Lease); err != nil {
				return err
			}
			if err := s.revokeSlotLease(record.Lease.LeaseID); err != nil {
				return err
			}
			if err := quarantine.QuarantineSlotLease(record.SlotID, record.Lease.LeaseID, record.Lease.Owner, now, "account lease expired"); err != nil && !errors.Is(err, slot.ErrLeaseNotOwned) && !errors.Is(err, slot.ErrSlotNotFound) {
				return err
			}
			continue
		}
		if release, ok := s.store.(interface {
			ReleaseSlotLease(string, string, string) error
		}); ok {
			if err := s.fenceExpiredSlotLease(record.Lease); err != nil {
				return err
			}
			if err := s.revokeSlotLease(record.Lease.LeaseID); err != nil {
				return err
			}
			if err := release.ReleaseSlotLease(record.SlotID, record.Lease.LeaseID, record.Lease.Owner); err != nil && !errors.Is(err, slot.ErrLeaseNotOwned) && !errors.Is(err, slot.ErrSlotNotFound) {
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
