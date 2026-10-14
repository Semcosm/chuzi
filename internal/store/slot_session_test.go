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

func TestStartAgentSlotRequiresFreshHealthForImmediateReady(t *testing.T) {
	for _, tc := range []struct {
		name      string
		requestAt time.Time
		wantState SlotSessionOperationState
	}{
		{name: "exact freshness boundary is stale", requestAt: storeTestTime.Add(SlotSessionFreshnessWindow), wantState: SlotSessionRequested},
		{name: "inside freshness window is ready", requestAt: storeTestTime.Add(SlotSessionFreshnessWindow - time.Nanosecond), wantState: SlotSessionReady},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database, _ := openTestStore(t)
			pool := testPoolConfig(1)
			if err := database.ReconcileJobPool(pool, storeTestTime); err != nil {
				t.Fatal(err)
			}
			summary := testEnvironmentSummary(1)
			summary.AgentHandle = "agent-1"
			if err := database.MarkSlotReady("pool-test-001", summary, storeTestTime); err != nil {
				t.Fatal(err)
			}
			operation, _, err := database.StartSlotSession(SlotSessionMutation{
				SlotID: "pool-test-001", Actor: "ui", IdempotencyKey: "ready-health", RequestedAt: tc.requestAt,
			})
			if err != nil {
				t.Fatal(err)
			}
			if operation.State != tc.wantState {
				t.Fatalf("operation state = %s, want %s", operation.State, tc.wantState)
			}
			wantReady := tc.wantState == SlotSessionReady
			if operation.AgentReady != wantReady || operation.WorkerReady != wantReady || operation.CompletedAt.IsZero() != !wantReady {
				t.Fatalf("operation readiness = %#v", operation)
			}
		})
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

