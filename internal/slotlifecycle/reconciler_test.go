package slotlifecycle

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/environment"
	"github.com/Semcosm/chuzi/internal/slot"
)

var lifecycleNow = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

type memorySlots struct {
	items  map[string]slot.Slot
	leases map[string]slot.Lease
}

type environmentAuthority struct {
	record environment.Record
	err    error
}

type mutablePoolSource struct{ pool slot.PoolConfig }

func (s mutablePoolSource) GetJobPool(string) (slot.PoolConfig, error) { return s.pool, nil }

func (a environmentAuthority) GetEnvironmentRecord(string, string) (environment.Record, error) {
	return a.record, a.err
}

func (m *memorySlots) ListSlots(pool string) ([]slot.Slot, error) {
	result := []slot.Slot{}
	for _, s := range m.items {
		if s.PoolID == pool {
			result = append(result, s)
		}
	}
	slot.SortSlots(result)
	return result, nil
}
func (m *memorySlots) GetSlotLease(id string) (slot.Lease, bool, error) {
	v, ok := m.leases[id]
	return v, ok, nil
}
func (m *memorySlots) SetSlotStatus(id string, status slot.Status, at time.Time) error {
	v, ok := m.items[id]
	if !ok {
		return slot.ErrSlotNotFound
	}
	if _, leased := m.leases[id]; leased && status != slot.Draining && status != slot.Leased {
		return slot.ErrLeaseHeld
	}
	if v.EnvironmentGeneration == 0 && status != slot.Provisioning && status != slot.Unprovisioned && status != slot.Retiring && status != slot.Deleted {
		return slot.ErrInvalidSlot
	}
	v.Status, v.UpdatedAt = status, at
	m.items[id] = v
	return nil
}
func (m *memorySlots) MarkSlotReady(id string, summary slot.EnvironmentSummary, at time.Time) error {
	v := m.items[id]
	v.EnvironmentID, v.EnvironmentVersion, v.EnvironmentGeneration = summary.EnvironmentID, summary.Version, summary.Generation
	v.Capabilities, v.ManifestDigest, v.Signer, v.Trusted = summary.Capabilities, summary.ManifestDigest, summary.Signer, summary.Trusted
	v.AgentHandle = summary.AgentHandle
	v.Status, v.HealthAt, v.UpdatedAt = slot.Ready, at, at
	m.items[id] = v
	return nil
}

func (m *memorySlots) UpsertSlot(value slot.Slot) error {
	m.items[value.SlotID] = value
	return nil
}

type fakeProvisioner struct {
	calls                               []string
	generations                         []uint64
	summary                             slot.EnvironmentSummary
	inspectErr, provisionErr, retireErr error
	shutdownCalls                       int
	owner                               string
}

type retryableProvisionError struct{}

func (retryableProvisionError) Error() string   { return "session unavailable" }
func (retryableProvisionError) Retryable() bool { return true }

func (f *fakeProvisioner) Provision(_ context.Context, request slot.ProvisionRequest) (slot.ProvisionResult, error) {
	f.calls = append(f.calls, "provision")
	f.generations = append(f.generations, request.EnvironmentGeneration)
	f.owner = request.Owner
	return slot.ProvisionResult{AgentHandle: "opaque", Summary: f.summary}, f.provisionErr
}
func (f *fakeProvisioner) Inspect(_ context.Context, _ slot.ProvisionRequest) (slot.EnvironmentSummary, error) {
	f.calls = append(f.calls, "inspect")
	return f.summary, f.inspectErr
}
func (f *fakeProvisioner) Retire(_ context.Context, _ slot.ProvisionRequest) error {
	f.calls = append(f.calls, "retire")
	return f.retireErr
}
func (f *fakeProvisioner) Shutdown(_ context.Context) error {
	f.shutdownCalls++
	return nil
}

