package account

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

var stateTestTime = time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)

func newTestEvent(id, accountID, requestID string, from, to Status) Event {
	return Event{
		EventID:    id,
		AccountID:  accountID,
		RequestID:  requestID,
		From:       from,
		To:         to,
		Reason:     "test",
		Actor:      "test-suite",
		OccurredAt: stateTestTime,
	}
}

func applyTestEvent(t *testing.T, state Snapshot, event Event) Snapshot {
	t.Helper()
	event.ExpectedRevision = state.Revision
	result, err := Apply(state, event)
	if err != nil {
		t.Fatalf("Apply(%s -> %s) error = %v", event.From, event.To, err)
	}
	return result.State
}

func stateAt(t *testing.T, status Status) Snapshot {
	t.Helper()
	state, err := NewSnapshot("account-1")
	if err != nil {
		t.Fatal(err)
	}
	if status == NoRequest {
		return state
	}

	state = applyTestEvent(t, state, newTestEvent("queue", "account-1", "request-1", NoRequest, Queued))
	if status == Queued {
		return state
	}
	state = applyTestEvent(t, state, newTestEvent("start", "account-1", "request-1", Queued, Starting))
	if status == Starting {
		return state
	}
	state = applyTestEvent(t, state, newTestEvent("login", "account-1", "request-1", Starting, LoggingIn))
	if status == LoggingIn {
		return state
	}
	if status == LoginFailed {
		return applyTestEvent(t, state, newTestEvent("failed", "account-1", "request-1", LoggingIn, LoginFailed))
	}
	state = applyTestEvent(t, state, newTestEvent("success", "account-1", "request-1", LoggingIn, LoginSucceeded))
	if status == LoginSucceeded {
		return state
	}
	if status == Expired {
		return applyTestEvent(t, state, newTestEvent("expired", "account-1", "request-1", LoginSucceeded, Expired))
	}
	t.Fatalf("stateAt does not support %s", status)
	return Snapshot{}
}

func TestCanTransitionMatchesDocumentedGraph(t *testing.T) {
	statuses := []Status{
		NoRequest, Queued, Starting, LoggingIn, LoginSucceeded,
		LoginFailed, Expired, Cancelled, Blocked,
	}
	wanted := map[Status]map[Status]bool{
		NoRequest: {
			Queued:  true,
			Blocked: true,
		},
		Queued: {
			Starting:  true,
			Cancelled: true,
			Blocked:   true,
		},
		Starting: {
			LoggingIn: true,
			Cancelled: true,
			Blocked:   true,
		},
		LoggingIn: {
			LoginSucceeded: true,
			LoginFailed:    true,
			Cancelled:      true,
			Blocked:        true,
		},
		LoginSucceeded: {
			Expired: true,
			Blocked: true,
		},
		LoginFailed: {
			Queued:  true,
			Blocked: true,
		},
		Expired: {
			Queued:  true,
			Blocked: true,
		},
	}

	for _, from := range statuses {
		for _, to := range statuses {
			want := wanted[from][to]
			if got := CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%s, %s) = %t, want %t", from, to, got, want)
			}
		}
	}
	if CanTransition(Status("UNKNOWN"), Queued) {
		t.Error("unknown state must not transition")
	}
}

func TestApplyCoversDocumentedTransitions(t *testing.T) {
	cases := []struct {
		name string
		from Status
		to   Status
	}{
		{"no-request-to-queued", NoRequest, Queued},
		{"no-request-to-blocked", NoRequest, Blocked},
		{"queued-to-starting", Queued, Starting},
		{"queued-to-cancelled", Queued, Cancelled},
		{"queued-to-blocked", Queued, Blocked},
		{"starting-to-logging-in", Starting, LoggingIn},
		{"starting-to-cancelled", Starting, Cancelled},
		{"starting-to-blocked", Starting, Blocked},
		{"logging-in-to-succeeded", LoggingIn, LoginSucceeded},
		{"logging-in-to-failed", LoggingIn, LoginFailed},
		{"logging-in-to-cancelled", LoggingIn, Cancelled},
		{"logging-in-to-blocked", LoggingIn, Blocked},
		{"login-succeeded-to-expired", LoginSucceeded, Expired},
		{"login-succeeded-to-blocked", LoginSucceeded, Blocked},
		{"login-failed-to-queued", LoginFailed, Queued},
		{"login-failed-to-blocked", LoginFailed, Blocked},
		{"expired-to-queued", Expired, Queued},
		{"expired-to-blocked", Expired, Blocked},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			state := stateAt(t, test.from)
			requestID := state.RequestID
			if state.Status == NoRequest {
				requestID = "request-1"
			}
			event := newTestEvent(test.name, state.AccountID, requestID, test.from, test.to)
			event.ExpectedRevision = state.Revision
			result, err := Apply(state, event)
			if err != nil {
				t.Fatalf("Apply() error = %v", err)
			}
			if result.Idempotent {
				t.Fatal("first application must not be idempotent")
			}
			if result.State.Status != test.to {
				t.Fatalf("state = %s, want %s", result.State.Status, test.to)
			}
			if result.State.Revision != state.Revision+1 || result.Audit.Revision != result.State.Revision {
				t.Fatalf("revision/state audit mismatch: state=%d audit=%d", result.State.Revision, result.Audit.Revision)
			}
		})
	}
}

