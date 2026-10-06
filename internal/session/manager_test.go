package session

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/internal/config"
	requestservice "github.com/Semcosm/chuzi/internal/request"
	"github.com/Semcosm/chuzi/internal/slot"
	"github.com/Semcosm/chuzi/internal/store"
)

var sessionTestNow = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

type fakeRuntime struct {
	steps  []string
	failAt string
}

func (r *fakeRuntime) call(step string) error {
	r.steps = append(r.steps, step)
	if r.failAt == step {
		return errors.New(step)
	}
	return nil
}
func (r *fakeRuntime) Provision(context.Context, Binding) error    { return r.call("provision") }
func (r *fakeRuntime) AgentReady(context.Context, Binding) error   { return r.call("agent") }
func (r *fakeRuntime) StartWorker(context.Context, Binding) error  { return r.call("worker") }
func (r *fakeRuntime) StartAdapter(context.Context, Binding) error { return r.call("adapter") }
func (r *fakeRuntime) Stop(context.Context, Binding) error         { return r.call("stop") }

type fakeRDP struct {
	issued, revoked int
	fail            bool
}

func (r *fakeRDP) Issue(context.Context, Binding) error {
	r.issued++
	if r.fail {
		return errors.New("broker unavailable")
	}
	return nil
}
func (r *fakeRDP) Revoke(context.Context, Binding) error { r.revoked++; return nil }

