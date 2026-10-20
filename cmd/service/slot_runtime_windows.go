//go:build windows

package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Semcosm/chuzi/internal/browser"
	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/environment"
	"github.com/Semcosm/chuzi/internal/observability"
	"github.com/Semcosm/chuzi/internal/servicehost"
	"github.com/Semcosm/chuzi/internal/slot"
	"github.com/Semcosm/chuzi/internal/slotagent"
	"github.com/Semcosm/chuzi/internal/slotlifecycle"
	"github.com/Semcosm/chuzi/internal/slotwindows"
	"github.com/Semcosm/chuzi/internal/store"
	"golang.org/x/sys/windows"
)

type slotAgentBridge struct{ resolver slotAgentResolver }

func (b slotAgentBridge) ResolveAgent(slotID, handle string) (browser.AgentConnection, error) {
	path, token, err := b.resolver.AgentEndpoint(slotID, handle)
	if err != nil {
		return browser.AgentConnection{}, err
	}
	return browser.AgentConnection{Path: path, Token: token}, nil
}

func configureWorkerFactory(cfg config.Config, options serviceOptions, factory browser.WorkerFactory, access slotProfileAccess) (browser.WorkerFactory, error) {
	if !cfg.WindowsJobPool.Enabled || access == nil {
		return factory, nil
	}
	resolver, ok := access.(slotAgentResolver)
	if !ok {
		return nil, fmt.Errorf("service: Windows slot agent unavailable")
	}
	// The slot agent always launches the versioned browser worker. An
	// automation adapter is a separate service-side protocol client that
	// consumes the worker's loopback handle; selecting the Adapter job kind
	// here would make the agent launch a different protocol boundary.
	return browser.NewAgentProcessFactory(slotAgentBridge{resolver: resolver}, slotagent.BrowserWorker)
}

func resolveSessionBootstrapper(options serviceOptions, recordFailure func(observability.Event)) slotwindows.SessionBootstrapper {
	if options.sessionBootstrapper != nil {
		return options.sessionBootstrapper
	}
	// The service starts the fixed broker under the one authenticated
	// interactive operator session before using its ACL-protected pipe. The
	// broker owns the deployment-specific WTS/RDP login implementation.
	inner := slotwindows.NewRunnerSessionBootstrapper()
	return newBrokerHelperBootstrapper(inner, recordFailure)
}

// brokerHelperBootstrapper binds the fixed broker client to the authenticated
// helper lifecycle. It never passes a slot token to the helper and forwards
// credentials only through the private service-authenticated channel.
type brokerHelperBootstrapper struct {
	inner   slotwindows.SessionBootstrapper
	record  func(observability.Event)
	runtime servicehost.TrustedRuntime
	starter *servicehost.WindowsHelperStarter
	mu      sync.Mutex
	process servicehost.HelperProcess
}

func newBrokerHelperBootstrapper(inner slotwindows.SessionBootstrapper, recordFailure func(observability.Event)) slotwindows.SessionBootstrapper {
	executable, err := os.Executable()
	if err != nil {
		return inner
	}
	runtime, err := servicehost.NewTrustedRuntime(filepath.Dir(filepath.Clean(executable)))
	if err != nil {
		return inner
	}
	return &brokerHelperBootstrapper{inner: inner, runtime: runtime, starter: servicehost.NewWindowsHelperStarter(), record: recordFailure}
}

func (b *brokerHelperBootstrapper) ensure(ctx context.Context) error {
	if b == nil || b.inner == nil || b.starter == nil {
		return servicehost.ErrHelperUnavailable
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	process := b.process
	if process != nil {
		if _, err := process.Registration(ctx); err == nil {
			return nil
		}
		_ = process.Stop(context.Background())
		b.process = nil
	}
	process, err := b.starter.Start(ctx, b.runtime, "", 0)
	if err != nil {
		return err
	}
	if _, err := process.Registration(ctx); err != nil {
		_ = process.Stop(context.Background())
		return err
	}
	b.process = process
	return nil
}

func (b *brokerHelperBootstrapper) Start(ctx context.Context, identity slotwindows.ManagedIdentity) (slotwindows.BootstrapSession, error) {
	if err := b.ensure(ctx); err != nil {
		b.recordFailureEvent(identity.SlotID, "broker_start")
		return slotwindows.BootstrapSession{}, slotwindows.ErrSessionUnavailable
	}
	return b.inner.Start(ctx, identity)
}

func (b *brokerHelperBootstrapper) StartWithCredential(ctx context.Context, identity slotwindows.ManagedIdentity, username string, password []uint16) (slotwindows.BootstrapSession, error) {
	if err := b.ensure(ctx); err != nil {
		b.recordFailureEvent(identity.SlotID, "broker_start")
		return slotwindows.BootstrapSession{}, slotwindows.ErrSessionUnavailable
	}
	secret := slotwindows.SessionBrokerSecret{PoolID: identity.PoolID, SlotID: identity.SlotID, Ordinal: identity.Ordinal, Generation: identity.Generation, SID: identity.SID, Owner: identity.Owner, Username: username, Password: append([]uint16(nil), password...)}
	defer clear(secret.Password)
	if _, err := rand.Read(secret.Nonce[:]); err != nil {
		b.recordFailureEvent(identity.SlotID, "broker_nonce_generation")
		return slotwindows.BootstrapSession{}, slotwindows.ErrSessionUnavailable
	}
	if err := slotwindows.SendSessionBrokerSecret(ctx, secret, b.runtime.Broker); err != nil {
		b.recordFailureEvent(identity.SlotID, "broker_secret_handoff")
		return slotwindows.BootstrapSession{}, slotwindows.ErrSessionUnavailable
	}
	started, err := b.inner.Start(ctx, identity)
	if err != nil {
		b.recordFailureEvent(identity.SlotID, "broker_login")
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = slotwindows.DropSessionBrokerSecret(cleanupCtx, slotwindows.SessionBrokerOwnership{PoolID: identity.PoolID, SlotID: identity.SlotID, Ordinal: identity.Ordinal, Generation: identity.Generation, SID: identity.SID, Owner: identity.Owner}, secret.Nonce, b.runtime.Broker)
		cancel()
	}
	return started, err
}

func (b *brokerHelperBootstrapper) recordFailureEvent(slotID, stage string) {
	if b == nil || b.record == nil {
		return
	}
	b.record(observability.Event{
		At: time.Now().UTC(), Component: "windows_session", Operation: "broker",
		Outcome: "failed", Resource: slotID, ErrorClass: "session_unavailable",
		FailureStage: stage,
	})
}

func (b *brokerHelperBootstrapper) Stop(ctx context.Context, identity slotwindows.ManagedIdentity, session slotwindows.BootstrapSession) error {
	if err := b.ensure(ctx); err != nil {
		return slotwindows.ErrSessionUnavailable
	}
	return b.inner.Stop(ctx, identity, session)
}

func (b *brokerHelperBootstrapper) Close(ctx context.Context) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	process := b.process
	b.process = nil
	b.mu.Unlock()
	if process != nil {
		return process.Stop(ctx)
	}
	return nil
}

