package browser

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/internal/observability"
	"github.com/Semcosm/chuzi/internal/queue"
)

var (
	ErrInvalidConfig    = errors.New("browser: invalid runner configuration")
	ErrInvalidWork      = errors.New("browser: invalid session work")
	ErrWorkerCrashed    = errors.New("browser: worker crashed")
	ErrWorkerProtocol   = errors.New("browser: worker protocol failure")
	ErrSessionCancelled = errors.New("browser: session cancelled")
)

// Clock is injected to make lease and timeout decisions deterministic.
type Clock func() time.Time

// LeaseKeeper is the subset of durable storage needed for session heartbeats.
// *store.Store satisfies it; a fake implementation is enough for lifecycle
// tests.
type LeaseKeeper interface {
	HeartbeatLease(accountID string, now time.Time, leaseID, owner string, ttl time.Duration) (account.Lease, error)
}

// RequestCancellationObserver is the narrow durable fact needed by a running
// session to stop after a request was cancelled.
type RequestCancellationObserver interface {
	IsRequestCancelled(requestID string) (bool, error)
}

// WorkerSpec contains only service-derived identifiers and the generated
// profile path. It deliberately has no credential or arbitrary user path.
type WorkerSpec struct {
	SessionID  string
	AccountID  string
	RequestID  string
	ProfileDir string
	LeaseID    string
	Owner      string
	Mode       string
}

func (s WorkerSpec) validate() error {
	if strings.TrimSpace(s.SessionID) == "" || strings.TrimSpace(s.AccountID) == "" ||
		strings.TrimSpace(s.RequestID) == "" || strings.TrimSpace(s.ProfileDir) == "" ||
		strings.TrimSpace(s.LeaseID) == "" || strings.TrimSpace(s.Owner) == "" {
		return ErrInvalidWork
	}
	return nil
}

// WorkerResult is the redacted runtime fact reported by a worker.
type WorkerResult struct {
	Succeeded bool
	Failure   account.FailureClass
	// Handle is an ephemeral runtime handle for an adapter that shares the
	// worker's browser process. It is never persisted or exposed to callers.
	Handle string
}

func (r WorkerResult) validate() error {
	if r.Succeeded {
		if r.Failure != "" {
			return ErrWorkerProtocol
		}
		return nil
	}
	switch r.Failure {
	case account.TransientFailure, account.CredentialFailure,
		account.PermissionFailure, account.ConfigurationFailure,
		account.UnknownFailure:
		return nil
	default:
		return ErrWorkerProtocol
	}
}

// Worker owns one process/session lifecycle. Run blocks until a terminal
// worker event or an error. Cancel is idempotent and must be safe while Run is
// blocked. Close releases process resources even after a crash or timeout.
type Worker interface {
	Run(context.Context) (WorkerResult, error)
	Cancel(context.Context) error
	Close(context.Context) error
}

// WorkerFactory starts one isolated worker for one queue claim.
type WorkerFactory interface {
	Start(context.Context, WorkerSpec) (Worker, error)
}

// Config controls the session runner. Heartbeats are disabled only when
// LeaseKeeper is nil; production wiring should always provide durable storage.
type Config struct {
	LeaseTTL             time.Duration
	HeartbeatInterval    time.Duration
	CancelTimeout        time.Duration
	ShutdownTimeout      time.Duration
	WorkerMode           string
	CancellationObserver RequestCancellationObserver
	Clock                Clock
	Sink                 observability.Sink
}

// Runner adapts a WorkerFactory to queue.Runner and keeps worker facts below
// the account state-machine boundary.
type Runner struct {
	factory       WorkerFactory
	leases        LeaseKeeper
	cancellations RequestCancellationObserver
	profiles      *Profiles
	config        Config
}

func New(factory WorkerFactory, leases LeaseKeeper, profiles *Profiles, config Config) (*Runner, error) {
	if factory == nil || profiles == nil || config.LeaseTTL <= 0 ||
		config.CancelTimeout <= 0 || config.ShutdownTimeout <= 0 || config.Clock == nil {
		return nil, ErrInvalidConfig
	}
	if config.HeartbeatInterval < 0 || (config.HeartbeatInterval > 0 && config.HeartbeatInterval >= config.LeaseTTL) {
		return nil, ErrInvalidConfig
	}
	if config.HeartbeatInterval == 0 && leases != nil {
		config.HeartbeatInterval = config.LeaseTTL / 3
		if config.HeartbeatInterval <= 0 {
			return nil, ErrInvalidConfig
		}
	}
	if config.Sink == nil {
		config.Sink = observability.NopSink{}
	}
	return &Runner{factory: factory, leases: leases, cancellations: config.CancellationObserver, profiles: profiles, config: config}, nil
}

