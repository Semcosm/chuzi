package account

import (
	"errors"
	"testing"
	"time"
)

func TestDeletionLifecycleIsDeterministicAndIdempotent(t *testing.T) {
	at := time.Date(2026, time.September, 30, 10, 0, 0, 0, time.UTC)
	state, err := NewDeletion("del-1", "account-1", "id_redacted", "admin", "operator_request", ProfilePurge, LoginSucceeded, at)
	if err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		id      string
		from    DeletionStage
		to      DeletionStage
		at      time.Time
		stopped bool
		revoked bool
		profile bool
	}{
		{"ev-1", DeletionRequested, DeletionStoppingSessions, at.Add(time.Second), true, false, false},
		{"ev-2", DeletionStoppingSessions, DeletionRevokingCredentials, at.Add(2 * time.Second), true, false, false},
		{"ev-3", DeletionRevokingCredentials, DeletionCleaningProfile, at.Add(3 * time.Second), true, true, false},
		{"ev-4", DeletionCleaningProfile, DeletionTombstoned, at.Add(4 * time.Second), true, true, true},
	}
	for _, step := range steps {
		event := DeletionEvent{EventID: step.id, DeletionID: "del-1", AccountID: "account-1", Actor: "admin", ReasonClass: "operator_request", ExpectedRevision: state.Revision, From: step.from, To: step.to, OccurredAt: step.at, SessionsStopped: step.stopped, CredentialsRevoked: step.revoked, ProfileActionApplied: step.profile}
		result, err := ApplyDeletion(state, event)
		if err != nil {
			t.Fatalf("apply %s: %v", step.id, err)
		}
		state = result.State
		duplicate, err := ApplyDeletion(state, event)
		if err != nil || !duplicate.Idempotent || duplicate.State.Revision != state.Revision {
			t.Fatalf("duplicate %s = %#v, %v", step.id, duplicate, err)
		}
	}
	if state.Stage != DeletionTombstoned || !state.SessionsStopped || !state.CredentialsRevoked || !state.ProfileActionApplied || state.CompletedAt.IsZero() {
		t.Fatalf("terminal deletion = %#v", state)
	}
	if _, err := ApplyDeletion(state, DeletionEvent{EventID: "late", DeletionID: "del-1", AccountID: "account-1", Actor: "admin", ReasonClass: "operator_request", From: DeletionTombstoned, To: DeletionBlocked, OccurredAt: at.Add(5 * time.Second)}); !errors.Is(err, ErrDeletionTerminal) {
		t.Fatalf("terminal transition error = %v", err)
	}
}

func TestDeletionRejectsConflictingEventAndInvalidCheckpoint(t *testing.T) {
	at := time.Date(2026, time.September, 30, 10, 0, 0, 0, time.UTC)
	state, err := NewDeletion("del-1", "account-1", "id_redacted", "admin", "operator_request", ProfilePurge, NoRequest, at)
	if err != nil {
		t.Fatal(err)
	}
	event := DeletionEvent{EventID: "ev-1", DeletionID: "del-1", AccountID: "account-1", Actor: "admin", ReasonClass: "operator_request", From: DeletionRequested, To: DeletionStoppingSessions, OccurredAt: at.Add(time.Second)}
	first, err := ApplyDeletion(state, event)
	if err != nil {
		t.Fatal(err)
	}
	conflict := event
	conflict.To = DeletionBlocked
	if _, err := ApplyDeletion(first.State, conflict); !errors.Is(err, ErrDeletionEventConflict) {
		t.Fatalf("conflicting event error = %v", err)
	}
	if _, err := ApplyDeletion(first.State, DeletionEvent{EventID: "ev-2", DeletionID: "del-1", AccountID: "account-1", Actor: "admin", ReasonClass: "operator_request", ExpectedRevision: first.State.Revision, From: DeletionStoppingSessions, To: DeletionTombstoned, OccurredAt: at.Add(2 * time.Second)}); !errors.Is(err, ErrDeletionInvalidTransition) {
		t.Fatalf("invalid transition error = %v", err)
	}
}

