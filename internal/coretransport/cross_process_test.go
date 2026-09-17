package coretransport

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/core"
	"github.com/Semcosm/chuzi/internal/coreapi"
	requestservice "github.com/Semcosm/chuzi/internal/request"
	"github.com/Semcosm/chuzi/internal/store"
)

const (
	crossProcessHelperEnv    = "CHUZI_CORETRANSPORT_HELPER"
	crossProcessEndpointEnv  = "CHUZI_CORETRANSPORT_ENDPOINT"
	crossProcessDataDirEnv   = "CHUZI_CORETRANSPORT_DATA_DIR"
	crossProcessHelperReady  = "READY"
	crossProcessCancelReady  = "BLOCK_READY"
	crossProcessCancelFinish = "BLOCK_CANCELLED"
)

// TestCrossProcessTransportContract starts the transport server in a separate
// test process and exercises the same endpoint a native client uses.
func TestCrossProcessTransportContract(t *testing.T) {
	endpoint, output := startCrossProcessHelper(t)
	waitCrossProcessLine(t, output, crossProcessHelperReady)

	if runtime.GOOS != "windows" {
		info, err := os.Stat(endpoint)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("socket mode = %o, want 600", info.Mode().Perm())
		}
		directory, err := os.Stat(filepath.Dir(endpoint))
		if err != nil {
			t.Fatal(err)
		}
		if directory.Mode().Perm() != 0o700 {
			t.Fatalf("socket directory mode = %o, want 700", directory.Mode().Perm())
		}
	}

	// A peer with an unknown protocol is rejected before any business method
	// can run, while the process remains available for a valid client.
	conn, err := Dial(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(conn)
	if err := json.NewEncoder(conn).Encode(Envelope{
		Protocol: "chuzi.core/v99",
		ID:       "unsupported",
		Method:   methodHello,
		Params:   json.RawMessage(`{"version":"chuzi.core/v99"}`),
	}); err != nil {
		t.Fatal(err)
	}
	var response Envelope
	if err := decoder.Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Type != "error" || response.Error == nil || response.Error.Code != coreapi.CodeUnavailable {
		t.Fatalf("unsupported version response = %#v", response)
	}
	_ = conn.Close()

	// Inspect a raw response so the cross-process assertion covers the wire,
	// not only the Go client's decoded projection.
	conn, err = Dial(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	decoder = json.NewDecoder(conn)
	if err := json.NewEncoder(conn).Encode(Envelope{
		Protocol: ProtocolVersion,
		ID:       "hello",
		Method:   methodHello,
		Params:   json.RawMessage(`{"version":"chuzi.core/v1"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Type != "result" {
		t.Fatalf("hello response = %#v", response)
	}
	secretInput := coreapi.SubmitRequest{
		RequestID:          "cross-process-request",
		AccountID:          "account-secret",
		IdempotencyKey:     "idempotency-secret",
		NotificationRoomID: "!room-secret:example.org",
		Actor:              "@actor-secret:example.org",
	}
	requestBytes, err := marshalRequest("submit", methodSubmitRequest, secretInput)
	if err != nil {
		t.Fatal(err)
	}
	requestBytes = append(requestBytes, '\n')
	if _, err := conn.Write(requestBytes); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Type != "result" || response.Result == nil {
		t.Fatalf("submit response = %#v", response)
	}
	wireResult := string(response.Result)
	for _, secret := range []string{"account-secret", "idempotency-secret", "room-secret", "actor-secret"} {
		if strings.Contains(wireResult, secret) {
			t.Fatalf("wire response leaked %q: %s", secret, wireResult)
		}
	}
	var submitted SubmitResult
	if err := json.Unmarshal(response.Result, &submitted); err != nil {
		t.Fatal(err)
	}
	if submitted.Request.Account == "account-secret" || submitted.Request.State != "QUEUED" {
		t.Fatalf("unsafe request projection = %#v", submitted.Request)
	}
	_ = conn.Close()

	client, err := Connect(context.Background(), endpoint, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	account, err := client.GetAccount(context.Background(), "account-secret")
	if err != nil {
		t.Fatal(err)
	}
	if account.Account == "account-secret" || account.State != "QUEUED" {
		t.Fatalf("unsafe account projection = %#v", account)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errorCh := make(chan error, 1)
	go func() {
		_, callErr := client.GetRequest(ctx, "block")
		errorCh <- callErr
	}()
	waitCrossProcessLine(t, output, crossProcessCancelReady)
	cancel()
	select {
	case callErr := <-errorCh:
		if got := coreapi.CodeOf(callErr); got != coreapi.CodeCancelled {
			t.Fatalf("cross-process cancellation code = %q, err=%v", got, callErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cross-process client did not return after cancellation")
	}
	waitCrossProcessLine(t, output, crossProcessCancelFinish)
}

// TestCrossProcessTransportHelper is selected only in the child process.
func TestCrossProcessTransportHelper(t *testing.T) {
	if os.Getenv(crossProcessHelperEnv) != "1" {
		return
	}
	dataDir := os.Getenv(crossProcessDataDirEnv)
	endpoint := os.Getenv(crossProcessEndpointEnv)
	if dataDir == "" || endpoint == "" {
		t.Fatal("cross-process helper configuration is missing")
	}
	cfg, err := config.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.CreateAccount("account-secret"); err != nil {
		t.Fatal(err)
	}
	sequence := 0
	clock := func() time.Time { return time.Date(2026, 9, 17, 16, 0, 0, 0, time.UTC) }
	requests, err := requestservice.New(database, clock, func(kind string) string {
		sequence++
		return fmt.Sprintf("%s-cross-%d", kind, sequence)
	}, "cross-process-test")
	if err != nil {
		t.Fatal(err)
	}
	api, err := core.New(core.Dependencies{Requests: requests, Store: database})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := Listen(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(crossProcessBlockingAPI{API: api}, listener, Config{})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(os.Stdout, crossProcessHelperReady)
	if err := server.Serve(); err != nil {
		t.Fatal(err)
	}
}

type crossProcessBlockingAPI struct {
	coreapi.API
}

func (a crossProcessBlockingAPI) GetRequest(ctx context.Context, requestID string) (coreapi.Request, error) {
	if requestID == "block" {
		fmt.Fprintln(os.Stdout, crossProcessCancelReady)
		<-ctx.Done()
		fmt.Fprintln(os.Stdout, crossProcessCancelFinish)
		return coreapi.Request{}, ctx.Err()
	}
	return a.API.GetRequest(ctx, requestID)
}

func startCrossProcessHelper(t *testing.T) (string, <-chan string) {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "runtime")
	endpoint := EndpointPath(dataDir)
	command := exec.Command(os.Args[0], "-test.run=^TestCrossProcessTransportHelper$", "-test.v")
	command.Env = append(os.Environ(),
		crossProcessHelperEnv+"=1",
		crossProcessEndpointEnv+"="+endpoint,
		crossProcessDataDirEnv+"="+dataDir,
	)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	output := make(chan string, 32)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			output <- scanner.Text()
		}
		close(output)
	}()
	t.Cleanup(func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		_ = command.Wait()
	})
	return endpoint, output
}

func waitCrossProcessLine(t *testing.T, output <-chan string, expected string) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case line, ok := <-output:
			if !ok {
				t.Fatalf("cross-process helper exited before %q", expected)
			}
			if line == expected {
				return
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for cross-process helper line %q", expected)
		}
	}
}