// Run implements queue.Runner. The queue remains responsible for durable
// STARTING/LOGGING_IN and terminal business transitions; this method returns
// only runtime facts and lifecycle errors.
func (r *Runner) Run(ctx context.Context, work queue.Work) (queue.Result, error) {
	if r == nil || ctx == nil {
		return queue.Result{}, ErrInvalidConfig
	}
	if err := ctx.Err(); err != nil {
		return queue.Result{Failure: account.TransientFailure}, err
	}
	if err := work.Request.Validate(); err != nil {
		return queue.Result{}, fmt.Errorf("%w: request: %v", ErrInvalidWork, err)
	}
	if err := work.Lease.Validate(); err != nil {
		return queue.Result{}, fmt.Errorf("%w: lease: %v", ErrInvalidWork, err)
	}
	now := r.config.Clock()
	if now.IsZero() {
		return queue.Result{}, ErrInvalidConfig
	}
	if work.Lease.Expired(now) {
		r.record(now, "start", "failed", work.Request.RequestID, "lease_expired", 0)
		return queue.Result{Failure: account.TransientFailure}, account.ErrLeaseExpired
	}
	startedAt := now
	r.record(startedAt, "start", "started", work.Request.RequestID, "", 0)
	profileDir, releaseProfile, err := r.profiles.Acquire(work.Request.AccountID)
	if err != nil {
		return queue.Result{Failure: account.ConfigurationFailure}, err
	}
	defer releaseProfile()
	spec := WorkerSpec{
		SessionID:  work.Request.RequestID,
		AccountID:  work.Request.AccountID,
		RequestID:  work.Request.RequestID,
		ProfileDir: profileDir,
		LeaseID:    work.Lease.LeaseID,
		Owner:      work.Lease.Owner,
		Mode:       r.config.WorkerMode,
	}
	worker, err := r.factory.Start(ctx, spec)
	if err != nil {
		r.record(r.config.Clock(), "start", "failed", work.Request.RequestID, "worker_start_failed", elapsed(startedAt, r.config.Clock()))
		return queue.Result{Failure: account.TransientFailure}, err
	}
	if worker == nil {
		r.record(r.config.Clock(), "start", "failed", work.Request.RequestID, "worker_unavailable", elapsed(startedAt, r.config.Clock()))
		return queue.Result{Failure: account.TransientFailure}, ErrWorkerCrashed
	}

	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), r.config.ShutdownTimeout)
		defer closeCancel()
		_ = worker.Close(closeCtx)
	}()

	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	heartbeatErr := make(chan error, 1)
	leaseState := &leaseState{value: work.Lease}
	if r.leases != nil {
		go r.heartbeat(runCtx, work, leaseState, heartbeatErr)
	}

	done := make(chan workerRun, 1)
	go func() {
		result, runErr := worker.Run(runCtx)
		done <- workerRun{result: result, err: runErr}
	}()

	var cancelOnce sync.Once
	cancelWorker := func() error {
		result := make(chan error, 1)
		cancelOnce.Do(func() {
			go func() {
				cancelCtx, cancel := context.WithTimeout(context.Background(), r.config.CancelTimeout)
				defer cancel()
				result <- worker.Cancel(cancelCtx)
			}()
		})
		select {
		case cancelErr := <-result:
			return cancelErr
		case <-time.After(r.config.CancelTimeout):
			return context.DeadlineExceeded
		}
	}

	select {
	case outcome := <-done:
		finishedAt := r.config.Clock()
		if finishedAt.IsZero() {
			return queue.Result{}, ErrInvalidConfig
		}
		if hbErr := readHeartbeatError(heartbeatErr); hbErr != nil {
			r.record(finishedAt, "run", "failed", work.Request.RequestID, "heartbeat_failed", elapsed(startedAt, finishedAt))
			return queue.Result{Failure: account.TransientFailure}, hbErr
		}
		if outcome.err != nil {
			r.record(finishedAt, "run", "failed", work.Request.RequestID, "worker_failed", elapsed(startedAt, finishedAt))
			return queue.Result{Failure: account.TransientFailure}, outcome.err
		}
		if err := outcome.result.validate(); err != nil {
			r.record(finishedAt, "run", "failed", work.Request.RequestID, "worker_protocol_failed", elapsed(startedAt, finishedAt))
			return queue.Result{Failure: account.UnknownFailure}, err
		}
		leaseState.mu.RLock()
		latestLease := leaseState.value
		leaseState.mu.RUnlock()
		if latestLease.Expired(finishedAt) {
			r.record(finishedAt, "run", "failed", work.Request.RequestID, "lease_expired", elapsed(startedAt, finishedAt))
			return queue.Result{Failure: account.TransientFailure}, account.ErrLeaseExpired
		}
		outcomeName := "failed"
		if outcome.result.Succeeded {
			outcomeName = "succeeded"
		}
		r.record(finishedAt, "run", outcomeName, work.Request.RequestID, string(outcome.result.Failure), elapsed(startedAt, finishedAt))
		return queue.Result{Succeeded: outcome.result.Succeeded, Failure: outcome.result.Failure}, nil
	case <-ctx.Done():
		cancelErr := cancelWorker()
		stop()
		select {
		case <-done:
		case <-time.After(r.config.CancelTimeout):
			if cancelErr == nil {
				cancelErr = context.DeadlineExceeded
			}
		}
		if cancelErr != nil {
			r.record(r.config.Clock(), "run", "failed", work.Request.RequestID, "cancel_failed", elapsed(startedAt, r.config.Clock()))
			return queue.Result{Failure: account.TransientFailure}, fmt.Errorf("cancel worker: %w", cancelErr)
		}
		r.record(r.config.Clock(), "run", "cancelled", work.Request.RequestID, "cancelled", elapsed(startedAt, r.config.Clock()))
		return queue.Result{Failure: account.TransientFailure}, ctx.Err()
	case hbErr := <-heartbeatErr:
		cancelErr := cancelWorker()
		stop()
		select {
		case <-done:
		case <-time.After(r.config.CancelTimeout):
		}
		if cancelErr != nil {
			r.record(r.config.Clock(), "run", "failed", work.Request.RequestID, "heartbeat_failed", elapsed(startedAt, r.config.Clock()))
			return queue.Result{Failure: account.TransientFailure}, fmt.Errorf("heartbeat failed: %v; cancel worker: %w", hbErr, cancelErr)
		}
		r.record(r.config.Clock(), "run", "failed", work.Request.RequestID, "heartbeat_failed", elapsed(startedAt, r.config.Clock()))
		return queue.Result{Failure: account.TransientFailure}, hbErr
	}
}