type serviceWindowsHostPreflight struct {
	options servicehost.WindowsPreflightOptions
}

func (p serviceWindowsHostPreflight) Check(ctx context.Context, managedSID string) error {
	options := p.options
	options.LogonSID = managedSID
	// RunWindowsPreflight retains the redacted report in PreflightFailure so
	// callers can distinguish a deterministic failed fact from an unavailable
	// check without exposing native error text.
	_, err := servicehost.RunWindowsPreflight(ctx, options, time.Now)
	return err
}

func hostPreflightFailureCode(err error) string {
	var failure *servicehost.PreflightFailure
	if !errors.As(err, &failure) {
		return "host_preflight_failed"
	}
	for _, fact := range failure.Report().Facts {
		if fact.State != servicehost.FactPass && fact.Reason != "" {
			return safeServiceDiagnosticToken(fact.Reason, "host_preflight_failed")
		}
	}
	return "host_preflight_failed"
}

func newSlotReconciler(cfg config.Config, options serviceOptions, database *store.Store, now func() time.Time, revoker slotCapabilityRevoker, environmentRuntime *serviceEnvironmentRuntime, pool slot.PoolConfig, recordProvisionFailure func(observability.Event)) (slotReconciler, slotProfileAccess, error) {
	if !cfg.WindowsJobPool.Enabled {
		reconciler, err := newLogicalSlotReconciler(database, now, options.owner)
		if err != nil {
			return nil, nil, err
		}
		return reconciler, nil, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	runtimeRoot := filepath.Dir(executable)
	if environmentRuntime == nil {
		return nil, nil, fmt.Errorf("service: trusted environment runtime is unavailable")
	}
	workerCommand, err := fixedWindowsNodeRuntime(runtimeRoot, options.workerCommand)
	if err != nil {
		return nil, nil, fmt.Errorf("service: Windows job pool requires the service-owned node.exe runtime")
	}
	environmentID, environmentVersion, manifestDigest, signer := "", "", "", ""
	requireTrusted := false
	runtimeHandoff := environment.RuntimeHandoff{}
	runtimePath, workerScript, adapterScript := "", "", ""
	// A resolver-backed service creates one immutable provisioner per pool and
	// fills these signed runtime fields immediately before that pool's pass.
	// Direct/native fixtures without a resolver retain their validated handoff.
	if environmentRuntime.RuntimeResolver == nil {
		if !environmentRuntime.Handoff.Valid() && pool.PoolID != "" {
			return nil, nil, fmt.Errorf("service: trusted environment runtime is unavailable")
		}
		if environmentRuntime.Handoff.Valid() {
			workerCommand, workerScript, err = fixedWindowsWorkerRuntime(environmentRuntime.Handoff, runtimeRoot, options)
			if err != nil {
				return nil, nil, err
			}
			adapterScript = environmentRuntime.Handoff.AdapterBridgePath()
			runtimeHandoff = environmentRuntime.Handoff
			runtimePath = runtimePackageRoot(environmentRuntime.Handoff)
			environmentID, environmentVersion = pool.EnvironmentID, pool.EnvironmentVersion
			manifestDigest, signer, requireTrusted = pool.ManifestDigest, pool.Signer, pool.RequireTrusted
		}
	}
	browserMode, browserCommand := "", ""
	if options.backend == backendHeadless || options.backend == backendHeaded {
		browserMode, browserCommand = options.backend, options.headlessBrowserCommand
	}
	sessionBootstrapper := resolveSessionBootstrapper(options, recordProvisionFailure)
	hostRuntime, err := servicehost.NewTrustedRuntime(runtimeRoot)
	if err != nil {
		return nil, nil, err
	}
	dataRoot, err := servicehost.FixedDataRoot()
	if err != nil {
		return nil, nil, err
	}
	requireAuthenticode, err := servicehost.RuntimeSignaturePolicy(runtimeRoot)
	if err != nil {
		return nil, nil, err
	}
	hostPreflight := serviceWindowsHostPreflight{options: servicehost.WindowsPreflightOptions{
		Runtime: hostRuntime, DataRoot: dataRoot, MaxSessions: 32,
		RequireAuthenticode: requireAuthenticode,
	}}
	provisionOptions := slotwindows.Options{DataDir: cfg.DataDir, UserPrefix: cfg.WindowsJobPool.UserPrefix, EnvironmentID: environmentID, Version: environmentVersion, ManifestDigest: manifestDigest, Signer: signer, RequireTrusted: requireTrusted, RDPEnabled: cfg.WindowsJobPool.RDPEnabled, AgentPath: filepath.Join(runtimeRoot, "chuzi-user-agent.exe"), Runtime: runtimeHandoff, RuntimePath: runtimePath, WorkerRuntimeRoot: runtimeRoot, WorkerCommand: workerCommand, WorkerScript: workerScript, AdapterScript: adapterScript, BrowserMode: browserMode, BrowserCommand: browserCommand, SessionIdleTimeout: time.Duration(cfg.WindowsJobPool.SessionIdleTimeoutSeconds) * time.Second, CapabilityRevoker: revoker, RuntimeResolver: environmentRuntime.RuntimeResolver, SessionBootstrapper: sessionBootstrapper, HostPreflight: hostPreflight}
	if recordProvisionFailure != nil {
		provisionOptions.ProvisionFailureRecorder = func(failure slotwindows.ProvisionFailureDiagnostic) {
			recordProvisionFailure(observability.Event{
				At: now().UTC(), Component: "windows_session", Operation: "provision",
				Outcome: "failed", Resource: failure.Resource, ErrorClass: failure.ErrorClass,
				FailureStage: failure.Stage, NativeErrorKind: failure.NativeErrorKind,
				NativeErrorCode: failure.NativeErrorCode,
			})
		}
	}
	provisioner, err := slotwindows.New(provisionOptions)
	if err != nil {
		return nil, nil, err
	}
	provisionTimeout := time.Duration(cfg.WindowsJobPool.ProvisionTimeoutSeconds) * time.Second
	cleanupTimeout := time.Duration(cfg.WindowsJobPool.CleanupTimeoutSeconds) * time.Second
	var leaseRevoker slotlifecycle.CapabilityRevoker
	if candidate, ok := revoker.(slotlifecycle.CapabilityRevoker); ok {
		leaseRevoker = candidate
	}
	allPools := &windowsAllPoolReconciler{database: database, provisioner: provisioner, sessionBootstrapper: sessionBootstrapper, provisionerFactory: func() (slotlifecycle.Provisioner, error) { return slotwindows.New(provisionOptions) }, now: now, provisionTimeout: provisionTimeout, cleanupTimeout: cleanupTimeout, leaseRevoker: leaseRevoker, owner: options.owner, items: make(map[string]*slotlifecycle.Reconciler), provisioners: make(map[string]slotlifecycle.Provisioner)}
	return allPools, allPools, nil
}

func runtimePackageRoot(runtime environment.RuntimeHandoff) string {
	if !runtime.Valid() {
		return ""
	}
	return runtime.PackageRoot()
}

// windowsAllPoolReconciler creates a dedicated OS provisioner for each pool
// while keeping one durable logical reconciler per pool. This prevents a
// signed runtime refresh for one pool from changing another pool's agents.
type windowsAllPoolReconciler struct {
	database                         *store.Store
	provisioner                      slotlifecycle.Provisioner
	sessionBootstrapper              slotwindows.SessionBootstrapper
	provisionerFactory               func() (slotlifecycle.Provisioner, error)
	now                              func() time.Time
	provisionTimeout, cleanupTimeout time.Duration
	leaseRevoker                     slotlifecycle.CapabilityRevoker
	owner                            string
	mu                               sync.Mutex
	items                            map[string]*slotlifecycle.Reconciler
	provisioners                     map[string]slotlifecycle.Provisioner
	recordEvent                      func(observability.Event)
}

// SetEventRecorder wires the service's bounded observability sinks after the
// runtime is assembled. Tests and direct users may leave it nil.
func (r *windowsAllPoolReconciler) SetEventRecorder(record func(observability.Event)) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.recordEvent = record
	r.mu.Unlock()
}