func TestStartSlotSessionSelectorIsPartOfIdempotencyPayload(t *testing.T) {
	database, _ := openTestStore(t)
	pool := testPoolConfig(1)
	if err := database.ReconcileJobPool(pool, storeTestTime); err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.StartSlotSession(SlotSessionMutation{PoolID: pool.PoolID, Actor: "ui", IdempotencyKey: "selector-key", RequestedAt: storeTestTime}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.StartSlotSession(SlotSessionMutation{SlotID: pool.PoolID + "-001", Actor: "ui", IdempotencyKey: "selector-key", RequestedAt: storeTestTime}); !errors.Is(err, ErrSlotSessionIdempotencyConflict) {
		t.Fatalf("selector conflict=%v", err)
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

func TestReconcileSlotSessionDeadlineDefersToLiveClaim(t *testing.T) {
	database, _ := openTestStore(t)
	if err := database.ReconcileJobPool(testPoolConfig(1), storeTestTime); err != nil {
		t.Fatal(err)
	}
	deadline := storeTestTime.Add(time.Second)
	op, _, err := database.StartSlotSession(SlotSessionMutation{
		PoolID:         "pool-test",
		Actor:          "ui",
		IdempotencyKey: "live-deadline",
		RequestedAt:    storeTestTime,
		Deadline:       deadline,
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := database.ClaimSlotSessionOperation(op.OperationID, "controller", storeTestTime, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.ReconcileSlotSessionOperations(deadline.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	inFlight, err := database.GetSlotSessionOperation(op.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if inFlight.State != SlotSessionProvisioning || inFlight.ClaimOwner != claimed.ClaimOwner || inFlight.ClaimUntil.IsZero() {
		t.Fatalf("live claim was projected at deadline: %#v", inFlight)
	}
	if _, err := database.CompleteSlotSessionOperation(op.OperationID, "controller", 0, SlotSessionReady, SlotSessionPhaseWorker, "", deadline.Add(time.Second)); !errors.Is(err, ErrSlotSessionDeadline) {
		t.Fatalf("completion after deadline err=%v", err)
	}
	completed, err := database.GetSlotSessionOperation(op.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != SlotSessionStopping || completed.FailureCode != "deadline_exceeded" || !completed.CompletedAt.IsZero() || completed.ClaimOwner != "" {
		t.Fatalf("deadline completion=%#v", completed)
	}
	if _, err := database.ClaimSlotSessionOperation(op.OperationID, "cleanup-controller", deadline.Add(2*time.Second), time.Minute); err != nil {
		t.Fatal(err)
	}
	cleaned, err := database.CompleteSlotSessionOperation(op.OperationID, "cleanup-controller", 0, SlotSessionFailed, SlotSessionPhaseStopped, "deadline_exceeded", deadline.Add(2*time.Second))
	if err != nil || cleaned.State != SlotSessionFailed || cleaned.FailureCode != "deadline_exceeded" || cleaned.CompletedAt.IsZero() {
		t.Fatalf("cleanup completion=%#v err=%v", cleaned, err)
	}
}

func TestReconcileSlotSessionDeadlineCreatesDurableCleanupIntentWithoutClaim(t *testing.T) {
	database, _ := openTestStore(t)
	if err := database.ReconcileJobPool(testPoolConfig(1), storeTestTime); err != nil {
		t.Fatal(err)
	}
	deadline := storeTestTime.Add(time.Second)
	op, _, err := database.StartSlotSession(SlotSessionMutation{
		PoolID:         "pool-test",
		Actor:          "ui",
		IdempotencyKey: "unclaimed-deadline",
		RequestedAt:    storeTestTime,
		Deadline:       deadline,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.ReconcileSlotSessionOperations(deadline.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	recovered, err := database.GetSlotSessionOperation(op.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.State != SlotSessionStopping || recovered.Phase != SlotSessionPhaseStopping || recovered.DesiredState != "stopped" || !recovered.CompletedAt.IsZero() || recovered.ClaimOwner != "" {
		t.Fatalf("expired unclaimed operation=%#v", recovered)
	}
	value, err := database.GetSlot(op.SlotID)
	if err != nil {
		t.Fatal(err)
	}
	if value.SessionDesiredState != "stopped" || value.Status != slot.Stopped {
		t.Fatalf("expired operation did not project stopped intent: %#v", value)
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
	if err != nil || op.State != SlotSessionReady || !op.AgentReady || !op.WorkerReady {
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
	if err := database.ReconcileSlotSessionOperations(storeTestTime.Add(3*time.Second + time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	projected, err := database.GetSlotSessionOperation(stopping.OperationID)
	if err != nil || projected.State != SlotSessionStopping {
		t.Fatalf("stop projection=%#v err=%v", projected, err)
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
	completed, err := database.GetSlotSessionOperation(stopping.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.AgentReady || completed.WorkerReady {
		t.Fatalf("stopped operation retained readiness facts: %#v", completed)
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

func TestExpiredStopAndCancelRemainCleanupIntents(t *testing.T) {
	for _, target := range []struct {
		name  string
		stop  func(*Store, SlotSessionAction) (SlotSessionOperation, bool, error)
		state SlotSessionOperationState
	}{
		{name: "stop", stop: func(database *Store, action SlotSessionAction) (SlotSessionOperation, bool, error) {
			return database.StopSlotSession(action)
		}, state: SlotSessionStopping},
		{name: "cancel", stop: func(database *Store, action SlotSessionAction) (SlotSessionOperation, bool, error) {
			return database.CancelSlotSession(action)
		}, state: SlotSessionCancelling},
	} {
		t.Run(target.name, func(t *testing.T) {
			database, _ := openTestStore(t)
			if err := database.ReconcileJobPool(testPoolConfig(1), storeTestTime); err != nil {
				t.Fatal(err)
			}
			if err := database.MarkSlotReady("pool-test-001", testEnvironmentSummary(1), storeTestTime.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			started, _, err := database.StartSlotSession(SlotSessionMutation{SlotID: "pool-test-001", Actor: "ui", IdempotencyKey: "expired-" + target.name, RequestedAt: storeTestTime.Add(2 * time.Second), Deadline: storeTestTime.Add(3 * time.Second)})
			if err != nil {
				t.Fatal(err)
			}
			operation, _, err := target.stop(database, SlotSessionAction{OperationID: started.OperationID, Actor: "ui", IdempotencyKey: "action-" + target.name, At: storeTestTime.Add(4 * time.Second)})
			if err != nil || operation.State != target.state || operation.FailureCode != "deadline_exceeded" || !operation.CompletedAt.IsZero() {
				t.Fatalf("operation=%#v err=%v", operation, err)
			}
		})
	}
}

func TestRetryBudgetExhaustionDefersTerminalFailureUntilCleanup(t *testing.T) {
	database, _ := openTestStore(t)
	if err := database.ReconcileJobPool(testPoolConfig(1), storeTestTime); err != nil {
		t.Fatal(err)
	}
	first, _, err := database.StartSlotSession(SlotSessionMutation{PoolID: "pool-test", Actor: "ui", IdempotencyKey: "retry-exhaustion", RequestedAt: storeTestTime, RetryBudget: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ClaimSlotSessionOperation(first.OperationID, "controller", storeTestTime, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CompleteSlotSessionOperation(first.OperationID, "controller", 0, SlotSessionFailed, SlotSessionPhaseAgent, "temporary", storeTestTime.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	retry, _, err := database.RetrySlotSession(SlotSessionAction{OperationID: first.OperationID, Actor: "ui", IdempotencyKey: "retry-exhaustion-2", At: storeTestTime.Add(2 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ClaimSlotSessionOperation(retry.OperationID, "controller", storeTestTime.Add(3*time.Second), time.Minute); err != nil {
		t.Fatal(err)
	}
	exhausted, err := database.ScheduleSlotSessionRetry(retry.OperationID, "controller", 0, SlotSessionPhaseAgent, "temporary", storeTestTime.Add(4*time.Second))
	if err != nil || exhausted.State != SlotSessionStopping || exhausted.FailureCode != "temporary" || !exhausted.CompletedAt.IsZero() || exhausted.ClaimOwner != "" {
		t.Fatalf("exhausted=%#v err=%v", exhausted, err)
	}
}

func TestCleanupFailureKeepsStoppingIntentRetryable(t *testing.T) {
	database, _ := openTestStore(t)
	if err := database.ReconcileJobPool(testPoolConfig(1), storeTestTime); err != nil {
		t.Fatal(err)
	}
	operation, _, err := database.StartSlotSession(SlotSessionMutation{PoolID: "pool-test", Actor: "ui", IdempotencyKey: "cleanup-failure", RequestedAt: storeTestTime})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ClaimSlotSessionOperation(operation.OperationID, "controller", storeTestTime, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := database.BeginSlotSessionCleanup(operation.OperationID, "controller", 0, false, storeTestTime.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	failedCleanup, err := database.CompleteSlotSessionOperation(operation.OperationID, "controller", 0, SlotSessionStopping, SlotSessionPhaseStopping, "stop_failed", storeTestTime.Add(2*time.Second))
	if err != nil || failedCleanup.State != SlotSessionStopping || failedCleanup.FailureCode != "stop_failed" || !failedCleanup.CompletedAt.IsZero() || failedCleanup.ClaimOwner != "" {
		t.Fatalf("failed cleanup=%#v err=%v", failedCleanup, err)
	}
	if _, err := database.ClaimSlotSessionOperation(operation.OperationID, "cleanup-controller", storeTestTime.Add(4*time.Second), time.Minute); err != nil {
		t.Fatal(err)
	}
	completed, err := database.CompleteSlotSessionOperation(operation.OperationID, "cleanup-controller", 0, SlotSessionFailed, SlotSessionPhaseStopped, "stop_failed", storeTestTime.Add(5*time.Second))
	if err != nil || completed.State != SlotSessionFailed || completed.FailureCode != "stop_failed" || completed.CompletedAt.IsZero() {
		t.Fatalf("terminal cleanup=%#v err=%v", completed, err)
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

func TestSlotSessionRuntimeFactsRejectMalformedValues(t *testing.T) {
	database, _ := openTestStore(t)
	if err := database.ReconcileJobPool(testPoolConfig(1), storeTestTime); err != nil {
		t.Fatal(err)
	}
	op, _, err := database.StartSlotSession(SlotSessionMutation{PoolID: "pool-test", Actor: "ui", IdempotencyKey: "fact-validation", RequestedAt: storeTestTime})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ClaimSlotSessionOperation(op.OperationID, "controller", storeTestTime, time.Minute); err != nil {
		t.Fatal(err)
	}
	checkpoint := SlotSessionCheckpoint{OperationID: op.OperationID, Owner: "controller", Generation: 1, ObservedAt: storeTestTime.Add(time.Second)}
	for name, invalid := range map[string]SlotSessionCheckpoint{
		"phase":                checkpointWithPhase(checkpoint, SlotSessionPhase("unknown")),
		"session state":        checkpointWithSessionState(checkpoint, "ready\n"),
		"worker without agent": {OperationID: checkpoint.OperationID, Owner: checkpoint.Owner, Generation: checkpoint.Generation, WorkerReady: true, ObservedAt: checkpoint.ObservedAt},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := database.CheckpointSlotSessionOperation(invalid); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("checkpoint=%#v err=%v", invalid, err)
			}
		})
	}
	if _, err := database.CompleteSlotSessionOperation(op.OperationID, "controller", 1, SlotSessionReady, SlotSessionPhase("unknown"), "", storeTestTime.Add(2*time.Second)); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid completion phase err=%v", err)
	}
	if _, err := database.CompleteSlotSessionOperation(op.OperationID, "controller", 1, SlotSessionReady, SlotSessionPhaseWorker, "bad failure\n", storeTestTime.Add(2*time.Second)); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid completion failure err=%v", err)
	}
	if _, err := database.ScheduleSlotSessionRetry(op.OperationID, "controller", 1, SlotSessionPhase("unknown"), "temporary", storeTestTime.Add(2*time.Second)); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid retry phase err=%v", err)
	}
	if _, err := database.ScheduleSlotSessionRetry(op.OperationID, "controller", 1, SlotSessionPhaseAgent, "temporary\n", storeTestTime.Add(2*time.Second)); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid retry failure err=%v", err)
	}
}

func checkpointWithPhase(checkpoint SlotSessionCheckpoint, phase SlotSessionPhase) SlotSessionCheckpoint {
	checkpoint.Phase = phase
	return checkpoint
}

func checkpointWithSessionState(checkpoint SlotSessionCheckpoint, state string) SlotSessionCheckpoint {
	checkpoint.SessionState = state
	return checkpoint
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

func TestPruneSlotSessionOperationsRetainsReferencedHistory(t *testing.T) {
	database, _ := openTestStore(t)
	if err := database.ReconcileJobPool(testPoolConfig(1), storeTestTime); err != nil {
		t.Fatal(err)
	}
	first, _, err := database.StartSlotSession(SlotSessionMutation{
		PoolID:         "pool-test",
		Actor:          "ui",
		IdempotencyKey: "prune-start",
		RequestedAt:    storeTestTime,
		RetryBudget:    2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ClaimSlotSessionOperation(first.OperationID, "controller", storeTestTime, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CompleteSlotSessionOperation(first.OperationID, "controller", 0, SlotSessionFailed, SlotSessionPhaseAgent, "temporary", storeTestTime.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	second, _, err := database.RetrySlotSession(SlotSessionAction{OperationID: first.OperationID, Actor: "ui", IdempotencyKey: "prune-retry-1", At: storeTestTime.Add(2 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ClaimSlotSessionOperation(second.OperationID, "controller", storeTestTime.Add(3*time.Second), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CompleteSlotSessionOperation(second.OperationID, "controller", 0, SlotSessionFailed, SlotSessionPhaseAgent, "temporary", storeTestTime.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	third, _, err := database.RetrySlotSession(SlotSessionAction{OperationID: second.OperationID, Actor: "ui", IdempotencyKey: "prune-retry-2", At: storeTestTime.Add(5 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ClaimSlotSessionOperation(third.OperationID, "controller", storeTestTime.Add(7*time.Second), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CompleteSlotSessionOperation(third.OperationID, "controller", 0, SlotSessionFailed, SlotSessionPhaseAgent, "temporary", storeTestTime.Add(8*time.Second)); err != nil {
		t.Fatal(err)
	}

	removed, err := database.PruneSlotSessionOperations(storeTestTime.Add(10*time.Second), 1, time.Hour)
	if err != nil || removed != 1 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
	if _, err := database.GetSlotSessionOperation(first.OperationID); !errors.Is(err, ErrSlotSessionOperationNotFound) {
		t.Fatalf("oldest operation err=%v", err)
	}
	if retained, err := database.GetSlotSessionOperation(second.OperationID); err != nil || retained.State != SlotSessionFailed {
		t.Fatalf("retained operation=%#v err=%v", retained, err)
	}
	if retained, err := database.GetSlotSessionOperation(third.OperationID); err != nil || retained.State != SlotSessionFailed {
		t.Fatalf("referenced operation=%#v err=%v", retained, err)
	}

	removed, err = database.PruneSlotSessionOperations(storeTestTime.Add(24*time.Hour), 1, time.Hour)
	if err != nil || removed != 1 {
		t.Fatalf("age-pruned=%d err=%v", removed, err)
	}
	if _, err := database.GetSlotSessionOperation(second.OperationID); !errors.Is(err, ErrSlotSessionOperationNotFound) {
		t.Fatalf("age-pruned operation err=%v", err)
	}
	if retained, err := database.GetSlotSessionOperation(third.OperationID); err != nil || retained.State != SlotSessionFailed {
		t.Fatalf("referenced operation after age prune=%#v err=%v", retained, err)
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

func TestSlotSessionClaimRenewalAndBoundedRetry(t *testing.T) {
	database, _ := openTestStore(t)
	if err := database.ReconcileJobPool(testPoolConfig(1), storeTestTime); err != nil {
		t.Fatal(err)
	}
	op, _, err := database.StartSlotSession(SlotSessionMutation{PoolID: "pool-test", Actor: "ui", IdempotencyKey: "renew", RequestedAt: storeTestTime, RetryBudget: 1})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := database.ClaimSlotSessionOperation(op.OperationID, "controller", storeTestTime, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	renewed, err := database.RenewSlotSessionOperation(op.OperationID, "controller", storeTestTime.Add(500*time.Millisecond), time.Second)
	if err != nil || !renewed.ClaimUntil.After(claimed.ClaimUntil) {
		t.Fatalf("renewed=%#v claimed=%#v err=%v", renewed, claimed, err)
	}
	if _, err := database.ScheduleSlotSessionRetry(op.OperationID, "controller", 0, SlotSessionPhaseLogin, "temporary", storeTestTime.Add(700*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	retry, err := database.GetSlotSessionOperation(op.OperationID)
	if err != nil || retry.State != SlotSessionRetrying || retry.Attempt != 2 || !retry.NextAttemptAt.Equal(storeTestTime.Add(1700*time.Millisecond)) {
		t.Fatalf("retry=%#v err=%v", retry, err)
	}
}