func TestApplyRecordsAuditAndIsIdempotent(t *testing.T) {
	state, err := NewSnapshot("account-1")
	if err != nil {
		t.Fatal(err)
	}
	event := newTestEvent("event-1", "account-1", "request-1", NoRequest, Queued)
	first, err := Apply(state, event)
	if err != nil {
		t.Fatal(err)
	}
	if first.Idempotent || first.State.Revision != 1 {
		t.Fatalf("unexpected first result: %#v", first)
	}
	if got := first.State.AppliedEvents[event.EventID]; got != first.Audit {
		t.Fatalf("audit index = %#v, audit = %#v", got, first.Audit)
	}
	if first.Audit.Event != event || first.Audit.Revision != 1 {
		t.Fatalf("unexpected audit record: %#v", first.Audit)
	}

	second, err := Apply(first.State, event)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Idempotent || second.Audit != first.Audit {
		t.Fatalf("unexpected duplicate result: %#v", second)
	}
	if !reflect.DeepEqual(second.State, first.State) {
		t.Fatalf("idempotent application changed state: before=%#v after=%#v", first.State, second.State)
	}

	conflict := event
	conflict.Reason = "different"
	if _, err := Apply(first.State, conflict); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("conflicting event error = %v, want ErrEventConflict", err)
	}
}

func TestApplyEnforcesAccountRequestAndFreshState(t *testing.T) {
	state, err := NewSnapshot("account-1")
	if err != nil {
		t.Fatal(err)
	}
	valid := newTestEvent("event-1", "account-1", "request-1", NoRequest, Queued)

	wrongAccount := valid
	wrongAccount.AccountID = "account-2"
	if _, err := Apply(state, wrongAccount); !errors.Is(err, ErrAccountMismatch) {
		t.Fatalf("wrong account error = %v, want ErrAccountMismatch", err)
	}

	wrongRequest := valid
	wrongRequest.RequestID = ""
	if _, err := Apply(state, wrongRequest); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("empty request error = %v, want ErrInvalidEvent", err)
	}

	state = applyTestEvent(t, state, valid)
	stale := newTestEvent("event-2", "account-1", "request-1", NoRequest, Starting)
	if _, err := Apply(state, stale); !errors.Is(err, ErrStaleEvent) {
		t.Fatalf("stale event error = %v, want ErrStaleEvent", err)
	}

	wrongRequest = newTestEvent("event-3", "account-1", "request-2", Queued, Starting)
	wrongRequest.ExpectedRevision = state.Revision
	if _, err := Apply(state, wrongRequest); !errors.Is(err, ErrRequestMismatch) {
		t.Fatalf("wrong request error = %v, want ErrRequestMismatch", err)
	}

	invalidTransition := newTestEvent("event-4", "account-1", "request-1", Queued, LoginSucceeded)
	invalidTransition.ExpectedRevision = state.Revision
	if _, err := Apply(state, invalidTransition); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("invalid transition error = %v, want ErrInvalidTransition", err)
	}
}

func TestApplyRejectsInvalidSnapshotsEventsAndTerminalStates(t *testing.T) {
	invalidState := Snapshot{AccountID: "account-1", Status: NoRequest, RequestID: "request-1"}
	if _, err := Apply(invalidState, Event{}); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("invalid snapshot error = %v, want ErrInvalidSnapshot", err)
	}

	state, err := NewSnapshot("account-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(state, Event{}); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("invalid event error = %v, want ErrInvalidEvent", err)
	}

	for _, status := range []Status{Cancelled, Blocked} {
		state = stateAt(t, Queued)
		state = applyTestEvent(t, state, newTestEvent("terminal", "account-1", "request-1", Queued, status))
		blockedEvent := newTestEvent("after-terminal", "account-1", "request-1", status, Queued)
		blockedEvent.ExpectedRevision = state.Revision
		if _, err := Apply(state, blockedEvent); !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("transition from %s error = %v, want ErrInvalidTransition", status, err)
		}
	}
}

func TestSnapshotValidateChecksAuditIndex(t *testing.T) {
	state, err := NewSnapshot("account-1")
	if err != nil {
		t.Fatal(err)
	}
	state.Revision = 1
	if !errors.Is(state.Validate(), ErrInvalidSnapshot) {
		t.Fatal("revision without an audit record must be invalid")
	}
}

func TestSnapshotValidateReplaysAuditHistory(t *testing.T) {
	state := stateAt(t, Starting)
	state.Status = LoggingIn
	if !errors.Is(state.Validate(), ErrInvalidSnapshot) {
		t.Fatal("snapshot status inconsistent with audit history must be invalid")
	}

	state = stateAt(t, Starting)
	state.AppliedEvents["start"] = AuditRecord{
		Event:    newTestEvent("start", "account-1", "request-1", Queued, LoggingIn),
		Revision: 2,
	}
	if !errors.Is(state.Validate(), ErrInvalidSnapshot) {
		t.Fatal("audit event with an inconsistent predecessor must be invalid")
	}
}

func TestApplyRejectsConcurrentStaleRevision(t *testing.T) {
	state, err := NewSnapshot("account-1")
	if err != nil {
		t.Fatal(err)
	}
	first := newTestEvent("event-1", "account-1", "request-1", NoRequest, Queued)
	firstResult, err := Apply(state, first)
	if err != nil {
		t.Fatal(err)
	}

	second := newTestEvent("event-2", "account-1", "request-2", NoRequest, Queued)
	if _, err := Apply(firstResult.State, second); !errors.Is(err, ErrStaleEvent) {
		t.Fatalf("stale concurrent event error = %v, want ErrStaleEvent", err)
	}
}
