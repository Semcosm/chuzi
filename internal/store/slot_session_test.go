package store

import (
	"errors"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/slot"
)

func TestStartSlotSessionIsIdempotentAndProjectsSlotFacts(t *testing.T) {
	database, _ := openTestStore(t)
	pool := testPoolConfig(1)
	if err := database.ReconcileJobPool(pool, storeTestTime); err != nil {
		t.Fatal(err)
	}
	first, idempotent, err := database.StartSlotSession(SlotSessionMutation{PoolID: pool.PoolID, Actor: "ui", IdempotencyKey: "session-key", RequestedAt: storeTestTime})
	if err != nil || idempotent || first.State != SlotSessionRequested || first.SlotID == "" {
		t.Fatalf("first=%#v idempotent=%v err=%v", first, idempotent, err)
	}
	second, idempotent, err := database.StartSlotSession(SlotSessionMutation{PoolID: pool.PoolID, Actor: "ui", IdempotencyKey: "session-key", RequestedAt: storeTestTime.Add(1)})
	if err != nil || !idempotent || second.OperationID != first.OperationID {
		t.Fatalf("repeat=%#v idempotent=%v err=%v", second, idempotent, err)
	}
	if err := database.MarkSlotReady(first.SlotID, testEnvironmentSummary(1), storeTestTime.Add(2)); err != nil {
		t.Fatal(err)
	}
	if err := database.ReconcileSlotSessionOperations(storeTestTime.Add(3)); err != nil {
		t.Fatal(err)
	}
	current, err := database.GetSlotSessionOperation(first.OperationID)
	if err != nil || current.State != SlotSessionReady || !current.AgentReady || current.SessionState != string(slot.Ready) {
		t.Fatalf("current=%#v err=%v", current, err)
	}
}

func TestStartSlotSessionRejectsIdempotencyPayloadConflict(t *testing.T) {
	database, _ := openTestStore(t)
	pool := testPoolConfig(1)
	if err := database.ReconcileJobPool(pool, storeTestTime); err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.StartSlotSession(SlotSessionMutation{PoolID: pool.PoolID, Actor: "ui", IdempotencyKey: "same", RequestedAt: storeTestTime}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.StartSlotSession(SlotSessionMutation{SlotID: pool.PoolID + "-001", Actor: "other", IdempotencyKey: "same", RequestedAt: storeTestTime}); !errors.Is(err, ErrSlotSessionIdempotencyConflict) {
		t.Fatalf("conflict=%v", err)
	}
}

func TestStartSlotSessionRejectsInvalidTargetAndStaleRevision(t *testing.T) {
	database, _ := openTestStore(t)
	pool := testPoolConfig(1)
	if err := database.ReconcileJobPool(pool, storeTestTime); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []SlotSessionMutation{
		{Actor: "ui", IdempotencyKey: "neither", RequestedAt: storeTestTime},
		{PoolID: pool.PoolID, SlotID: pool.PoolID + "-001", Actor: "ui", IdempotencyKey: "both", RequestedAt: storeTestTime},
	} {
		if _, _, err := database.StartSlotSession(mutation); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("mutation=%#v err=%v", mutation, err)
		}
	}
	current, err := database.GetJobPool(pool.PoolID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.StartSlotSession(SlotSessionMutation{PoolID: pool.PoolID, Actor: "ui", IdempotencyKey: "stale", ExpectedRevision: current.ConfigRevision + 1, RequestedAt: storeTestTime}); !errors.Is(err, ErrSlotSessionRevision) {
		t.Fatalf("stale revision err=%v", err)
	}
	if _, _, err := database.StartSlotSession(SlotSessionMutation{PoolID: pool.PoolID, Actor: "ui", IdempotencyKey: "stale", ExpectedRevision: current.ConfigRevision, RequestedAt: storeTestTime}); err != nil {
		t.Fatalf("failed transaction retained idempotency key: %v", err)
	}
}

func TestReconcileSlotSessionOperationProjectsQuarantineAndMissingOperation(t *testing.T) {
	database, _ := openTestStore(t)
	pool := testPoolConfig(1)
	if err := database.ReconcileJobPool(pool, storeTestTime); err != nil {
		t.Fatal(err)
	}
	operation, _, err := database.StartSlotSession(SlotSessionMutation{PoolID: pool.PoolID, Actor: "ui", IdempotencyKey: "quarantine", RequestedAt: storeTestTime})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.QuarantineSlot(operation.SlotID, storeTestTime.Add(time.Second), "session unavailable"); err != nil {
		t.Fatal(err)
	}
	if err := database.ReconcileSlotSessionOperations(storeTestTime.Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	current, err := database.GetSlotSessionOperation(operation.OperationID)
	if err != nil || current.State != SlotSessionFailed || current.FailureCode != "slot_quarantined" || current.CompletedAt.IsZero() {
		t.Fatalf("operation=%#v err=%v", current, err)
	}
	if _, err := database.GetSlotSessionOperation("slotop-missing"); !errors.Is(err, ErrSlotSessionOperationNotFound) {
		t.Fatalf("missing operation err=%v", err)
	}
}
