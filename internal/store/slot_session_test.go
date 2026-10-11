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
	if _, _, err := database.StartSlotSession(SlotSessionMutation{SlotID: first.SlotID, Actor: "ui", IdempotencyKey: "different-key", RequestedAt: storeTestTime.Add(4)}); !errors.Is(err, ErrSlotSessionActionConflict) {
		t.Fatalf("duplicate active operation err=%v", err)
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

func TestStartSlotSessionExplicitDeadlineIsPartOfIdempotencyPayload(t *testing.T) {
	database, _ := openTestStore(t)
	pool := testPoolConfig(1)
	if err := database.ReconcileJobPool(pool, storeTestTime); err != nil {
		t.Fatal(err)
	}
	deadline := storeTestTime.Add(30 * time.Second)
	if _, _, err := database.StartSlotSession(SlotSessionMutation{PoolID: pool.PoolID, Actor: "ui", IdempotencyKey: "deadline-key", Deadline: deadline, RequestedAt: storeTestTime}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.StartSlotSession(SlotSessionMutation{PoolID: pool.PoolID, Actor: "ui", IdempotencyKey: "deadline-key", Deadline: deadline.Add(time.Second), RequestedAt: storeTestTime.Add(time.Second)}); !errors.Is(err, ErrSlotSessionIdempotencyConflict) {
		t.Fatalf("deadline conflict=%v", err)
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

func TestStartSlotSessionSkipsQuarantinedAndRetiringSlots(t *testing.T) {
	database, _ := openTestStore(t)
	if err := database.ReconcileJobPool(testPoolConfig(2), storeTestTime); err != nil {
		t.Fatal(err)
	}
	if err := database.MarkSlotReady("pool-test-001", testEnvironmentSummary(1), storeTestTime.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := database.QuarantineSlot("pool-test-001", storeTestTime.Add(time.Second), "test"); err != nil {
		t.Fatal(err)
	}
	operation, _, err := database.StartSlotSession(SlotSessionMutation{PoolID: "pool-test", Actor: "ui", IdempotencyKey: "select-2", RequestedAt: storeTestTime.Add(2 * time.Second)})
	if err != nil || operation.SlotID != "pool-test-002" {
		t.Fatalf("operation=%#v err=%v", operation, err)
	}
}

func TestSlotSessionClaimRecoveryAndLateCompletionFence(t *testing.T) {
	database, _ := openTestStore(t)
	if err := database.ReconcileJobPool(testPoolConfig(1), storeTestTime); err != nil {
		t.Fatal(err)
	}
	op, _, err := database.StartSlotSession(SlotSessionMutation{PoolID: "pool-test", Actor: "ui", IdempotencyKey: "claim", RequestedAt: storeTestTime, RetryBudget: 1})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := database.ClaimSlotSessionOperation(op.OperationID, "controller-a", storeTestTime, time.Minute)
	if err != nil || claimed.State != SlotSessionProvisioning {
		t.Fatalf("claimed=%#v err=%v", claimed, err)
	}
	if _, err := database.ClaimSlotSessionOperation(op.OperationID, "controller-b", storeTestTime.Add(time.Second), time.Minute); !errors.Is(err, ErrSlotSessionClaim) {
		t.Fatalf("live claim err=%v", err)
	}
	recovered, err := database.ClaimSlotSessionOperation(op.OperationID, "controller-b", storeTestTime.Add(2*time.Minute), time.Minute)
	if err != nil || recovered.ClaimOwner != "controller-b" {
		t.Fatalf("recovered=%#v err=%v", recovered, err)
	}
	if _, err := database.CompleteSlotSessionOperation(op.OperationID, "controller-a", 0, SlotSessionReady, SlotSessionPhaseWorker, "", storeTestTime.Add(2*time.Minute+time.Second)); !errors.Is(err, ErrSlotSessionClaim) {
		t.Fatalf("late completion err=%v", err)
	}
	completed, err := database.CompleteSlotSessionOperation(op.OperationID, "controller-b", 0, SlotSessionFailed, SlotSessionPhaseAgent, "agent_unavailable", storeTestTime.Add(2*time.Minute+time.Second))
	if err != nil || completed.State != SlotSessionFailed || completed.ClaimOwner != "" {
		t.Fatalf("completed=%#v err=%v", completed, err)
	}
	retry, idempotent, err := database.RetrySlotSession(SlotSessionAction{OperationID: op.OperationID, Actor: "ui", IdempotencyKey: "retry", At: storeTestTime.Add(3 * time.Minute)})
	if err != nil || idempotent || retry.OperationID == op.OperationID || retry.Attempt != 2 {
		t.Fatalf("retry=%#v idempotent=%v err=%v", retry, idempotent, err)
	}
	repeat, idempotent, err := database.RetrySlotSession(SlotSessionAction{OperationID: op.OperationID, Actor: "ui", IdempotencyKey: "retry", At: storeTestTime.Add(4 * time.Minute)})
	if err != nil || !idempotent || repeat.OperationID != retry.OperationID {
		t.Fatalf("repeat=%#v idempotent=%v err=%v", repeat, idempotent, err)
	}
}

func TestStopReadySlotSessionChangesDesiredIntent(t *testing.T) {
	database, _ := openTestStore(t)
	if err := database.ReconcileJobPool(testPoolConfig(1), storeTestTime); err != nil {
		t.Fatal(err)
	}
	if err := database.MarkSlotReady("pool-test-001", testEnvironmentSummary(1), storeTestTime.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	op, _, err := database.StartSlotSession(SlotSessionMutation{PoolID: "pool-test", Actor: "ui", IdempotencyKey: "ready", RequestedAt: storeTestTime.Add(2 * time.Second)})
	if err != nil || op.State != SlotSessionReady {
		t.Fatalf("start=%#v err=%v", op, err)
	}
	stopping, _, err := database.StopSlotSession(SlotSessionAction{OperationID: op.OperationID, Actor: "ui", IdempotencyKey: "stop", At: storeTestTime.Add(3 * time.Second)})
	if err != nil || stopping.State != SlotSessionStopping || stopping.DesiredState != "stopped" {
		t.Fatalf("stop=%#v err=%v", stopping, err)
	}
	current, err := database.GetSlot(op.SlotID)
	if err != nil || current.SessionDesiredState != "stopped" {
		t.Fatalf("slot=%#v err=%v", current, err)
	}
	status, err := database.SlotPoolStatus("pool-test", storeTestTime.Add(3*time.Second))
	if err != nil || status.Ready != 0 || status.Stopped != 1 {
		t.Fatalf("status=%#v err=%v", status, err)
	}
	if _, err := database.ClaimSlotSessionOperation(stopping.OperationID, "controller", storeTestTime.Add(3*time.Second), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CompleteSlotSessionOperation(stopping.OperationID, "controller", 0, SlotSessionStopped, SlotSessionPhaseStopped, "", storeTestTime.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	started, _, err := database.StartSlotSession(SlotSessionMutation{SlotID: op.SlotID, Actor: "ui", IdempotencyKey: "revive", RequestedAt: storeTestTime.Add(4 * time.Second)})
	if err != nil || started.SlotID != op.SlotID {
		t.Fatalf("revive=%#v err=%v", started, err)
	}
	revived, err := database.GetSlot(op.SlotID)
	if err != nil || revived.Status != slot.Provisioning || revived.SessionDesiredState != "running" {
		t.Fatalf("revived slot=%#v err=%v", revived, err)
	}
}

func TestSlotSessionActionKeyCannotChangeActionPayload(t *testing.T) {
	database, _ := openTestStore(t)
	if err := database.ReconcileJobPool(testPoolConfig(1), storeTestTime); err != nil {
		t.Fatal(err)
	}
	op, _, err := database.StartSlotSession(SlotSessionMutation{PoolID: "pool-test", Actor: "ui", IdempotencyKey: "action-payload", RequestedAt: storeTestTime})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.CancelSlotSession(SlotSessionAction{OperationID: op.OperationID, Actor: "ui", IdempotencyKey: "same-action", At: storeTestTime.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.StopSlotSession(SlotSessionAction{OperationID: op.OperationID, Actor: "ui", IdempotencyKey: "same-action", At: storeTestTime.Add(time.Second)}); !errors.Is(err, ErrSlotSessionActionConflict) {
		t.Fatalf("action kind conflict=%v", err)
	}
}

func TestSlotSessionCheckpointFencesOwnerAndGeneration(t *testing.T) {
	database, _ := openTestStore(t)
	if err := database.ReconcileJobPool(testPoolConfig(1), storeTestTime); err != nil {
		t.Fatal(err)
	}
	op, _, err := database.StartSlotSession(SlotSessionMutation{PoolID: "pool-test", Actor: "ui", IdempotencyKey: "checkpoint", RequestedAt: storeTestTime})
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.ClaimSlotSessionOperation(op.OperationID, "controller-a", storeTestTime, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CheckpointSlotSessionOperation(SlotSessionCheckpoint{OperationID: op.OperationID, Owner: "controller-b", Generation: 1, SessionState: "worker", ObservedAt: storeTestTime.Add(time.Second)}); !errors.Is(err, ErrSlotSessionClaim) {
		t.Fatalf("owner fence=%v", err)
	}
	if _, err := database.CheckpointSlotSessionOperation(SlotSessionCheckpoint{OperationID: op.OperationID, Owner: "controller-a", Generation: 1, SessionState: "worker", ObservedAt: storeTestTime.Add(time.Second)}); err != nil {
		t.Fatalf("initial generation=%v", err)
	}
	if _, err := database.CheckpointSlotSessionOperation(SlotSessionCheckpoint{OperationID: op.OperationID, Owner: "controller-a", Generation: 2, SessionState: "worker", ObservedAt: storeTestTime.Add(2 * time.Second)}); !errors.Is(err, ErrSlotSessionLateResult) {
		t.Fatalf("generation fence=%v", err)
	}
	updated, err := database.RecordSlotSessionFacts(op.OperationID, "controller-a", 1, "worker", true, storeTestTime.Add(2*time.Second))
	if err != nil || updated.SessionState != "worker" || !updated.AgentReady {
		t.Fatalf("checkpoint=%#v err=%v", updated, err)
	}
}

func TestSlotSessionRetryBackoffBlocksEarlyClaim(t *testing.T) {
	database, _ := openTestStore(t)
	if err := database.ReconcileJobPool(testPoolConfig(1), storeTestTime); err != nil {
		t.Fatal(err)
	}
	op, _, err := database.StartSlotSession(SlotSessionMutation{PoolID: "pool-test", Actor: "ui", IdempotencyKey: "backoff", RequestedAt: storeTestTime, RetryBudget: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ClaimSlotSessionOperation(op.OperationID, "controller", storeTestTime, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CompleteSlotSessionOperation(op.OperationID, "controller", 0, SlotSessionFailed, SlotSessionPhaseAgent, "temporary", storeTestTime.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	retry, _, err := database.RetrySlotSession(SlotSessionAction{OperationID: op.OperationID, Actor: "ui", IdempotencyKey: "backoff-retry", At: storeTestTime.Add(2 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if retry.NextAttemptAt != storeTestTime.Add(3*time.Second) {
		t.Fatalf("next attempt=%s", retry.NextAttemptAt)
	}
	if _, err := database.ClaimSlotSessionOperation(retry.OperationID, "controller", storeTestTime.Add(2*time.Second), time.Minute); !errors.Is(err, ErrSlotSessionClaim) {
		t.Fatalf("early claim=%v", err)
	}
	if _, err := database.ClaimSlotSessionOperation(retry.OperationID, "controller", storeTestTime.Add(3*time.Second), time.Minute); err != nil {
		t.Fatalf("ready claim=%v", err)
	}
}

func TestSlotSessionTransitionTableRejectsTerminalRevival(t *testing.T) {
	for _, tc := range []struct {
		from SlotSessionOperationState
		to   SlotSessionOperationState
		ok   bool
	}{
		{SlotSessionRequested, SlotSessionProvisioning, true},
		{SlotSessionProvisioning, SlotSessionReady, true},
		{SlotSessionReady, SlotSessionStopping, true},
		{SlotSessionCancelling, SlotSessionCancelled, true},
		{SlotSessionReady, SlotSessionRequested, false},
		{SlotSessionStopped, SlotSessionProvisioning, false},
	} {
		if got := SlotSessionTransitionAllowed(tc.from, tc.to); got != tc.ok {
			t.Fatalf("transition %s -> %s = %v, want %v", tc.from, tc.to, got, tc.ok)
		}
	}
}