func openSessionStore(t *testing.T) *store.Store {
	t.Helper()
	cfg, err := config.New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
func ids() func(string) string {
	n := 0
	return func(kind string) string { n++; return kind + "-" + string(rune('a'+n)) }
}
func prepareSession(t *testing.T, db *store.Store, ready bool) {
	t.Helper()
	if _, err := db.CreateAccount("account-1"); err != nil {
		t.Fatal(err)
	}
	requests, err := requestservice.New(db, func() time.Time { return sessionTestNow }, ids(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := requests.Submit(requestservice.SubmitInput{RequestID: "request-1", AccountID: "account-1", IdempotencyKey: "idem-1", Actor: "test"}); err != nil {
		t.Fatal(err)
	}
	pool := slot.PoolConfig{PoolID: "pool-1", EnvironmentID: "env-base", EnvironmentVersion: "1.0.0", DesiredSlots: 1, MaxConcurrency: 1, Capabilities: []string{"windows-desktop"}}
	if err := db.ReconcileJobPool(pool, sessionTestNow); err != nil {
		t.Fatal(err)
	}
	slots, err := db.ListSlots(pool.PoolID)
	if err != nil {
		t.Fatal(err)
	}
	if ready {
		if err := db.MarkSlotReady(slots[0].SlotID, slot.EnvironmentSummary{EnvironmentID: pool.EnvironmentID, Version: pool.EnvironmentVersion, Generation: 4, Capabilities: pool.Capabilities, Trusted: true, AgentHandle: "agent-1", SessionState: "ready", DesktopReady: true, UpdatedAt: sessionTestNow}, sessionTestNow); err != nil {
			t.Fatal(err)
		}
	}
}
func newManager(t *testing.T, db *store.Store, runtime Runtime, rdp RDPProvider) *Manager {
	t.Helper()
	m, err := New(Config{Store: db, Runtime: runtime, RDP: rdp, Owner: "session-test", NewID: ids(), Clock: func() time.Time { return sessionTestNow }})
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func startInput() StartInput {
	return StartInput{SessionID: "session-1", RequestID: "request-1", AccountID: "account-1", PoolID: "pool-1", EnvironmentID: "env-base", EnvironmentVersion: "1.0.0", AdapterID: "test-adapter", AdapterVersion: "1.0.0", Actor: "ui", LeaseTTL: time.Minute, MaxConcurrency: 1, Now: sessionTestNow}
}

func TestStartSessionRunsClosedProviderChainAndStopReleasesLease(t *testing.T) {
	db := openSessionStore(t)
	prepareSession(t, db, true)
	runtime := &fakeRuntime{}
	rdp := &fakeRDP{}
	manager := newManager(t, db, runtime, rdp)
	record, err := manager.Start(context.Background(), startInput())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if record.Phase != string(RDPAvailable) || record.EnvironmentGeneration != 4 || !record.AgentReady || !record.WorkerReady || !record.AdapterReady {
		t.Fatalf("record=%#v", record)
	}
	if len(runtime.steps) != 4 || rdp.issued != 1 {
		t.Fatalf("steps=%v rdp=%d", runtime.steps, rdp.issued)
	}
	stopped, err := manager.Stop(context.Background(), record.SessionID, FailureCancelled)
	if err != nil || stopped.Phase != string(Stopped) {
		t.Fatalf("stop=%#v err=%v", stopped, err)
	}
	if rdp.revoked != 1 {
		t.Fatalf("rdp revoke=%d", rdp.revoked)
	}
	leases, err := db.ListSlotLeases()
	if err != nil || len(leases) != 0 {
		t.Fatalf("leases=%#v err=%v", leases, err)
	}
	if _, exists, err := db.GetLease("account-1"); err != nil || exists {
		t.Fatalf("account lease after stop = exists:%v err:%v", exists, err)
	}
}

func TestNoCapacityAndAdapterFailureAreStableAndCleanup(t *testing.T) {
	db := openSessionStore(t)
	prepareSession(t, db, false)
	manager := newManager(t, db, &fakeRuntime{}, nil)
	record, err := manager.Start(context.Background(), startInput())
	if err == nil {
		t.Fatalf("expected no capacity error, record=%#v err=%v", record, err)
	}
	if record.FailureCode != FailureNoCapacity {
		t.Fatalf("record=%#v", record)
	}
	db2 := openSessionStore(t)
	prepareSession(t, db2, true)
	runtime := &fakeRuntime{failAt: "adapter"}
	manager2 := newManager(t, db2, runtime, nil)
	record, err = manager2.Start(context.Background(), startInput())
	if err == nil || record.Phase != string(Failed) || record.FailureCode != FailureAdapter {
		t.Fatalf("record=%#v err=%v", record, err)
	}
	leases, _ := db2.ListSlotLeases()
	if len(leases) != 0 {
		t.Fatalf("failed session retained leases: %#v", leases)
	}
	if _, exists, err := db2.GetLease("account-1"); err != nil || exists {
		t.Fatalf("failed session retained account lease: exists:%v err:%v", exists, err)
	}
}

func TestStartRejectsRequestAccountMismatchBeforeClaiming(t *testing.T) {
	db := openSessionStore(t)
	prepareSession(t, db, true)
	if _, err := db.CreateAccount("account-2"); err != nil {
		t.Fatal(err)
	}
	manager := newManager(t, db, &fakeRuntime{}, nil)
	input := startInput()
	input.AccountID = "account-2"
	if _, err := manager.Start(context.Background(), input); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("mismatched account error = %v", err)
	}
	request, err := db.GetRequest("request-1")
	if err != nil || request.State != account.Queued {
		t.Fatalf("request after mismatch = %#v, err=%v", request, err)
	}
	if _, exists, err := db.GetLease("account-1"); err != nil || exists {
		t.Fatalf("account lease after mismatch = exists:%v err:%v", exists, err)
	}
}

func TestGenerationFenceAndRestartRecovery(t *testing.T) {
	db := openSessionStore(t)
	prepareSession(t, db, true)
	manager := newManager(t, db, &fakeRuntime{}, nil)
	record, err := manager.Start(context.Background(), startInput())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.MarkPhase(record.SessionID, 99, WorkerStarting); !errors.Is(err, ErrGenerationFence) {
		t.Fatalf("fence err=%v", err)
	}
	if err := manager.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	recovered, err := manager.Get(record.SessionID)
	if err != nil || recovered.Phase != string(Failed) || recovered.FailureCode != FailureServiceRestarted {
		t.Fatalf("recovered=%#v err=%v", recovered, err)
	}
	leases, _ := db.ListSlotLeases()
	if len(leases) != 0 {
		t.Fatalf("restart retained leases: %#v", leases)
	}
}

func TestRDPUnavailableKeepsRunningWithStableFailure(t *testing.T) {
	db := openSessionStore(t)
	prepareSession(t, db, true)
	manager := newManager(t, db, &fakeRuntime{}, &fakeRDP{fail: true})
	record, err := manager.Start(context.Background(), startInput())
	if err != nil || record.Phase != string(Running) || record.FailureCode != FailureRDP {
		t.Fatalf("record=%#v err=%v", record, err)
	}
}