func (r *windowsAllPoolReconciler) recordSessionEvent(at time.Time, operation, phase, outcome, class string, duration time.Duration, attempts ...int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	record := r.recordEvent
	r.mu.Unlock()
	if record != nil {
		attempt := 0
		if len(attempts) > 0 {
			attempt = attempts[0]
		}
		record(observability.Event{At: at.UTC(), Component: "slot_session", Operation: operation + "_" + phase, Outcome: outcome, ErrorClass: class, Attempt: attempt, Duration: duration})
	}
}

func (r *windowsAllPoolReconciler) Reconcile(ctx context.Context) error { return r.ReconcileAll(ctx) }

// ReconcileSlotSessionOperations is the durable operation controller. It
// claims pending work after restart, performs OS calls outside bbolt, and
// commits only while the claim and generation fences remain valid. Ordinary
// pool prewarm remains owned by ReconcileAll; this path is for explicit Core
// Start/Stop/Cancel intents.
func (r *windowsAllPoolReconciler) ReconcileSlotSessionOperations(ctx context.Context, owner string, now time.Time) error {
	if r == nil || r.database == nil || ctx == nil || strings.TrimSpace(owner) == "" {
		return errors.New("service: invalid slot session controller")
	}
	operations, err := r.database.ListSlotSessionOperations()
	if err != nil {
		return err
	}
	var first error
	var firstMu sync.Mutex
	workers := 4
	if len(operations) < workers {
		workers = len(operations)
	}
	sem := make(chan struct{}, workers)
	claimTTL := 30 * time.Second
	if r.provisionTimeout+5*time.Second > claimTTL {
		claimTTL = r.provisionTimeout + 5*time.Second
	}
	var group sync.WaitGroup
	for _, operation := range operations {
		operation := operation
		if operation.State == store.SlotSessionReady || operation.State == store.SlotSessionStopped || operation.State == store.SlotSessionFailed || operation.State == store.SlotSessionCancelled {
			continue
		}
		group.Add(1)
		go func() {
			defer group.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			claimed, claimErr := r.database.ClaimSlotSessionOperation(operation.OperationID, owner, now.UTC(), claimTTL)
			if claimErr != nil {
				// Another live controller or a backoff window owns this operation.
				if errors.Is(claimErr, store.ErrSlotSessionClaim) {
					return
				}
				firstMu.Lock()
				if first == nil {
					first = claimErr
				}
				firstMu.Unlock()
				return
			}
			if advanceErr := r.advanceClaimedSlotSession(ctx, owner, now.UTC(), claimed); advanceErr != nil {
				firstMu.Lock()
				if first == nil {
					first = advanceErr
				}
				firstMu.Unlock()
			}
		}()
	}
	group.Wait()
	return first
}