func TestDeletionEnforcesCheckpointDependenciesAndTombstoneCompleteness(t *testing.T) {
	at := time.Date(2026, time.September, 30, 11, 0, 0, 0, time.UTC)
	state, err := NewDeletion("del-1", "account-1", "id_redacted", "admin", "operator_request", ProfilePurge, NoRequest, at)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		state DeletionSnapshot
		event DeletionEvent
	}{
		{"credentials require sessions", func() DeletionSnapshot { s := state.Clone(); s.Stage = DeletionStoppingSessions; return s }(), DeletionEvent{To: DeletionRevokingCredentials, CredentialsRevoked: true}},
		{"profile requires credentials", func() DeletionSnapshot {
			s := state.Clone()
			s.Stage = DeletionRevokingCredentials
			s.SessionsStopped = true
			return s
		}(), DeletionEvent{To: DeletionCleaningProfile, ProfileActionApplied: true}},
		{"tombstone requires all checkpoints", func() DeletionSnapshot {
			s := state.Clone()
			s.Stage = DeletionCleaningProfile
			s.SessionsStopped = true
			s.CredentialsRevoked = true
			return s
		}(), DeletionEvent{To: DeletionTombstoned}},
	} {
		event := tc.event
		event.EventID = tc.name
		event.DeletionID = tc.state.DeletionID
		event.AccountID = tc.state.AccountID
		event.Actor = tc.state.Actor
		event.ReasonClass = tc.state.ReasonClass
		event.ExpectedRevision = tc.state.Revision
		event.From = tc.state.Stage
		event.OccurredAt = at.Add(time.Second)
		if _, err := ApplyDeletion(tc.state, event); !errors.Is(err, ErrDeletionInvalidTransition) {
			t.Errorf("%s error = %v", tc.name, err)
		}
	}

	invalid := state.Clone()
	invalid.CredentialsRevoked = true
	if err := invalid.Validate(); !errors.Is(err, ErrInvalidDeletion) {
		t.Fatalf("invalid checkpoint snapshot error = %v", err)
	}
	invalid = state.Clone()
	invalid.SessionsStopped = true
	invalid.ProfileActionApplied = true
	if err := invalid.Validate(); !errors.Is(err, ErrInvalidDeletion) {
		t.Fatalf("invalid profile checkpoint snapshot error = %v", err)
	}
}

func TestDeletionRejectsTimeReversalAndResumesAtActualCheckpoint(t *testing.T) {
	at := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	state, err := NewDeletion("del-1", "account-1", "id_redacted", "admin", "operator_request", ProfilePurge, NoRequest, at)
	if err != nil {
		t.Fatal(err)
	}
	apply := func(event DeletionEvent) {
		t.Helper()
		result, err := ApplyDeletion(state, event)
		if err != nil {
			t.Fatalf("apply %s: %v", event.EventID, err)
		}
		state = result.State
	}
	apply(DeletionEvent{EventID: "ev-1", DeletionID: state.DeletionID, AccountID: state.AccountID, Actor: state.Actor, ReasonClass: state.ReasonClass, ExpectedRevision: state.Revision, From: state.Stage, To: DeletionStoppingSessions, OccurredAt: at.Add(time.Second)})
	apply(DeletionEvent{EventID: "ev-2", DeletionID: state.DeletionID, AccountID: state.AccountID, Actor: state.Actor, ReasonClass: state.ReasonClass, ExpectedRevision: state.Revision, From: state.Stage, To: DeletionRetryWait, OccurredAt: at.Add(2 * time.Second)})
	wrong := DeletionEvent{EventID: "ev-3-wrong", DeletionID: state.DeletionID, AccountID: state.AccountID, Actor: state.Actor, ReasonClass: state.ReasonClass, ExpectedRevision: state.Revision, From: state.Stage, To: DeletionRevokingCredentials, OccurredAt: at.Add(3 * time.Second)}
	if _, err := ApplyDeletion(state, wrong); !errors.Is(err, ErrDeletionInvalidTransition) {
		t.Fatalf("wrong retry checkpoint error = %v", err)
	}
	backwards := wrong
	backwards.EventID = "ev-3-backwards"
	backwards.To = DeletionStoppingSessions
	backwards.OccurredAt = at.Add(time.Second)
	if _, err := ApplyDeletion(state, backwards); !errors.Is(err, ErrDeletionStaleEvent) {
		t.Fatalf("backwards event error = %v", err)
	}
	apply(DeletionEvent{EventID: "ev-3", DeletionID: state.DeletionID, AccountID: state.AccountID, Actor: state.Actor, ReasonClass: state.ReasonClass, ExpectedRevision: state.Revision, From: state.Stage, To: DeletionStoppingSessions, OccurredAt: at.Add(3 * time.Second), SessionsStopped: true})
	apply(DeletionEvent{EventID: "ev-4", DeletionID: state.DeletionID, AccountID: state.AccountID, Actor: state.Actor, ReasonClass: state.ReasonClass, ExpectedRevision: state.Revision, From: state.Stage, To: DeletionRevokingCredentials, OccurredAt: at.Add(4 * time.Second), SessionsStopped: true})
	apply(DeletionEvent{EventID: "ev-5", DeletionID: state.DeletionID, AccountID: state.AccountID, Actor: state.Actor, ReasonClass: state.ReasonClass, ExpectedRevision: state.Revision, From: state.Stage, To: DeletionRetryWait, OccurredAt: at.Add(5 * time.Second), SessionsStopped: true})
	wrong = DeletionEvent{EventID: "ev-6-wrong", DeletionID: state.DeletionID, AccountID: state.AccountID, Actor: state.Actor, ReasonClass: state.ReasonClass, ExpectedRevision: state.Revision, From: state.Stage, To: DeletionStoppingSessions, OccurredAt: at.Add(6 * time.Second), SessionsStopped: true}
	if _, err := ApplyDeletion(state, wrong); !errors.Is(err, ErrDeletionInvalidTransition) {
		t.Fatalf("wrong credential retry checkpoint error = %v", err)
	}
	right := wrong
	right.EventID = "ev-6"
	right.To = DeletionRevokingCredentials
	if _, err := ApplyDeletion(state, right); err != nil {
		t.Fatalf("actual retry checkpoint error = %v", err)
	}
}
