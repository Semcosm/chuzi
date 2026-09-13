package matrix

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
	if auth != "Bearer secret-token" || !strings.Contains(sendPath, "/send/m.room.message/reply-1") {
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

func TestGatewayHandlesSyncMessageAndUsesStableReplyEvent(t *testing.T) {
	harness := newMatrixHarness(t)
	var sent bool
	var replyPath string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/_matrix/client/v3/sync":
			if sent {
				<-request.Context().Done()
				return
			}
			sent = true
			_, _ = writer.Write([]byte(`{"next_batch":"batch-2","rooms":{"join":{"!ops:example.org":{"timeline":{"events":[{"type":"m.room.message","event_id":"$gateway","sender":"@alice:example.org","content":{"msgtype":"m.text","body":"!ugs request account-1"}}]}}}}}`))
		default:
			replyPath = request.URL.Path
			writer.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()
	client, err := NewHTTPClient(HTTPClientConfig{HomeserverURL: server.URL, AccessToken: "token"})
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := NewGateway(GatewayConfig{Client: client, Adapter: harness.adapter, SyncTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := gateway.Run(ctx); !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("gateway error = %v", err)
	}
	if !sent || !strings.Contains(replyPath, "/send/m.room.message/reply-") {
		t.Fatalf("gateway did not send stable reply: sent=%t path=%q", sent, replyPath)
	}
}
