package store

import (
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/internal/slot"
)

func TestClaimRequestWithSlotTargetsRequestedQueueItem(t *testing.T) {
	database, _ := openTestStore(t)
	for _, id := range []string{"account-a", "account-b"} {
		if _, err := database.CreateAccount(id); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []struct{ request, account string }{{"request-a", "account-a"}, {"request-b", "account-b"}} {
		request := createRequest(t, database, value.request, value.account, "idem-"+value.request, storeTestTime)
		if _, _, err := database.CreateRequest(request); err != nil {
			t.Fatal(err)
		}
		applyEvent(t, database, "queued-"+value.request, value.account, value.request, account.NoRequest, account.Queued, storeTestTime)
	}
	pool := testPoolConfig(1)
	if err := database.ReconcileJobPool(pool, storeTestTime); err != nil {
		t.Fatal(err)
	}
	items, err := database.ListSlots(pool.PoolID)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MarkSlotReady(items[0].SlotID, testEnvironmentSummary(1), storeTestTime); err != nil {
		t.Fatal(err)
	}
	claim, err := database.ClaimRequestWithSlot(storeTestTime, "request-b", "lease-b", "session-owner", time.Minute, "event-b", "session-owner", "session start", QueueOptions{MaxGlobalConcurrency: 1}, pool.PoolID, slot.EnvironmentRequirement{EnvironmentID: pool.EnvironmentID, Version: pool.EnvironmentVersion, RequireTrusted: true})
	if err != nil {
		t.Fatal(err)
	}
	if claim.Claim.Request.RequestID != "request-b" || claim.Claim.Request.AccountID != "account-b" {
		t.Fatalf("claimed wrong request: %#v", claim.Claim.Request)
	}
	if state, err := database.GetAccount("account-a"); err != nil || state.Status != account.Queued {
		t.Fatalf("unrequested account state = %#v, %v", state, err)
	}
	if err := database.ReleaseLease("account-b", claim.Claim.Lease.LeaseID, "session-owner"); err != nil {
		t.Fatal(err)
	}
	if err := database.ReleaseSlotLease(claim.Slot.SlotID, claim.SlotLease.LeaseID, "session-owner"); err != nil {
		t.Fatal(err)
	}
}
