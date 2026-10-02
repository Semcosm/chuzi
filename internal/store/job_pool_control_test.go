package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/environment"
	"github.com/Semcosm/chuzi/internal/slot"
)

func TestJobPoolIdempotencyRejectsChangedPayload(t *testing.T) {
	database, _ := openTestStore(t)
	config := slot.PoolConfig{PoolID: "pool-idem", EnvironmentID: "env/v1", EnvironmentVersion: "1.0.0", DesiredSlots: 1, ManifestDigest: strings.Repeat("a", 64), Signer: "signer"}
	if _, _, err := database.ApplyJobPool(JobPoolMutation{Config: config, ExpectedRevision: 0, IdempotencyKey: "same-key", Actor: "operator", RequestedAt: storeTestTime}); err != nil {
		t.Fatal(err)
	}
	config.DesiredSlots = 2
	if _, _, err := database.ApplyJobPool(JobPoolMutation{Config: config, ExpectedRevision: 0, IdempotencyKey: "same-key", Actor: "operator", RequestedAt: storeTestTime}); !errors.Is(err, ErrJobPoolIdempotencyConflict) {
		t.Fatalf("changed idempotency payload error = %v", err)
	}
}

func TestJobPoolControlRevisionIdempotencyAndProjection(t *testing.T) {
	database, _ := openTestStore(t)
	config := slot.PoolConfig{PoolID: "pool-control", EnvironmentID: "env/v1", EnvironmentVersion: "1.0.0", DesiredSlots: 2, MaxConcurrency: 1, ManifestDigest: "digest", Signer: "signer", RequireTrusted: true}
	operation, idempotent, err := database.ApplyJobPool(JobPoolMutation{Config: config, Operation: "apply", ExpectedRevision: 0, IdempotencyKey: "idem-1", Actor: "operator", RequestedAt: storeTestTime})
	if err != nil || idempotent || operation.ConfigRevision != 1 || operation.State != JobPoolRequested {
		t.Fatalf("first apply = %#v, idempotent=%v, err=%v", operation, idempotent, err)
	}
	stored, err := database.GetJobPool("pool-control")
	if err != nil || stored.ConfigRevision != 1 || stored.MaxConcurrency != 1 || stored.UpdatedBy != "operator" {
		t.Fatalf("stored config = %#v, err=%v", stored, err)
	}
	if slots, err := database.ListSlots("pool-control"); err != nil || len(slots) != 2 {
		t.Fatalf("logical slots = %d, err=%v", len(slots), err)
	}
	repeated, idempotent, err := database.ApplyJobPool(JobPoolMutation{Config: config, Operation: "apply", ExpectedRevision: 0, IdempotencyKey: "idem-1", Actor: "operator", RequestedAt: storeTestTime.Add(time.Second)})
	if err != nil || !idempotent || repeated.OperationID != operation.OperationID {
		t.Fatalf("repeat apply = %#v, idempotent=%v, err=%v", repeated, idempotent, err)
	}
	config.DesiredSlots = 3
	if _, _, err := database.ApplyJobPool(JobPoolMutation{Config: config, Operation: "apply", ExpectedRevision: 0, IdempotencyKey: "idem-2", Actor: "operator", RequestedAt: storeTestTime.Add(2 * time.Second)}); !errors.Is(err, ErrJobPoolStaleRevision) {
		t.Fatalf("stale revision = %v", err)
	}
	projection, err := database.ReconcileJobPoolControl("pool-control", storeTestTime.Add(3*time.Second))
	if err != nil || projection.Status.Desired != 2 || projection.Status.Unprovisioned != 2 {
		t.Fatalf("projection = %#v, err=%v", projection, err)
	}
	audit, err := database.ListJobPoolAudit("pool-control", 10)
	if err != nil || len(audit) != 3 || audit[1].ToState != JobPoolValidating || audit[2].ToState != JobPoolProvisioning {
		t.Fatalf("reconcile audit = %#v, err=%v", audit, err)
	}
}

