package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/core"
	"github.com/Semcosm/chuzi/internal/coreapi"
	"github.com/Semcosm/chuzi/internal/request"
	"github.com/Semcosm/chuzi/internal/store"
)

func TestCoreFacadeProjectsSafeRequestAccountResultEventsAndNotifications(t *testing.T) {
	database, err := store.Open(config.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.CreateAccount("account-secret"); err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC) }
	ids := 0
	requests, err := request.New(database, clock, func(kind string) string {
		ids++
		return kind + "-" + string(rune('0'+ids))
	}, "id_service")
	if err != nil {
		t.Fatal(err)
	}
	api, err := core.New(core.Dependencies{Requests: requests, Store: database})
	if err != nil {
		t.Fatal(err)
	}

	created, idempotent, err := api.SubmitRequest(context.Background(), coreapi.SubmitRequest{
		RequestID: "request-1", AccountID: "account-secret", IdempotencyKey: "idem-secret",
		NotificationRoomID: "!private:example.org", Actor: "@alice:example.org",
	})
	if err != nil || idempotent {
		t.Fatalf("SubmitRequest() = %#v, %v, duplicate=%v", created, err, idempotent)
	}
	if created.Account == "account-secret" || created.State != "QUEUED" {
		t.Fatalf("request projection leaked or has wrong state: %#v", created)
	}
	raw, _ := json.Marshal(created)
	if strings.Contains(string(raw), "idem-secret") || strings.Contains(string(raw), "private:example.org") {
		t.Fatalf("request projection contains secret input: %s", raw)
	}

	accountView, err := api.GetAccount(context.Background(), "account-secret")
	if err != nil || accountView.Account == "account-secret" || accountView.State != "QUEUED" {
		t.Fatalf("GetAccount() = %#v, %v", accountView, err)
	}
	result, err := api.GetResult(context.Background(), "request-1")
	if err != nil || result.Outcome != "pending" || result.Failure != "" {
		t.Fatalf("GetResult() = %#v, %v", result, err)
	}
	events, err := api.ListEvents(context.Background(), coreapi.EventQuery{RequestID: "request-1"})
	if err != nil || len(events) != 1 || events[0].To != "QUEUED" {
		t.Fatalf("ListEvents() = %#v, %v", events, err)
	}
	notifications, err := api.ListNotifications(context.Background(), coreapi.NotificationQuery{RequestID: "request-1"})
	if err != nil || len(notifications) != 1 || notifications[0].Status != "pending" {
		t.Fatalf("ListNotifications() = %#v, %v", notifications, err)
	}
	notificationJSON, _ := json.Marshal(notifications[0])
	if strings.Contains(string(notificationJSON), "private:example.org") || strings.Contains(string(notificationJSON), "account-secret") {
		t.Fatalf("notification projection leaked raw identifiers: %s", notificationJSON)
	}

	cancelled, err := api.CancelRequest(context.Background(), coreapi.CancelRequest{RequestID: "request-1", Actor: "@alice:example.org"})
	if err != nil || cancelled.State != "CANCELLED" {
		t.Fatalf("CancelRequest() = %#v, %v", cancelled, err)
	}
	result, err = api.GetResult(context.Background(), "request-1")
	if err != nil || result.Outcome != "cancelled" || result.State != "CANCELLED" {
		t.Fatalf("cancelled GetResult() = %#v, %v", result, err)
	}
	events, err = api.ListEvents(context.Background(), coreapi.EventQuery{RequestID: "request-1"})
	if err != nil || len(events) != 2 || events[1].To != "CANCELLED" {
		t.Fatalf("cancelled ListEvents() = %#v, %v", events, err)
	}
}

func TestCoreFacadeUsesStableErrorClassificationsAndContext(t *testing.T) {
	database, err := store.Open(config.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	requests, err := request.New(database, time.Now, func(string) string { return "id_event" }, "id_service")
	if err != nil {
		t.Fatal(err)
	}
	api, err := core.New(core.Dependencies{Requests: requests, Store: database})
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.GetRequest(context.Background(), "missing")
	if got := coreapi.CodeOf(err); got != coreapi.CodeNotFound {
		t.Fatalf("missing request error code = %q, want %q (%v)", got, coreapi.CodeNotFound, err)
	}
	if strings.Contains(err.Error(), "request not found") || strings.Contains(err.Error(), "bbolt") {
		t.Fatalf("error leaked internal details: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = api.GetRequest(ctx, "missing")
	if got := coreapi.CodeOf(err); got != coreapi.CodeCancelled {
		t.Fatalf("cancelled context code = %q, want %q", got, coreapi.CodeCancelled)
	}
	var classified *coreapi.Error
	if !errors.As(err, &classified) {
		t.Fatalf("cancelled context did not return coreapi.Error: %T", err)
	}
}
