package sessionruntime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// ErrJournalNotFound is the only non-fatal OwnershipJournal.Load result.
// Implementations must use it for an absent slot record; every other load
// error is treated as unknown ownership and fails closed.
var ErrJournalNotFound = errors.New("sessionruntime: ownership record not found")

const defaultProfilePolicy = "managed-slot-v1"

// RuntimeDependencies are the trusted providers for one managed slot. None
// of these interfaces accepts a Core/UI path, endpoint, command, or password.
type RuntimeDependencies struct {
	Host     HostPreflight
	Profiles ProfileProvider
	Accounts AccountProvider
	Login    LoginAdapter
	Sessions SessionProvider
	Desktop  DesktopProvider
	Agent    AgentProvider
	Worker   WorkerProvider
	Journal  OwnershipJournal
}

// RuntimeOptions contains deployment policy, not request data. Now is
// injectable so ownership and readiness tests remain deterministic.
type RuntimeOptions struct {
	Now            func() time.Time
	Freshness      time.Duration
	CleanupTimeout time.Duration
	ProfilePolicy  string
}

// StartOptions selects an explicitly requested account recovery. Recovery
// rotates a disposable local password through AccountProvider; it never reads
// a password from ordinary state. Revision is the caller's durable fence.
type StartOptions struct {
	Recover  bool
	Revision uint64
}

// RunResult is an internal service result. Status contains only the contract's
// redaction-safe runtime facts. Cleanup is populated when a failed run needed
// fencing or resource release.
type RunResult struct {
	Status  Status
	Cleanup CleanupReport
}

type CleanupReport struct {
	Attempted bool
	Complete  bool
	Failure   Code
}

// Runtime serializes operations per slot and coordinates the provider graph.
// The journal remains the durable cross-process fence; this mutex only prevents
// two calls in the same process from racing through side effects.
type Runtime struct {
	deps     RuntimeDependencies
	opts     RuntimeOptions
	mu       sync.Mutex
	busy     map[string]struct{}
	desktops map[string]Desktop
}