func (r *windowsAllPoolReconciler) advanceClaimedSlotSession(ctx context.Context, owner string, now time.Time, operation store.SlotSessionOperation) error {
	startedAt := time.Now()
	observedAt := func() time.Time { return now.Add(time.Since(startedAt)).UTC() }
	phaseStarted := time.Now()
	defer func() {
		r.recordSessionEvent(observedAt(), "attempt", string(operation.Phase), "completed", "", time.Since(startedAt), operation.Attempt)
	}()
	failOperation := func(phase store.SlotSessionPhase, code string, cause error) error {
		r.recordSessionEvent(observedAt(), "failure", string(phase), "failed", code, time.Since(phaseStarted), operation.Attempt)
		_, completeErr := r.database.CompleteSlotSessionOperation(operation.OperationID, owner, operation.EnvironmentGeneration, store.SlotSessionFailed, phase, code, observedAt())
		if completeErr != nil {
			return completeErr
		}
		return cause
	}
	retryOperation := func(phase store.SlotSessionPhase, code string, cause error) error {
		var retryable interface{ Retryable() bool }
		if !errors.As(cause, &retryable) || !retryable.Retryable() {
			return failOperation(phase, code, cause)
		}
		if _, retryErr := r.database.ScheduleSlotSessionRetry(operation.OperationID, owner, operation.EnvironmentGeneration, phase, code, observedAt()); retryErr != nil {
			return retryErr
		}
		r.recordSessionEvent(observedAt(), "recovery", string(phase), "retrying", code, time.Since(phaseStarted), operation.Attempt)
		return cause
	}
	cleanupIntent := operation.DesiredState == "stopped" || operation.State == store.SlotSessionStopping || operation.State == store.SlotSessionCancelling
	if !operation.Deadline.IsZero() && !now.Before(operation.Deadline) && !cleanupIntent {
		var transitionErr error
		operation, transitionErr = r.database.BeginSlotSessionCleanup(operation.OperationID, owner, operation.EnvironmentGeneration, false, now)
		if transitionErr != nil {
			return transitionErr
		}
		cleanupIntent = true
	}
	callCtx := ctx
	var baseCancel context.CancelFunc
	if !cleanupIntent && !operation.Deadline.IsZero() {
		remaining := operation.Deadline.Sub(now)
		if remaining <= 0 {
			remaining = time.Nanosecond
		}
		callCtx, baseCancel = context.WithTimeout(ctx, remaining)
		defer baseCancel()
	}
	callCtx, cancel := context.WithCancel(callCtx)
	defer cancel()
	// A Stop/Cancel commit must interrupt an active native login or profile
	// call. Polling is deliberately outside bbolt transactions; the durable
	// owner/generation fence remains the authority if a provider ignores the
	// context and returns a late result.
	watchDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		defer close(watchDone)
		lastRenew := time.Time{}
		for {
			select {
			case <-callCtx.Done():
				return
			case <-ticker.C:
				if lastRenew.IsZero() || time.Since(lastRenew) >= 5*time.Second {
					if _, renewErr := r.database.RenewSlotSessionOperation(operation.OperationID, owner, observedAt(), 30*time.Second); renewErr != nil {
						cancel()
						return
					}
					lastRenew = time.Now()
				}
				latest, lookupErr := r.database.GetSlotSessionOperation(operation.OperationID)
				if lookupErr != nil {
					continue
				}
				generationChanged := operation.EnvironmentGeneration != 0 && latest.EnvironmentGeneration != operation.EnvironmentGeneration
				if latest.ClaimOwner != owner || generationChanged || latest.DesiredState == "stopped" || latest.State == store.SlotSessionStopping || latest.State == store.SlotSessionCancelling {
					cancel()
					return
				}
			}
		}
	}()
	defer func() {
		cancel()
		<-watchDone
	}()
	value, err := r.database.GetSlot(operation.SlotID)
	if err != nil {
		if cleanupIntent {
			_, _ = r.database.CompleteSlotSessionOperation(operation.OperationID, owner, operation.EnvironmentGeneration, operation.State, store.SlotSessionPhaseStopping, "slot_not_found", observedAt())
		} else {
			_, _ = r.database.CompleteSlotSessionOperation(operation.OperationID, owner, operation.EnvironmentGeneration, store.SlotSessionFailed, store.SlotSessionPhaseStopping, "slot_not_found", observedAt())
		}
		return err
	}
	pool, err := r.database.GetJobPool(value.PoolID)
	if err != nil {
		if cleanupIntent {
			_, cleanupErr := r.database.CompleteSlotSessionOperation(operation.OperationID, owner, operation.EnvironmentGeneration, operation.State, store.SlotSessionPhaseStopping, "cleanup_failed", observedAt())
			if cleanupErr != nil {
				return cleanupErr
			}
			return err
		}
		return failOperation(store.SlotSessionPhaseEnvironment, "pool_unavailable", err)
	}
	provisioner, err := r.provisionerForSlot(value.SlotID)
	if err != nil {
		if cleanupIntent {
			_, cleanupErr := r.database.CompleteSlotSessionOperation(operation.OperationID, owner, operation.EnvironmentGeneration, operation.State, store.SlotSessionPhaseStopping, "cleanup_failed", observedAt())
			if cleanupErr != nil {
				return cleanupErr
			}
			return err
		}
		return failOperation(store.SlotSessionPhaseEnvironment, "provisioner_unavailable", err)
	}
	generation := value.EnvironmentGeneration
	if generation == 0 {
		generation = 1
	}
	request := slot.ProvisionRequest{SlotID: value.SlotID, PoolID: value.PoolID, Ordinal: value.Ordinal, Owner: r.owner, EnvironmentGeneration: generation, PackageGeneration: slotPackageGeneration(provisioner), Requirement: pool.Requirement()}
	recordCleanupFailure := func(current store.SlotSessionOperation, phase store.SlotSessionPhase, code string, cause error) error {
		r.recordSessionEvent(observedAt(), "cleanup", string(phase), "failed", code, time.Since(startedAt), current.Attempt)
		if _, leased, leaseErr := r.database.GetSlotLease(value.SlotID); leaseErr == nil && !leased {
			// Cleanup failure is a quarantine condition. The durable operation
			// remains retryable, while pool prewarm and new work stay away from
			// resources whose ownership has not yet been proven clean.
			_ = r.database.QuarantineSlot(value.SlotID, observedAt(), code)
		}
		_, completeErr := r.database.CompleteSlotSessionOperation(current.OperationID, owner, current.EnvironmentGeneration, current.State, phase, code, observedAt())
		if completeErr != nil {
			return completeErr
		}
		return cause
	}
	stopClaimed := func(current store.SlotSessionOperation) error {
		cleanupStarted := time.Now()
		if current.ClaimOwner != owner {
			return store.ErrSlotSessionClaim
		}
		if _, leased, leaseErr := r.database.GetSlotLease(value.SlotID); leaseErr != nil {
			return recordCleanupFailure(current, store.SlotSessionPhaseStopping, "cleanup_failed", leaseErr)
		} else if leased {
			// Queue/lease recovery owns the active job. Release this controller
			// claim while waiting so a later pass can observe the lease release
			// instead of delaying Stop for the full claim TTL.
			_, releaseErr := r.database.CompleteSlotSessionOperation(current.OperationID, owner, current.EnvironmentGeneration, current.State, store.SlotSessionPhaseStopping, "", observedAt())
			return releaseErr
		}
		stopper, ok := provisioner.(slotlifecycle.SessionStopper)
		if !ok {
			return recordCleanupFailure(current, store.SlotSessionPhaseStopping, "cleanup_failed", errors.New("service: session cleanup unavailable"))
		}
		stopCtx, stopCancel := context.WithTimeout(ctx, 30*time.Second)
		err = stopper.StopSession(stopCtx, request)
		stopCancel()
		if err != nil {
			return recordCleanupFailure(current, store.SlotSessionPhaseStopping, "cleanup_failed", err)
		}
		stopAt := observedAt()
		if err := r.database.SetSlotStatus(value.SlotID, slot.Stopped, stopAt); err != nil {
			return err
		}
		state := store.SlotSessionStopped
		if current.State == store.SlotSessionCancelling {
			state = store.SlotSessionCancelled
		}
		failureCode := current.FailureCode
		if failureCode == "" && !current.Deadline.IsZero() && !observedAt().Before(current.Deadline) {
			failureCode = "deadline_exceeded"
		}
		if failureCode != "" {
			// Cleanup proved the native resources are gone, but a deadline or
			// exhausted retry remains the operation's outcome. Preserve that
			// durable reason and commit terminal Failed after cleanup.
			state = store.SlotSessionFailed
		}
		_, err = r.database.CompleteSlotSessionOperation(current.OperationID, owner, generation, state, store.SlotSessionPhaseStopped, failureCode, observedAt())
		outcome := "completed"
		if failureCode != "" {
			outcome = "failed"
		}
		r.recordSessionEvent(observedAt(), "cleanup", string(store.SlotSessionPhaseStopped), outcome, failureCode, time.Since(cleanupStarted), current.Attempt)
		return err
	}
	cleanupAfterFailure := func(current store.SlotSessionOperation, failureCodes ...string) error {
		if current.State != store.SlotSessionStopping && current.State != store.SlotSessionCancelling {
			transitioned, transitionErr := r.database.BeginSlotSessionCleanup(current.OperationID, owner, current.EnvironmentGeneration, false, observedAt(), failureCodes...)
			if transitionErr != nil {
				return transitionErr
			}
			current = transitioned
		}
		return stopClaimed(current)
	}
	deadlineReached := func() bool {
		return !operation.Deadline.IsZero() && !observedAt().Before(operation.Deadline)
	}
	if operation.DesiredState != "stopped" && operation.State != store.SlotSessionStopping && operation.State != store.SlotSessionCancelling {
		if pool.DesiredSlots == 0 || pool.DesiredState == "disabled" || pool.DesiredState == "draining" {
			return failOperation(store.SlotSessionPhaseEnvironment, "pool_unavailable", errors.New("service: pool unavailable"))
		}
		if updater, ok := provisioner.(slotlifecycle.PoolRuntimeUpdater); ok {
			if updateErr := updater.UpdatePoolRuntime(callCtx, pool); updateErr != nil {
				if latest, lookupErr := r.database.GetSlotSessionOperation(operation.OperationID); lookupErr == nil && (latest.DesiredState == "stopped" || latest.State == store.SlotSessionStopping || latest.State == store.SlotSessionCancelling) {
					return stopClaimed(latest)
				}
				if deadlineReached() {
					if latest, lookupErr := r.database.GetSlotSessionOperation(operation.OperationID); lookupErr == nil {
						return cleanupAfterFailure(latest)
					}
				}
				return retryOperation(store.SlotSessionPhaseEnvironment, "environment_unavailable", updateErr)
			}
			// The runtime handoff is resolved above. Stamp the request only after
			// that update so its package-generation fence matches the signed
			// package the provisioner will validate.
			request.PackageGeneration = slotPackageGeneration(provisioner)
		}
	}
	if operation.DesiredState == "stopped" || operation.State == store.SlotSessionStopping || operation.State == store.SlotSessionCancelling {
		return stopClaimed(operation)
	}
	result, err := provisioner.Provision(callCtx, request)
	if err != nil {
		if latest, lookupErr := r.database.GetSlotSessionOperation(operation.OperationID); lookupErr == nil && (latest.DesiredState == "stopped" || latest.State == store.SlotSessionStopping || latest.State == store.SlotSessionCancelling) {
			return stopClaimed(latest)
		}
		if errors.Is(err, slotwindows.ErrCleanup) {
			if latest, lookupErr := r.database.GetSlotSessionOperation(operation.OperationID); lookupErr == nil {
				// Provision attempted rollback but could not prove that all
				// owned resources disappeared. Keep the operation in the
				// durable cleanup path; a direct terminal Failed result would
				// strand a user/session or permit a later generation to reuse it.
				return cleanupAfterFailure(latest)
			}
		}
		if deadlineReached() || errors.Is(err, context.DeadlineExceeded) {
			if latest, lookupErr := r.database.GetSlotSessionOperation(operation.OperationID); lookupErr == nil {
				return cleanupAfterFailure(latest)
			}
		}
		if errors.Is(err, slotwindows.ErrHostPreflight) {
			return retryOperation(store.SlotSessionPhaseHost, hostPreflightFailureCode(err), err)
		}
		return retryOperation(store.SlotSessionPhaseEnvironment, "provision_failed", err)
	}
	if deadlineReached() {
		if latest, lookupErr := r.database.GetSlotSessionOperation(operation.OperationID); lookupErr == nil {
			return cleanupAfterFailure(latest)
		}
	}
	// Provision creates the native resources but does not prove that the
	// agent and worker are independently reachable. Keep readiness false until
	// the subsequent Inspect health gate succeeds.
	checkpoint := store.SlotSessionCheckpoint{OperationID: operation.OperationID, Owner: owner, Generation: generation, Phase: store.SlotSessionPhaseWorker, SessionState: "provisioning", ObservedAt: observedAt()}
	if _, err := r.database.CheckpointSlotSessionOperation(checkpoint); err != nil {
		if latest, lookupErr := r.database.GetSlotSessionOperation(operation.OperationID); lookupErr == nil && (latest.DesiredState == "stopped" || latest.State == store.SlotSessionStopping || latest.State == store.SlotSessionCancelling) {
			return stopClaimed(latest)
		}
		return err
	}
	if latest, lookupErr := r.database.GetSlotSessionOperation(operation.OperationID); lookupErr == nil && (latest.DesiredState == "stopped" || latest.State == store.SlotSessionStopping || latest.State == store.SlotSessionCancelling) {
		return stopClaimed(latest)
	}
	inspected, err := provisioner.Inspect(callCtx, request)
	if err != nil || result.AgentHandle == "" || inspected.AgentHandle == "" || validateSlotReadiness(result.Summary, pool, generation) != nil || validateSlotReadiness(inspected, pool, generation) != nil {
		if latest, lookupErr := r.database.GetSlotSessionOperation(operation.OperationID); lookupErr == nil && (latest.DesiredState == "stopped" || latest.State == store.SlotSessionStopping || latest.State == store.SlotSessionCancelling) {
			return stopClaimed(latest)
		}
		if deadlineReached() || errors.Is(err, context.DeadlineExceeded) {
			if latest, lookupErr := r.database.GetSlotSessionOperation(operation.OperationID); lookupErr == nil {
				return cleanupAfterFailure(latest)
			}
		}
		if errors.Is(err, slotwindows.ErrCleanup) {
			if latest, lookupErr := r.database.GetSlotSessionOperation(operation.OperationID); lookupErr == nil {
				return cleanupAfterFailure(latest)
			}
		}
		if err == nil {
			err = errors.New("service: session readiness unavailable")
		}
		var retryable interface{ Retryable() bool }
		if !errors.As(err, &retryable) || !retryable.Retryable() {
			if latest, lookupErr := r.database.GetSlotSessionOperation(operation.OperationID); lookupErr == nil {
				return cleanupAfterFailure(latest, "readiness_failed")
			}
		}
		return retryOperation(store.SlotSessionPhaseWorker, "readiness_failed", err)
	}
	latest, err := r.database.GetSlotSessionOperation(operation.OperationID)
	if err != nil {
		return err
	}
	if latest.DesiredState == "stopped" || latest.State == store.SlotSessionStopping || latest.State == store.SlotSessionCancelling {
		return stopClaimed(latest)
	}
	if deadlineReached() {
		return cleanupAfterFailure(latest)
	}
	readyAt := observedAt()
	if _, err := r.database.CheckpointSlotSessionOperation(store.SlotSessionCheckpoint{
		OperationID:  operation.OperationID,
		Owner:        owner,
		Generation:   generation,
		Phase:        store.SlotSessionPhaseWorker,
		SessionState: inspected.SessionState,
		AgentReady:   true,
		WorkerReady:  inspected.WorkerReady,
		ObservedAt:   readyAt,
	}); err != nil {
		if latest, lookupErr := r.database.GetSlotSessionOperation(operation.OperationID); lookupErr == nil && (latest.DesiredState == "stopped" || latest.State == store.SlotSessionStopping || latest.State == store.SlotSessionCancelling) {
			return stopClaimed(latest)
		}
		return err
	}
	if latest, lookupErr := r.database.GetSlotSessionOperation(operation.OperationID); lookupErr == nil {
		if latest.DesiredState == "stopped" || latest.State == store.SlotSessionStopping || latest.State == store.SlotSessionCancelling {
			return stopClaimed(latest)
		}
	}
	if err := r.database.MarkSlotReady(value.SlotID, inspected, readyAt); err != nil {
		if latest, lookupErr := r.database.GetSlotSessionOperation(operation.OperationID); lookupErr == nil && (latest.DesiredState == "stopped" || latest.State == store.SlotSessionStopping || latest.State == store.SlotSessionCancelling) {
			return stopClaimed(latest)
		}
		return err
	}
	if latest, lookupErr := r.database.GetSlotSessionOperation(operation.OperationID); lookupErr == nil {
		if latest.DesiredState == "stopped" || latest.State == store.SlotSessionStopping || latest.State == store.SlotSessionCancelling {
			return stopClaimed(latest)
		}
	}
	_, err = r.database.CompleteSlotSessionOperation(operation.OperationID, owner, generation, store.SlotSessionReady, store.SlotSessionPhaseWorker, "", observedAt())
	return err
}

