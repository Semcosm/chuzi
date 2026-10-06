// Package session closes the request-to-runtime product boundary. It owns
// only runtime lifecycle facts; account business state remains in account and
// queue, while credentials and Windows connection material remain opaque.
package session

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/Semcosm/chuzi/internal/slot"
	"github.com/Semcosm/chuzi/internal/store"
)

type Phase string

const (
	Requested       Phase = "requested"
	SlotClaimed     Phase = "slot_claimed"
	Provisioning    Phase = "provisioning"
	AgentReady      Phase = "agent_ready"
	WorkerStarting  Phase = "worker_starting"
	AdapterStarting Phase = "adapter_starting"
	Running         Phase = "running"
	RDPAvailable    Phase = "rdp_available"
	Stopping        Phase = "stopping"
	Stopped         Phase = "stopped"
	Failed          Phase = "failed"
)

const (
	FailureNoCapacity       = "no_capacity"
	FailureEnvironment      = "environment_untrusted"
	FailurePackage          = "package_unavailable"
	FailureProvision        = "slot_provision_failed"
	FailureAgent            = "agent_unavailable"
	FailureWorker           = "worker_failed"
	FailureAdapter          = "adapter_failed"
	FailureRDP              = "rdp_unavailable"
	FailureCancelled        = "cancelled"
	FailureServiceRestarted = "service_restarted"
	FailureTimeout          = "timeout"
)

var (
	ErrInvalidInput    = errors.New("session: invalid input")
	ErrDuplicate       = errors.New("session: session already exists")
	ErrGenerationFence = errors.New("session: stale environment generation")
	ErrNotStoppable    = errors.New("session: session cannot be stopped")
)

// StartInput deliberately has no path, command, executable, desktop, user,
// endpoint, password, pipe or token fields. All runtime material is derived
// from service-owned pool/environment records.
type StartInput struct {
	SessionID          string
	RequestID          string
	AccountID          string
	PoolID             string
	EnvironmentID      string
	EnvironmentVersion string
	AdapterID          string
	AdapterVersion     string
	Actor              string
	Now                time.Time
	LeaseTTL           time.Duration
	MaxConcurrency     int
}

type Binding struct {
	SessionID             string
	RequestID             string
	AccountID             string
	PoolID                string
	SlotID                string
	SlotLeaseID           string
	AccountLeaseID        string
	EnvironmentID         string
	EnvironmentVersion    string
	EnvironmentGeneration uint64
	AgentHandle           string
	AdapterID             string
	AdapterVersion        string
}

// Runtime is the controlled provider chain. Production implementations use
// the Windows provisioner/agent and browser worker protocol; tests use a
// deterministic signed-adapter fixture. Methods receive only the binding.
type Runtime interface {
	Provision(context.Context, Binding) error
	AgentReady(context.Context, Binding) error
	StartWorker(context.Context, Binding) error
	StartAdapter(context.Context, Binding) error
	Stop(context.Context, Binding) error
}

// RDPProvider is optional because deployments may run headless. It receives
// an opaque binding and must keep connection material in the credential
// boundary. Issue returns no endpoint or credential to this package.
type RDPProvider interface {
	Issue(context.Context, Binding) error
	Revoke(context.Context, Binding) error
}

type DurableStore interface {
	GetSession(string) (store.SessionRecord, error)
	GetRequest(string) (store.Request, error)
	ListSessions() ([]store.SessionRecord, error)
	UpsertSession(store.SessionRecord) error
	ClaimRequestWithSlot(time.Time, string, string, string, time.Duration, string, string, string, store.QueueOptions, string, slot.EnvironmentRequirement) (store.SlotClaim, error)
	ReleaseLease(string, string, string) error
	ReleaseSlotLease(string, string, string) error
}

type Config struct {
	Store   DurableStore
	Runtime Runtime
	RDP     RDPProvider
	Owner   string
	NewID   func(string) string
	Clock   func() time.Time
}

type Manager struct {
	store   DurableStore
	runtime Runtime
	rdp     RDPProvider
	owner   string
	newID   func(string) string
	clock   func() time.Time
	mu      sync.Mutex
}

