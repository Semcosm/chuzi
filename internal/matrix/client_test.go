package matrix

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPClientSendsStableTransactionAndSyncsJoinedMessages(t *testing.T) {
	var sendPath, auth string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		auth = request.Header.Get("Authorization")
		switch {
		case request.Method == http.MethodPut:
			sendPath = request.URL.Path
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte(`{"event_id":"$sent"}`))
		case request.URL.Path == "/_matrix/client/v3/sync":
			_, _ = writer.Write([]byte(`{"next_batch":"batch-1","rooms":{"join":{"!ops:example.org":{"timeline":{"events":[{"type":"m.room.message","event_id":"$event","sender":"@alice:example.org","content":{"msgtype":"m.text","body":"!ugs help"}}]}}}}}`))
		default:
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.String())
		}
	}))
	defer server.Close()
	client, err := NewHTTPClient(HTTPClientConfig{HomeserverURL: server.URL, AccessToken: "secret-token"})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Send(context.Background(), "!ops:example.org", "reply-1", "safe body"); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer secret-token" || sendPath != "/_matrix/client/v3/rooms/!ops:example.org/send/m.room.message/reply-1" {
		t.Fatalf("send request path/auth = %q / %q", sendPath, auth)
	}
	response, err := client.Sync(context.Background(), "", time.Second)
	if err != nil || response.NextBatch != "batch-1" {
		t.Fatalf("sync response = %#v, %v", response, err)
	}
	if response.Rooms.Join["!ops:example.org"].Timeline.Events[0].Content.Body != "!ugs help" {
		t.Fatal("sync message body was not decoded")
	}
}

func TestHTTPClientRejectsUnauthorizedWithoutLeakingToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte("secret-token must not escape"))
	}))
	defer server.Close()
	client, err := NewHTTPClient(HTTPClientConfig{HomeserverURL: server.URL, AccessToken: "secret-token"})
	if err != nil {
		t.Fatal(err)
	}
	err = client.Health(context.Background())
	if !errors.Is(err, ErrUnauthorized) || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("health error = %v", err)
	}
}

func TestHTTPClientRejectsInvalidSyncCursor(t *testing.T) {
	for _, nextBatch := range []string{"", " leading", "trailing ", "bad\x00cursor"} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			_, _ = writer.Write([]byte(`{"next_batch":` + strconv.Quote(nextBatch) + `,"rooms":{"join":{}}}`))
		}))
		client, err := NewHTTPClient(HTTPClientConfig{HomeserverURL: server.URL, AccessToken: "token"})
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		_, err = client.Sync(context.Background(), "", time.Second)
		server.Close()
		if !errors.Is(err, ErrSyncProtocol) {
			t.Fatalf("Sync next_batch %q = %v, want ErrSyncProtocol", nextBatch, err)
		}
	}
}

func TestGatewayHandlesSyncMessageAndUsesStableReplyEvent(t *testing.T) {
	harness := newMatrixHarness(t)
	var sent atomic.Bool
	var replyPath string
	replyDone := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/_matrix/client/v3/sync":
			if sent.Swap(true) {
				<-request.Context().Done()
				return
			}
			_, _ = writer.Write([]byte(`{"next_batch":"batch-2","rooms":{"join":{"!ops:example.org":{"timeline":{"events":[{"type":"m.room.message","event_id":"$gateway","sender":"@alice:example.org","content":{"msgtype":"m.text","body":"!ugs request account-1"}}]}}}}}`))
		default:
			replyPath = request.URL.Path
			writer.WriteHeader(http.StatusOK)
			close(replyDone)
		}
	}))
	defer server.Close()
	client, err := NewHTTPClient(HTTPClientConfig{HomeserverURL: server.URL, AccessToken: "token"})
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := NewGateway(GatewayConfig{Client: client, Adapter: harness.adapter, CursorStore: harness.store, SyncTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runDone := make(chan error, 1)
	go func() { runDone <- gateway.Run(ctx) }()
	select {
	case <-replyDone:
		cancel()
	case err := <-runDone:
		t.Fatalf("gateway stopped before sending reply: %v", err)
	}
	if err := <-runDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("gateway error = %v", err)
	}
	if !sent.Load() || !strings.Contains(replyPath, "/send/m.room.message/reply-") {
		t.Fatalf("gateway did not send stable reply: sent=%t path=%q", sent.Load(), replyPath)
	}
}

