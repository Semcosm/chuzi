package matrix

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/observability"
	requestservice "github.com/Semcosm/chuzi/internal/request"
	"github.com/Semcosm/chuzi/internal/store"
)

var matrixTestTime = time.Date(2026, time.September, 10, 16, 0, 0, 0, time.UTC)

type matrixHarness struct {
	store   *store.Store
	service *requestservice.Service
	clock   *time.Time
	adapter *Adapter
}

func newMatrixHarness(t *testing.T) matrixHarness {
	t.Helper()
	cfg, err := config.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.CreateAccount("account-1"); err != nil {
		t.Fatal(err)
	}
	now := matrixTestTime
	counter := 0
	service, err := requestservice.New(database, func() time.Time { return now }, func(kind string) string {
		counter++
		return fmt.Sprintf("%s-%d", kind, counter)
	}, "request-service")
	if err != nil {
		t.Fatal(err)
	}
	sink := observability.FuncSink(func(observability.Event) {})
	adapter, err := NewAdapter(service, Policy{Rooms: map[string]map[string]Role{
		"!ops:example.org":   {"@alice:example.org": RoleUser},
		"!admin:example.org": {"@admin:example.org": RoleAdmin},
		"!other:example.org": {"@alice:example.org": RoleUser},
	}}, Config{Clock: func() time.Time { return now }, Sink: sink})
	if err != nil {
		t.Fatal(err)
	}
	return matrixHarness{store: database, service: service, clock: &now, adapter: adapter}
}

func TestParseCommandRejectsAmbiguousArguments(t *testing.T) {
	command, err := ParseCommand("!ugs request account-1")
	if err != nil || command.Kind != CommandRequest || command.Value != "account-1" {
		t.Fatalf("parsed command = %#v, %v", command, err)
	}
	for _, body := range []string{
		"!ugs request account-1 extra",
		"!ugs status",
		"!ugs request account-1\nsecret",
		"!other request account-1",
	} {
		if _, err := ParseCommand(body); !errors.Is(err, ErrInvalidCommand) {
			t.Fatalf("ParseCommand(%q) = %v, want ErrInvalidCommand", body, err)
		}
	}
}

func TestAdapterAuthorizesScopesAndDeduplicatesMatrixEvents(t *testing.T) {
	harness := newMatrixHarness(t)
	unauthorized := IncomingEvent{EventID: "$denied", RoomID: "!blocked:example.org", UserID: "@alice:example.org", Body: "!ugs help"}
	if _, err := harness.adapter.Handle(unauthorized); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("unauthorized event error = %v", err)
	}
	event := IncomingEvent{EventID: "$event-1", RoomID: "!ops:example.org", UserID: "@alice:example.org", Body: "!ugs request account-1"}
	first, err := harness.adapter.Handle(event)
	if err != nil {
		t.Fatal(err)
	}
	if first.RequestID == "" || !strings.Contains(first.Body, "status=QUEUED") || strings.Contains(first.Body, "account-1") {
		t.Fatalf("request reply = %#v", first)
	}
	second, err := harness.adapter.Handle(event)
	if err != nil || !strings.Contains(second.Body, "duplicate=true") || second.RequestID != first.RequestID {
		t.Fatalf("duplicate reply = %#v, %v", second, err)
	}
	notifications, err := harness.store.ListNotifications()
	if err != nil || len(notifications) != 1 {
		t.Fatalf("duplicate notifications = %#v, %v", notifications, err)
	}
	status, err := harness.adapter.Handle(IncomingEvent{EventID: "$status-1", RoomID: event.RoomID, UserID: event.UserID, Body: "!ugs status " + first.RequestID})
	if err != nil || !strings.Contains(status.Body, "status=QUEUED") || strings.Contains(status.Body, "account-1") {
		t.Fatalf("status reply = %#v, %v", status, err)
	}
	if _, err := harness.adapter.Handle(IncomingEvent{EventID: "$cross-room", RoomID: "!other:example.org", UserID: event.UserID, Body: "!ugs status " + first.RequestID}); !errors.Is(err, ErrNotVisible) {
		t.Fatalf("cross-room status error = %v", err)
	}
	adminStatus, err := harness.adapter.Handle(IncomingEvent{EventID: "$admin-status", RoomID: "!admin:example.org", UserID: "@admin:example.org", Body: "!ugs status " + first.RequestID})
	if err != nil || !strings.Contains(adminStatus.Body, "status=QUEUED") {
		t.Fatalf("admin status = %#v, %v", adminStatus, err)
	}
	cancelled, err := harness.adapter.Handle(IncomingEvent{EventID: "$cancel-1", RoomID: event.RoomID, UserID: event.UserID, Body: "!ugs cancel " + first.RequestID})
	if err != nil || !strings.Contains(cancelled.Body, "status=CANCELLED") {
		t.Fatalf("cancel reply = %#v, %v", cancelled, err)
	}
	notifications, err = harness.store.ListNotifications()
	if err != nil || len(notifications) != 2 {
		t.Fatalf("state notifications = %#v, %v", notifications, err)
	}
}

type fakeSender struct {
	err   error
	calls []fakeSend
}

type fakeSend struct {
	roomID  string
	eventID string
	body    string
}

func (f *fakeSender) Send(ctx context.Context, roomID, eventID, body string) error {
	return f.send(ctx, roomID, eventID, body)
}

func (f *fakeSender) send(_ context.Context, roomID, eventID, body string) error {
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, fakeSend{roomID: roomID, eventID: eventID, body: body})
	return nil
}

func TestNotifierRetriesAfterDisconnectAndRendersOnlyRedactedState(t *testing.T) {
	harness := newMatrixHarness(t)
	requestReply, err := harness.adapter.Handle(IncomingEvent{EventID: "$event-2", RoomID: "!ops:example.org", UserID: "@alice:example.org", Body: "!ugs request account-1"})
	if err != nil {
		t.Fatal(err)
	}
	sender := &fakeSender{err: errors.New("matrix token must not escape")}
	notifier, err := NewNotifier(harness.store, sender, NotifierConfig{
		Owner:     "matrix-notifier",
		ClaimTTL:  time.Minute,
		RetryBase: time.Second,
		RetryMax:  4 * time.Second,
		BatchSize: 10,
		Clock:     func() time.Time { return *harness.clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	*harness.clock = matrixTestTime.Add(2 * time.Second)
	first, err := notifier.Flush(context.Background())
	if err != nil || first.Claimed != 1 || first.Retried != 1 || first.Delivered != 0 {
		t.Fatalf("failed flush = %#v, %v", first, err)
	}
	sender.err = nil
	*harness.clock = matrixTestTime.Add(3 * time.Second)
	second, err := notifier.Flush(context.Background())
	if err != nil || second.Claimed != 1 || second.Delivered != 1 {
		t.Fatalf("recovery flush = %#v, %v", second, err)
	}
	if len(sender.calls) != 1 || sender.calls[0].eventID == "" || sender.calls[0].roomID != "!ops:example.org" {
		t.Fatalf("sender calls = %#v", sender.calls)
	}
	if strings.Contains(sender.calls[0].body, "account-1") || !strings.Contains(sender.calls[0].body, "status=QUEUED") {
		t.Fatalf("rendered notification = %q", sender.calls[0].body)
	}
	if requestReply.EventID == "" {
		t.Fatal("reply event ID unexpectedly empty")
	}
}