func elapsed(start, end time.Time) time.Duration {
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return 0
	}
	return end.Sub(start)
}

func (r *Runner) record(at time.Time, operation, outcome, requestID, errorClass string, duration time.Duration) {
	if r == nil || r.config.Sink == nil {
		return
	}
	r.config.Sink.Record(observability.Event{At: at, Component: "worker", Operation: operation, Outcome: outcome, RequestID: observability.RedactIdentifier(requestID), ErrorClass: errorClass, Duration: duration})
}

type workerRun struct {
	result WorkerResult
	err    error
}

type leaseState struct {
	mu    sync.RWMutex
	value account.Lease
}

func (r *Runner) heartbeat(ctx context.Context, work queue.Work, state *leaseState, failures chan<- error) {
	ticker := time.NewTicker(r.config.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := r.config.Clock()
			if now.IsZero() {
				sendHeartbeatError(failures, ErrInvalidConfig)
				return
			}
			if r.cancellations != nil {
				cancelled, err := r.cancellations.IsRequestCancelled(work.Request.RequestID)
				if err != nil {
					sendHeartbeatError(failures, err)
					return
				}
				if cancelled {
					sendHeartbeatError(failures, ErrSessionCancelled)
					return
				}
			}
			updated, err := r.leases.HeartbeatLease(work.Request.AccountID, now, work.Lease.LeaseID, work.Lease.Owner, r.config.LeaseTTL)
			if err != nil {
				sendHeartbeatError(failures, err)
				return
			}
			state.mu.Lock()
			state.value = updated
			state.mu.Unlock()
		}
	}
}

func sendHeartbeatError(failures chan<- error, err error) {
	select {
	case failures <- err:
	default:
	}
}

func readHeartbeatError(failures <-chan error) error {
	select {
	case err := <-failures:
		return err
	default:
		return nil
	}
}
