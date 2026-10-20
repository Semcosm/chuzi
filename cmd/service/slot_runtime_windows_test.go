//go:build windows

package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/environment"
	"github.com/Semcosm/chuzi/internal/observability"
	"github.com/Semcosm/chuzi/internal/servicehost"
	"github.com/Semcosm/chuzi/internal/slot"
	"github.com/Semcosm/chuzi/internal/slotlifecycle"
	"github.com/Semcosm/chuzi/internal/slotwindows"
	"github.com/Semcosm/chuzi/internal/store"
)

type serviceSessionBootstrapper struct{}

func (serviceSessionBootstrapper) Start(context.Context, slotwindows.ManagedIdentity) (slotwindows.BootstrapSession, error) {
	return slotwindows.BootstrapSession{ID: 17, State: "active"}, nil
}

func (serviceSessionBootstrapper) Stop(context.Context, slotwindows.ManagedIdentity, slotwindows.BootstrapSession) error {
	return nil
}

func TestResolveSessionBootstrapperUsesInjectedProvider(t *testing.T) {
	fake := serviceSessionBootstrapper{}
	resolved := resolveSessionBootstrapper(serviceOptions{sessionBootstrapper: fake}, nil)
	if _, ok := resolved.(serviceSessionBootstrapper); !ok {
		t.Fatalf("resolved provider = %T, want injected fake", resolved)
	}
}

func TestResolveSessionBootstrapperDefaultsToFixedBrokerClient(t *testing.T) {
	resolved := resolveSessionBootstrapper(serviceOptions{}, nil)
	if resolved == nil {
		t.Fatal("default session bootstrapper is nil")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := resolved.Start(ctx, slotwindows.ManagedIdentity{})
	if !errors.Is(err, slotwindows.ErrSessionUnavailable) {
		t.Fatalf("cancelled broker client error = %v", err)
	}
}

func TestBrokerFailureEventSeparatesBrokerStartupFromLogin(t *testing.T) {
	buffer := observability.NewEventBuffer(4)
	broker := &brokerHelperBootstrapper{record: buffer.Record}
	broker.recordFailureEvent("slot-private-id", "broker_start")
	events := buffer.Snapshot(4)
	if len(events) != 1 {
		t.Fatalf("events = %#v", events)
	}
	event := events[0]
	if event.FailureStage != "broker_start" || event.ErrorClass != "session_unavailable" || event.Outcome != "failed" {
		t.Fatalf("broker failure event = %#v", event)
	}
	if strings.Contains(event.Resource, "slot-private-id") {
		t.Fatalf("slot id leaked into event: %#v", event)
	}
}

type dynamicPoolProvisioner struct {
	mu        sync.Mutex
	calls     map[string]int
	stopCalls int
}

type packageGenerationPoolProvisioner struct {
	*dynamicPoolProvisioner
	mu                   sync.Mutex
	packageGeneration    uint64
	provisionGenerations []uint64
	inspectGenerations   []uint64
	stopGenerations      []uint64
}

func (p *packageGenerationPoolProvisioner) UpdatePoolRuntime(context.Context, slot.PoolConfig) error {
	p.mu.Lock()
	p.packageGeneration = 9
	p.mu.Unlock()
	return nil
}

func (p *packageGenerationPoolProvisioner) PackageGeneration() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.packageGeneration
}

func (p *packageGenerationPoolProvisioner) validatePackageGeneration(request slot.ProvisionRequest) error {
	if generation := p.PackageGeneration(); generation != 0 && request.PackageGeneration != generation {
		return slotwindows.ErrInvalidOptions
	}
	return nil
}

func (p *packageGenerationPoolProvisioner) Provision(ctx context.Context, request slot.ProvisionRequest) (slot.ProvisionResult, error) {
	if err := p.validatePackageGeneration(request); err != nil {
		return slot.ProvisionResult{}, err
	}
	p.mu.Lock()
	p.provisionGenerations = append(p.provisionGenerations, request.PackageGeneration)
	p.mu.Unlock()
	return p.dynamicPoolProvisioner.Provision(ctx, request)
}

func (p *packageGenerationPoolProvisioner) Inspect(ctx context.Context, request slot.ProvisionRequest) (slot.EnvironmentSummary, error) {
	if err := p.validatePackageGeneration(request); err != nil {
		return slot.EnvironmentSummary{}, err
	}
	p.mu.Lock()
	p.inspectGenerations = append(p.inspectGenerations, request.PackageGeneration)
	p.mu.Unlock()
	return p.dynamicPoolProvisioner.Inspect(ctx, request)
}

