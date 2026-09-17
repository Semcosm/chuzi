package core_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/internal/automation"
	"github.com/Semcosm/chuzi/internal/browser"
	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/core"
	"github.com/Semcosm/chuzi/internal/coreapi"
	"github.com/Semcosm/chuzi/internal/coretest"
	"github.com/Semcosm/chuzi/internal/matrix"
	"github.com/Semcosm/chuzi/internal/queue"
	"github.com/Semcosm/chuzi/internal/request"
	"github.com/Semcosm/chuzi/internal/store"
)

var verticalTime = time.Date(2026, 9, 17, 15, 0, 0, 0, time.UTC)

type verticalHarness struct {
	database   *store.Store
	clock      *coretest.Clock
	ids        *coretest.IDs
	api        coreapi.API
	scheduler  *queue.Scheduler
	notifier   *matrix.Notifier
	creds      *coretest.CredentialProvider
	workers    *coretest.BrowserWorkerFactory
	automation *coretest.AutomationAdapter
	sender     *coretest.MatrixSender
}

func newVerticalHarness(t *testing.T, workerPlans []coretest.WorkerPlan, automationPlans []coretest.AutomationPlan, retry account.RetryPolicy) *verticalHarness {
	t.Helper()
	cfg, err := config.New(filepath.Join(t.TempDir(), "runtime"))
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
	clock := coretest.NewClock(verticalTime)
	ids := coretest.NewIDs()
	requests, err := request.New(database, clock.Now, ids.Next, "coretest-api")
	if err != nil {
		t.Fatal(err)
	}
	api, err := core.New(core.Dependencies{Requests: requests, Store: database})
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := browser.NewProfiles(cfg)
	if err != nil {
		t.Fatal(err)
	}
	workers := &coretest.BrowserWorkerFactory{Plans: workerPlans}
	automationFake := &coretest.AutomationAdapter{Plans: automationPlans}
	creds := &coretest.CredentialProvider{Payload: []byte("credential-secret")}
	pipeline, err := core.NewPipelineRunner(database, core.PipelineConfig{
		Factory: workers, Credentials: creds, Automation: automationFake, Profiles: profiles,
		Clock: clock.Now, Actor: "coretest-runner",
		Operation: automation.Operation{Name: "login"},
		Browser:   browser.Config{LeaseTTL: 10 * time.Minute, CancelTimeout: 100 * time.Millisecond, ShutdownTimeout: 100 * time.Millisecond, Clock: clock.Now},
	})
	if err != nil {
		t.Fatal(err)
	}
	scheduler, err := queue.New(database, pipeline, queue.Config{
		Owner: "coretest-queue", LeaseTTL: 10 * time.Minute, RunTimeout: 100 * time.Millisecond,
		MaxGlobalConcurrency: 1, RetryPolicy: retry, Clock: clock.Now, NewID: ids.Next,
	})
	if err != nil {
		t.Fatal(err)
	}
	sender := &coretest.MatrixSender{}
	notifier, err := matrix.NewNotifier(database, sender, matrix.NotifierConfig{
		Owner: "coretest-notifier", ClaimTTL: time.Minute, RetryBase: time.Minute, RetryMax: 4 * time.Minute,
		BatchSize: 32, Clock: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &verticalHarness{database: database, clock: clock, ids: ids, api: api, scheduler: scheduler, notifier: notifier, creds: creds, workers: workers, automation: automationFake, sender: sender}
}

func submit(t *testing.T, api coreapi.API, requestID string) coreapi.Request {
	t.Helper()
	request, duplicate, err := api.SubmitRequest(context.Background(), coreapi.SubmitRequest{
		RequestID: requestID, AccountID: "account-1", IdempotencyKey: "idem-" + requestID,
		NotificationRoomID: "!room:example.org", Actor: "@test:example.org",
	})
	if err != nil || duplicate || request.State != string(account.Queued) {
		t.Fatalf("submit = %#v duplicate=%v err=%v", request, duplicate, err)
	}
	return request
}

func TestVerticalSliceCompletesCoreRequestAndDeliversOutbox(t *testing.T) {
	h := newVerticalHarness(t, nil, nil, account.RetryPolicy{MaxAttempts: 1})
	submit(t, h.api, "request-success")
	duplicate, idempotent, err := h.api.SubmitRequest(context.Background(), coreapi.SubmitRequest{
		RequestID: "request-success", AccountID: "account-1", IdempotencyKey: "idem-request-success", NotificationRoomID: "!room:example.org",
	})
	if err != nil || !idempotent || duplicate.State != string(account.Queued) {
		t.Fatalf("duplicate submit = %#v duplicate=%v err=%v", duplicate, idempotent, err)
	}
	if _, err := h.scheduler.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := h.api.GetResult(context.Background(), "request-success")
	if err != nil || result.Outcome != "succeeded" || result.State != string(account.LoginSucceeded) {
		t.Fatalf("result = %#v err=%v", result, err)
	}
	events, err := h.api.ListEvents(context.Background(), coreapi.EventQuery{RequestID: "request-success", Limit: 16})
	states := make(map[string]bool, len(events))
	for _, event := range events {
		states[event.To] = true
	}
	if err != nil || len(events) != 4 || !states[string(account.Queued)] || !states[string(account.Starting)] || !states[string(account.LoggingIn)] || !states[string(account.LoginSucceeded)] {
		t.Fatalf("audit events = %#v err=%v", events, err)
	}
	if h.creds.UseCount() != 1 || len(h.automation.Sessions) != 1 || len(h.workers.Specs) != 1 {
		t.Fatalf("pipeline calls credentials=%d automation=%d workers=%d", h.creds.UseCount(), len(h.automation.Sessions), len(h.workers.Specs))
	}
	if h.automation.Sessions[0].ProfileDir == "" || h.automation.Ops[0].Name != "login" {
		t.Fatalf("automation session/op = %#v/%#v", h.automation.Sessions[0], h.automation.Ops[0])
	}
	delivery, err := h.notifier.Flush(context.Background())
	if err != nil || delivery.Delivered != 4 {
		t.Fatalf("delivery = %#v err=%v", delivery, err)
	}
	second, err := h.notifier.Flush(context.Background())
	if err != nil || second.Claimed != 0 || len(h.sender.Sends) != 4 {
		t.Fatalf("duplicate delivery = %#v sends=%d err=%v", second, len(h.sender.Sends), err)
	}
	notifications, err := h.api.ListNotifications(context.Background(), coreapi.NotificationQuery{RequestID: "request-success"})
	if err != nil || len(notifications) != 4 {
		t.Fatalf("notifications = %#v err=%v", notifications, err)
	}
	for _, notification := range notifications {
		if notification.Status != "delivered" {
			t.Fatalf("notification not delivered: %#v", notification)
		}
	}
}

func TestVerticalSliceCancellationRaceWinsOverWorkerTerminalFact(t *testing.T) {
	started := make(chan struct{})
	h := newVerticalHarness(t, []coretest.WorkerPlan{{Block: true, Started: started}}, nil, account.RetryPolicy{MaxAttempts: 2})
	submit(t, h.api, "request-cancel")
	resultCh := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _, err := h.scheduler.RunOnce(ctx); resultCh <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	cancelled, err := h.api.CancelRequest(context.Background(), coreapi.CancelRequest{RequestID: "request-cancel", Actor: "@test:example.org", Reason: "user stopped"})
	if err != nil || cancelled.State != string(account.Cancelled) {
		t.Fatalf("cancel = %#v err=%v", cancelled, err)
	}
	cancel()
	if err := <-resultCh; err != nil {
		t.Fatal(err)
	}
	result, err := h.api.GetResult(context.Background(), "request-cancel")
	if err != nil || result.Outcome != "cancelled" {
		t.Fatalf("cancel race result = %#v err=%v", result, err)
	}
	if h.creds.UseCount() != 0 || len(h.automation.Sessions) != 0 {
		t.Fatalf("cancelled worker reached pipeline credentials=%d automation=%d", h.creds.UseCount(), len(h.automation.Sessions))
	}
}

func TestVerticalSliceTimeoutAndWorkerCrashAreClassified(t *testing.T) {
	for _, test := range []struct {
		name string
		plan coretest.WorkerPlan
		want account.FailureClass
	}{
		{name: "timeout", plan: coretest.WorkerPlan{Block: true}, want: account.TransientFailure},
		{name: "crash", plan: coretest.WorkerPlan{Err: coretest.ErrFakeWorkerCrashed}, want: account.TransientFailure},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newVerticalHarness(t, []coretest.WorkerPlan{test.plan}, nil, account.RetryPolicy{MaxAttempts: 1})
			submit(t, h.api, "request-"+test.name)
			if _, err := h.scheduler.RunOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			result, err := h.api.GetResult(context.Background(), "request-"+test.name)
			if err != nil || result.State != string(account.LoginFailed) || result.Failure != string(test.want) {
				t.Fatalf("result = %#v err=%v", result, err)
			}
		})
	}
}