func validateSlotReadiness(summary slot.EnvironmentSummary, pool slot.PoolConfig, generation uint64) error {
	if err := summary.Validate(); err != nil || summary.EnvironmentID != pool.EnvironmentID || summary.Version != pool.EnvironmentVersion || summary.Generation != generation || !summary.Trusted || !summary.DesktopReady || !summary.WorkerReady || summary.SessionState != "ready" || summary.AgentVersion == "" || summary.AgentHandle == "" {
		return errors.New("service: incomplete slot readiness")
	}
	if pool.RequireTrusted && !summary.Trusted {
		return errors.New("service: untrusted slot environment")
	}
	if pool.ManifestDigest != "" && summary.ManifestDigest != pool.ManifestDigest {
		return errors.New("service: slot manifest mismatch")
	}
	if pool.Signer != "" && summary.Signer != pool.Signer {
		return errors.New("service: slot signer mismatch")
	}
	capabilities := make(map[string]struct{}, len(summary.Capabilities))
	for _, capability := range summary.Capabilities {
		capabilities[capability] = struct{}{}
	}
	for _, required := range pool.Capabilities {
		if _, ok := capabilities[required]; !ok {
			return errors.New("service: slot capability missing")
		}
	}
	return nil
}

func (r *windowsAllPoolReconciler) ReconcileAll(ctx context.Context) error {
	if r == nil || ctx == nil {
		return fmt.Errorf("service: invalid Windows slot reconciler")
	}
	pools, err := r.database.ListJobPools()
	if err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(pools))
	var first error
	var firstMu sync.Mutex
	for _, pool := range pools {
		seen[pool.PoolID] = struct{}{}
	}
	// A slow login in one pool must not hold every other pool's reconcile
	// pass. The bound keeps native session creation from becoming an unbounded
	// process fan-out while allowing independent ordinals/pools to progress.
	workers := 4
	if len(pools) < workers {
		workers = len(pools)
	}
	sem := make(chan struct{}, workers)
	var group sync.WaitGroup
	for _, pool := range pools {
		pool := pool
		if err := ctx.Err(); err != nil {
			return err
		}
		group.Add(1)
		go func() {
			defer group.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			recordErr := func(value error) {
				if value == nil {
					return
				}
				firstMu.Lock()
				defer firstMu.Unlock()
				if first == nil {
					first = value
				}
			}
			if _, controlErr := r.database.ReconcileJobPoolControl(pool.PoolID, r.now().UTC()); controlErr != nil {
				recordErr(controlErr)
				return
			}
			reconciler, reconcileErr := r.forPool(pool)
			if reconcileErr != nil {
				recordErr(reconcileErr)
				return
			}
			recordErr(reconciler.Reconcile(ctx))
			if _, controlErr := r.database.ReconcileJobPoolControl(pool.PoolID, r.now().UTC()); controlErr != nil && !errors.Is(controlErr, slot.ErrPoolNotFound) {
				recordErr(controlErr)
			}
		}()
	}
	group.Wait()
	var retired []slotlifecycle.Provisioner
	r.mu.Lock()
	for poolID := range r.items {
		if _, ok := seen[poolID]; !ok {
			delete(r.items, poolID)
			if provisioner := r.provisioners[poolID]; provisioner != nil {
				retired = append(retired, provisioner)
				delete(r.provisioners, poolID)
			}
		}
	}
	r.mu.Unlock()
	for _, provisioner := range retired {
		if shutdowner, ok := provisioner.(interface{ Shutdown(context.Context) error }); ok {
			_ = shutdowner.Shutdown(ctx)
		}
	}
	return first
}