func (p *packageGenerationPoolProvisioner) StopSession(ctx context.Context, request slot.ProvisionRequest) error {
	if err := p.validatePackageGeneration(request); err != nil {
		return err
	}
	p.mu.Lock()
	p.stopGenerations = append(p.stopGenerations, request.PackageGeneration)
	p.mu.Unlock()
	return p.dynamicPoolProvisioner.StopSession(ctx, request)
}

func (p *dynamicPoolProvisioner) Provision(_ context.Context, request slot.ProvisionRequest) (slot.ProvisionResult, error) {
	p.mu.Lock()
	p.calls[request.PoolID]++
	p.mu.Unlock()
	summary := slot.EnvironmentSummary{
		EnvironmentID:  request.Requirement.EnvironmentID,
		Version:        request.Requirement.Version,
		Generation:     request.EnvironmentGeneration,
		Capabilities:   append([]string(nil), request.Requirement.Capabilities...),
		ManifestDigest: request.Requirement.ManifestDigest,
		Signer:         request.Requirement.Signer,
		Trusted:        true,
		AgentVersion:   "test-agent",
		SessionState:   "ready",
		DesktopReady:   true,
		WorkerReady:    true,
		AgentHandle:    "opaque",
		UpdatedAt:      time.Now().UTC(),
	}
	return slot.ProvisionResult{AgentHandle: "opaque", Summary: summary}, nil
}

func (p *dynamicPoolProvisioner) Inspect(_ context.Context, request slot.ProvisionRequest) (slot.EnvironmentSummary, error) {
	result, err := p.Provision(context.Background(), request)
	return result.Summary, err
}

func (*dynamicPoolProvisioner) Retire(context.Context, slot.ProvisionRequest) error { return nil }

func (p *dynamicPoolProvisioner) StopSession(context.Context, slot.ProvisionRequest) error {
	p.mu.Lock()
	p.stopCalls++
	p.mu.Unlock()
	return nil
}

type failingInspectProvisioner struct {
	*dynamicPoolProvisioner
	err error
}

type retryableHostPreflightFailure struct{}

func (retryableHostPreflightFailure) Error() string   { return "host preflight unavailable" }
func (retryableHostPreflightFailure) Unwrap() error   { return slotwindows.ErrHostPreflight }
func (retryableHostPreflightFailure) Retryable() bool { return true }

type hostPreflightFailureProvisioner struct {
	*dynamicPoolProvisioner
	err error
}

func (p *hostPreflightFailureProvisioner) Provision(context.Context, slot.ProvisionRequest) (slot.ProvisionResult, error) {
	return slot.ProvisionResult{}, p.err
}

func (p *failingInspectProvisioner) Inspect(context.Context, slot.ProvisionRequest) (slot.EnvironmentSummary, error) {
	return slot.EnvironmentSummary{}, p.err
}

type gatedSessionProvisioner struct {
	*dynamicPoolProvisioner
	inspectStarted chan struct{}
	allowInspect   chan struct{}
	inspectOnce    sync.Once
}

func (p *gatedSessionProvisioner) Inspect(ctx context.Context, request slot.ProvisionRequest) (slot.EnvironmentSummary, error) {
	p.inspectOnce.Do(func() { close(p.inspectStarted) })
	select {
	case <-p.allowInspect:
	case <-ctx.Done():
		return slot.EnvironmentSummary{}, ctx.Err()
	}
	return p.dynamicPoolProvisioner.Inspect(ctx, request)
}

type blockedPoolProvisioner struct {
	*dynamicPoolProvisioner
	blockedPool string
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
}

type cancellationProvisioner struct {
	*dynamicPoolProvisioner
	started            chan struct{}
	release            chan struct{}
	startedOnce        sync.Once
	ignoreCancellation bool
	cleanupStarted     chan struct{}
	cleanupRelease     chan struct{}
	cleanupOnce        sync.Once
}

func (p *cancellationProvisioner) Provision(ctx context.Context, request slot.ProvisionRequest) (slot.ProvisionResult, error) {
	p.startedOnce.Do(func() { close(p.started) })
	if p.ignoreCancellation {
		<-p.release
		return p.dynamicPoolProvisioner.Provision(context.Background(), request)
	}
	select {
	case <-p.release:
		return p.dynamicPoolProvisioner.Provision(ctx, request)
	case <-ctx.Done():
		return slot.ProvisionResult{}, ctx.Err()
	}
}

