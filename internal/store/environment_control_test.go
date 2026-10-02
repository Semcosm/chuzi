package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/environment"
)

func TestEnvironmentOperationIsIdempotentAndAudited(t *testing.T) {
	database, _ := openTestStore(t)
	mutation := EnvironmentMutation{EnvironmentID: "env/v1", Version: "1.0.0", Operation: "trust", IdempotencyKey: "env-key", Actor: "operator", RequestedAt: storeTestTime}
	first, idempotent, err := database.ApplyEnvironmentOperation(mutation)
	if err != nil || idempotent || first.State != "requested" {
		t.Fatalf("first operation = %#v, idempotent=%v, err=%v", first, idempotent, err)
	}
	if first.EnvironmentGeneration != 0 {
		t.Fatalf("initial generation = %d", first.EnvironmentGeneration)
	}
	repeated, idempotent, err := database.ApplyEnvironmentOperation(mutation)
	if err != nil || !idempotent || repeated.OperationID != first.OperationID {
		t.Fatalf("repeat operation = %#v, idempotent=%v, err=%v", repeated, idempotent, err)
	}
	if _, _, err := database.ApplyEnvironmentOperation(EnvironmentMutation{EnvironmentID: mutation.EnvironmentID, Version: mutation.Version, Operation: mutation.Operation, PackageRef: "other", IdempotencyKey: mutation.IdempotencyKey, Actor: mutation.Actor, RequestedAt: mutation.RequestedAt}); !errors.Is(err, ErrEnvironmentIdempotencyConflict) {
		t.Fatalf("changed payload error = %v", err)
	}
	if _, err := database.UpdateEnvironmentOperation(first.OperationID, "failed", "environment_untrusted", storeTestTime.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	audit, err := database.ListEnvironmentAudit(mutation.EnvironmentID, mutation.Version, 10)
	if err != nil || len(audit) != 2 || audit[0].Actor != mutation.Actor || audit[1].FailureCode != "environment_untrusted" {
		t.Fatalf("audit = %#v, err=%v", audit, err)
	}
}

func TestEnvironmentGateRejectsUntrustedEnable(t *testing.T) {
	database, _ := openTestStore(t)
	record := environment.Record{EnvironmentID: "env/v1", Version: "1.0.0", ManifestDigest: strings.Repeat("b", 64), Signer: "signer", Installed: true, Verified: true, Generation: 1, UpdatedAt: storeTestTime}
	if err := database.PutEnvironmentRecord(record); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ApplyEnvironmentGate(record.EnvironmentID, record.Version, "enable", storeTestTime); !errors.Is(err, environment.ErrNotTrusted) {
		t.Fatalf("enable error = %v", err)
	}
}

func TestEnvironmentOperationRejectsStaleRevision(t *testing.T) {
	database, _ := openTestStore(t)
	record := environment.Record{EnvironmentID: "env/v1", Version: "1.0.0", ManifestDigest: strings.Repeat("c", 64), Signer: "signer", Installed: true, Verified: true, Trusted: true, Enabled: true, Healthy: true, Ready: true, Generation: 2, UpdatedAt: storeTestTime}
	if err := database.PutEnvironmentRecord(record); err != nil {
		t.Fatal(err)
	}
	_, _, err := database.ApplyEnvironmentOperation(EnvironmentMutation{EnvironmentID: record.EnvironmentID, Version: record.Version, Operation: "disable", ExpectedRevision: 1, IdempotencyKey: "stale", Actor: "operator", RequestedAt: storeTestTime})
	if !errors.Is(err, ErrEnvironmentStaleRevision) {
		t.Fatalf("stale environment revision = %v", err)
	}
}

func TestEnvironmentOperationCarriesGeneration(t *testing.T) {
	database, _ := openTestStore(t)
	record := environment.Record{EnvironmentID: "env/v1", Version: "1.0.0", ManifestDigest: strings.Repeat("e", 64), Signer: "signer", Installed: true, Verified: true, Trusted: true, Enabled: true, Healthy: true, Ready: true, Generation: 4, UpdatedAt: storeTestTime}
	if err := database.PutEnvironmentRecord(record); err != nil {
		t.Fatal(err)
	}
	operation, _, err := database.ApplyEnvironmentOperation(EnvironmentMutation{EnvironmentID: record.EnvironmentID, Version: record.Version, Operation: "disable", IdempotencyKey: "generation-key", Actor: "operator", RequestedAt: storeTestTime})
	if err != nil || operation.EnvironmentGeneration != record.Generation {
		t.Fatalf("operation = %#v, err=%v", operation, err)
	}
	updated, err := database.UpdateEnvironmentOperation(operation.OperationID, "applied", "", storeTestTime.Add(time.Second))
	if err != nil || updated.EnvironmentGeneration != record.Generation {
		t.Fatalf("updated operation = %#v, err=%v", updated, err)
	}
	audit, err := database.ListEnvironmentAudit(record.EnvironmentID, record.Version, 10)
	if err != nil || len(audit) != 2 || audit[0].EnvironmentGeneration != record.Generation || audit[1].EnvironmentGeneration != record.Generation {
		t.Fatalf("audit = %#v, err=%v", audit, err)
	}
}

func TestRecoverEnvironmentOperationAfterRestart(t *testing.T) {
	database, _ := openTestStore(t)
	digest := strings.Repeat("f", 64)
	operation, _, err := database.ApplyEnvironmentOperation(EnvironmentMutation{EnvironmentID: "env/v1", Version: "1.0.0", Operation: "install", PackageRef: "catalog-v1", IdempotencyKey: "restart-key", Actor: "operator", RequestedAt: storeTestTime})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.UpdateEnvironmentOperation(operation.OperationID, "provisioning", "", storeTestTime.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := database.PutEnvironmentRecord(environment.Record{EnvironmentID: "env/v1", Version: "1.0.0", ManifestDigest: digest, Signer: "signer", Installed: true, Verified: true, Trusted: true, Enabled: true, Healthy: true, Ready: true, Generation: 1, UpdatedAt: storeTestTime.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := database.RecoverEnvironmentOperations(storeTestTime.Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	recovered, err := database.GetEnvironmentOperation(operation.OperationID)
	if err != nil || recovered.State != "applied" || recovered.FailureCode != "" {
		t.Fatalf("recovered operation = %#v, err=%v", recovered, err)
	}
}

func TestCompleteEnvironmentOperationCommitsRecordAndAuditTogether(t *testing.T) {
	database, _ := openTestStore(t)
	operation, _, err := database.ApplyEnvironmentOperation(EnvironmentMutation{EnvironmentID: "env/v1", Version: "1.0.0", Operation: "trust", IdempotencyKey: "atomic-key", Actor: "operator", RequestedAt: storeTestTime})
	if err != nil {
		t.Fatal(err)
	}
	record := environment.Record{EnvironmentID: "env/v1", Version: "1.0.0", ManifestDigest: strings.Repeat("1", 64), Signer: "signer", Installed: true, Verified: true, Trusted: true, Enabled: true, Healthy: true, Ready: true, Generation: 2, UpdatedAt: storeTestTime.Add(time.Second)}
	if _, err := database.CompleteEnvironmentOperation(operation.OperationID, "applied", "", record, storeTestTime.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	stored, err := database.GetEnvironmentRecord(record.EnvironmentID, record.Version)
	if err != nil || stored.Generation != 2 {
		t.Fatalf("record = %#v, err=%v", stored, err)
	}
	audit, err := database.ListEnvironmentAudit(record.EnvironmentID, record.Version, 10)
	if err != nil || len(audit) != 2 || audit[1].ToState != "applied" || audit[1].EnvironmentGeneration != 2 {
		t.Fatalf("audit = %#v, err=%v", audit, err)
	}
}
