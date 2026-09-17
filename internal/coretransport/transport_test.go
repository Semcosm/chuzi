//go:build !windows

package coretransport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/coreapi"
)

type testAPI struct {
	started  chan struct{}
	canceled chan struct{}
}

func (a *testAPI) SubmitRequest(context.Context, coreapi.SubmitRequest) (coreapi.Request, bool, error) {
	return coreapi.Request{RequestID: "req-1", Account: "id_account", State: "QUEUED", Attempt: 0, CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(1, 0).UTC()}, false, nil
}
func (a *testAPI) GetRequest(ctx context.Context, id string) (coreapi.Request, error) {
	if id == "block" {
		close(a.started)
		<-ctx.Done()
		close(a.canceled)
		return coreapi.Request{}, ctx.Err()
	}
	return coreapi.Request{RequestID: id, Account: "id_account", State: "QUEUED"}, nil
}
func (a *testAPI) GetAccount(context.Context, string) (coreapi.Account, error) {
	return coreapi.Account{Account: "id_account", State: "NO_REQUEST"}, nil
}
func (a *testAPI) CancelRequest(context.Context, coreapi.CancelRequest) (coreapi.Request, error) {
	return coreapi.Request{RequestID: "req-1", State: "CANCELLED"}, nil
}
func (a *testAPI) GetResult(context.Context, string) (coreapi.Result, error) {
	return coreapi.Result{RequestID: "req-1", State: "LOGIN_SUCCEEDED", Outcome: "succeeded"}, nil
}
func (a *testAPI) ListEvents(context.Context, coreapi.EventQuery) ([]coreapi.Event, error) {
	return []coreapi.Event{{EventID: "id_event", Account: "id_account", Operation: "state"}}, nil
}
func (a *testAPI) ListNotifications(context.Context, coreapi.NotificationQuery) ([]coreapi.Notification, error) {
	return []coreapi.Notification{{EventID: "id_event", Account: "id_account", RequestID: "req-1", State: "LOGIN_SUCCEEDED", Status: "delivered"}}, nil
}

func startTestServer(t *testing.T, api coreapi.API) (string, func()) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "core.sock")
	listener, err := Listen(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(api, listener, Config{})
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve() }()
	return path, func() { _ = server.Close(); <-serveDone }
}

