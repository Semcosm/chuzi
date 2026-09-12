package automation

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func validSession(t *testing.T) Session {
	t.Helper()
	return Session{
		SessionID:  "session-1",
		AccountID:  "account-1",
		RequestID:  "request-1",
		ProfileDir: filepath.Join(t.TempDir(), "profiles", "session-1"),
		Runtime:    "headless-cdp",
		Handle:     "handle-1",
	}
}

func TestContractValidatesSessionAndOperationBoundaries(t *testing.T) {
	if err := validSession(t).Validate(); err != nil {
		t.Fatal(err)
	}
	invalid := validSession(t)
	invalid.ProfileDir = "relative/profile"
	if !errors.Is(invalid.Validate(), ErrInvalidContract) {
		t.Fatalf("relative profile was accepted: %v", invalid.Validate())
	}
	operation := Operation{ID: "operation-1", Name: "launch", Parameters: map[string]string{"mode": "test"}, Deadline: time.Now()}
	if err := operation.Validate(); err != nil {
		t.Fatal(err)
	}
	operation.Parameters[""] = "invalid"
	if !errors.Is(operation.Validate(), ErrInvalidContract) {
		t.Fatalf("empty parameter name was accepted: %v", operation.Validate())
	}
	delete(operation.Parameters, "")
	operation.Parameters["access_token"] = "must-not-cross-boundary"
	if !errors.Is(operation.Validate(), ErrInvalidContract) {
		t.Fatal("credential parameter was accepted")
	}
}

func TestDescriptorAndResultValidation(t *testing.T) {
	descriptor := Descriptor{
		ID: "fake", Version: "0.1.0", API: APIVersion,
		Capabilities: []Capability{{ID: "bettergi.session", Version: "1"}},
	}
	if err := descriptor.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := descriptor.SortedCapabilities()[0].ID; got != "bettergi.session" {
		t.Fatalf("capability ordering = %q", got)
	}
	if err := (Result{Succeeded: true}).Validate(); err != nil {
		t.Fatal(err)
	}
	failure := Failure{Class: FailureTransient, Code: "plugin_unavailable", Retryable: true}
	if err := (Result{Failure: &failure}).Validate(); err != nil {
		t.Fatal(err)
	}
	if errors.Is((Result{Succeeded: false}).Validate(), ErrInvalidContract) == false {
		t.Fatal("failed result without classification was accepted")
	}
	if err := (Result{Succeeded: true, Facts: map[string]string{"token": "secret"}}).Validate(); !errors.Is(err, ErrInvalidContract) {
		t.Fatal("credential result fact was accepted")
	}
}

func TestProtocolEnvelopeUsesVersionedJSONLMessageTypes(t *testing.T) {
	message := Request("op-1", Execute, map[string]string{"operation": "probe"})
	if err := message.Validate(); err != nil {
		t.Fatal(err)
	}
	if message.Protocol != APIVersion || message.Type != Execute {
		t.Fatalf("message = %#v", message)
	}
	if err := (Envelope{Protocol: "v0", ID: "id", Type: Hello}).Validate(); err == nil {
		t.Fatal("unsupported protocol was accepted")
	}
	if err := (Envelope{Protocol: APIVersion, ID: "id", Type: Error}).Validate(); err == nil {
		t.Fatal("error without stable code was accepted")
	}
	if err := (Envelope{Protocol: APIVersion, ID: "id", Type: "unknown"}).Validate(); err == nil {
		t.Fatal("unknown message type was accepted")
	}
}

type fakeAdapter struct {
	descriptor Descriptor
	result     Result
	last       Operation
	cancelled  string
	closed     bool
}

func (f *fakeAdapter) Describe(context.Context) (Descriptor, error) { return f.descriptor, nil }
func (f *fakeAdapter) Execute(_ context.Context, session Session, operation Operation) (Result, error) {
	if err := session.Validate(); err != nil {
		return Result{}, err
	}
	if err := operation.Validate(); err != nil {
		return Result{}, err
	}
	f.last = operation
	return f.result, nil
}
func (f *fakeAdapter) Cancel(_ context.Context, operationID string) error {
	f.cancelled = operationID
	return nil
}
func (f *fakeAdapter) Close(context.Context) error { f.closed = true; return nil }

func TestFakeAdapterCoversCancellationAndCloseWithoutBusinessStateWrites(t *testing.T) {
	adapter := &fakeAdapter{
		descriptor: Descriptor{ID: "fake", Version: "1", API: APIVersion},
		result:     Result{Succeeded: true, Facts: map[string]string{"phase": "ready"}},
	}
	result, err := adapter.Execute(context.Background(), validSession(t), Operation{ID: "op-1", Name: "probe"})
	if err != nil || !result.Succeeded || adapter.last.Name != "probe" {
		t.Fatalf("Execute() = %#v, %v", result, err)
	}
	if err := adapter.Cancel(context.Background(), "op-1"); err != nil || adapter.cancelled != "op-1" {
		t.Fatalf("Cancel() = %v, id=%q", err, adapter.cancelled)
	}
	if err := adapter.Close(context.Background()); err != nil || !adapter.closed {
		t.Fatalf("Close() = %v, closed=%v", err, adapter.closed)
	}
}
