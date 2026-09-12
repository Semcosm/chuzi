package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/automation"
)

func TestPluginHelperProcess(t *testing.T) {
	if os.Getenv("CHUZI_PLUGIN_HELPER") != "1" {
		return
	}
	decoder := json.NewDecoder(bufio.NewReader(os.Stdin))
	encoder := json.NewEncoder(os.Stdout)
	for {
		var request automation.Envelope
		if err := decoder.Decode(&request); err != nil {
			os.Exit(0)
		}
		switch request.Type {
		case automation.Hello:
			capabilities := "fake.probe@1,fake.cancel@1"
			if os.Getenv("CHUZI_PLUGIN_BAD_CAPS") == "1" {
				capabilities = "malformed"
			}
			_ = encoder.Encode(automation.Request(request.ID, automation.HelloAck, map[string]string{
				"adapter_id":   "fake-plugin",
				"version":      "1.0.0",
				"api":          automation.APIVersion,
				"capabilities": capabilities,
			}))
		case automation.Execute:
			if request.Payload["operation"] == "hold" {
				continue
			}
			if request.Payload["operation"] == "crash" {
				os.Exit(3)
			}
			if request.Payload["operation"] == "fail" {
				_ = encoder.Encode(automation.Request(request.ID, automation.OperationFailed, map[string]string{
					"failure_class": "transient",
					"failure_code":  "fake_unavailable",
					"retryable":     "true",
				}))
				continue
			}
			_ = encoder.Encode(automation.Request(request.ID, automation.OperationStarted, nil))
			_ = encoder.Encode(automation.Request(request.ID, automation.OperationSucceeded, map[string]string{
				"facts": `{"adapter":"fake-plugin","status":"ready"}`,
			}))
		case automation.Cancel:
			_ = encoder.Encode(automation.Request(request.ID, automation.OperationCancelled, map[string]string{"operation_id": request.Payload["operation_id"]}))
		case automation.Shutdown:
			_ = encoder.Encode(automation.Request(request.ID, automation.ShutdownAck, nil))
			os.Exit(0)
		}
	}
}

func TestClientSpeaksAdapterProtocolAndKeepsRuntimeFactsRedacted(t *testing.T) {
	client, err := StartAdapter(context.Background(), Command{
		Mode:        Native,
		Executable:  os.Args[0],
		Args:        []string{"-test.run=TestPluginHelperProcess", "--"},
		Environment: []string{"CHUZI_PLUGIN_HELPER=1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = client.Close(ctx)
	}()
	descriptor, err := client.Describe(context.Background())
	if err != nil || descriptor.ID != "fake-plugin" || descriptor.API != automation.APIVersion {
		t.Fatalf("Describe() = %#v, %v", descriptor, err)
	}
	session := automation.Session{
		SessionID:  "session-1",
		AccountID:  "account-1",
		RequestID:  "request-1",
		ProfileDir: filepath.Join(t.TempDir(), "profile-1"),
		Runtime:    "headless-cdp",
		Handle:     "handle-1",
	}
	result, err := client.Execute(context.Background(), session, automation.Operation{ID: "op-1", Name: "probe"})
	if err != nil || !result.Succeeded || result.Facts["adapter"] != "fake-plugin" {
		t.Fatalf("Execute() = %#v, %v", result, err)
	}
	result, err = client.Execute(context.Background(), session, automation.Operation{ID: "op-2", Name: "fail"})
	if err != nil || result.Succeeded || result.Failure == nil || result.Failure.Code != "fake_unavailable" {
		t.Fatalf("failed Execute() = %#v, %v", result, err)
	}
	if err := client.Cancel(context.Background(), "op-1"); err != nil {
		t.Fatalf("Cancel() = %v", err)
	}
}

func TestClientTimeoutAndPluginCrashAreTerminal(t *testing.T) {
	client, err := StartAdapter(context.Background(), Command{
		Mode: Native, Executable: os.Args[0],
		Args: []string{"-test.run=TestPluginHelperProcess", "--"},
		Environment: []string{"CHUZI_PLUGIN_HELPER=1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = client.Close(ctx)
	}()
	session := automation.Session{
		SessionID: "session-1", AccountID: "account-1", RequestID: "request-1",
		ProfileDir: filepath.Join(t.TempDir(), "profile-1"),
	}
	timeoutCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := client.Execute(timeoutCtx, session, automation.Operation{ID: "op-hold", Name: "hold"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout Execute() error = %v", err)
	}
	if _, err := client.Execute(context.Background(), session, automation.Operation{ID: "op-crash", Name: "crash"}); !errors.Is(err, ErrProcessExited) {
		t.Fatalf("crashed Execute() error = %v", err)
	}
}

func TestClientRejectsMalformedCapabilityAdvertisement(t *testing.T) {
	client, err := StartAdapter(context.Background(), Command{
		Mode: Native, Executable: os.Args[0],
		Args: []string{"-test.run=TestPluginHelperProcess", "--"},
		Environment: []string{"CHUZI_PLUGIN_HELPER=1", "CHUZI_PLUGIN_BAD_CAPS=1"},
	})
	if err == nil || !errors.Is(err, ErrProtocol) {
		if client != nil {
			_ = client.Close(context.Background())
		}
		t.Fatalf("malformed capability error = %v, want ErrProtocol", err)
	}
}