func TestReconcilerShutdownDelegatesToProcessProvisioner(t *testing.T) {
	db := &memorySlots{items: map[string]slot.Slot{}, leases: map[string]slot.Lease{}}
	provisioner := &fakeProvisioner{}
	reconciler, err := New(db, provisioner, validPool(0), func() time.Time { return lifecycleNow }, time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provisioner.shutdownCalls != 1 {
		t.Fatalf("shutdown calls = %d, want 1", provisioner.shutdownCalls)
	}
}

func validSummary(generation uint64) slot.EnvironmentSummary {
	return slot.EnvironmentSummary{EnvironmentID: "env/v1", Version: "1.0", Generation: generation, Capabilities: []string{"desktop"}, Trusted: true, AgentVersion: "1.2.0", SessionState: "ready", DesktopReady: true, UpdatedAt: lifecycleNow}
}
func validPool(desired int) slot.PoolConfig {
	return slot.PoolConfig{PoolID: "pool", EnvironmentID: "env/v1", EnvironmentVersion: "1.0", DesiredSlots: desired, Capabilities: []string{"desktop"}, RequireTrusted: true, UpdatedAt: lifecycleNow}
}
func slotRecord(status slot.Status, generation uint64) slot.Slot {
	return slot.Slot{SlotID: "pool-001", Ordinal: 1, PoolID: "pool", EnvironmentID: "env/v1", EnvironmentGeneration: generation, RequireTrusted: true, Status: status, CreatedAt: lifecycleNow, UpdatedAt: lifecycleNow}
}

func TestReconcileProvisionInspectAndRetire(t *testing.T) {
	db := &memorySlots{items: map[string]slot.Slot{"pool-001": slotRecord(slot.Unprovisioned, 0)}, leases: map[string]slot.Lease{}}
	provisioner := &fakeProvisioner{summary: validSummary(1)}
	reconciler, err := New(db, provisioner, validPool(1), func() time.Time { return lifecycleNow }, time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if db.items["pool-001"].Status != slot.Ready || len(provisioner.calls) != 2 || provisioner.calls[0] != "provision" || provisioner.calls[1] != "inspect" || len(provisioner.generations) != 1 || provisioner.generations[0] != 1 {
		t.Fatalf("provision state=%#v calls=%v", db.items["pool-001"], provisioner.calls)
	}
	if db.items["pool-001"].AgentHandle != "opaque" {
		t.Fatalf("agent handle was not committed: %#v", db.items["pool-001"])
	}
	if provisioner.owner != "service" {
		t.Fatalf("provision owner = %q", provisioner.owner)
	}
	provisioner.calls = nil
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(provisioner.calls) != 1 || provisioner.calls[0] != "inspect" {
		t.Fatalf("healthy reconcile calls=%v", provisioner.calls)
	}

	provisioner.calls = nil
	provisioner.inspectErr = errors.New("private OS detail")
	shrink, err := New(db, provisioner, validPool(0), func() time.Time { return lifecycleNow }, time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	db.leases["pool-001"] = slot.Lease{SlotID: "pool-001", EnvironmentGeneration: 1}
	if err := shrink.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if db.items["pool-001"].Status != slot.Draining || len(provisioner.calls) != 0 {
		t.Fatalf("leased scale-down state=%s calls=%v", db.items["pool-001"].Status, provisioner.calls)
	}
	delete(db.leases, "pool-001")
	if err := shrink.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if db.items["pool-001"].Status != slot.Deleted || len(provisioner.calls) != 1 || provisioner.calls[0] != "retire" {
		t.Fatalf("retirement state=%s calls=%v", db.items["pool-001"].Status, provisioner.calls)
	}
}

func TestReconcileRefreshesDurablePoolConfiguration(t *testing.T) {
	db := &memorySlots{items: map[string]slot.Slot{"pool-001": slotRecord(slot.Unprovisioned, 0)}, leases: map[string]slot.Lease{}}
	provisioner := &fakeProvisioner{summary: validSummary(1)}
	updated := validPool(1)
	reconciler, err := New(db, provisioner, validPool(0), func() time.Time { return lifecycleNow }, time.Minute, time.Minute, mutablePoolSource{pool: updated})
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if db.items["pool-001"].Status != slot.Ready || len(provisioner.calls) != 2 {
		t.Fatalf("durable pool projection was not applied: slot=%#v calls=%v", db.items["pool-001"], provisioner.calls)
	}
}

func TestReconcileRequiresExactReadyEnvironmentAuthority(t *testing.T) {
	db := &memorySlots{items: map[string]slot.Slot{"pool-001": slotRecord(slot.Unprovisioned, 0)}, leases: map[string]slot.Lease{}}
	p := validPool(1)
	p.ManifestDigest, p.Signer = strings.Repeat("a", 64), "signer"
	summary := validSummary(1)
	summary.ManifestDigest, summary.Signer = p.ManifestDigest, p.Signer
	provisioner := &fakeProvisioner{summary: summary}
	authority := environmentAuthority{record: environment.Record{EnvironmentID: p.EnvironmentID, Version: p.EnvironmentVersion, Capabilities: []string{"desktop"}, ManifestDigest: p.ManifestDigest, Signer: p.Signer, Installed: true, Verified: true, Trusted: true, Enabled: true, Healthy: true, Ready: true, Generation: 1, UpdatedAt: lifecycleNow}}
	r, err := New(db, provisioner, p, func() time.Time { return lifecycleNow }, time.Minute, time.Minute, authority, "operator")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provisioner.owner != "operator" {
		t.Fatalf("owner = %q", provisioner.owner)
	}
	bad := authority
	bad.record.Capabilities = nil
	r, err = New(db, provisioner, p, func() time.Time { return lifecycleNow }, time.Minute, time.Minute, bad)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Reconcile(context.Background()); err == nil {
		t.Fatal("authority without required capability was accepted")
	}
}

func TestReconcileFailureDoesNotMarkSlotReady(t *testing.T) {
	db := &memorySlots{items: map[string]slot.Slot{"pool-001": slotRecord(slot.Unprovisioned, 0)}, leases: map[string]slot.Lease{}}
	provisioner := &fakeProvisioner{summary: validSummary(1), provisionErr: errors.New("secret native error")}
	reconciler, _ := New(db, provisioner, validPool(1), func() time.Time { return lifecycleNow }, time.Minute, time.Minute)
	err := reconciler.Reconcile(context.Background())
	if err == nil || err.Error() != "slotlifecycle: provisioning unavailable" {
		t.Fatalf("provision error = %v", err)
	}
	if db.items["pool-001"].Status == slot.Ready {
		t.Fatal("failed provision became schedulable")
	}
}

func TestReconcileHealthGateQuarantinesAfterProvision(t *testing.T) {
	db := &memorySlots{items: map[string]slot.Slot{"pool-001": slotRecord(slot.Unprovisioned, 0)}, leases: map[string]slot.Lease{}}
	provisioner := &fakeProvisioner{summary: validSummary(1), inspectErr: errors.New("health unavailable")}
	reconciler, _ := New(db, provisioner, validPool(1), func() time.Time { return lifecycleNow }, time.Minute, time.Minute)
	if err := reconciler.Reconcile(context.Background()); err == nil || err.Error() != "slotlifecycle: health check failed" {
		t.Fatalf("health gate error = %v", err)
	}
	if db.items["pool-001"].Status != slot.Quarantined {
		t.Fatalf("health failure status = %s, want quarantined", db.items["pool-001"].Status)
	}
	if len(provisioner.calls) != 2 || provisioner.calls[0] != "provision" || provisioner.calls[1] != "inspect" {
		t.Fatalf("lifecycle order = %v", provisioner.calls)
	}
}

func TestReconcileKeepsRetryableSessionFailureProvisioning(t *testing.T) {
	db := &memorySlots{items: map[string]slot.Slot{"pool-001": slotRecord(slot.Unprovisioned, 0)}, leases: map[string]slot.Lease{}}
	provisioner := &fakeProvisioner{summary: validSummary(1), provisionErr: retryableProvisionError{}}
	reconciler, _ := New(db, provisioner, validPool(1), func() time.Time { return lifecycleNow }, time.Minute, time.Minute)
	if err := reconciler.Reconcile(context.Background()); err == nil {
		t.Fatal("retryable provision error unexpectedly succeeded")
	}
	if db.items["pool-001"].Status != slot.Provisioning {
		t.Fatalf("retryable failure quarantined slot: %s", db.items["pool-001"].Status)
	}
}

func TestReconcileLeavesQuarantinedSlotIsolated(t *testing.T) {
	db := &memorySlots{items: map[string]slot.Slot{
		"pool-001": slotRecord(slot.Quarantined, 1),
	}, leases: map[string]slot.Lease{}}
	provisioner := &fakeProvisioner{summary: validSummary(1)}
	reconciler, err := New(db, provisioner, validPool(1), func() time.Time { return lifecycleNow }, time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if db.items["pool-001"].Status != slot.Quarantined || len(provisioner.calls) != 0 {
		t.Fatalf("quarantined slot was retried: status=%s calls=%v", db.items["pool-001"].Status, provisioner.calls)
	}
}

func TestReconcileBumpsGenerationForEnvironmentChange(t *testing.T) {
	db := &memorySlots{items: map[string]slot.Slot{
		"pool-001": {SlotID: "pool-001", Ordinal: 1, PoolID: "pool", EnvironmentID: "env/v1", EnvironmentVersion: "0.9", EnvironmentGeneration: 1, Status: slot.Ready, CreatedAt: lifecycleNow, UpdatedAt: lifecycleNow},
	}, leases: map[string]slot.Lease{}}
	provisioner := &fakeProvisioner{summary: validSummary(2)}
	reconciler, err := New(db, provisioner, validPool(1), func() time.Time { return lifecycleNow }, time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(provisioner.generations) != 1 || provisioner.generations[0] != 2 || db.items["pool-001"].EnvironmentGeneration != 2 {
		t.Fatalf("environment change reused generation: calls=%v slot=%#v", provisioner.generations, db.items["pool-001"])
	}
}

func TestReconcileDrainsLeasedSlotForEnvironmentChange(t *testing.T) {
	db := &memorySlots{items: map[string]slot.Slot{
		"pool-001": {SlotID: "pool-001", Ordinal: 1, PoolID: "pool", EnvironmentID: "env/v1", EnvironmentVersion: "0.9", EnvironmentGeneration: 1, Capabilities: []string{"desktop"}, Status: slot.Leased, CreatedAt: lifecycleNow, UpdatedAt: lifecycleNow},
	}, leases: map[string]slot.Lease{
		"pool-001": {SlotID: "pool-001", PoolID: "pool", EnvironmentGeneration: 1},
	}}
	provisioner := &fakeProvisioner{summary: validSummary(2)}
	reconciler, err := New(db, provisioner, validPool(1), func() time.Time { return lifecycleNow }, time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if db.items["pool-001"].Status != slot.Draining {
		t.Fatalf("leased environment change status = %s", db.items["pool-001"].Status)
	}
	if len(provisioner.calls) != 0 {
		t.Fatalf("leased environment change provisioner calls = %v", provisioner.calls)
	}
}

func TestReconcileAcceptsAdditionalCapabilities(t *testing.T) {
	db := &memorySlots{items: map[string]slot.Slot{
		"pool-001": {SlotID: "pool-001", Ordinal: 1, PoolID: "pool", EnvironmentID: "env/v1", EnvironmentVersion: "1.0", EnvironmentGeneration: 1, Capabilities: []string{"desktop", "screenshot"}, Trusted: true, RequireTrusted: true, Status: slot.Ready, CreatedAt: lifecycleNow, UpdatedAt: lifecycleNow},
	}, leases: map[string]slot.Lease{}}
	provisioner := &fakeProvisioner{summary: validSummary(1)}
	reconciler, err := New(db, provisioner, validPool(1), func() time.Time { return lifecycleNow }, time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(provisioner.calls) != 1 || provisioner.calls[0] != "inspect" {
		t.Fatalf("additional capabilities triggered reprovisioning: %v", provisioner.calls)
	}
}

func TestReconcileTrustPolicyChangeCreatesNewGeneration(t *testing.T) {
	db := &memorySlots{items: map[string]slot.Slot{
		"pool-001": {SlotID: "pool-001", Ordinal: 1, PoolID: "pool", EnvironmentID: "env/v1", EnvironmentVersion: "1.0", EnvironmentGeneration: 3, Capabilities: []string{"desktop"}, Trusted: true, RequireTrusted: false, Status: slot.Ready, CreatedAt: lifecycleNow, UpdatedAt: lifecycleNow},
	}, leases: map[string]slot.Lease{}}
	provisioner := &fakeProvisioner{summary: validSummary(4)}
	pool := validPool(1)
	pool.RequireTrusted = true
	reconciler, err := New(db, provisioner, pool, func() time.Time { return lifecycleNow }, time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(provisioner.generations) != 1 || provisioner.generations[0] != 4 || db.items["pool-001"].EnvironmentGeneration != 4 || !db.items["pool-001"].RequireTrusted {
		t.Fatalf("trust policy generation = %#v calls=%v", db.items["pool-001"], provisioner.calls)
	}
}
