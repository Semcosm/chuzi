package core

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/internal/automation"
	"github.com/Semcosm/chuzi/internal/browser"
)

var pipelineTestTime = time.Date(2026, 9, 17, 16, 0, 0, 0, time.UTC)

type pipelineTestWorker struct {
	result  browser.WorkerResult
	runErr  error
	cancelN int
	closeN  int
}

func (w *pipelineTestWorker) Run(context.Context) (browser.WorkerResult, error) {
	return w.result, w.runErr
}

func (w *pipelineTestWorker) Cancel(context.Context) error {
	w.cancelN++
	return nil
}

func (w *pipelineTestWorker) Close(context.Context) error {
	w.closeN++
	return nil
}

type pipelineTestFactory struct{ worker browser.Worker }

func (f pipelineTestFactory) Start(context.Context, browser.WorkerSpec) (browser.Worker, error) {
	return f.worker, nil
}

type pipelineTestCredentials struct {
	payload []byte
	calls   int
	err     error
}

func (c *pipelineTestCredentials) Use(_ context.Context, _ string, _ string, _ time.Time, fn func([]byte) error) error {
	c.calls++
	if c.err != nil {
		return c.err
	}
	return fn(append([]byte(nil), c.payload...))
}

type pipelineTestAdapter struct {
	result       automation.Result
	err          error
	cancels      int
	lastSession  automation.Session
	last         automation.Operation
	mutateParams bool
}

func (a *pipelineTestAdapter) Describe(context.Context) (automation.Descriptor, error) {
	return automation.Descriptor{ID: "pipeline-test", Version: "1", API: automation.APIVersion}, nil
}

func (a *pipelineTestAdapter) Execute(_ context.Context, session automation.Session, operation automation.Operation) (automation.Result, error) {
	a.lastSession = session
	a.last = operation
	if a.mutateParams {
		operation.Parameters["mutated"] = "yes"
	}
	return a.result, a.err
}

func (a *pipelineTestAdapter) Cancel(context.Context, string) error {
	a.cancels++
	return nil
}

func (a *pipelineTestAdapter) Close(context.Context) error { return nil }

type pipelineTestAwareAdapter struct {
	pipelineTestAdapter
	used []byte
	use  bool
}

func (a *pipelineTestAwareAdapter) ExecuteWithCredential(ctx context.Context, _ automation.Session, _ automation.Operation, use automation.CredentialUse) (automation.Result, error) {
	if !a.use {
		return a.result, a.err
	}
	return a.result, use(ctx, func(payload []byte) error {
		a.used = append([]byte(nil), payload...)
		return a.err
	})
}

func newPipelineTestWorker(t *testing.T, credentials CredentialProvider, adapter automation.Adapter, operation automation.Operation) (browser.Worker, *pipelineTestWorker) {
	t.Helper()
	runtime := &pipelineTestWorker{result: browser.WorkerResult{Succeeded: true}}
	factory := &pipelineFactory{
		delegate:    pipelineTestFactory{worker: runtime},
		credentials: credentials,
		automation:  adapter,
		clock:       func() time.Time { return pipelineTestTime },
		actor:       "pipeline-test",
		operation:   operation,
	}
	worker, err := factory.Start(context.Background(), browser.WorkerSpec{
		SessionID: "session-1", AccountID: "account-1", RequestID: "request-1",
		ProfileDir: filepath.Join(t.TempDir(), "profile"), LeaseID: "lease-1", Owner: "owner", Mode: "headless-cdp",
	})
	if err != nil {
		t.Fatal(err)
	}
	return worker, runtime
}

func TestPipelineClassifiesAdapterErrorSeparatelyFromCredentialFailure(t *testing.T) {
	wantErr := errors.New("adapter unavailable")
	credentials := &pipelineTestCredentials{payload: []byte("secret")}
	adapter := &pipelineTestAdapter{err: wantErr}
	worker, _ := newPipelineTestWorker(t, credentials, adapter, automation.Operation{Name: "probe"})
	result, err := worker.Run(context.Background())
	if !errors.Is(err, wantErr) || result.Failure != account.TransientFailure {
		t.Fatalf("adapter error result = %#v, %v", result, err)
	}
	if credentials.calls != 1 {
		t.Fatalf("credential calls = %d, want 1", credentials.calls)
	}
}