func TestVerticalSliceRetriesAutomationFailureDeterministically(t *testing.T) {
	h := newVerticalHarness(t, nil, []coretest.AutomationPlan{
		{Result: automation.Result{Succeeded: false, Failure: &automation.Failure{Class: automation.FailureTransient, Code: "network", Retryable: true}}},
		{Result: automation.Result{Succeeded: true}},
	}, account.RetryPolicy{MaxAttempts: 2, BaseDelay: time.Minute, MaxDelay: time.Minute})
	submit(t, h.api, "request-retry")
	first, err := h.scheduler.RunOnce(context.Background())
	if err != nil || !first.Retried {
		t.Fatalf("first retry = %#v err=%v", first, err)
	}
	pending, err := h.api.GetResult(context.Background(), "request-retry")
	if err != nil || pending.Outcome != "pending" || pending.Attempt != 1 {
		t.Fatalf("pending retry = %#v err=%v", pending, err)
	}
	h.clock.Advance(time.Minute)
	second, err := h.scheduler.RunOnce(context.Background())
	if err != nil || !second.Succeeded {
		t.Fatalf("second retry = %#v err=%v", second, err)
	}
	if h.creds.UseCount() != 2 || len(h.automation.Sessions) != 2 {
		t.Fatalf("retry calls credentials=%d automation=%d", h.creds.UseCount(), len(h.automation.Sessions))
	}
}