func New(config Config) (*Manager, error) {
	if config.Store == nil || config.Runtime == nil || strings.TrimSpace(config.Owner) == "" || config.NewID == nil {
		return nil, ErrInvalidInput
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	return &Manager{store: config.Store, runtime: config.Runtime, rdp: config.RDP, owner: config.Owner, newID: config.NewID, clock: config.Clock}, nil
}

func (m *Manager) Start(ctx context.Context, input StartInput) (store.SessionRecord, error) {
	if m == nil || ctx == nil || ctx.Err() != nil {
		return store.SessionRecord{}, ErrInvalidInput
	}
	if err := validateInput(input); err != nil {
		return store.SessionRecord{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, err := m.store.GetSession(input.SessionID); err == nil {
		if sameStart(existing, input) {
			return existing, nil
		}
		return store.SessionRecord{}, ErrDuplicate
	} else if !errors.Is(err, store.ErrSessionNotFound) {
		return store.SessionRecord{}, err
	}
	request, err := m.store.GetRequest(input.RequestID)
	if err != nil {
		return store.SessionRecord{}, err
	}
	if request.AccountID != input.AccountID {
		return store.SessionRecord{}, ErrInvalidInput
	}
	now := input.Now
	if now.IsZero() {
		now = m.clock().UTC()
	}
	record := store.SessionRecord{SessionID: input.SessionID, RequestID: input.RequestID, AccountID: input.AccountID, PoolID: input.PoolID, EnvironmentID: input.EnvironmentID, EnvironmentVersion: input.EnvironmentVersion, AdapterID: input.AdapterID, AdapterVersion: input.AdapterVersion, Phase: string(Requested), CreatedAt: now, UpdatedAt: now}
	if err := m.store.UpsertSession(record); err != nil {
		return store.SessionRecord{}, err
	}
	claim, err := m.store.ClaimRequestWithSlot(now, input.RequestID, m.newID("slot-lease"), m.owner, input.LeaseTTL, m.newID("session-claim"), m.owner, "session start", store.QueueOptions{MaxGlobalConcurrency: input.MaxConcurrency}, input.PoolID, slot.EnvironmentRequirement{EnvironmentID: input.EnvironmentID, Version: input.EnvironmentVersion, RequireTrusted: true})
	if err != nil {
		return m.fail(record, classifyClaimFailure(err), err)
	}
	if claim.Claim.Request.RequestID != input.RequestID || claim.Claim.Request.AccountID != input.AccountID {
		_ = m.store.ReleaseLease(claim.Claim.Request.AccountID, claim.Claim.Lease.LeaseID, m.owner)
		_ = m.store.ReleaseSlotLease(claim.Slot.SlotID, claim.SlotLease.LeaseID, m.owner)
		return m.fail(record, FailureNoCapacity, ErrDuplicate)
	}
	binding := Binding{SessionID: input.SessionID, RequestID: claim.Claim.Request.RequestID, AccountID: claim.Claim.Request.AccountID, PoolID: claim.Slot.PoolID, SlotID: claim.Slot.SlotID, SlotLeaseID: claim.SlotLease.LeaseID, AccountLeaseID: claim.Claim.Lease.LeaseID, EnvironmentID: claim.Slot.EnvironmentID, EnvironmentVersion: claim.Slot.EnvironmentVersion, EnvironmentGeneration: claim.Slot.EnvironmentGeneration, AgentHandle: claim.Slot.AgentHandle, AdapterID: input.AdapterID, AdapterVersion: input.AdapterVersion}
	record = m.update(record, SlotClaimed, "")
	record.SlotID, record.SlotLeaseID, record.AccountLeaseID, record.EnvironmentGeneration = binding.SlotID, binding.SlotLeaseID, binding.AccountLeaseID, binding.EnvironmentGeneration
	if err := m.store.UpsertSession(record); err != nil {
		_ = m.store.ReleaseLease(binding.AccountID, binding.AccountLeaseID, m.owner)
		_ = m.store.ReleaseSlotLease(binding.SlotID, binding.SlotLeaseID, m.owner)
		return store.SessionRecord{}, err
	}
	steps := []struct {
		phase Phase
		code  string
		call  func(context.Context) error
	}{
		{Provisioning, FailureProvision, func(c context.Context) error { return m.runtime.Provision(c, binding) }},
		{AgentReady, FailureAgent, func(c context.Context) error { return m.runtime.AgentReady(c, binding) }},
		{WorkerStarting, FailureWorker, func(c context.Context) error { return m.runtime.StartWorker(c, binding) }},
		{AdapterStarting, FailureAdapter, func(c context.Context) error { return m.runtime.StartAdapter(c, binding) }},
	}
	for _, step := range steps {
		record = m.update(record, step.phase, "")
		if err := m.store.UpsertSession(record); err != nil {
			return m.failWithCleanup(ctx, record, binding, step.code, err)
		}
		if err := step.call(ctx); err != nil {
			return m.failWithCleanup(ctx, record, binding, step.code, err)
		}
		switch step.phase {
		case AgentReady:
			record.AgentReady = true
		case WorkerStarting:
			record.WorkerReady = true
		case AdapterStarting:
			record.AdapterReady = true
		}
		if err := m.store.UpsertSession(record); err != nil {
			return m.failWithCleanup(ctx, record, binding, step.code, err)
		}
	}
	record = m.update(record, Running, "")
	if err := m.store.UpsertSession(record); err != nil {
		return m.failWithCleanup(ctx, record, binding, FailureWorker, err)
	}
	if m.rdp != nil {
		if err := m.rdp.Issue(ctx, binding); err == nil {
			record = m.update(record, RDPAvailable, "")
		} else {
			// RDP is an optional presentation capability; keep the runtime
			// running and expose a stable warning for the workspace.
			record = m.update(record, Running, FailureRDP)
		}
		if err := m.store.UpsertSession(record); err != nil {
			return m.failWithCleanup(ctx, record, binding, FailureRDP, err)
		}
	}
	return record, nil
}

func (m *Manager) Stop(ctx context.Context, sessionID, reason string) (store.SessionRecord, error) {
	if m == nil || ctx == nil || strings.TrimSpace(sessionID) == "" {
		return store.SessionRecord{}, ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	record, err := m.store.GetSession(sessionID)
	if err != nil {
		return store.SessionRecord{}, err
	}
	if record.Phase == string(Stopped) || record.Phase == string(Failed) {
		return record, nil
	}
	record = m.update(record, Stopping, "")
	_ = m.store.UpsertSession(record)
	binding := bindingFromRecord(record)
	stopErr := m.runtime.Stop(ctx, binding)
	if m.rdp != nil {
		_ = m.rdp.Revoke(ctx, binding)
	}
	if record.AccountLeaseID != "" {
		_ = m.store.ReleaseLease(record.AccountID, record.AccountLeaseID, m.owner)
	}
	if record.SlotID != "" && record.SlotLeaseID != "" {
		if releaseErr := m.store.ReleaseSlotLease(record.SlotID, record.SlotLeaseID, m.owner); stopErr == nil {
			stopErr = releaseErr
		}
	}
	if stopErr != nil {
		record = m.update(record, Failed, runtimeFailureCode(stopErr))
		_ = m.store.UpsertSession(record)
		return record, stopErr
	}
	record = m.update(record, Stopped, stopReason(reason))
	if err := m.store.UpsertSession(record); err != nil {
		return store.SessionRecord{}, err
	}
	return record, nil
}

func (m *Manager) Get(sessionID string) (store.SessionRecord, error) {
	if m == nil || strings.TrimSpace(sessionID) == "" {
		return store.SessionRecord{}, ErrInvalidInput
	}
	return m.store.GetSession(sessionID)
}

// List returns the deterministic, metadata-only session projections.
func (m *Manager) List() ([]store.SessionRecord, error) {
	if m == nil {
		return nil, ErrInvalidInput
	}
	return m.store.ListSessions()
}

// MarkPhase applies a runtime callback only when its environment generation
// still matches the durable slot binding. Late worker/adapter callbacks are
// therefore harmless after a slot recycle.
func (m *Manager) MarkPhase(sessionID string, generation uint64, phase Phase) (store.SessionRecord, error) {
	if m == nil || strings.TrimSpace(sessionID) == "" || generation == 0 {
		return store.SessionRecord{}, ErrInvalidInput
	}
	record, err := m.store.GetSession(sessionID)
	if err != nil {
		return store.SessionRecord{}, err
	}
	if record.EnvironmentGeneration != generation {
		return store.SessionRecord{}, ErrGenerationFence
	}
	if !phaseValid(phase) {
		return store.SessionRecord{}, ErrInvalidInput
	}
	if record.Phase == string(Stopped) || record.Phase == string(Failed) {
		return record, nil
	}
	record = m.update(record, phase, record.FailureCode)
	if err := m.store.UpsertSession(record); err != nil {
		return store.SessionRecord{}, err
	}
	return record, nil
}

// Recover fences every non-terminal runtime after a service restart. It is
// deliberately idempotent: persisted terminal records and already-released
// leases are not touched, so restart cannot duplicate sessions or capacity.
func (m *Manager) Recover(ctx context.Context) error {
	if m == nil || ctx == nil {
		return ErrInvalidInput
	}
	records, err := m.store.ListSessions()
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.Phase == string(Stopped) || record.Phase == string(Failed) {
			continue
		}
		recovered, _ := m.Stop(ctx, record.SessionID, FailureServiceRestarted)
		recovered.Phase = string(Failed)
		recovered.FailureCode = FailureServiceRestarted
		recovered.UpdatedAt = m.clock().UTC()
		if err := m.store.UpsertSession(recovered); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) fail(record store.SessionRecord, code string, cause error) (store.SessionRecord, error) {
	record = m.update(record, Failed, code)
	if err := m.store.UpsertSession(record); err != nil {
		return store.SessionRecord{}, err
	}
	return record, cause
}

func (m *Manager) failWithCleanup(ctx context.Context, record store.SessionRecord, binding Binding, code string, cause error) (store.SessionRecord, error) {
	_ = m.runtime.Stop(ctx, binding)
	if m.rdp != nil {
		_ = m.rdp.Revoke(ctx, binding)
	}
	if binding.AccountLeaseID != "" {
		_ = m.store.ReleaseLease(binding.AccountID, binding.AccountLeaseID, m.owner)
	}
	if binding.SlotID != "" && binding.SlotLeaseID != "" {
		_ = m.store.ReleaseSlotLease(binding.SlotID, binding.SlotLeaseID, m.owner)
	}
	record = m.update(record, Failed, code)
	if err := m.store.UpsertSession(record); err != nil {
		return store.SessionRecord{}, err
	}
	return record, cause
}

func (m *Manager) update(record store.SessionRecord, phase Phase, failure string) store.SessionRecord {
	record.Phase = string(phase)
	record.FailureCode = failure
	record.UpdatedAt = m.clock().UTC()
	return record
}

func phaseValid(phase Phase) bool {
	switch phase {
	case Requested, SlotClaimed, Provisioning, AgentReady, WorkerStarting, AdapterStarting, Running, RDPAvailable, Stopping, Stopped, Failed:
		return true
	default:
		return false
	}
}

func stopReason(reason string) string {
	switch reason {
	case FailureCancelled, FailureServiceRestarted, FailureTimeout:
		return reason
	default:
		return FailureCancelled
	}
}

func runtimeFailureCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return FailureTimeout
	}
	if errors.Is(err, context.Canceled) {
		return FailureCancelled
	}
	return FailureProvision
}

func bindingFromRecord(record store.SessionRecord) Binding {
	return Binding{SessionID: record.SessionID, RequestID: record.RequestID, AccountID: record.AccountID, PoolID: record.PoolID, SlotID: record.SlotID, SlotLeaseID: record.SlotLeaseID, AccountLeaseID: record.AccountLeaseID, EnvironmentID: record.EnvironmentID, EnvironmentVersion: record.EnvironmentVersion, EnvironmentGeneration: record.EnvironmentGeneration, AdapterID: record.AdapterID, AdapterVersion: record.AdapterVersion}
}

func sameStart(record store.SessionRecord, input StartInput) bool {
	return record.RequestID == input.RequestID && record.AccountID == input.AccountID &&
		record.PoolID == input.PoolID && record.EnvironmentID == input.EnvironmentID &&
		record.EnvironmentVersion == input.EnvironmentVersion && record.AdapterID == input.AdapterID &&
		record.AdapterVersion == input.AdapterVersion
}

func validateInput(input StartInput) error {
	values := []string{input.SessionID, input.RequestID, input.AccountID, input.PoolID, input.EnvironmentID, input.EnvironmentVersion, input.AdapterID, input.AdapterVersion, input.Actor}
	for _, value := range values {
		if value == "" || value != strings.TrimSpace(value) || len(value) > 256 || strings.ContainsAny(value, "\x00\r\n\t\\/") {
			return ErrInvalidInput
		}
	}
	if input.LeaseTTL <= 0 || input.MaxConcurrency < 1 {
		return ErrInvalidInput
	}
	return nil
}

func classifyClaimFailure(err error) string {
	switch {
	case errors.Is(err, store.ErrQueueCapacity), errors.Is(err, slot.ErrSlotUnavailable), errors.Is(err, store.ErrQueueEmpty):
		return FailureNoCapacity
	case errors.Is(err, store.ErrEnvironmentUnavailable):
		return FailureEnvironment
	default:
		return FailurePackage
	}
}

func (p Phase) Terminal() bool { return p == Stopped || p == Failed }