func TestPipelineAwareAdapterReceivesOnlyBoundCredentialCallback(t *testing.T) {
	credentials := &pipelineTestCredentials{payload: []byte("secret")}
	adapter := &pipelineTestAwareAdapter{pipelineTestAdapter: pipelineTestAdapter{result: automation.Result{Succeeded: true}}, use: true}
	worker, _ := newPipelineTestWorker(t, credentials, adapter, automation.Operation{Name: "login"})
	result, err := worker.Run(context.Background())
	if err != nil || !result.Succeeded {
		t.Fatalf("aware adapter result = %#v, %v", result, err)
	}
	if credentials.calls != 1 || string(adapter.used) != "secret" {
		t.Fatalf("credential callback calls=%d payload=%q", credentials.calls, adapter.used)
	}
}

func TestPipelineAwareCredentialBackendErrorIsNotAdapterFailure(t *testing.T) {
	wantErr := errors.New("credential unavailable")
	credentials := &pipelineTestCredentials{err: wantErr}
	adapter := &pipelineTestAwareAdapter{pipelineTestAdapter: pipelineTestAdapter{result: automation.Result{Succeeded: true}}, use: true}
	worker, _ := newPipelineTestWorker(t, credentials, adapter, automation.Operation{Name: "login"})
	result, err := worker.Run(context.Background())
	if err != nil || result.Failure != account.CredentialFailure {
		t.Fatalf("credential error result = %#v, %v", result, err)
	}
}