func TestVerticalSliceAutomationErrorIsNotCredentialFailure(t *testing.T) {
	h := newVerticalHarness(t, nil, []coretest.AutomationPlan{{Err: errors.New("adapter crashed")}}, account.RetryPolicy{MaxAttempts: 1})
	submit(t, h.api, "request-adapter-error")
	if _, err := h.scheduler.RunOnce(context.Background()); err != nil {
		// The queue records the classified runtime failure even when the runner
		// returns the adapter error; no transport caller receives its text.
		t.Fatalf("classified adapter error = %v", err)
	}
	result, err := h.api.GetResult(context.Background(), "request-adapter-error")
	if err != nil || result.Failure != string(account.TransientFailure) || result.Failure == string(account.CredentialFailure) {
		t.Fatalf("adapter error result = %#v err=%v", result, err)
	}
}

func TestVerticalSliceCredentialFailureDoesNotRetry(t *testing.T) {
	h := newVerticalHarness(t, nil, nil, account.RetryPolicy{MaxAttempts: 3, BaseDelay: time.Minute, MaxDelay: time.Minute})
	h.creds.Failures = []error{errors.New("credential unavailable")}
	submit(t, h.api, "request-credential")
	outcome, err := h.scheduler.RunOnce(context.Background())
	if err != nil || outcome.Retried {
		t.Fatalf("credential outcome = %#v err=%v", outcome, err)
	}
	result, err := h.api.GetResult(context.Background(), "request-credential")
	if err != nil || result.State != string(account.LoginFailed) || result.Failure != string(account.CredentialFailure) {
		t.Fatalf("credential result = %#v err=%v", result, err)
	}
	if len(h.automation.Sessions) != 0 {
		t.Fatal("automation ran after credential failure")
	}
}