func TestUnixContractHandshakeAndCoreCall(t *testing.T) {
	api := &testAPI{started: make(chan struct{}), canceled: make(chan struct{})}
	path, stop := startTestServer(t, api)
	defer stop()
	client, err := Connect(context.Background(), path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	request, idempotent, err := client.SubmitRequest(context.Background(), coreapi.SubmitRequest{RequestID: "req-1", AccountID: "account-1", IdempotencyKey: "key"})
	if err != nil || idempotent || request.RequestID != "req-1" {
		t.Fatalf("submit = %#v %v %v", request, idempotent, err)
	}
	if _, err := client.GetRequest(context.Background(), "req-2"); err != nil {
		t.Fatal(err)
	}
	if account, err := client.GetAccount(context.Background(), "account-1"); err != nil || account.State != "NO_REQUEST" {
		t.Fatalf("account = %#v, err=%v", account, err)
	}
	if result, err := client.GetResult(context.Background(), "req-1"); err != nil || result.Outcome != "succeeded" {
		t.Fatalf("result = %#v, err=%v", result, err)
	}
	if events, err := client.ListEvents(context.Background(), coreapi.EventQuery{RequestID: "req-1"}); err != nil || len(events) != 1 {
		t.Fatalf("events = %#v, err=%v", events, err)
	}
	if notifications, err := client.ListNotifications(context.Background(), coreapi.NotificationQuery{RequestID: "req-1"}); err != nil || len(notifications) != 1 {
		t.Fatalf("notifications = %#v, err=%v", notifications, err)
	}
	cancelled, err := client.CancelRequest(context.Background(), coreapi.CancelRequest{RequestID: "req-1", Reason: "test"})
	if err != nil || cancelled.State != "CANCELLED" {
		t.Fatalf("cancel = %#v, err=%v", cancelled, err)
	}
}

func TestRejectsUnsupportedVersionAndUnknownMethod(t *testing.T) {
	api := &testAPI{started: make(chan struct{}), canceled: make(chan struct{})}
	path, stop := startTestServer(t, api)
	defer stop()
	conn, err := Dial(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	encoder := json.NewEncoder(conn)
	decoder := json.NewDecoder(conn)
	if err := encoder.Encode(Envelope{Protocol: "chuzi.core/v99", ID: "x", Method: methodHello, Params: json.RawMessage(`{"version":"chuzi.core/v99"}`)}); err != nil {
		t.Fatal(err)
	}
	var response Envelope
	if err := decoder.Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Type != "error" || response.Error == nil || response.Error.Code != coreapi.CodeUnavailable {
		t.Fatalf("version response = %#v", response)
	}
	if err := encoder.Encode(Envelope{Protocol: ProtocolVersion, ID: "h", Method: methodHello, Params: json.RawMessage(`{"version":"chuzi.core/v1"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&response); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(Envelope{Protocol: ProtocolVersion, ID: "y", Method: "unknown_method", Params: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Type != "error" || response.Error == nil || response.Error.Code != coreapi.CodeInvalidArgument {
		t.Fatalf("unknown response = %#v error=%#v", response, response.Error)
	}
}

func TestRejectsBusinessCallsBeforeHandshake(t *testing.T) {
	api := &testAPI{started: make(chan struct{}), canceled: make(chan struct{})}
	path, stop := startTestServer(t, api)
	defer stop()
	conn, err := Dial(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(Envelope{
		Protocol: ProtocolVersion,
		ID:       "before-hello",
		Method:   methodGetRequest,
		Params:   json.RawMessage(`{"request_id":"req-1"}`),
	}); err != nil {
		t.Fatal(err)
	}
	var response Envelope
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Type != "error" || response.Error == nil || response.Error.Code != coreapi.CodeInvalidArgument {
		t.Fatalf("pre-handshake response = %#v", response)
	}
}

func TestEndpointBusyIsNotUnlinked(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "core.sock")
	listener, err := Listen(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if _, err := Listen(context.Background(), path); !errors.Is(err, ErrEndpointBusy) {
		t.Fatalf("second listener error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("active socket was removed: %v", err)
	}
}

func TestStaleSocketIsReplaced(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "core.sock")
	stale, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	listener, err := Listen(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if info, err := os.Stat(path); err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("replacement socket = %#v, err=%v", info, err)
	}
}

func TestTransportCancellationCancelsCoreCall(t *testing.T) {
	api := &testAPI{started: make(chan struct{}), canceled: make(chan struct{})}
	path, stop := startTestServer(t, api)
	defer stop()
	client, err := Connect(context.Background(), path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { _, err := client.GetRequest(ctx, "block"); errCh <- err }()
	select {
	case <-api.started:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-errCh:
		if got := coreapi.CodeOf(err); got != coreapi.CodeCancelled {
			t.Fatalf("cancel error code = %q, err=%v", got, err)
		}
	case <-time.After(time.Second):
		t.Fatal("client did not return after cancellation")
	}
	select {
	case <-api.canceled:
	case <-time.After(time.Second):
		t.Fatal("server call was not cancelled")
	}
}

func TestSocketIsOwnerOnlyAndResponseIsRedactedShape(t *testing.T) {
	api := &testAPI{started: make(chan struct{}), canceled: make(chan struct{})}
	path, stop := startTestServer(t, api)
	defer stop()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %o", info.Mode().Perm())
	}
	client, err := Connect(context.Background(), path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	request, err := client.GetRequest(context.Background(), "safe")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(request)
	if strings.Contains(string(raw), "idempotency") || strings.Contains(string(raw), "room") || strings.Contains(string(raw), "Profile") {
		t.Fatalf("sensitive response = %s", raw)
	}
	if _, err := net.ResolveUnixAddr("unix", path); err != nil {
		t.Fatal(err)
	}
}

func TestSocketDirectoryIsOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	listener, err := Listen(context.Background(), filepath.Join(dir, "core.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("socket directory mode = %o", info.Mode().Perm())
	}
}

func TestClientMultiplexesConcurrentCallsByRequestID(t *testing.T) {
	api := &testAPI{started: make(chan struct{}), canceled: make(chan struct{})}
	path, stop := startTestServer(t, api)
	defer stop()
	client, err := Connect(context.Background(), path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	const calls = 16
	errs := make(chan error, calls)
	for i := 0; i < calls; i++ {
		go func(i int) {
			request, err := client.GetRequest(context.Background(), "concurrent-"+formatUint(uint64(i)))
			if err == nil && request.RequestID != "concurrent-"+formatUint(uint64(i)) {
				err = errors.New("response matched the wrong request")
			}
			errs <- err
		}(i)
	}
	for i := 0; i < calls; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

func TestRejectsOversizedFrame(t *testing.T) {
	api := &testAPI{started: make(chan struct{}), canceled: make(chan struct{})}
	dir := t.TempDir()
	path := filepath.Join(dir, "core.sock")
	listener, err := Listen(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(api, listener, Config{MaxFrameBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve() }()
	defer func() { _ = server.Close(); <-serveDone }()
	conn, err := Dial(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	frame := make([]byte, 2048)
	frame[len(frame)-1] = '\n'
	if _, err := conn.Write(frame); err != nil {
		t.Fatal(err)
	}
	var response Envelope
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Error == nil || response.Error.Code != coreapi.CodeInvalidArgument {
		t.Fatalf("oversized response = %#v", response)
	}
}

func TestResponseFrameLimitReturnsStableError(t *testing.T) {
	var output bytes.Buffer
	var writeMu sync.Mutex
	sendEnvelope(&writeMu, &output, response("large", strings.Repeat("x", 2048)), 1024)
	var envelope Envelope
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Type != "error" || envelope.Error == nil || envelope.Error.Code != coreapi.CodeInvalidArgument {
		t.Fatalf("oversized response = %#v", envelope)
	}
}