func NewRuntime(deps RuntimeDependencies, opts RuntimeOptions) (*Runtime, error) {
	if deps.Host == nil || deps.Profiles == nil || deps.Accounts == nil || deps.Login == nil || deps.Sessions == nil || deps.Desktop == nil || deps.Agent == nil || deps.Worker == nil || deps.Journal == nil {
		return nil, ErrInvalidArgument
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	if opts.Freshness <= 0 {
		opts.Freshness = 30 * time.Second
	}
	if opts.CleanupTimeout <= 0 {
		opts.CleanupTimeout = 10 * time.Second
	}
	if strings.TrimSpace(opts.ProfilePolicy) == "" {
		opts.ProfilePolicy = defaultProfilePolicy
	}
	return &Runtime{deps: deps, opts: opts, busy: make(map[string]struct{}), desktops: make(map[string]Desktop)}, nil
}

// Start performs a complete environment -> profile -> login -> WTS -> desktop
// -> Agent -> worker sequence. A LoginAdapter result is only a candidate: the
// session provider is queried again and the returned SID and session ID must
// match before any later phase runs.
func (r *Runtime) Start(ctx context.Context, identity Identity, options StartOptions) (RunResult, error) {
	var result RunResult
	if ctx == nil {
		return result, classified(CodeInvalidArgument, "nil context", ErrInvalidArgument)
	}
	if err := identity.Validate(); err != nil {
		return result, classified(CodeInvalidArgument, "invalid identity", err)
	}
	release, err := r.acquire(identity.SlotID)
	if err != nil {
		return result, err
	}
	defer release()
	if err := contextFailure(ctx); err != nil {
		return result, err
	}

	facts, err := r.deps.Host.Check(ctx)
	if err != nil {
		return result, callFailure(ctx, CodeSessionUnavailable, "host preflight unavailable", err)
	}
	now := r.now()
	if err := validateHostFacts(facts, identity.Ordinal, now, r.opts.Freshness); err != nil {
		return result, err
	}
	if err := contextFailure(ctx); err != nil {
		return result, err
	}
	result.Status = Status{Phase: PhaseEnvironment, Generation: identity.Generation, UpdatedAt: now}
	result.Status.Checks = append(result.Status.Checks, r.check(LayerHost, ReadinessReady, identity, facts.ObservedAt))

	record, existing, err := r.prepareRecord(ctx, identity, options.Revision)
	if err != nil {
		return result, err
	}
	if existing {
		return result, classified(CodeAlreadyStarted, "slot has an owned runtime", ErrAlreadyStarted)
	}
	if err := r.deps.Journal.Begin(ctx, record); err != nil {
		return result, callFailure(ctx, CodeOwnershipUnknown, "ownership intent unavailable", err)
	}

	profileReady := false
	accountReady := false
	var lease SecretLease
	var candidate Session
	var verified Session
	var desktop Desktop
	agentStarted := false
	workerStarted := false
	cleanup := func(primary error) (RunResult, error) {
		result.Cleanup = r.cleanup(ctx, identity, record, profileReady, accountReady, candidate, desktop, agentStarted, workerStarted, true)
		if result.Cleanup.Complete {
			return result, primary
		}
		return result, classified(CodeCleanup, "runtime cleanup failed", primary)
	}

	result.Status.Phase = PhaseProfile
	if err := r.deps.Profiles.Ensure(ctx, identity, ProfileSpec{Generation: identity.Generation, PolicyID: r.opts.ProfilePolicy}); err != nil {
		return cleanup(callFailure(ctx, CodeSessionUnavailable, "profile unavailable", err))
	}
	profileReady = true
	result.Status.Checks = append(result.Status.Checks, r.check(LayerProfile, ReadinessReady, identity, r.now()))
	if err := contextFailure(ctx); err != nil {
		return cleanup(err)
	}

	result.Status.Phase = PhaseLogin
	if options.Recover {
		lease, err = r.deps.Accounts.Recover(ctx, identity)
	} else {
		lease, err = r.deps.Accounts.Create(ctx, identity)
	}
	if err != nil {
		return cleanup(callFailure(ctx, CodeSessionUnavailable, "managed account unavailable", err))
	}
	accountReady = true
	defer lease.Wipe()
	if err := lease.WithPassword(func(password []uint16) error {
		if len(password) == 0 || lease.Username() == "" {
			return ErrSecretInvalid
		}
		return nil
	}); err != nil {
		return cleanup(classified(CodeInvalidArgument, "invalid managed secret", err))
	}
	candidate, err = r.deps.Login.Login(ctx, identity, lease)
	lease.Wipe()
	if err != nil {
		return cleanup(callFailure(ctx, CodeSessionUnavailable, "login unavailable", err))
	}
	if candidate.ID == 0 {
		return cleanup(classified(CodeSessionUnavailable, "login returned no session", ErrSessionUnavailable))
	}
	if candidate.State != "active" || candidate.SID != identity.SID {
		return cleanup(classified(CodeSessionChanged, "login session identity changed", ErrSessionChanged))
	}
	if err := contextFailure(ctx); err != nil {
		return cleanup(err)
	}

	result.Status.Phase = PhaseWTS
	verified, err = r.verifyCandidate(ctx, identity, candidate)
	if err != nil {
		return cleanup(err)
	}
	record.SessionID = verified.ID
	record.Phase = JournalCommitted
	record.UpdatedAt = r.now()
	if err := r.deps.Journal.Commit(ctx, record); err != nil {
		return cleanup(callFailure(ctx, CodeOwnershipUnknown, "session ownership commit unavailable", err))
	}
	result.Status.Session = verified
	wtsCheck := r.check(LayerWTS, ReadinessReady, identity, verified.ObservedAt)
	wtsCheck.SessionID = verified.ID
	result.Status.Checks = append(result.Status.Checks, wtsCheck)

	result.Status.Phase = PhaseDesktop
	desktop, err = r.deps.Desktop.Ensure(ctx, identity, verified)
	if err != nil {
		return cleanup(callFailure(ctx, CodeSessionUnavailable, "desktop unavailable", err))
	}
	if err := contextFailure(ctx); err != nil {
		return cleanup(err)
	}
	if desktop.SessionID != verified.ID || desktop.Generation != identity.Generation || desktop.ReadyAt.IsZero() {
		return cleanup(classified(CodeSessionChanged, "desktop identity changed", ErrSessionChanged))
	}
	r.mu.Lock()
	r.desktops[identity.SlotID] = desktop
	r.mu.Unlock()
	record.Phase = JournalCheckpoint
	record.UpdatedAt = r.now()
	if err := r.deps.Journal.Checkpoint(ctx, record); err != nil {
		return cleanup(callFailure(ctx, CodeOwnershipUnknown, "desktop checkpoint unavailable", err))
	}
	result.Status.Checks = append(result.Status.Checks, r.check(LayerShell, ReadinessReady, identity, desktop.ReadyAt))

	result.Status.Phase = PhaseAgent
	if err := r.deps.Agent.Start(ctx, identity, verified, desktop); err != nil {
		return cleanup(callFailure(ctx, CodeSessionUnavailable, "agent unavailable", err))
	}
	agentStarted = true
	if err := contextFailure(ctx); err != nil {
		return cleanup(err)
	}
	agent, err := r.deps.Agent.Health(ctx, identity)
	if err != nil || !agent.Ready || agent.Generation != identity.Generation {
		if err == nil {
			err = ErrSessionUnavailable
		}
		return cleanup(classified(CodeStaleReadiness, "agent readiness is stale", err))
	}
	result.Status.AgentReady = true
	result.Status.Checks = append(result.Status.Checks, r.check(LayerAgent, ReadinessReady, identity, agent.ObservedAt))
	if err := contextFailure(ctx); err != nil {
		return cleanup(err)
	}

	result.Status.Phase = PhaseWorker
	if err := r.deps.Worker.Start(ctx, identity); err != nil {
		return cleanup(callFailure(ctx, CodeSessionUnavailable, "worker unavailable", err))
	}
	workerStarted = true
	if err := contextFailure(ctx); err != nil {
		return cleanup(err)
	}
	worker, err := r.deps.Worker.Health(ctx, identity)
	if err != nil || !worker.Ready || strings.TrimSpace(worker.Capability) == "" {
		if err == nil {
			err = ErrSessionUnavailable
		}
		return cleanup(classified(CodeStaleReadiness, "worker readiness is stale", err))
	}
	result.Status.WorkerReady = true
	result.Status.Checks = append(result.Status.Checks, r.check(LayerWorker, ReadinessReady, identity, worker.ObservedAt))

	if err := contextFailure(ctx); err != nil {
		return cleanup(err)
	}
	record.Phase = JournalCheckpoint
	record.UpdatedAt = r.now()
	if err := r.deps.Journal.Checkpoint(ctx, record); err != nil {
		return cleanup(callFailure(ctx, CodeOwnershipUnknown, "runtime checkpoint unavailable", err))
	}
	result.Status.Phase = PhaseWorker
	result.Status.UpdatedAt = r.now()
	return result, nil
}

// Stop fences the owned Agent, worker, desktop, login, and WTS session. A
// session is marked stopped only after an independent Find confirms that the
// exact SID no longer has an active session.
func (r *Runtime) Stop(ctx context.Context, identity Identity) (RunResult, error) {
	var result RunResult
	if ctx == nil {
		return result, classified(CodeInvalidArgument, "nil context", ErrInvalidArgument)
	}
	if err := identity.Validate(); err != nil {
		return result, classified(CodeInvalidArgument, "invalid identity", err)
	}
	if err := contextFailure(ctx); err != nil {
		return result, err
	}
	release, err := r.acquire(identity.SlotID)
	if err != nil {
		return result, err
	}
	defer release()
	record, err := r.deps.Journal.Load(ctx, identity.SlotID)
	if err != nil {
		if errors.Is(err, ErrJournalNotFound) {
			return result, classified(CodeOwnershipUnknown, "slot ownership is unknown", err)
		}
		return result, callFailure(ctx, CodeOwnershipUnknown, "ownership load unavailable", err)
	}
	if err := record.Validate(); err != nil {
		return result, classified(CodeOwnershipUnknown, "invalid ownership record", err)
	}
	if record.PoolID != identity.PoolID || record.SlotID != identity.SlotID || record.Ordinal != identity.Ordinal || record.Generation != identity.Generation || record.SID != identity.SID || record.Owner != identity.Owner {
		return result, classified(CodeOwnershipMismatch, "ownership does not match slot", ErrOwnershipMismatch)
	}
	if record.Phase == JournalStopped {
		result.Status = Status{Phase: PhaseStopped, Generation: identity.Generation, UpdatedAt: r.now()}
		return result, nil
	}
	if record.Phase == JournalUnknown {
		return result, classified(CodeOwnershipUnknown, "slot ownership requires repair", ErrOwnershipUnknown)
	}
	current, findErr := r.deps.Sessions.Find(ctx, identity.SID)
	if findErr == nil && current.ID != 0 && current.ID != record.SessionID {
		return result, classified(CodeSessionChanged, "session identity changed", ErrSessionChanged)
	}
	if findErr != nil && !sessionGone(current) {
		return result, callFailure(ctx, CodeSessionUnavailable, "session state unavailable", findErr)
	}
	desktop := r.desktop(identity.SlotID, record)
	cleanup := r.cleanup(ctx, identity, record, false, false, current, desktop, true, true, true)
	result.Cleanup = cleanup
	if !cleanup.Complete {
		return result, classified(CodeCleanup, "runtime cleanup failed", ErrCleanup)
	}
	result.Status = Status{Phase: PhaseStopped, Generation: identity.Generation, UpdatedAt: r.now()}
	return result, nil
}

func (r *Runtime) prepareRecord(ctx context.Context, identity Identity, requested uint64) (OwnershipRecord, bool, error) {
	old, err := r.deps.Journal.Load(ctx, identity.SlotID)
	if err == nil {
		if err := old.Validate(); err != nil {
			return OwnershipRecord{}, false, classified(CodeOwnershipUnknown, "invalid ownership record", err)
		}
		if old.Generation > identity.Generation {
			return OwnershipRecord{}, false, classified(CodeStaleGeneration, "slot generation is stale", ErrStaleGeneration)
		}
		if old.Phase == JournalUnknown {
			return OwnershipRecord{}, false, classified(CodeOwnershipUnknown, "slot ownership requires repair", ErrOwnershipUnknown)
		}
		if old.Phase != JournalStopped {
			return OwnershipRecord{}, true, nil
		}
		if old.PoolID != identity.PoolID || old.Ordinal != identity.Ordinal || old.SID != identity.SID || old.Owner != identity.Owner {
			return OwnershipRecord{}, false, classified(CodeOwnershipMismatch, "ownership does not match slot", ErrOwnershipMismatch)
		}
		if requested <= old.Revision {
			requested = old.Revision + 1
		}
	} else if !errors.Is(err, ErrJournalNotFound) {
		return OwnershipRecord{}, false, callFailure(ctx, CodeOwnershipUnknown, "ownership load unavailable", err)
	}
	if requested == 0 {
		requested = 1
	}
	record := OwnershipRecord{Version: OwnershipJournalVersion, PoolID: identity.PoolID, SlotID: identity.SlotID, Ordinal: identity.Ordinal, SID: identity.SID, Generation: identity.Generation, Owner: identity.Owner, Phase: JournalIntent, Revision: requested, UpdatedAt: r.now()}
	if err := record.Validate(); err != nil {
		return OwnershipRecord{}, false, classified(CodeInvalidArgument, "invalid ownership record", err)
	}
	return record, false, nil
}

func (r *Runtime) verifyCandidate(ctx context.Context, identity Identity, candidate Session) (Session, error) {
	if candidate.ID == 0 || candidate.State != "active" {
		return Session{}, classified(CodeSessionChanged, "login session is not active", ErrSessionChanged)
	}
	current, err := r.deps.Sessions.Find(ctx, identity.SID)
	if err != nil {
		return Session{}, callFailure(ctx, CodeSessionUnavailable, "session verification unavailable", err)
	}
	if current.ID != candidate.ID {
		return Session{}, classified(CodeSessionChanged, "session identity changed", ErrSessionChanged)
	}
	if err := current.ValidateActive(identity); err != nil {
		return Session{}, classified(CodeSessionChanged, "session identity changed", err)
	}
	return current, nil
}

func (r *Runtime) cleanup(parent context.Context, identity Identity, record OwnershipRecord, removeProfile, removeAccount bool, session Session, desktop Desktop, agentStarted, workerStarted, markStopped bool) CleanupReport {
	report := CleanupReport{Attempted: true}
	ctx, cancel := context.WithTimeout(context.Background(), r.opts.CleanupTimeout)
	defer cancel()
	failed := false
	if workerStarted {
		if err := r.deps.Worker.Stop(ctx, identity); err != nil {
			failed = true
		}
	}
	if agentStarted {
		if err := r.deps.Agent.Stop(ctx, identity); err != nil {
			failed = true
		}
	}
	if desktop.SessionID != 0 {
		if err := r.deps.Desktop.Stop(ctx, identity, desktop); err != nil {
			failed = true
		}
	}
	if session.ID != 0 && session.SID != "" && session.SID != identity.SID {
		// A provider returned a candidate for another SID. It is not safe to
		// stop whatever the expected SID currently owns.
		failed = true
	}
	if session.ID != 0 && session.SID == identity.SID {
		if err := r.deps.Login.Logout(ctx, identity, session); err != nil {
			failed = true
		}
		current, findErr := r.deps.Sessions.Find(ctx, identity.SID)
		if findErr == nil && current.ID != 0 && current.ID != session.ID {
			// Never stop a session that differs from the candidate returned by
			// this login attempt; it may belong to another generation.
			failed = true
		} else if findErr == nil && current.ID != 0 {
			if err := r.deps.Sessions.Stop(ctx, identity, current); err != nil {
				failed = true
			}
		} else if findErr != nil && !sessionGone(current) {
			failed = true
		}
		current, findErr = r.deps.Sessions.Find(ctx, identity.SID)
		if findErr == nil && current.ID != 0 || (findErr != nil && !sessionGone(current)) {
			failed = true
		}
	} else if current, findErr := r.deps.Sessions.Find(ctx, identity.SID); findErr == nil && current.ID != 0 || (findErr != nil && !sessionGone(current)) {
		// A late login without a recorded candidate cannot be adopted or
		// stopped safely. Leave ownership unknown for explicit repair.
		failed = true
	}
	if !failed && removeProfile {
		if err := r.deps.Profiles.Remove(ctx, identity); err != nil {
			failed = true
		}
	}
	if !failed && removeAccount {
		if err := r.deps.Accounts.Delete(ctx, identity); err != nil {
			failed = true
		}
	}
	if failed {
		report.Failure = CodeCleanup
		// A failed cleanup must remain visibly unknown. Future reconciliation
		// cannot mistake a partially torn-down session for a fresh start.
		record.Phase = JournalUnknown
		record.UpdatedAt = r.now()
		_ = r.deps.Journal.Checkpoint(ctx, record)
		return report
	}
	if markStopped {
		record.Phase = JournalStopped
		record.SessionID = 0
		record.UpdatedAt = r.now()
		if err := r.deps.Journal.MarkStopped(ctx, record); err != nil {
			report.Failure = CodeCleanup
			record.Phase = JournalUnknown
			record.UpdatedAt = r.now()
			_ = r.deps.Journal.Checkpoint(ctx, record)
			return report
		}
	}
	r.mu.Lock()
	delete(r.desktops, identity.SlotID)
	r.mu.Unlock()
	report.Complete = true
	return report
}

func (r *Runtime) desktop(slot string, record OwnershipRecord) Desktop {
	r.mu.Lock()
	defer r.mu.Unlock()
	if desktop, ok := r.desktops[slot]; ok {
		return desktop
	}
	return Desktop{SessionID: record.SessionID, Generation: record.Generation, ReadyAt: record.UpdatedAt}
}

func (r *Runtime) acquire(slot string) (func(), error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.busy[slot]; ok {
		return nil, classified(CodeConflict, "slot operation already running", ErrConflict)
	}
	r.busy[slot] = struct{}{}
	return func() {
		r.mu.Lock()
		delete(r.busy, slot)
		r.mu.Unlock()
	}, nil
}

func (r *Runtime) now() time.Time {
	return r.opts.Now().UTC()
}

func (r *Runtime) check(layer ReadinessLayer, state ReadinessState, identity Identity, observed time.Time) ReadinessCheck {
	if observed.IsZero() {
		observed = r.now()
	}
	return ReadinessCheck{Layer: layer, State: state, Generation: identity.Generation, SessionID: 0, ObservedAt: observed, FreshUntil: r.now().Add(r.opts.Freshness)}
}

func validateHostFacts(facts HostFacts, ordinal int, now time.Time, freshness time.Duration) error {
	if strings.TrimSpace(facts.Edition) == "" || strings.TrimSpace(facts.BuildClass) == "" || facts.Capacity < ordinal || facts.Capacity <= 0 || !facts.RDPEnabled || !facts.NLARequired || !facts.CertTrusted || facts.ObservedAt.IsZero() || facts.ObservedAt.After(now) {
		return classified(CodeUnsupported, "host preflight rejected", ErrUnsupported)
	}
	if freshness <= 0 || now.Sub(facts.ObservedAt) > freshness {
		return classified(CodeStaleReadiness, "host preflight is stale", ErrDeadline)
	}
	return nil
}

func sessionGone(session Session) bool {
	return session.ID == 0 && (session.State == "logged_off" || session.State == "stopped")
}

func contextFailure(ctx context.Context) error {
	if ctx == nil {
		return classified(CodeInvalidArgument, "nil context", ErrInvalidArgument)
	}
	switch ctx.Err() {
	case nil:
		return nil
	case context.Canceled:
		return classified(CodeCancelled, "operation cancelled", ErrCancelled)
	default:
		return classified(CodeDeadline, "operation deadline exceeded", ErrDeadline)
	}
}

func callFailure(ctx context.Context, code Code, message string, cause error) error {
	if ctxErr := contextFailure(ctx); ctxErr != nil {
		return ctxErr
	}
	return classified(code, message, cause)
}

func classified(code Code, message string, cause error) *Error {
	return &Error{Code: code, Message: message, cause: cause}
}