func (p *cancellationProvisioner) StopSession(ctx context.Context, request slot.ProvisionRequest) error {
	if err := p.dynamicPoolProvisioner.StopSession(ctx, request); err != nil {
		return err
	}
	if p.cleanupStarted == nil {
		return nil
	}
	p.cleanupOnce.Do(func() { close(p.cleanupStarted) })
	select {
	case <-p.cleanupRelease:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *blockedPoolProvisioner) Provision(ctx context.Context, request slot.ProvisionRequest) (slot.ProvisionResult, error) {
	if request.PoolID == p.blockedPool {
		p.startedOnce.Do(func() { close(p.started) })
		select {
		case <-p.release:
		case <-ctx.Done():
			return slot.ProvisionResult{}, ctx.Err()
		}
	}
	return p.dynamicPoolProvisioner.Provision(ctx, request)
}

type shutdownProvisioner struct {
	*dynamicPoolProvisioner
	mu            sync.Mutex
	fenceErr      error
	fenced        []slot.Lease
	stopped       []slot.ProvisionRequest
	shutdownCalls int
}

func (p *shutdownProvisioner) StopSlotLease(_ context.Context, lease slot.Lease) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fenced = append(p.fenced, lease)
	return p.fenceErr
}

func (p *shutdownProvisioner) StopSession(_ context.Context, request slot.ProvisionRequest) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopped = append(p.stopped, request)
	return nil
}

func (p *shutdownProvisioner) Shutdown(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.shutdownCalls++
	return nil
}

