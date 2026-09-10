package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/internal/config"
)

func TestNotificationsAreAtomicWithStateEventsAndRecoverAfterRestart(t *testing.T) {
	cfg, err := config.New(filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateAccount("account-1"); err != nil {
		t.Fatal(err)
	}
	request := createRequest(t, service, "request-1", "account-1", "idem-1", storeTestTime)
	request.NotificationRoomID = "!ops:example.org"
	if _, _, err := service.CreateRequest(request); err != nil {
		t.Fatal(err)
	}
	applyRoomEvent := func(eventID string, from, to account.Status, at time.Time) {
		t.Helper()
		state, err := service.GetAccount("account-1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.ApplyEvent(account.Event{
			EventID:          eventID,
			AccountID:        "account-1",
			RequestID:        "request-1",
			From:             from,
			ExpectedRevision: state.Revision,
			To:               to,
			Reason:           "notification test",
			Actor:            "test",
			OccurredAt:       at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	applyRoomEvent("event-queued", account.NoRequest, account.Queued, storeTestTime.Add(time.Second))
	applyRoomEvent("event-starting", account.Queued, account.Starting, storeTestTime.Add(2*time.Second))
	notifications, err := service.ListNotifications()
	if err != nil || len(notifications) != 2 {
		t.Fatalf("notifications = %#v, %v", notifications, err)
	}
	if notifications[0].EventID != "event-queued" || notifications[0].State != account.Queued || notifications[1].State != account.Starting {
		t.Fatalf("notification order = %#v", notifications)
	}
	if notifications[0].RoomID != request.NotificationRoomID {
		t.Fatalf("notification room = %q", notifications[0].RoomID)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	recovered, err := restarted.ListNotifications()
	if err != nil || len(recovered) != 2 {
		t.Fatalf("recovered notifications = %#v, %v", recovered, err)
	}
}

func TestNotificationClaimRetryAndCompletionAreOwnedAndIdempotent(t *testing.T) {
	service, _ := openTestStore(t)
	if _, err := service.CreateAccount("account-1"); err != nil {
		t.Fatal(err)
	}
	request := createRequest(t, service, "request-1", "account-1", "idem-1", storeTestTime)
	request.NotificationRoomID = "!ops:example.org"
	if _, _, err := service.CreateRequest(request); err != nil {
		t.Fatal(err)
	}
	applyEvent(t, service, "event-queued", "account-1", "request-1", account.NoRequest, account.Queued, storeTestTime.Add(time.Second))
	claimed, err := service.ClaimNotifications(storeTestTime.Add(2*time.Second), "notifier-a", time.Minute, 10)
	if err != nil || len(claimed) != 1 || claimed[0].Attempt != 1 {
		t.Fatalf("claimed = %#v, %v", claimed, err)
	}
	if _, err := service.ClaimNotifications(storeTestTime.Add(2*time.Second), "notifier-b", time.Minute, 10); err != nil {
		t.Fatal(err)
	} else if recovered, _ := service.ListNotifications(); len(recovered) != 1 || recovered[0].ClaimedBy != "notifier-a" {
		t.Fatalf("active claim was replaced = %#v", recovered)
	}
	if err := service.RetryNotification("event-queued", "notifier-b", storeTestTime.Add(2*time.Second), storeTestTime.Add(3*time.Second)); !errors.Is(err, ErrNotificationLease) {
		t.Fatalf("wrong owner retry = %v", err)
	}
	if err := service.RetryNotification("event-queued", "notifier-a", storeTestTime.Add(2*time.Second), storeTestTime.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	claimed, err = service.ClaimNotifications(storeTestTime.Add(3*time.Second), "notifier-b", time.Minute, 10)
	if err != nil || len(claimed) != 1 || claimed[0].Attempt != 2 {
		t.Fatalf("reclaimed = %#v, %v", claimed, err)
	}
	if err := service.CompleteNotification("event-queued", "notifier-b", storeTestTime.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := service.CompleteNotification("event-queued", "notifier-b", storeTestTime.Add(5*time.Second)); err != nil {
		t.Fatalf("duplicate completion = %v", err)
	}
	all, err := service.ListNotifications()
	if err != nil || len(all) != 1 || all[0].DeliveredAt.IsZero() || all[0].ClaimedBy != "" {
		t.Fatalf("completed notification = %#v, %v", all, err)
	}
}

func TestRequestsWithoutNotificationRoomsDoNotCreateOutboxRecords(t *testing.T) {
	service, _ := openTestStore(t)
	if _, err := service.CreateAccount("account-1"); err != nil {
		t.Fatal(err)
	}
	request := createRequest(t, service, "request-1", "account-1", "idem-1", storeTestTime)
	if _, _, err := service.CreateRequest(request); err != nil {
		t.Fatal(err)
	}
	applyEvent(t, service, "event-queued", "account-1", "request-1", account.NoRequest, account.Queued, storeTestTime.Add(time.Second))
	if notifications, err := service.ListNotifications(); err != nil || len(notifications) != 0 {
		t.Fatalf("unbound notifications = %#v, %v", notifications, err)
	}
}

func TestFailureNotificationCarriesOnlyClassifiedFailure(t *testing.T) {
	service, _ := openTestStore(t)
	if _, err := service.CreateAccount("account-1"); err != nil {
		t.Fatal(err)
	}
	request := createRequest(t, service, "request-1", "account-1", "idem-1", storeTestTime)
	request.NotificationRoomID = "!ops:example.org"
	if _, _, err := service.CreateRequest(request); err != nil {
		t.Fatal(err)
	}
	applyEvent(t, service, "event-queued", "account-1", "request-1", account.NoRequest, account.Queued, storeTestTime.Add(time.Second))
	applyEvent(t, service, "event-starting", "account-1", "request-1", account.Queued, account.Starting, storeTestTime.Add(2*time.Second))
	applyEvent(t, service, "event-logging-in", "account-1", "request-1", account.Starting, account.LoggingIn, storeTestTime.Add(3*time.Second))
	state, err := service.GetAccount("account-1")
	if err != nil {
		t.Fatal(err)
	}
	failed := account.Event{
		EventID:          "event-failed",
		AccountID:        "account-1",
		RequestID:        "request-1",
		From:             account.LoggingIn,
		ExpectedRevision: state.Revision,
		To:               account.LoginFailed,
		Reason:           "worker returned a private provider error",
		Actor:            "runner",
		OccurredAt:       storeTestTime.Add(4 * time.Second),
	}
	if _, err := service.RecordFailure(failed, account.CredentialFailure, time.Time{}, nil); err != nil {
		t.Fatal(err)
	}
	notifications, err := service.ListNotifications()
	if err != nil || len(notifications) != 4 {
		t.Fatalf("failure notifications = %#v, %v", notifications, err)
	}
	if notifications[3].State != account.LoginFailed || notifications[3].Failure != account.CredentialFailure {
		t.Fatalf("classified failure notification = %#v", notifications[3])
	}
}