func TestVerticalSliceRecoversExpiredLoggingInAfterRestart(t *testing.T) {
	h := newVerticalHarness(t, nil, nil, account.RetryPolicy{MaxAttempts: 2, BaseDelay: time.Minute, MaxDelay: time.Minute})
	submit(t, h.api, "request-restart")
	claim, err := h.database.ClaimNext(h.clock.Now(), "lease-crash", "crashed-worker", time.Minute, "claim-crash", "crashed-worker", "claim", store.QueueOptions{MaxGlobalConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.database.ApplyEvent(account.Event{EventID: "start-crash", AccountID: "account-1", RequestID: "request-restart", From: account.Starting, ExpectedRevision: claim.Transition.State.Revision, To: account.LoggingIn, Reason: "worker entered session", Actor: "crashed-worker", OccurredAt: h.clock.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := h.database.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := h.database.Config()
	restarted, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	profiles, err := browser.NewProfiles(cfg)
	if err != nil {
		t.Fatal(err)
	}
	pipeline, err := core.NewPipelineRunner(restarted, core.PipelineConfig{
		Factory: &coretest.BrowserWorkerFactory{}, Credentials: &coretest.CredentialProvider{Payload: []byte("secret")}, Automation: &coretest.AutomationAdapter{}, Profiles: profiles,
		Clock: h.clock.Now, Actor: "recovery-runner", Operation: automation.Operation{Name: "login"},
		Browser: browser.Config{LeaseTTL: time.Minute, CancelTimeout: 100 * time.Millisecond, ShutdownTimeout: 100 * time.Millisecond, Clock: h.clock.Now},
	})
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := queue.New(restarted, pipeline, queue.Config{Owner: "recovery-queue", LeaseTTL: time.Minute, RunTimeout: time.Second, MaxGlobalConcurrency: 1, RetryPolicy: account.RetryPolicy{MaxAttempts: 2, BaseDelay: time.Minute, MaxDelay: time.Minute}, Clock: h.clock.Now, NewID: h.ids.Next})
	if err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(2 * time.Minute)
	pass, err := recovered.RunOnce(context.Background())
	if err != nil || !pass.Idle {
		t.Fatalf("recovery pass = %#v err=%v", pass, err)
	}
	request, err := restarted.GetRequest("request-restart")
	if err != nil || request.State != account.Queued || request.LastFailure != account.TransientFailure || !request.NotBefore.Equal(h.clock.Now().Add(time.Minute)) {
		t.Fatalf("recovered request = %#v err=%v", request, err)
	}
	h.clock.Advance(time.Minute)
	if _, err := recovered.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := restarted.GetRequest("request-restart")
	if err != nil || result.State != account.LoginSucceeded {
		t.Fatalf("post-recovery request = %#v err=%v", result, err)
	}
}

func TestVerticalSliceNotifierRetriesWithStableEventID(t *testing.T) {
	h := newVerticalHarness(t, nil, nil, account.RetryPolicy{MaxAttempts: 1})
	submit(t, h.api, "request-notify")
	if _, err := h.scheduler.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.sender.Failures = []error{errors.New("matrix offline")}
	first, err := h.notifier.Flush(context.Background())
	if err != nil || first.Retried != 1 || first.Delivered != 3 {
		t.Fatalf("first notify = %#v err=%v", first, err)
	}
	h.clock.Advance(time.Minute)
	second, err := h.notifier.Flush(context.Background())
	if err != nil || second.Delivered != 1 || len(h.sender.Sends) != 5 {
		t.Fatalf("second notify = %#v sends=%d err=%v", second, len(h.sender.Sends), err)
	}
	if h.sender.Sends[0].EventID != h.sender.Sends[4].EventID {
		t.Fatalf("retry changed event ID: first=%s retry=%s", h.sender.Sends[0].EventID, h.sender.Sends[4].EventID)
	}
}