func TestJobPoolOperationCarriesEnvironmentGeneration(t *testing.T) {
	database, _ := openTestStore(t)
	digest := strings.Repeat("d", 64)
	if err := database.PutEnvironmentRecord(environment.Record{EnvironmentID: "env/v1", Version: "1.0.0", ManifestDigest: digest, Signer: "signer", Generation: 7, UpdatedAt: storeTestTime}); err != nil {
		t.Fatal(err)
	}
	operation, _, err := database.ApplyJobPool(JobPoolMutation{Config: slot.PoolConfig{PoolID: "pool-generation", EnvironmentID: "env/v1", EnvironmentVersion: "1.0.0", ManifestDigest: digest, Signer: "signer", DesiredSlots: 1}, IdempotencyKey: "generation-key", Actor: "operator", RequestedAt: storeTestTime})
	if err != nil || operation.EnvironmentGeneration != 7 {
		t.Fatalf("operation = %#v, err=%v", operation, err)
	}
	audit, err := database.ListJobPoolAudit("pool-generation", 10)
	if err != nil || len(audit) != 1 || audit[0].EnvironmentGeneration != 7 {
		t.Fatalf("audit = %#v, err=%v", audit, err)
	}
}

func TestJobPoolDrainRetainsLeasedSlotUntilRelease(t *testing.T) {
	database, _ := openTestStore(t)
	config := slot.PoolConfig{PoolID: "pool-drain", EnvironmentID: "env/v1", EnvironmentVersion: "1.0.0", DesiredSlots: 1, ManifestDigest: "digest", Signer: "signer"}
	if _, _, err := database.ApplyJobPool(JobPoolMutation{Config: config, Operation: "apply", ExpectedRevision: 0, IdempotencyKey: "idem-1", Actor: "operator", RequestedAt: storeTestTime}); err != nil {
		t.Fatal(err)
	}
	if err := database.MarkSlotReady("pool-drain-001", slot.EnvironmentSummary{EnvironmentID: "env/v1", Version: "1.0.0", Generation: 1, ManifestDigest: "digest", Signer: "signer", Trusted: true, UpdatedAt: storeTestTime}, storeTestTime); err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.AcquireSlotLease(storeTestTime, "pool-drain", config.Requirement(), "request-1", "account-1", "owner-1", "lease-1", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.DrainJobPool("pool-drain", 1, "idem-drain", "operator", storeTestTime.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	projection, err := database.ReconcileJobPoolControl("pool-drain", storeTestTime.Add(2*time.Second))
	if err != nil || projection.Status.Leased != 0 || projection.Status.Draining != 1 || projection.ReconcileState != JobPoolDraining {
		t.Fatalf("draining projection = %#v, err=%v", projection, err)
	}
}

func TestFailedEnvironmentReconcileRestoresPreviousReadyGeneration(t *testing.T) {
	database, _ := openTestStore(t)
	digest := strings.Repeat("a", 64)
	readyRecord := environment.Record{EnvironmentID: "env/v1", Version: "1.0.0", Capabilities: []string{"desktop"}, ManifestDigest: digest, Signer: "signer", Installed: true, Verified: true, Trusted: true, Enabled: true, Healthy: true, Ready: true, Generation: 3, UpdatedAt: storeTestTime}
	if err := database.PutEnvironmentRecord(readyRecord); err != nil {
		t.Fatal(err)
	}
	config := slot.PoolConfig{PoolID: "pool-rollback", EnvironmentID: readyRecord.EnvironmentID, EnvironmentVersion: readyRecord.Version, DesiredSlots: 1, Capabilities: readyRecord.Capabilities, ManifestDigest: digest, Signer: readyRecord.Signer, RequireTrusted: true}
	if _, _, err := database.ApplyJobPool(JobPoolMutation{Config: config, ExpectedRevision: 0, IdempotencyKey: "rollback-initial", Actor: "operator", RequestedAt: storeTestTime}); err != nil {
		t.Fatal(err)
	}
	if err := database.MarkSlotReady("pool-rollback-001", slot.EnvironmentSummary{EnvironmentID: config.EnvironmentID, Version: config.EnvironmentVersion, Generation: 1, Capabilities: config.Capabilities, ManifestDigest: digest, Signer: config.Signer, Trusted: true, AgentVersion: "agent", SessionState: "ready", DesktopReady: true, UpdatedAt: storeTestTime}, storeTestTime); err != nil {
		t.Fatal(err)
	}
	changed := config
	changed.EnvironmentID, changed.EnvironmentVersion = "env/v2", "2.0.0"
	changed.ManifestDigest = strings.Repeat("b", 64)
	operation, _, err := database.ApplyJobPool(JobPoolMutation{Config: changed, ExpectedRevision: 1, IdempotencyKey: "rollback-change", Actor: "operator", RequestedAt: storeTestTime.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	projection, err := database.ReconcileJobPoolControl(config.PoolID, storeTestTime.Add(2*time.Second))
	if err != nil || projection.ReconcileState != JobPoolRolledBack || projection.LastFailureCode != "environment_unavailable" {
		t.Fatalf("rollback projection = %#v, err=%v", projection, err)
	}
	stored, err := database.GetJobPool(config.PoolID)
	if err != nil || stored.EnvironmentID != config.EnvironmentID || stored.ConfigRevision != 1 {
		t.Fatalf("restored config = %#v, err=%v", stored, err)
	}
	slots, err := database.ListSlots(config.PoolID)
	if err != nil || len(slots) != 1 || slots[0].Status != slot.Ready || slots[0].EnvironmentGeneration != 1 {
		t.Fatalf("restored slots = %#v, err=%v", slots, err)
	}
	recovered, err := database.GetJobPoolOperation(operation.OperationID)
	if err != nil || recovered.State != JobPoolRolledBack {
		t.Fatalf("operation = %#v, err=%v", recovered, err)
	}
}

func TestRecoverStaleJobPoolOperation(t *testing.T) {
	database, _ := openTestStore(t)
	config := slot.PoolConfig{PoolID: "pool-recover", EnvironmentID: "env/v1", EnvironmentVersion: "1.0.0", DesiredSlots: 1, ManifestDigest: "digest", Signer: "signer"}
	first, _, err := database.ApplyJobPool(JobPoolMutation{Config: config, ExpectedRevision: 0, IdempotencyKey: "recover-one", Actor: "operator", RequestedAt: storeTestTime})
	if err != nil {
		t.Fatal(err)
	}
	config.DesiredSlots = 2
	second, _, err := database.ApplyJobPool(JobPoolMutation{Config: config, ExpectedRevision: 1, IdempotencyKey: "recover-two", Actor: "operator", RequestedAt: storeTestTime.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.RecoverStaleJobPoolOperations(config.PoolID, storeTestTime.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	recovered, err := database.GetJobPoolOperation(first.OperationID)
	if err != nil || recovered.State != JobPoolCancelled || recovered.FailureCode != "superseded" {
		t.Fatalf("stale operation = %#v, err=%v", recovered, err)
	}
	current, err := database.GetJobPoolOperation(second.OperationID)
	if err != nil || current.State == JobPoolCancelled {
		t.Fatalf("current operation = %#v, err=%v", current, err)
	}
}

func TestPoolUpdateDoesNotUnquarantineSlot(t *testing.T) {
	database, _ := openTestStore(t)
	config := slot.PoolConfig{PoolID: "pool-quarantine", EnvironmentID: "env/v1", EnvironmentVersion: "1.0.0", DesiredSlots: 1, ManifestDigest: "digest", Signer: "signer"}
	if err := database.ReconcileJobPool(config, storeTestTime); err != nil {
		t.Fatal(err)
	}
	if err := database.MarkSlotReady("pool-quarantine-001", slot.EnvironmentSummary{EnvironmentID: config.EnvironmentID, Version: config.EnvironmentVersion, Generation: 1, ManifestDigest: config.ManifestDigest, Signer: config.Signer, Trusted: true, AgentVersion: "agent", SessionState: "ready", DesktopReady: true, UpdatedAt: storeTestTime}, storeTestTime); err != nil {
		t.Fatal(err)
	}
	if err := database.QuarantineSlot("pool-quarantine-001", storeTestTime.Add(time.Second), "test failure"); err != nil {
		t.Fatal(err)
	}
	changed := config
	changed.EnvironmentID = "env/v2"
	changed.EnvironmentVersion = "2.0.0"
	if err := database.ReconcileJobPool(changed, storeTestTime.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	slots, err := database.ListSlots(config.PoolID)
	if err != nil || len(slots) != 1 || slots[0].Status != slot.Quarantined {
		t.Fatalf("quarantine after update = %#v, err=%v", slots, err)
	}
}