func TestSlotSessionReadinessStaysFalseUntilInspectPasses(t *testing.T) {
	dataDir := t.TempDir()
	cfg, err := config.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	now := time.Date(2026, time.October, 10, 1, 0, 0, 0, time.UTC)
	digest := strings.Repeat("a", 64)
	pool := slot.PoolConfig{
		PoolID: "pool-readiness", EnvironmentID: "env/readiness", EnvironmentVersion: "1.0.0",
		DesiredSlots: 1, Capabilities: []string{"desktop"}, ManifestDigest: digest, Signer: "test-signer", RequireTrusted: true,
	}
	if err := database.PutEnvironmentRecord(environment.Record{
		EnvironmentID: pool.EnvironmentID, Version: pool.EnvironmentVersion, Capabilities: pool.Capabilities,
		ManifestDigest: digest, Signer: pool.Signer, Installed: true, Verified: true, Trusted: true,
		Enabled: true, Healthy: true, Ready: true, Generation: 1, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.ReconcileJobPool(pool, now); err != nil {
		t.Fatal(err)
	}
	op, _, err := database.StartSlotSession(store.SlotSessionMutation{PoolID: pool.PoolID, Actor: "test", IdempotencyKey: "readiness", RequestedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	provisioner := &gatedSessionProvisioner{
		dynamicPoolProvisioner: &dynamicPoolProvisioner{calls: make(map[string]int)},
		inspectStarted:         make(chan struct{}),
		allowInspect:           make(chan struct{}),
	}
	reconciler := &windowsAllPoolReconciler{
		database: database, provisioner: provisioner, now: func() time.Time { return now },
		provisionTimeout: time.Second, cleanupTimeout: time.Second, owner: "service",
		items: make(map[string]*slotlifecycle.Reconciler), provisioners: make(map[string]slotlifecycle.Provisioner),
	}
	result := make(chan error, 1)
	go func() { result <- reconciler.ReconcileSlotSessionOperations(context.Background(), "service", now) }()
	select {
	case <-provisioner.inspectStarted:
	case <-time.After(time.Second):
		t.Fatal("session Inspect did not start")
	}
	checkpoint, err := database.GetSlotSessionOperation(op.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.AgentReady || checkpoint.WorkerReady || checkpoint.SessionState != "provisioning" {
		t.Fatalf("premature readiness checkpoint: %#v", checkpoint)
	}
	close(provisioner.allowInspect)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	completed, err := database.GetSlotSessionOperation(op.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != store.SlotSessionReady || !completed.AgentReady || !completed.WorkerReady {
		t.Fatalf("completed readiness=%#v", completed)
	}
}

func TestSlotSessionUsesResolvedPackageGenerationForStartInspectAndStop(t *testing.T) {
	database, pool, operation, now := prepareSessionRuntimeTest(t, "package-generation")
	defer database.Close()
	provisioner := &packageGenerationPoolProvisioner{
		dynamicPoolProvisioner: &dynamicPoolProvisioner{calls: make(map[string]int)},
	}
	reconciler := newSessionRuntimeTestReconciler(database, provisioner, pool, now)
	if err := reconciler.ReconcileSlotSessionOperations(context.Background(), "service", now); err != nil {
		t.Fatalf("start reconciliation failed: %v", err)
	}
	ready, err := database.GetSlotSessionOperation(operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if ready.State != store.SlotSessionReady || !ready.AgentReady || !ready.WorkerReady {
		t.Fatalf("start did not reach ready with resolved package generation: %#v", ready)
	}
	if _, _, err := database.StopSlotSession(store.SlotSessionAction{
		OperationID: ready.OperationID, Actor: "test", IdempotencyKey: "stop-package-generation", At: now.Add(time.Second),
	}); err != nil {
		t.Fatalf("stop intent failed: %v", err)
	}
	if err := reconciler.ReconcileSlotSessionOperations(context.Background(), "service", now.Add(time.Second)); err != nil {
		t.Fatalf("stop reconciliation failed: %v", err)
	}
	stopped, err := database.GetSlotSessionOperation(operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.State != store.SlotSessionStopped {
		t.Fatalf("stop did not complete: %#v", stopped)
	}
	provisioner.mu.Lock()
	defer provisioner.mu.Unlock()
	for name, generations := range map[string][]uint64{
		"provision": provisioner.provisionGenerations,
		"inspect":   provisioner.inspectGenerations,
		"stop":      provisioner.stopGenerations,
	} {
		if len(generations) == 0 {
			t.Fatalf("%s did not receive a request", name)
		}
		for _, generation := range generations {
			if generation != 9 {
				t.Fatalf("%s package generation = %d, want resolved generation 9", name, generation)
			}
		}
	}
}

func TestSlotSessionNonRetryableReadinessFailureCleansRuntime(t *testing.T) {
	dataDir := t.TempDir()
	cfg, err := config.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	now := time.Date(2026, time.October, 10, 2, 0, 0, 0, time.UTC)
	digest := strings.Repeat("b", 64)
	pool := slot.PoolConfig{
		PoolID: "pool-readiness-failure", EnvironmentID: "env/readiness-failure", EnvironmentVersion: "1.0.0",
		DesiredSlots: 1, Capabilities: []string{"desktop"}, ManifestDigest: digest, Signer: "test-signer", RequireTrusted: true,
	}
	if err := database.PutEnvironmentRecord(environment.Record{
		EnvironmentID: pool.EnvironmentID, Version: pool.EnvironmentVersion, Capabilities: pool.Capabilities,
		ManifestDigest: digest, Signer: pool.Signer, Installed: true, Verified: true, Trusted: true,
		Enabled: true, Healthy: true, Ready: true, Generation: 1, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.ReconcileJobPool(pool, now); err != nil {
		t.Fatal(err)
	}
	operation, _, err := database.StartSlotSession(store.SlotSessionMutation{PoolID: pool.PoolID, Actor: "test", IdempotencyKey: "readiness-failure", RequestedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	base := &dynamicPoolProvisioner{calls: make(map[string]int)}
	provisioner := &failingInspectProvisioner{dynamicPoolProvisioner: base, err: errors.New("private readiness detail")}
	reconciler := &windowsAllPoolReconciler{
		database: database, provisioner: provisioner, now: func() time.Time { return now },
		provisionTimeout: time.Second, cleanupTimeout: time.Second, owner: "service",
		items: make(map[string]*slotlifecycle.Reconciler), provisioners: make(map[string]slotlifecycle.Provisioner),
	}
	if err := reconciler.ReconcileSlotSessionOperations(context.Background(), "service", now); err != nil {
		t.Fatal(err)
	}
	completed, err := database.GetSlotSessionOperation(operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != store.SlotSessionFailed || completed.Phase != store.SlotSessionPhaseStopped || completed.FailureCode != "readiness_failed" || completed.AgentReady || completed.WorkerReady {
		t.Fatalf("readiness failure operation = %#v", completed)
	}
	value, err := database.GetSlot(operation.SlotID)
	if err != nil {
		t.Fatal(err)
	}
	if value.Status != slot.Stopped {
		t.Fatalf("readiness failure slot status = %s", value.Status)
	}
	base.mu.Lock()
	defer base.mu.Unlock()
	if base.stopCalls != 1 {
		t.Fatalf("readiness failure cleanup calls = %d", base.stopCalls)
	}
}

func TestSlotSessionHostPreflightFailureIsDurablyClassified(t *testing.T) {
	database, pool, operation, now := prepareSessionRuntimeTest(t, "host-preflight", 1)
	defer database.Close()
	preflightReport, preflightErr := servicehost.RunPreflight(context.Background(), servicehost.FactSourceFunc(func(context.Context) ([]servicehost.Fact, error) {
		facts := make([]servicehost.Fact, 0, len(servicehost.RequiredFactNames))
		for _, name := range servicehost.RequiredFactNames {
			fact := servicehost.Fact{Name: name, State: servicehost.FactPass, Reason: "supported"}
			if name == "certificate_policy" {
				fact.State, fact.Reason = servicehost.FactFail, "rdp_certificate_missing"
			}
			facts = append(facts, fact)
		}
		return facts, nil
	}), func() time.Time { return now })
	if !errors.Is(preflightErr, servicehost.ErrPreflight) || preflightReport.Ready {
		t.Fatalf("preflight report=%#v err=%v", preflightReport, preflightErr)
	}
	provisioner := &hostPreflightFailureProvisioner{
		dynamicPoolProvisioner: &dynamicPoolProvisioner{calls: make(map[string]int)},
		err:                    errors.Join(slotwindows.ErrHostPreflight, preflightErr),
	}
	reconciler := newSessionRuntimeTestReconciler(database, provisioner, pool, now)
	if err := reconciler.ReconcileSlotSessionOperations(context.Background(), "service", now); err != nil && !errors.Is(err, slotwindows.ErrHostPreflight) {
		t.Fatalf("host preflight reconcile error = %v", err)
	}
	updated, err := database.GetSlotSessionOperation(operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != store.SlotSessionFailed || updated.Phase != store.SlotSessionPhaseHost || updated.FailureCode != "rdp_certificate_missing" || updated.CompletedAt.IsZero() {
		t.Fatalf("host preflight operation = %#v", updated)
	}
}

func TestSlotSessionUnavailableHostFactSchedulesRetry(t *testing.T) {
	database, pool, operation, now := prepareSessionRuntimeTest(t, "host-preflight-unavailable", 1)
	defer database.Close()
	provisioner := &hostPreflightFailureProvisioner{
		dynamicPoolProvisioner: &dynamicPoolProvisioner{calls: make(map[string]int)},
		err:                    retryableHostPreflightFailure{},
	}
	reconciler := newSessionRuntimeTestReconciler(database, provisioner, pool, now)
	if err := reconciler.ReconcileSlotSessionOperations(context.Background(), "service", now); err != nil && !errors.Is(err, slotwindows.ErrHostPreflight) {
		t.Fatalf("host preflight reconcile error = %v", err)
	}
	updated, err := database.GetSlotSessionOperation(operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != store.SlotSessionRetrying || updated.Phase != store.SlotSessionPhaseHost || updated.FailureCode != "host_preflight_failed" || !updated.CompletedAt.IsZero() {
		t.Fatalf("unavailable host fact operation = %#v", updated)
	}
}

func TestSlotSessionCancelInterruptsProvisionAndCommitsCancelledAfterCleanup(t *testing.T) {
	database, pool, operation, now := prepareSessionRuntimeTest(t, "cancel")
	defer database.Close()
	provisioner := &cancellationProvisioner{
		dynamicPoolProvisioner: &dynamicPoolProvisioner{calls: make(map[string]int)},
		started:                make(chan struct{}), release: make(chan struct{}),
		cleanupStarted: make(chan struct{}), cleanupRelease: make(chan struct{}),
	}
	reconciler := newSessionRuntimeTestReconciler(database, provisioner, pool, now)
	result := make(chan error, 1)
	go func() { result <- reconciler.ReconcileSlotSessionOperations(context.Background(), "service", now) }()
	select {
	case <-provisioner.started:
	case <-time.After(time.Second):
		t.Fatal("Provision did not start")
	}
	if _, _, err := database.CancelSlotSession(store.SlotSessionAction{OperationID: operation.OperationID, Actor: "ui", IdempotencyKey: "cancel", At: now.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-provisioner.cleanupStarted:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not start after cancellation")
	}
	select {
	case reconcileErr := <-result:
		t.Fatalf("cancel reconcile returned before cleanup completed: %v", reconcileErr)
	default:
	}
	cleanupPending, err := database.GetSlotSessionOperation(operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if cleanupPending.State != store.SlotSessionCancelling || cleanupPending.Phase != store.SlotSessionPhaseStopping || !cleanupPending.CompletedAt.IsZero() {
		t.Fatalf("operation became terminal before cleanup completed: %#v", cleanupPending)
	}
	close(provisioner.cleanupRelease)
	select {
	case reconcileErr := <-result:
		if reconcileErr != nil {
			t.Fatalf("cancel reconcile = %v", reconcileErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not finish")
	}
	completed, err := database.GetSlotSessionOperation(operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != store.SlotSessionCancelled || completed.Phase != store.SlotSessionPhaseStopped {
		t.Fatalf("cancelled operation = %#v", completed)
	}
	provisioner.mu.Lock()
	defer provisioner.mu.Unlock()
	if provisioner.stopCalls != 1 {
		t.Fatalf("cleanup calls = %d, want 1", provisioner.stopCalls)
	}
}

func TestSlotSessionLateProvisionResultCannotPublishReadiness(t *testing.T) {
	database, pool, operation, now := prepareSessionRuntimeTest(t, "late")
	defer database.Close()
	provisioner := &cancellationProvisioner{
		dynamicPoolProvisioner: &dynamicPoolProvisioner{calls: make(map[string]int)},
		started:                make(chan struct{}), release: make(chan struct{}), ignoreCancellation: true,
	}
	reconciler := newSessionRuntimeTestReconciler(database, provisioner, pool, now)
	result := make(chan error, 1)
	go func() { result <- reconciler.ReconcileSlotSessionOperations(context.Background(), "service", now) }()
	select {
	case <-provisioner.started:
	case <-time.After(time.Second):
		t.Fatal("Provision did not start")
	}
	if _, _, err := database.CancelSlotSession(store.SlotSessionAction{OperationID: operation.OperationID, Actor: "ui", IdempotencyKey: "cancel-late", At: now.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	close(provisioner.release)
	select {
	case reconcileErr := <-result:
		if reconcileErr != nil {
			t.Fatalf("late completion reconcile = %v", reconcileErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("late completion did not finish")
	}
	completed, err := database.GetSlotSessionOperation(operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != store.SlotSessionCancelled || completed.AgentReady || completed.WorkerReady || completed.Phase != store.SlotSessionPhaseStopped {
		t.Fatalf("late completion operation = %#v", completed)
	}
	provisioner.mu.Lock()
	defer provisioner.mu.Unlock()
	if provisioner.stopCalls != 1 {
		t.Fatalf("late completion cleanup calls = %d, want 1", provisioner.stopCalls)
	}
}

func TestWindowsAllPoolReconcilerDiscoversEveryStoredPool(t *testing.T) {
	dataDir := t.TempDir()
	cfg, err := config.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	now := time.Date(2026, time.October, 10, 0, 0, 0, 0, time.UTC)
	digest := strings.Repeat("a", 64)
	for _, poolID := range []string{"pool-a", "pool-b"} {
		if err := database.PutEnvironmentRecord(environment.Record{
			EnvironmentID: "env/" + poolID, Version: "1.0.0", Capabilities: []string{"desktop"},
			ManifestDigest: digest, Signer: "test-signer", Installed: true, Verified: true, Trusted: true,
			Enabled: true, Healthy: true, Ready: true, Generation: 1, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		if err := database.ReconcileJobPool(slot.PoolConfig{
			PoolID: poolID, EnvironmentID: "env/" + poolID, EnvironmentVersion: "1.0.0",
			DesiredSlots: 1, Capabilities: []string{"desktop"}, ManifestDigest: digest, Signer: "test-signer", RequireTrusted: true,
		}, now); err != nil {
			t.Fatal(err)
		}
	}

	provisioner := &dynamicPoolProvisioner{calls: make(map[string]int)}
	reconciler := &windowsAllPoolReconciler{
		database: database, provisioner: provisioner, now: func() time.Time { return now },
		provisionTimeout: time.Second, cleanupTimeout: time.Second, owner: "service", items: make(map[string]*slotlifecycle.Reconciler),
	}
	if err := reconciler.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(reconciler.items) != 2 {
		t.Fatalf("cached reconcilers = %d, want 2", len(reconciler.items))
	}
	provisioner.mu.Lock()
	defer provisioner.mu.Unlock()
	for _, poolID := range []string{"pool-a", "pool-b"} {
		if provisioner.calls[poolID] == 0 {
			t.Fatalf("pool %q was not provisioned", poolID)
		}
	}
}

func TestWindowsAllPoolReconcilerProgressesAnotherPoolDuringSlowLogin(t *testing.T) {
	dataDir := t.TempDir()
	cfg, err := config.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	now := time.Date(2026, time.October, 10, 3, 0, 0, 0, time.UTC)
	digest := strings.Repeat("c", 64)
	for _, poolID := range []string{"pool-a", "pool-b"} {
		if err := database.PutEnvironmentRecord(environment.Record{
			EnvironmentID: "env/" + poolID, Version: "1.0.0", Capabilities: []string{"desktop"},
			ManifestDigest: digest, Signer: "test-signer", Installed: true, Verified: true, Trusted: true,
			Enabled: true, Healthy: true, Ready: true, Generation: 1, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		if err := database.ReconcileJobPool(slot.PoolConfig{
			PoolID: poolID, EnvironmentID: "env/" + poolID, EnvironmentVersion: "1.0.0",
			DesiredSlots: 1, Capabilities: []string{"desktop"}, ManifestDigest: digest, Signer: "test-signer", RequireTrusted: true,
		}, now); err != nil {
			t.Fatal(err)
		}
	}

	provisioner := &blockedPoolProvisioner{
		dynamicPoolProvisioner: &dynamicPoolProvisioner{calls: make(map[string]int)},
		blockedPool:            "pool-a",
		started:                make(chan struct{}),
		release:                make(chan struct{}),
	}
	reconciler := &windowsAllPoolReconciler{
		database: database, provisioner: provisioner, now: func() time.Time { return now },
		provisionTimeout: time.Second, cleanupTimeout: time.Second, owner: "service",
		items: make(map[string]*slotlifecycle.Reconciler), provisioners: make(map[string]slotlifecycle.Provisioner),
	}
	result := make(chan error, 1)
	go func() { result <- reconciler.ReconcileAll(context.Background()) }()
	select {
	case <-provisioner.started:
	case <-time.After(time.Second):
		t.Fatal("pool A login did not start")
	}
	deadline := time.Now().Add(time.Second)
	for {
		value, getErr := database.GetSlot("pool-b-001")
		if getErr == nil && value.Status == slot.Ready {
			break
		}
		if time.Now().After(deadline) {
			close(provisioner.release)
			t.Fatalf("pool B ordinal 1 did not become ready during pool A login: slot=%#v err=%v", value, getErr)
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(provisioner.release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestWindowsAllPoolShutdownFencesLeaseStopsBootOnlySlotAndClosesProvisioner(t *testing.T) {
	database, pool, now := prepareShutdownPool(t, 2)
	defer database.Close()
	markShutdownSlotReady(t, database, pool, "pool-shutdown-001", "agent-one", now)
	markShutdownSlotReady(t, database, pool, "pool-shutdown-002", "agent-two", now)
	bootOnly, err := database.GetSlot("pool-shutdown-002")
	if err != nil {
		t.Fatal(err)
	}
	bootOnly.Status = slot.Provisioning
	bootOnly.AgentHandle = ""
	bootOnly.HealthAt = time.Time{}
	bootOnly.UpdatedAt = now.Add(time.Second)
	if err := database.UpsertSlot(bootOnly); err != nil {
		t.Fatal(err)
	}
	lease, _, err := database.AcquireSlotLease(now.Add(2*time.Second), pool.PoolID, pool.Requirement(), "request-one", "account-one", "queue-owner", "lease-one", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	provisioner := &shutdownProvisioner{dynamicPoolProvisioner: &dynamicPoolProvisioner{calls: make(map[string]int)}}
	reconciler := &windowsAllPoolReconciler{
		database: database, provisioner: provisioner, now: func() time.Time { return now },
		provisionTimeout: time.Second, cleanupTimeout: time.Second, owner: "service",
		items: make(map[string]*slotlifecycle.Reconciler), provisioners: make(map[string]slotlifecycle.Provisioner),
	}
	if err := reconciler.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, exists, err := database.GetSlotLease(lease.SlotID); err != nil || exists {
		t.Fatalf("shutdown lease remains: exists=%v err=%v", exists, err)
	}
	provisioner.mu.Lock()
	defer provisioner.mu.Unlock()
	if len(provisioner.fenced) != 1 || provisioner.fenced[0] != lease {
		t.Fatalf("shutdown lease fences = %#v, want exact lease %#v", provisioner.fenced, lease)
	}
	stopped := make(map[string]bool, len(provisioner.stopped))
	for _, request := range provisioner.stopped {
		stopped[request.SlotID] = true
	}
	if !stopped[lease.SlotID] || !stopped[bootOnly.SlotID] {
		t.Fatalf("shutdown stopped slots = %#v", provisioner.stopped)
	}
	if provisioner.shutdownCalls != 1 {
		t.Fatalf("provisioner shutdown calls = %d, want 1", provisioner.shutdownCalls)
	}
}

func TestWindowsAllPoolShutdownPreservesLeaseWhenFenceFails(t *testing.T) {
	database, pool, now := prepareShutdownPool(t, 1)
	defer database.Close()
	markShutdownSlotReady(t, database, pool, "pool-shutdown-001", "agent-one", now)
	lease, _, err := database.AcquireSlotLease(now.Add(time.Second), pool.PoolID, pool.Requirement(), "request-one", "account-one", "queue-owner", "lease-one", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	fenceErr := errors.New("lease fence failed")
	provisioner := &shutdownProvisioner{dynamicPoolProvisioner: &dynamicPoolProvisioner{calls: make(map[string]int)}, fenceErr: fenceErr}
	reconciler := &windowsAllPoolReconciler{
		database: database, provisioner: provisioner, now: func() time.Time { return now },
		provisionTimeout: time.Second, cleanupTimeout: time.Second, owner: "service",
		items: make(map[string]*slotlifecycle.Reconciler), provisioners: make(map[string]slotlifecycle.Provisioner),
	}
	if err := reconciler.Shutdown(context.Background()); !errors.Is(err, fenceErr) {
		t.Fatalf("shutdown fence error = %v", err)
	}
	remaining, exists, err := database.GetSlotLease(lease.SlotID)
	if err != nil || !exists || remaining != lease {
		t.Fatalf("failed fence changed durable lease: lease=%#v exists=%v err=%v", remaining, exists, err)
	}
	provisioner.mu.Lock()
	defer provisioner.mu.Unlock()
	if len(provisioner.fenced) != 1 || provisioner.fenced[0] != lease {
		t.Fatalf("failed shutdown fences = %#v", provisioner.fenced)
	}
	if len(provisioner.stopped) != 0 {
		t.Fatalf("protected slot received StopSession: %#v", provisioner.stopped)
	}
	if provisioner.shutdownCalls != 1 {
		t.Fatalf("provisioner shutdown calls = %d, want 1", provisioner.shutdownCalls)
	}
}

func prepareShutdownPool(t *testing.T, desired int) (*store.Store, slot.PoolConfig, time.Time) {
	t.Helper()
	cfg, err := config.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.October, 10, 4, 0, 0, 0, time.UTC)
	digest := strings.Repeat("d", 64)
	pool := slot.PoolConfig{
		PoolID: "pool-shutdown", EnvironmentID: "env/shutdown", EnvironmentVersion: "1.0.0", DesiredSlots: desired,
		Capabilities: []string{"desktop"}, ManifestDigest: digest, Signer: "test-signer", RequireTrusted: true,
	}
	if err := database.PutEnvironmentRecord(environment.Record{
		EnvironmentID: pool.EnvironmentID, Version: pool.EnvironmentVersion, Capabilities: pool.Capabilities,
		ManifestDigest: digest, Signer: pool.Signer, Installed: true, Verified: true, Trusted: true, Enabled: true, Healthy: true, Ready: true, Generation: 1, UpdatedAt: now,
	}); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.ReconcileJobPool(pool, now); err != nil {
		database.Close()
		t.Fatal(err)
	}
	return database, pool, now
}

func markShutdownSlotReady(t *testing.T, database *store.Store, pool slot.PoolConfig, slotID, agentHandle string, now time.Time) {
	t.Helper()
	if err := database.MarkSlotReady(slotID, slot.EnvironmentSummary{
		EnvironmentID: pool.EnvironmentID, Version: pool.EnvironmentVersion, Generation: 1, Capabilities: append([]string(nil), pool.Capabilities...),
		ManifestDigest: pool.ManifestDigest, Signer: pool.Signer, Trusted: true, AgentVersion: "test-agent", SessionState: "ready",
		DesktopReady: true, WorkerReady: true, AgentHandle: agentHandle, UpdatedAt: now,
	}, now); err != nil {
		t.Fatal(err)
	}
}

func prepareSessionRuntimeTest(t *testing.T, key string, retryBudget ...int) (*store.Store, slot.PoolConfig, store.SlotSessionOperation, time.Time) {
	t.Helper()
	cfg, err := config.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.October, 10, 5, 0, 0, 0, time.UTC)
	digest := strings.Repeat("e", 64)
	pool := slot.PoolConfig{PoolID: "pool-runtime-" + key, EnvironmentID: "env/runtime-" + key, EnvironmentVersion: "1.0.0", DesiredSlots: 1, Capabilities: []string{"desktop"}, ManifestDigest: digest, Signer: "test-signer", RequireTrusted: true}
	if err := database.PutEnvironmentRecord(environment.Record{EnvironmentID: pool.EnvironmentID, Version: pool.EnvironmentVersion, Capabilities: pool.Capabilities, ManifestDigest: digest, Signer: pool.Signer, Installed: true, Verified: true, Trusted: true, Enabled: true, Healthy: true, Ready: true, Generation: 1, UpdatedAt: now}); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.ReconcileJobPool(pool, now); err != nil {
		database.Close()
		t.Fatal(err)
	}
	budget := 0
	if len(retryBudget) > 0 {
		budget = retryBudget[0]
	}
	operation, _, err := database.StartSlotSession(store.SlotSessionMutation{PoolID: pool.PoolID, Actor: "test", IdempotencyKey: key, RequestedAt: now, RetryBudget: budget})
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	return database, pool, operation, now
}

func newSessionRuntimeTestReconciler(database *store.Store, provisioner slotlifecycle.Provisioner, pool slot.PoolConfig, now time.Time) *windowsAllPoolReconciler {
	return &windowsAllPoolReconciler{database: database, provisioner: provisioner, now: func() time.Time { return now }, provisionTimeout: time.Second, cleanupTimeout: time.Second, owner: "service", items: make(map[string]*slotlifecycle.Reconciler), provisioners: make(map[string]slotlifecycle.Provisioner)}
}
