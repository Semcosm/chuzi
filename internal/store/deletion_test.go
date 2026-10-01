package store

import (
	"errors"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/account"
)

func TestStorePersistsDeletionAndBlocksNewRequests(t *testing.T) {
	service, cfg := openTestStore(t)
	if _, err := service.CreateAccount("account-1"); err != nil {
		t.Fatal(err)
	}
	at := storeTestTime
	created, idempotent, err := service.RequestDeletion(DeletionRequest{DeletionID: "del-1", AccountID: "account-1", Actor: "admin", ReasonClass: "operator_request", ProfilePolicy: account.ProfilePurge, RequestedAt: at})
	if err != nil || idempotent || created.Stage != account.DeletionRequested {
		t.Fatalf("RequestDeletion() = %#v, %t, %v", created, idempotent, err)
	}
	duplicate, idempotent, err := service.RequestDeletion(DeletionRequest{DeletionID: "del-1", AccountID: "account-1", Actor: "admin", ReasonClass: "operator_request", ProfilePolicy: account.ProfilePurge, RequestedAt: at})
	if err != nil || !idempotent || duplicate.DeletionID != "del-1" {
		t.Fatalf("duplicate RequestDeletion() = %#v, %t, %v", duplicate, idempotent, err)
	}
	request, err := NewRequest("request-after-delete", "account-1", "idem-after-delete", at)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.CreateRequest(request); !errors.Is(err, ErrDeletionInProgress) {
		t.Fatalf("request after deletion error = %v", err)
	}
	if got, err := service.GetDeletion("account-1"); err != nil || got.DeletionID != "del-1" || got.AccountLabel == "account-1" {
		t.Fatalf("GetDeletion() = %#v, %v", got, err)
	}
	if got, err := service.GetDeletionByID("del-1"); err != nil || got.AccountID != "account-1" {
		t.Fatalf("GetDeletionByID() = %#v, %v", got, err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if got, err := restarted.GetDeletion("account-1"); err != nil || got.DeletionID != "del-1" {
		t.Fatalf("recovered deletion = %#v, %v", got, err)
	}
}

func TestStoreAdvancesDeletionToTombstoneIdempotently(t *testing.T) {
	service, _ := openTestStore(t)
	if _, err := service.CreateAccount("account-1"); err != nil {
		t.Fatal(err)
	}
	at := storeTestTime
	if _, _, err := service.RequestDeletion(DeletionRequest{DeletionID: "del-1", AccountID: "account-1", Actor: "admin", ReasonClass: "operator_request", RequestedAt: at}); err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		id      string
		from    account.DeletionStage
		to      account.DeletionStage
		offset  time.Duration
		stopped bool
		revoked bool
		profile bool
	}{
		{"ev-1", account.DeletionRequested, account.DeletionStoppingSessions, time.Second, true, false, false},
		{"ev-2", account.DeletionStoppingSessions, account.DeletionRevokingCredentials, 2 * time.Second, true, false, false},
		{"ev-3", account.DeletionRevokingCredentials, account.DeletionCleaningProfile, 3 * time.Second, true, true, false},
		{"ev-4", account.DeletionCleaningProfile, account.DeletionTombstoned, 4 * time.Second, true, true, true},
	}
	for _, step := range steps {
		current, err := service.GetDeletion("account-1")
		if err != nil {
			t.Fatal(err)
		}
		event := account.DeletionEvent{EventID: step.id, DeletionID: current.DeletionID, AccountID: current.AccountID, Actor: current.Actor, ReasonClass: current.ReasonClass, ExpectedRevision: current.Revision, From: step.from, To: step.to, OccurredAt: at.Add(step.offset), SessionsStopped: step.stopped, CredentialsRevoked: step.revoked, ProfileActionApplied: step.profile}
		result, err := service.AdvanceDeletion(event)
		if err != nil {
			t.Fatalf("AdvanceDeletion(%s) = %v", step.id, err)
		}
		if result.State.Stage != step.to {
			t.Fatalf("stage after %s = %s", step.id, result.State.Stage)
		}
		duplicate, err := service.AdvanceDeletion(event)
		if err != nil || !duplicate.Idempotent || duplicate.State.Revision != result.State.Revision {
			t.Fatalf("duplicate %s = %#v, %v", step.id, duplicate, err)
		}
	}
	final, err := service.GetDeletion("account-1")
	if err != nil || final.Stage != account.DeletionTombstoned || final.CompletedAt.IsZero() {
		t.Fatalf("final deletion = %#v, %v", final, err)
	}
	request, err := NewRequest("request-after-tombstone", "account-1", "idem-after-tombstone", at.Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.CreateRequest(request); !errors.Is(err, ErrAccountDeleted) {
		t.Fatalf("request after tombstone error = %v", err)
	}
}
