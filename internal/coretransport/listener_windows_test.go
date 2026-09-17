//go:build windows && coretransport_native_test

package coretransport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/coreapi"
)

type windowsTestAPI struct{}

func (windowsTestAPI) SubmitRequest(context.Context, coreapi.SubmitRequest) (coreapi.Request, bool, error) {
	return coreapi.Request{RequestID: "req-1", Account: "id_account", State: "QUEUED"}, false, nil
}
func (windowsTestAPI) GetRequest(context.Context, string) (coreapi.Request, error) {
	return coreapi.Request{RequestID: "req-1", Account: "id_account", State: "QUEUED"}, nil
}
func (windowsTestAPI) GetAccount(context.Context, string) (coreapi.Account, error) {
	return coreapi.Account{Account: "id_account", State: "NO_REQUEST"}, nil
}
func (windowsTestAPI) CancelRequest(context.Context, coreapi.CancelRequest) (coreapi.Request, error) {
	return coreapi.Request{RequestID: "req-1", State: "CANCELLED"}, nil
}
func (windowsTestAPI) GetResult(context.Context, string) (coreapi.Result, error) {
	return coreapi.Result{RequestID: "req-1", State: "QUEUED", Outcome: "pending"}, nil
}
func (windowsTestAPI) ListEvents(context.Context, coreapi.EventQuery) ([]coreapi.Event, error) {
	return []coreapi.Event{{EventID: "id_event", Account: "id_account", RequestID: "req-1"}}, nil
}
func (windowsTestAPI) ListNotifications(context.Context, coreapi.NotificationQuery) ([]coreapi.Notification, error) {
	return []coreapi.Notification{{EventID: "id_event", Account: "id_account", RequestID: "req-1", Status: "pending"}}, nil
}

func TestWindowsNamedPipeEndpointDerivation(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	sum := sha256.Sum256([]byte(filepath.Clean(dataDir)))
	want := `\\.\pipe\chuzi-core-` + hex.EncodeToString(sum[:8])
	if got := EndpointPath(dataDir); got != want {
		t.Fatalf("EndpointPath() = %q, want %q", got, want)
	}
}

func TestWindowsNamedPipeRoundTrip(t *testing.T) {
	path := EndpointPath(filepath.Join(t.TempDir(), "data"))
	listener, err := Listen(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(windowsTestAPI{}, listener, Config{})
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve() }()
	defer func() {
		_ = server.Close()
		<-serveDone
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := Connect(ctx, path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	request, idempotent, err := client.SubmitRequest(ctx, coreapi.SubmitRequest{RequestID: "req-1", AccountID: "account-1", IdempotencyKey: "key"})
	if err != nil || idempotent || request.RequestID != "req-1" {
		t.Fatalf("submit = %#v, idempotent=%v, err=%v", request, idempotent, err)
	}
	if !strings.HasPrefix(path, `\\.\pipe\chuzi-core-`) {
		t.Fatalf("named pipe path = %q", path)
	}
}