func (r *windowsAllPoolReconciler) forPool(pool slot.PoolConfig) (*slotlifecycle.Reconciler, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if value, ok := r.items[pool.PoolID]; ok {
		return value, nil
	}
	provisioner := r.provisioner
	var err error
	if r.provisionerFactory != nil {
		provisioner, err = r.provisionerFactory()
		if err != nil {
			return nil, err
		}
	}
	operationState := slotlifecycle.SessionOperationStateLookup(func(operationID string) (string, error) {
		operation, err := r.database.GetSlotSessionOperation(operationID)
		if err != nil {
			return "", err
		}
		return string(operation.State), nil
	})
	value, err := slotlifecycle.New(r.database, provisioner, pool, r.now, r.provisionTimeout, r.cleanupTimeout, r.leaseRevoker, r.database, operationState, r.owner)
	if err != nil {
		return nil, err
	}
	r.items[pool.PoolID] = value
	if r.provisioners == nil {
		r.provisioners = make(map[string]slotlifecycle.Provisioner)
	}
	r.provisioners[pool.PoolID] = provisioner
	return value, nil
}

func (r *windowsAllPoolReconciler) Shutdown(ctx context.Context) error {
	// Stop every durable slot before closing provisioners. This covers a
	// boot-only login whose process was never published into the in-memory
	// agent map and makes graceful shutdown consume the same StopSession fence
	// as explicit lifecycle operations.
	var first error
	protectedSlots := make(map[string]struct{})
	leasesKnown := false
	// Drain durable slot leases before stopping the provisioner. This is the
	// shutdown equivalent of lease-expiry recovery: fence the Agent first,
	// then release only the exact lease owner so no stale worker survives the
	// service boundary.
	if leases, leaseErr := r.database.ListSlotLeases(); leaseErr == nil {
		leasesKnown = true
		for _, record := range leases {
			provisioner, resolveErr := r.provisionerForSlot(record.SlotID)
			if resolveErr != nil {
				protectedSlots[record.SlotID] = struct{}{}
				if first == nil {
					first = resolveErr
				}
				continue
			}
			if fence, ok := provisioner.(interface {
				StopSlotLease(context.Context, slot.Lease) error
			}); ok {
				if fenceErr := fence.StopSlotLease(ctx, record.Lease); fenceErr != nil {
					protectedSlots[record.SlotID] = struct{}{}
					if first == nil {
						first = fenceErr
					}
					continue
				}
			} else {
				// Without an ownership-aware fence, releasing the durable lease
				// would allow the later cleanup pass to stop an active job.
				protectedSlots[record.SlotID] = struct{}{}
				if first == nil {
					first = errors.New("service: lease fence unavailable")
				}
				continue
			}
			if releaseErr := r.database.ReleaseSlotLease(record.SlotID, record.Lease.LeaseID, record.Lease.Owner); releaseErr != nil {
				protectedSlots[record.SlotID] = struct{}{}
				if first == nil {
					first = releaseErr
				}
			}
		}
	} else if first == nil {
		first = leaseErr
	}
	if pools, err := r.database.ListJobPools(); err == nil {
		for _, pool := range pools {
			slots, listErr := r.database.ListSlots(pool.PoolID)
			if listErr != nil {
				if first == nil {
					first = listErr
				}
				continue
			}
			if _, provisionErr := r.forPool(pool); provisionErr != nil {
				if first == nil {
					first = provisionErr
				}
				continue
			}
			r.mu.Lock()
			provisioner := r.provisioners[pool.PoolID]
			r.mu.Unlock()
			stopper, ok := provisioner.(slotlifecycle.SessionStopper)
			if !ok {
				continue
			}
			for _, value := range slots {
				if value.Status == slot.Deleted || value.EnvironmentGeneration == 0 {
					continue
				}
				if !leasesKnown {
					continue
				}
				if _, protected := protectedSlots[value.SlotID]; protected {
					continue
				}
				request := slot.ProvisionRequest{SlotID: value.SlotID, PoolID: value.PoolID, Ordinal: value.Ordinal, Owner: r.owner, EnvironmentGeneration: value.EnvironmentGeneration, PackageGeneration: slotPackageGeneration(provisioner), Requirement: pool.Requirement()}
				if stopErr := stopper.StopSession(ctx, request); stopErr != nil && !errors.Is(stopErr, slot.ErrSlotNotFound) && first == nil {
					first = stopErr
				}
			}
		}
	} else {
		first = err
	}
	r.mu.Lock()
	provisioners := make([]slotlifecycle.Provisioner, 0, len(r.provisioners))
	for _, provisioner := range r.provisioners {
		provisioners = append(provisioners, provisioner)
	}
	if len(provisioners) == 0 && r.provisionerFactory == nil && r.provisioner != nil {
		provisioners = append(provisioners, r.provisioner)
	}
	r.mu.Unlock()
	for _, provisioner := range provisioners {
		if shutdowner, ok := provisioner.(interface{ Shutdown(context.Context) error }); ok {
			if err := shutdowner.Shutdown(ctx); err != nil && first == nil {
				first = err
			}
		}
	}
	if closer, ok := r.sessionBootstrapper.(interface{ Close(context.Context) error }); ok {
		if err := closer.Close(ctx); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (r *windowsAllPoolReconciler) provisionerForSlot(slotID string) (slotlifecycle.Provisioner, error) {
	if r == nil || r.database == nil {
		return nil, slot.ErrSlotNotFound
	}
	value, err := r.database.GetSlot(slotID)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	provisioner := r.provisioners[value.PoolID]
	fallback := r.provisioner
	factory := r.provisionerFactory
	if provisioner == nil && factory == nil {
		provisioner = fallback
	}
	r.mu.Unlock()
	if provisioner != nil {
		return provisioner, nil
	}
	if factory == nil {
		return nil, slot.ErrSlotUnavailable
	}
	pool, err := r.database.GetJobPool(value.PoolID)
	if err != nil {
		return nil, err
	}
	if _, err := r.forPool(pool); err != nil {
		return nil, err
	}
	r.mu.Lock()
	provisioner = r.provisioners[value.PoolID]
	r.mu.Unlock()
	if provisioner == nil {
		return nil, slot.ErrSlotUnavailable
	}
	return provisioner, nil
}

func slotPackageGeneration(provisioner slotlifecycle.Provisioner) uint64 {
	if provider, ok := provisioner.(interface{ PackageGeneration() uint64 }); ok {
		return provider.PackageGeneration()
	}
	return 0
}

func (r *windowsAllPoolReconciler) GrantProfile(ctx context.Context, slotID, profilePath string) error {
	provisioner, err := r.provisionerForSlot(slotID)
	if err != nil {
		return err
	}
	access, ok := provisioner.(interface {
		GrantProfile(context.Context, string, string) error
	})
	if !ok {
		return slot.ErrSlotUnavailable
	}
	return access.GrantProfile(ctx, slotID, profilePath)
}

func (r *windowsAllPoolReconciler) RevokeProfile(ctx context.Context, slotID, profilePath string) error {
	provisioner, err := r.provisionerForSlot(slotID)
	if err != nil {
		return err
	}
	access, ok := provisioner.(interface {
		RevokeProfile(context.Context, string, string) error
	})
	if !ok {
		return slot.ErrSlotUnavailable
	}
	return access.RevokeProfile(ctx, slotID, profilePath)
}

func (r *windowsAllPoolReconciler) AgentEndpoint(slotID, handle string) (string, string, error) {
	provisioner, err := r.provisionerForSlot(slotID)
	if err != nil {
		return "", "", err
	}
	resolver, ok := provisioner.(interface {
		AgentEndpoint(string, string) (string, string, error)
	})
	if !ok {
		return "", "", slot.ErrSlotUnavailable
	}
	return resolver.AgentEndpoint(slotID, handle)
}

// fixedWindowsWorkerRuntime is the allowlist for the user-agent process. The
// Windows slot boundary runs only the packaged Node worker entry points; a
// service flag cannot turn a managed slot into a shell or arbitrary launcher.
func fixedWindowsWorkerRuntime(runtime environment.RuntimeHandoff, serviceRuntimeRoot string, options serviceOptions) (string, string, error) {
	if !runtime.Valid() || serviceRuntimeRoot == "" || !filepath.IsAbs(serviceRuntimeRoot) {
		return "", "", fmt.Errorf("service: invalid Windows runtime root")
	}
	packageRoot := runtime.PackageRoot()
	workerScript := runtime.WorkerPath()
	serviceRuntimeRoot = filepath.Clean(serviceRuntimeRoot)
	if !windowsRuntimeDirectory(packageRoot) || !windowsRuntimeDirectory(serviceRuntimeRoot) {
		return "", "", fmt.Errorf("service: invalid Windows runtime root")
	}
	resolved, err := fixedWindowsNodeRuntime(serviceRuntimeRoot, options.workerCommand)
	if err != nil {
		return "", "", fmt.Errorf("service: Windows job pool requires the service-owned node.exe runtime")
	}
	script := filepath.Clean(workerScript)
	if options.backend == backendNode && runtime.WorkerName() != environment.WorkerEntrypointName {
		return "", "", fmt.Errorf("service: signed environment has no deferred worker entrypoint")
	}
	if options.backend != backendNode && runtime.WorkerName() != environment.HeadlessEntrypointName {
		return "", "", fmt.Errorf("service: signed environment has no CDP worker entrypoint")
	}
	if !windowsRuntimeFile(packageRoot, script) {
		return "", "", fmt.Errorf("service: packaged browser worker entry point is unavailable")
	}
	return filepath.Clean(resolved), script, nil
}

func fixedWindowsNodeRuntime(runtimeRoot, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", fmt.Errorf("empty worker runtime")
	}
	resolved := requested
	if filepath.IsAbs(requested) {
		resolved = filepath.Clean(requested)
	} else {
		if requested != "node" && !strings.EqualFold(requested, "node.exe") {
			return "", fmt.Errorf("unsupported worker runtime")
		}
		resolved = filepath.Join(runtimeRoot, "node.exe")
	}
	resolved, err := filepath.Abs(resolved)
	if err != nil || !strings.EqualFold(filepath.Base(resolved), "node.exe") || !windowsRuntimeFile(runtimeRoot, resolved) {
		return "", fmt.Errorf("worker runtime is outside package")
	}
	return resolved, nil
}

func resolveServiceWorkerCommand(options serviceOptions, windowsPool bool) (string, error) {
	if windowsPool {
		executable, err := os.Executable()
		if err != nil {
			return "", err
		}
		return fixedWindowsNodeRuntime(filepath.Dir(executable), options.workerCommand)
	}
	return exec.LookPath(options.workerCommand)
}

func windowsRuntimeDirectory(path string) bool {
	attrs, err := windowsRuntimeAttributes(path)
	return err == nil && attrs&0x10 != 0
}

func windowsRuntimeFile(root, path string) bool {
	if !filepath.IsAbs(path) || !filepath.IsAbs(root) {
		return false
	}
	root, path = filepath.Clean(root), filepath.Clean(path)
	rel, err := filepath.Rel(strings.ToLower(root), strings.ToLower(path))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false
	}
	if !windowsRuntimeDirectory(root) {
		return false
	}
	volume := filepath.VolumeName(path)
	if volume == "" {
		return false
	}
	rest := strings.TrimLeft(strings.TrimPrefix(path, volume), `\/`)
	current := volume + string(filepath.Separator)
	if _, err := windowsRuntimeAttributes(current); err != nil {
		return false
	}
	for _, component := range strings.FieldsFunc(rest, func(r rune) bool { return r == '\\' || r == '/' }) {
		current = filepath.Join(current, component)
		attrs, err := windowsRuntimeAttributes(current)
		if err != nil || attrs&0x400 != 0 {
			return false
		}
		if current != path && attrs&0x10 == 0 {
			return false
		}
	}
	attrs, err := windowsRuntimeAttributes(path)
	return err == nil && attrs&0x10 == 0 && attrs&0x400 == 0
}

func windowsRuntimeAttributes(path string) (uint32, error) {
	wide, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.GetFileAttributes(wide)
}