func TestPipelineRejectsMalformedWorkerFactBeforeCredentialUse(t *testing.T) {
	runtime := &pipelineTestWorker{result: browser.WorkerResult{Succeeded: true, Failure: account.TransientFailure}}
	credentials := &pipelineTestCredentials{payload: []byte("secret")}
	adapter := &pipelineTestAdapter{result: automation.Result{Succeeded: true}}
	factory := &pipelineFactory{
		delegate: pipelineTestFactory{worker: runtime}, credentials: credentials, automation: adapter,
		clock: func() time.Time { return pipelineTestTime }, actor: "pipeline-test", operation: automation.Operation{Name: "probe"},
	}
	worker, err := factory.Start(context.Background(), browser.WorkerSpec{SessionID: "s", AccountID: "a", RequestID: "r", ProfileDir: filepath.Join(t.TempDir(), "p"), LeaseID: "l", Owner: "o"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := worker.Run(context.Background())
	if err != nil || result.Failure != account.UnknownFailure || credentials.calls != 0 {
		t.Fatalf("malformed worker result = %#v, %v credential_calls=%d", result, err, credentials.calls)
	}
}

func TestPipelineRejectsNilContext(t *testing.T) {
	adapter := &pipelineTestAdapter{result: automation.Result{Succeeded: true}}
	worker, _ := newPipelineTestWorker(t, &pipelineTestCredentials{payload: []byte("secret")}, adapter, automation.Operation{Name: "probe"})
	result, err := worker.Run(nil)
	if err != nil || result.Failure != account.ConfigurationFailure {
		t.Fatalf("nil context result = %#v, %v", result, err)
	}
}

func TestPipelineCancelReachesBrowserAndAdapter(t *testing.T) {
	adapter := &pipelineTestAdapter{result: automation.Result{Succeeded: true}}
	worker, runtime := newPipelineTestWorker(t, &pipelineTestCredentials{payload: []byte("secret")}, adapter, automation.Operation{Name: "probe"})
	if err := worker.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	if runtime.cancelN != 1 || adapter.cancels != 1 {
		t.Fatalf("cancel counts worker=%d adapter=%d", runtime.cancelN, adapter.cancels)
	}
}

func TestPipelineCopiesOperationParametersPerRequest(t *testing.T) {
	parameters := map[string]string{"mode": "test"}
	adapter := &pipelineTestAdapter{result: automation.Result{Succeeded: true}, mutateParams: true}
	credentials := &pipelineTestCredentials{payload: []byte("secret")}
	worker, _ := newPipelineTestWorker(t, credentials, adapter, automation.Operation{Name: "probe", Parameters: parameters})
	if _, err := worker.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, mutated := parameters["mutated"]; mutated {
		t.Fatal("adapter mutation leaked into configured operation parameters")
	}
}

func TestPipelineEvaluatesGenshinPageFactsInCore(t *testing.T) {
	tests := []struct {
		name    string
		facts   map[string]string
		success bool
		failure account.FailureClass
	}{
		{
			name: "authenticated",
			facts: map[string]string{
				"platform": "genshin-cloudgame", "flow": "authorized-session-check",
				"page": "recognized", "shell": "present", "session": "authenticated",
			},
			success: true,
		},
		{
			name: "not authenticated",
			facts: map[string]string{
				"platform": "genshin-cloudgame", "flow": "authorized-session-check",
				"page": "recognized", "shell": "present", "session": "not_authenticated",
			},
			failure: account.CredentialFailure,
		},
		{
			name: "unrecognized page",
			facts: map[string]string{
				"platform": "unknown", "flow": "authorized-session-check",
				"page": "unrecognized", "shell": "missing", "session": "authenticated",
			},
			failure: account.UnknownFailure,
		},
		{
			name: "contradictory authentication markers",
			facts: map[string]string{
				"platform": "genshin-cloudgame", "flow": "authorized-session-check",
				"page": "recognized", "shell": "present", "session": "unknown",
			},
			failure: account.UnknownFailure,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter := &pipelineTestAdapter{result: automation.Result{Succeeded: true, Facts: test.facts}}
			worker, _ := newPipelineTestWorker(t, &pipelineTestCredentials{payload: []byte("unused")}, adapter, automation.Operation{Name: genshinCloudGameOperation})
			result, err := worker.Run(context.Background())
			if err != nil || result.Succeeded != test.success || result.Failure != test.failure {
				t.Fatalf("pipeline result = %#v, %v; want success=%v failure=%q", result, err, test.success, test.failure)
			}
		})
	}
}

func TestPipelinePropagatesEphemeralBrowserHandleToAdapter(t *testing.T) {
	runtime := &pipelineTestWorker{result: browser.WorkerResult{Succeeded: true, Handle: "headless-cdp://127.0.0.1:9222"}}
	adapter := &pipelineTestAdapter{result: automation.Result{Succeeded: true}}
	credentials := &pipelineTestCredentials{payload: []byte("secret")}
	factory := &pipelineFactory{
		delegate: pipelineTestFactory{worker: runtime}, credentials: credentials, automation: adapter,
		clock: func() time.Time { return pipelineTestTime }, actor: "pipeline-test", operation: automation.Operation{Name: "probe"},
	}
	worker, err := factory.Start(context.Background(), browser.WorkerSpec{SessionID: "s", AccountID: "a", RequestID: "r", ProfileDir: filepath.Join(t.TempDir(), "p"), LeaseID: "l", Owner: "o", Mode: "adapter"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if adapter.lastSession.Handle != runtime.result.Handle {
		t.Fatalf("adapter handle = %q, want %q", adapter.lastSession.Handle, runtime.result.Handle)
	}
}

func TestPipelineRejectsUntrustedBrowserHandleBeforeAdapter(t *testing.T) {
	runtime := &pipelineTestWorker{result: browser.WorkerResult{Succeeded: true, Handle: "headless-cdp://192.0.2.1:9222"}}
	adapter := &pipelineTestAdapter{result: automation.Result{Succeeded: true}}
	credentials := &pipelineTestCredentials{payload: []byte("secret")}
	factory := &pipelineFactory{
		delegate: pipelineTestFactory{worker: runtime}, credentials: credentials, automation: adapter,
		clock: func() time.Time { return pipelineTestTime }, actor: "pipeline-test", operation: automation.Operation{Name: "probe"},
	}
	worker, err := factory.Start(context.Background(), browser.WorkerSpec{SessionID: "s", AccountID: "a", RequestID: "r", ProfileDir: filepath.Join(t.TempDir(), "p"), LeaseID: "l", Owner: "o", Mode: "adapter"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := worker.Run(context.Background())
	if err != nil || result.Failure != account.UnknownFailure || credentials.calls != 0 {
		t.Fatalf("untrusted browser handle result = %#v, %v credential_calls=%d", result, err, credentials.calls)
	}
	if adapter.lastSession.SessionID != "" {
		t.Fatal("adapter executed despite untrusted browser handle")
	}
}

func TestPipelineRequiresBrowserHandleForAdapterMode(t *testing.T) {
	runtime := &pipelineTestWorker{result: browser.WorkerResult{Succeeded: true}}
	adapter := &pipelineTestAdapter{result: automation.Result{Succeeded: true}}
	credentials := &pipelineTestCredentials{payload: []byte("secret")}
	factory := &pipelineFactory{
		delegate: pipelineTestFactory{worker: runtime}, credentials: credentials, automation: adapter,
		clock: func() time.Time { return pipelineTestTime }, actor: "pipeline-test", operation: automation.Operation{Name: "probe"},
	}
	worker, err := factory.Start(context.Background(), browser.WorkerSpec{SessionID: "s", AccountID: "a", RequestID: "r", ProfileDir: filepath.Join(t.TempDir(), "p"), LeaseID: "l", Owner: "o", Mode: "adapter"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := worker.Run(context.Background())
	if err != nil || result.Failure != account.UnknownFailure || credentials.calls != 0 {
		t.Fatalf("missing browser handle result = %#v, %v credential_calls=%d", result, err, credentials.calls)
	}
	if adapter.lastSession.SessionID != "" {
		t.Fatal("adapter executed without a worker-owned browser handle")
	}
}
