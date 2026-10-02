package store

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/internal/slot"
	"github.com/Semcosm/chuzi/migrations"
	"go.etcd.io/bbolt"
)

func testPoolConfig(desired int) slot.PoolConfig {
	return slot.PoolConfig{
		PoolID:             "pool-test",
		EnvironmentID:      "chuzi-environment/v1",
		EnvironmentVersion: "1.0.0",
		DesiredSlots:       desired,
		Capabilities:       []string{"cdp", "windows-desktop"},
		ManifestDigest:     "sha256:test",
		Signer:             "test-signer",
		RequireTrusted:     true,
	}
}

func testEnvironmentSummary(generation uint64) slot.EnvironmentSummary {
	return slot.EnvironmentSummary{
		EnvironmentID:  "chuzi-environment/v1",
		Version:        "1.0.0",
		Generation:     generation,
		Capabilities:   []string{"cdp", "windows-desktop"},
		ManifestDigest: "sha256:test",
		Signer:         "test-signer",
		Trusted:        true,
		UpdatedAt:      storeTestTime,
	}
}

func TestJobPoolReconcileDesiredAndReadyCapacity(t *testing.T) {
	database, _ := openTestStore(t)
	if err := database.ReconcileJobPool(testPoolConfig(0), storeTestTime); err != nil {
		t.Fatal(err)
	}
	items, err := database.ListSlots("pool-test")
	if err != nil || len(items) != 0 {
		t.Fatalf("desired zero slots = %#v, %v", items, err)
	}
	if err := database.ReconcileJobPool(testPoolConfig(3), storeTestTime.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	items, err = database.ListSlots("pool-test")
	if err != nil || len(items) != 3 {
		t.Fatalf("desired three slots = %d, %v", len(items), err)
	}
	status, err := database.SlotPoolStatus("pool-test", storeTestTime.Add(2*time.Second))
	if err != nil || status.Desired != 3 || status.Unprovisioned != 3 || status.Ready != 0 {
		t.Fatalf("unprovisioned status = %#v, %v", status, err)
	}
	if err := database.MarkSlotReady(items[0].SlotID, testEnvironmentSummary(1), storeTestTime.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	status, err = database.SlotPoolStatus("pool-test", storeTestTime.Add(4*time.Second))
	if err != nil || status.Desired != 3 || status.Ready != 1 || status.Unprovisioned != 2 {
		t.Fatalf("ready status = %#v, %v", status, err)
	}
	for _, max := range []struct{ limit, want int }{{1, 1}, {4, 1}} {
		capacity, err := database.EffectiveSlotCapacity("pool-test", max.limit, storeTestTime.Add(4*time.Second))
		if err != nil || capacity != max.want {
			t.Fatalf("effective capacity max=%d = %d, %v; want %d", max.limit, capacity, err, max.want)
		}
	}
}

func TestJobPoolScaleDownUnprovisionedSlotRemainsValid(t *testing.T) {
	database, _ := openTestStore(t)
	now := storeTestTime
	if err := database.ReconcileJobPool(testPoolConfig(1), now); err != nil {
		t.Fatal(err)
	}
	if err := database.ReconcileJobPool(testPoolConfig(0), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	item, err := database.GetSlot("pool-test-001")
	if err != nil || item.Status != slot.Retiring {
		t.Fatalf("unprovisioned slot after scale down = %#v, %v", item, err)
	}
	if err := database.ValidateDatabase(); err != nil {
		t.Fatalf("scaled-down unprovisioned slot validation = %v", err)
	}
}

func TestJobPoolScaleDownDrainsLeasedSlotAndScaleUpRevivesRetiringSlot(t *testing.T) {
	database, _ := openTestStore(t)
	now := storeTestTime
	pool := testPoolConfig(2)
	if err := database.ReconcileJobPool(pool, now); err != nil {
		t.Fatal(err)
	}
	items, err := database.ListSlots(pool.PoolID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if err := database.MarkSlotReady(item.SlotID, testEnvironmentSummary(uint64(item.Ordinal)), now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.CreateAccount("account-1"); err != nil {
		t.Fatal(err)
	}
	request := createRequest(t, database, "request-1", "account-1", "idem-1", now)
	if _, _, err := database.CreateRequest(request); err != nil {
		t.Fatal(err)
	}
	applyEvent(t, database, "event-queued-1", "account-1", "request-1", account.NoRequest, account.Queued, now)
	applyEvent(t, database, "event-starting-1", "account-1", "request-1", account.Queued, account.Starting, now.Add(time.Second))
	if _, err := database.AcquireLease("account-1", now, "lease-1", "owner-1", time.Minute); err != nil {
		t.Fatal(err)
	}
	lease, leased, err := database.AcquireSlotLease(now, pool.PoolID, pool.Requirement(), "request-1", "account-1", "owner-1", "lease-1-slot", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.ReconcileJobPool(slot.PoolConfig{PoolID: pool.PoolID, EnvironmentID: pool.EnvironmentID, EnvironmentVersion: pool.EnvironmentVersion, DesiredSlots: 0, Capabilities: pool.Capabilities, RequireTrusted: true}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	shrunk, err := database.GetSlot(leased.SlotID)
	if err != nil || shrunk.Status != slot.Draining {
		t.Fatalf("leased slot after scale down = %#v, %v", shrunk, err)
	}
	if err := database.ValidateDatabase(); err != nil {
		t.Fatalf("draining slot validation = %v", err)
	}
	if err := database.ReleaseSlotLease(leased.SlotID, lease.LeaseID, lease.Owner); err != nil {
		t.Fatal(err)
	}
	retiring, err := database.GetSlot(leased.SlotID)
	if err != nil || retiring.Status != slot.Retiring {
		t.Fatalf("released slot after scale down = %#v, %v", retiring, err)
	}
	if err := database.ReconcileJobPool(pool, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	revived, err := database.GetSlot(leased.SlotID)
	if err != nil || revived.Status != slot.Ready {
		t.Fatalf("slot after scale up = %#v, %v", revived, err)
	}
}

func TestSlotLeaseAcquireHeartbeatReleaseAndExpiryRecovery(t *testing.T) {
	database, _ := openTestStore(t)
	now := storeTestTime
	if err := database.ReconcileJobPool(testPoolConfig(1), now); err != nil {
		t.Fatal(err)
	}
	items, err := database.ListSlots("pool-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MarkSlotReady(items[0].SlotID, testEnvironmentSummary(1), now); err != nil {
		t.Fatal(err)
	}
	requirement := testPoolConfig(1).Requirement()
	lease, value, err := database.AcquireSlotLease(now, "pool-test", requirement, "request-1", "account-1", "owner-1", "lease-1", time.Minute)
	if err != nil || value.Status != slot.Leased || lease.SlotID != value.SlotID {
		t.Fatalf("first acquire = %#v, %#v, %v", lease, value, err)
	}
	duplicate, duplicateValue, err := database.AcquireSlotLease(now.Add(time.Second), "pool-test", requirement, "request-1", "account-1", "owner-1", "lease-1", time.Minute)
	if err != nil || duplicate != lease || duplicateValue.SlotID != value.SlotID {
		t.Fatalf("duplicate acquire = %#v, %#v, %v", duplicate, duplicateValue, err)
	}
	if _, _, err := database.AcquireSlotLease(now, "pool-test", requirement, "request-2", "account-2", "owner-2", "lease-2", time.Minute); !errors.Is(err, slot.ErrSlotUnavailable) {
		t.Fatalf("competing acquire = %v, want slot unavailable", err)
	}
	heartbeat, err := database.HeartbeatSlotLease(value.SlotID, now.Add(30*time.Second), lease.LeaseID, lease.Owner, time.Minute)
	if err != nil || !heartbeat.ExpiresAt.After(lease.ExpiresAt) {
		t.Fatalf("heartbeat = %#v, %v", heartbeat, err)
	}
	healthy, err := database.GetSlot(value.SlotID)
	if err != nil || !healthy.HealthAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("slot health after heartbeat = %#v, %v", healthy, err)
	}
	if err := database.ReleaseSlotLease(value.SlotID, "stale-lease", "owner-2"); !errors.Is(err, slot.ErrLeaseNotOwned) {
		t.Fatalf("stale release = %v", err)
	}
	if err := database.QuarantineSlotLease(value.SlotID, heartbeat.LeaseID, "owner-2", now.Add(30*time.Second), "stale completion"); !errors.Is(err, slot.ErrLeaseNotOwned) {
		t.Fatalf("stale quarantine = %v", err)
	}
	if err := database.ReleaseSlotLease(value.SlotID, heartbeat.LeaseID, heartbeat.Owner); err != nil {
		t.Fatal(err)
	}
	if err := database.ReleaseSlotLease(value.SlotID, heartbeat.LeaseID, heartbeat.Owner); err != nil {
		t.Fatalf("repeat release = %v", err)
	}
	lease, value, err = database.AcquireSlotLease(now.Add(2*time.Minute), "pool-test", requirement, "request-3", "account-3", "owner-3", "lease-3", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if replacement, replacementSlot, err := database.AcquireSlotLease(lease.ExpiresAt, "pool-test", requirement, "request-4", "account-4", "owner-4", "lease-4", time.Minute); err != nil || replacement.LeaseID != "lease-4" || replacementSlot.Status != slot.Leased {
		t.Fatalf("expired lease replacement = %#v, %#v, %v", replacement, replacementSlot, err)
	}
	if err := database.ReleaseSlotLease(value.SlotID, "lease-3", "owner-3"); !errors.Is(err, slot.ErrLeaseNotOwned) {
		t.Fatalf("stale release after expiry replacement = %v", err)
	}
	lease, value, err = database.AcquireSlotLease(lease.ExpiresAt.Add(time.Minute), "pool-test", requirement, "request-5", "account-5", "owner-5", "lease-5", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.ReleaseSlotLease(value.SlotID, heartbeat.LeaseID, heartbeat.Owner); !errors.Is(err, slot.ErrLeaseNotOwned) {
		t.Fatalf("stale release after newer lease = %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(database.Config())
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if err := restarted.RecoverExpiredSlotLeases(lease.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	status, err := restarted.SlotPoolStatus("pool-test", lease.ExpiresAt)
	if err != nil || status.Ready != 1 || status.Leased != 0 {
		t.Fatalf("recovered expired lease status = %#v, %v", status, err)
	}
	if err := restarted.QuarantineSlot(value.SlotID, lease.ExpiresAt.Add(time.Second), "test failure"); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err = Open(database.Config())
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	status, err = restarted.SlotPoolStatus("pool-test", lease.ExpiresAt.Add(2*time.Second))
	if err != nil || status.Quarantined != 1 || status.Ready != 0 {
		t.Fatalf("quarantine after restart = %#v, %v", status, err)
	}
}

func TestConcurrentSlotAcquireAllowsOneOwner(t *testing.T) {
	database, _ := openTestStore(t)
	if err := database.ReconcileJobPool(testPoolConfig(1), storeTestTime); err != nil {
		t.Fatal(err)
	}
	items, err := database.ListSlots("pool-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MarkSlotReady(items[0].SlotID, testEnvironmentSummary(1), storeTestTime); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 1; i <= 2; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := database.AcquireSlotLease(storeTestTime, "pool-test", testPoolConfig(1).Requirement(), "request-"+string(rune('0'+i)), "account-"+string(rune('0'+i)), "owner-"+string(rune('0'+i)), "lease-"+string(rune('0'+i)), time.Minute)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	var acquired, unavailable int
	for err := range results {
		if err == nil {
			acquired++
		} else if errors.Is(err, slot.ErrSlotUnavailable) {
			unavailable++
		} else {
			t.Fatalf("concurrent acquire error = %v", err)
		}
	}
	if acquired != 1 || unavailable != 1 {
		t.Fatalf("concurrent acquire counts = acquired %d unavailable %d", acquired, unavailable)
	}
}

func TestValidateDatabaseRejectsOrphanedSlotLease(t *testing.T) {
	database, _ := openTestStore(t)
	now := storeTestTime
	pool := testPoolConfig(1)
	if err := database.ReconcileJobPool(pool, now); err != nil {
		t.Fatal(err)
	}
	item, err := database.GetSlot(pool.PoolID + "-001")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MarkSlotReady(item.SlotID, testEnvironmentSummary(1), now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateAccount("account-validate"); err != nil {
		t.Fatal(err)
	}
	request := createRequest(t, database, "request-validate", "account-validate", "idem-validate", now)
	if _, _, err := database.CreateRequest(request); err != nil {
		t.Fatal(err)
	}
	applyEvent(t, database, "event-validate-queued", request.AccountID, request.RequestID, account.NoRequest, account.Queued, now)
	applyEvent(t, database, "event-validate-starting", request.AccountID, request.RequestID, account.Queued, account.Starting, now.Add(time.Second))
	if _, err := database.AcquireLease(request.AccountID, now, "lease-validate", "owner-validate", time.Minute); err != nil {
		t.Fatal(err)
	}
	lease, _, err := database.AcquireSlotLease(now, pool.PoolID, pool.Requirement(), request.RequestID, request.AccountID, "owner-validate", "lease-validate-slot", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte(migrations.SlotLeasesBucket))
		var corrupted slot.Lease
		if err := json.Unmarshal(bucket.Get([]byte(lease.SlotID)), &corrupted); err != nil {
			return err
		}
		corrupted.RequestID = "missing-request"
		raw, err := json.Marshal(corrupted)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(lease.SlotID), raw)
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.ValidateDatabase(); !errors.Is(err, ErrCorruptData) {
		t.Fatalf("orphaned slot lease validation = %v, want ErrCorruptData", err)
	}
}

func TestExpiredDuplicateSlotAcquireStartsAFreshLease(t *testing.T) {
	database, _ := openTestStore(t)
	now := storeTestTime
	if err := database.ReconcileJobPool(testPoolConfig(1), now); err != nil {
		t.Fatal(err)
	}
	items, err := database.ListSlots("pool-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MarkSlotReady(items[0].SlotID, testEnvironmentSummary(1), now); err != nil {
		t.Fatal(err)
	}
	first, value, err := database.AcquireSlotLease(now, "pool-test", testPoolConfig(1).Requirement(), "request-1", "account-1", "owner-1", "lease-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	fresh, freshValue, err := database.AcquireSlotLease(first.ExpiresAt, "pool-test", testPoolConfig(1).Requirement(), "request-1", "account-1", "owner-1", "lease-1", time.Minute)
	if err != nil {
		t.Fatalf("expired duplicate acquire = %#v, %#v, %v", fresh, freshValue, err)
	}
	if !fresh.AcquiredAt.Equal(first.ExpiresAt) || freshValue.SlotID != value.SlotID {
		t.Fatalf("fresh lease = %#v, slot=%#v", fresh, freshValue)
	}
}