func TestGatewayContinuesAfterRateLimitedEvent(t *testing.T) {
	harness := newRateLimitedMatrixHarness(t)
	var syncCalls atomic.Int32
	replyDone := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/_matrix/client/v3/sync":
			if syncCalls.Add(1) == 1 {
				_, _ = writer.Write([]byte("{\"next_batch\":\"batch-1\",\"rooms\":{\"join\":{\"!ops:example.org\":{\"timeline\":{\"events\":[{\"type\":\"m.room.message\",\"event_id\":\"$first\",\"sender\":\"@alice:example.org\",\"content\":{\"msgtype\":\"m.text\",\"body\":\"!ugs request account-1\"}},{\"type\":\"m.room.message\",\"event_id\":\"$limited\",\"sender\":\"@alice:example.org\",\"content\":{\"msgtype\":\"m.text\",\"body\":\"!ugs request account-2\"}}]}}}}}"))
				return
			}
			_, _ = writer.Write([]byte("{\"next_batch\":\"batch-2\",\"rooms\":{\"join\":{\"!ops:example.org\":{\"timeline\":{\"events\":[{\"type\":\"m.room.message\",\"event_id\":\"$help\",\"sender\":\"@alice:example.org\",\"content\":{\"msgtype\":\"m.text\",\"body\":\"!ugs help\"}}]}}}}}"))
		default:
			if strings.Contains(request.URL.Path, "/send/") {
				close(replyDone)
				writer.WriteHeader(http.StatusOK)
				_, _ = writer.Write([]byte("{\"event_id\":\"$reply\"}"))
				return
			}
			t.Errorf("unexpected Matrix request: %s %s", request.Method, request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := NewHTTPClient(HTTPClientConfig{HomeserverURL: server.URL, AccessToken: "token"})
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := NewGateway(GatewayConfig{Client: client, Adapter: harness.adapter, CursorStore: harness.store, SyncTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runDone := make(chan error, 1)
	go func() { runDone <- gateway.Run(ctx) }()
	select {
	case <-replyDone:
		cancel()
	case <-time.After(2 * time.Second):
		t.Fatal("gateway did not process event after rate limit")
	}
	if err := <-runDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("gateway error after rate-limited event = %v", err)
	}
}

func TestGatewayLoadsPersistedCursorAfterRestart(t *testing.T) {
	harness := newMatrixHarness(t)
	if err := harness.store.SetMatrixSyncCursor("batch-before-restart"); err != nil {
		t.Fatal(err)
	}
	seenSince := make(chan string, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/_matrix/client/v3/sync" {
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.String())
		}
		seenSince <- request.URL.Query().Get("since")
		_, _ = writer.Write([]byte(`{"next_batch":"batch-after-restart","rooms":{"join":{}}}`))
	}))
	defer server.Close()
	client, err := NewHTTPClient(HTTPClientConfig{HomeserverURL: server.URL, AccessToken: "token"})
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := NewGateway(GatewayConfig{Client: client, Adapter: harness.adapter, CursorStore: harness.store, SyncTimeout: time.Second, PollInterval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runDone := make(chan error, 1)
	go func() { runDone <- gateway.Run(ctx) }()
	select {
	case since := <-seenSince:
		if since != "batch-before-restart" {
			t.Fatalf("sync since = %q, want persisted cursor", since)
		}
	case <-time.After(time.Second):
		t.Fatal("gateway did not issue sync")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		cursor, cursorErr := harness.store.GetMatrixSyncCursor()
		if cursorErr == nil && cursor == "batch-after-restart" {
			cancel()
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err := <-runDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("gateway error = %v", err)
	}
}

func TestGatewayDoesNotAdvanceCursorWhenReplySendFails(t *testing.T) {
	harness := newMatrixHarness(t)
	var failSend atomic.Bool
	failSend.Store(true)
	var sendAttempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/_matrix/client/v3/sync":
			_, _ = writer.Write([]byte(`{"next_batch":"batch-send","rooms":{"join":{"!ops:example.org":{"timeline":{"events":[{"type":"m.room.message","event_id":"$send-failure","sender":"@alice:example.org","content":{"msgtype":"m.text","body":"!ugs request account-1"}}]}}}}}}`))
		default:
			if failSend.Load() {
				sendAttempts.Add(1)
				writer.WriteHeader(http.StatusBadGateway)
				return
			}
			sendAttempts.Add(1)
			writer.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()
	client, err := NewHTTPClient(HTTPClientConfig{HomeserverURL: server.URL, AccessToken: "token"})
	if err != nil {
		t.Fatal(err)
	}
	firstGateway, err := NewGateway(GatewayConfig{Client: client, Adapter: harness.adapter, CursorStore: harness.store, SyncTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := firstGateway.Run(context.Background()); !errors.Is(err, ErrHTTPFailure) {
		t.Fatalf("failed gateway error = %v", err)
	}
	cursor, err := harness.store.GetMatrixSyncCursor()
	if err != nil || cursor != "" {
		t.Fatalf("cursor after failed send = %q, %v", cursor, err)
	}
	failSend.Store(false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	secondGateway, err := NewGateway(GatewayConfig{Client: client, Adapter: harness.adapter, CursorStore: harness.store, SyncTimeout: time.Second, PollInterval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runDone := make(chan error, 1)
	go func() { runDone <- secondGateway.Run(ctx) }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		cursor, cursorErr := harness.store.GetMatrixSyncCursor()
		if cursorErr == nil && cursor == "batch-send" {
			cancel()
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err := <-runDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("recovery gateway error = %v", err)
	}
	if sendAttempts.Load() != 2 {
		t.Fatalf("send attempts = %d, want failed send plus retry", sendAttempts.Load())
	}
}

func TestGatewayDeduplicatesRepeatedEventsBeforeCommittingBatch(t *testing.T) {
	harness := newMatrixHarness(t)
	var sends atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/_matrix/client/v3/sync":
			_, _ = writer.Write([]byte(`{"next_batch":"batch-duplicate","rooms":{"join":{"!ops:example.org":{"timeline":{"events":[{"type":"m.room.message","event_id":"$duplicate","sender":"@alice:example.org","content":{"msgtype":"m.text","body":"!ugs request account-1"}},{"type":"m.room.message","event_id":"$duplicate","sender":"@alice:example.org","content":{"msgtype":"m.text","body":"!ugs request account-1"}}]}}}}}}`))
		default:
			sends.Add(1)
			writer.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()
	client, err := NewHTTPClient(HTTPClientConfig{HomeserverURL: server.URL, AccessToken: "token"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gateway, err := NewGateway(GatewayConfig{Client: client, Adapter: harness.adapter, CursorStore: harness.store, SyncTimeout: time.Second, PollInterval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runDone := make(chan error, 1)
	go func() { runDone <- gateway.Run(ctx) }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		cursor, cursorErr := harness.store.GetMatrixSyncCursor()
		if sends.Load() == 1 && cursorErr == nil && cursor == "batch-duplicate" {
			cancel()
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err := <-runDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("gateway error = %v", err)
	}
	if sends.Load() != 1 {
		t.Fatalf("send attempts = %d, want one send for duplicate event IDs", sends.Load())
	}
	notifications, err := harness.store.ListNotifications()
	if err != nil || len(notifications) != 1 {
		t.Fatalf("notifications after duplicate events = %#v, %v", notifications, err)
	}
}
