package queue

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/internal/config"
	requestservice "github.com/Semcosm/chuzi/internal/request"
	"github.com/Semcosm/chuzi/internal/store"
)

var schedulerTestTime = time.Date(2026, time.September, 9, 12, 0, 0, 0, time.UTC)

type fakeRunner struct {
	results []Result
	errors  []error
	works   []Work
}

func (r *fakeRunner) Run(_ context.Context, work Work) (Result, error) {
	r.works = append(r.works, work)
	if len(r.errors) > 0 {
		err := r.errors[0]
		r.errors = r.errors[1:]
		if err != nil {
			return Result{}, err
		}
	}
	if len(r.results) == 0 {
		return Result{Succeeded: true}, nil
	}
	result := r.results[0]
	r.results = r.results[1:]
	return result, nil
}

type advancingRunner struct {
	clock   *testClock
	advance time.Duration
	result  Result
}

func (r *advancingRunner) Run(_ context.Context, work Work) (Result, error) {
	r.clock.now = r.clock.now.Add(r.advance)
	return r.result, nil
}

type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time { return c.now }

func openQueueTestStore(t *testing.T) *store.Store {
	t.Helper()
	cfg, err := config.New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	return database
}

func newTestIDs() IDGenerator {
	counts := make(map[string]int)
	return func(kind string) string {
		counts[kind]++
		return fmt.Sprintf("%s-%d", kind, counts[kind])
	}
}

func newTestRequestService(t *testing.T, database *store.Store, clock *testClock) *requestservice.Service {
	t.Helper()
	service, err := requestservice.New(database, clock.Now, requestservice.IDGenerator(newTestIDs()), "queue-test")
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func newTestScheduler(t *testing.T, database *store.Store, runner Runner, clock *testClock, policy account.RetryPolicy) *Scheduler {
	t.Helper()
	scheduler, err := New(database, runner, Config{
		Owner:                "queue-test",
		LeaseTTL:             10 * time.Minute,
		RunTimeout:           time.Minute,
		MaxGlobalConcurrency: 1,
		RetryPolicy:          policy,
		Clock:                clock.Now,
		NewID:                newTestIDs(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return scheduler
}

func createAccount(t *testing.T, database *store.Store, accountID string) {
	t.Helper()
	if _, err := database.CreateAccount(accountID); err != nil {
		t.Fatal(err)
	}
}

func requireRequestState(t *testing.T, database *store.Store, requestID string, want account.Status) store.Request {
	t.Helper()
	request, err := database.GetRequest(requestID)
	if err != nil {
		t.Fatal(err)
	}
	if request.State != want {
		t.Fatalf("request %s state = %s, want %s", requestID, request.State, want)
	}
	return request
}

func TestRunOnceUsesFakeRunnerAndPersistsSuccess(t *testing.T) {
	database := openQueueTestStore(t)
	createAccount(t, database, "account-1")
	clock := &testClock{now: schedulerTestTime}
	requests := newTestRequestService(t, database, clock)
	created, idempotent, err := requests.Submit(requestservice.SubmitInput{
		RequestID:      "request-1",
		AccountID:      "account-1",
		IdempotencyKey: "idem-1",
	})
	if err != nil || idempotent || created.State != account.Queued {
		t.Fatalf("first submit = %#v, %t, %v", created, idempotent, err)
	}
	duplicate, idempotent, err := requests.Submit(requestservice.SubmitInput{
		RequestID:      "request-1",
		AccountID:      "account-1",
		IdempotencyKey: "idem-1",
	})
	if err != nil || !idempotent || duplicate.RequestID != created.RequestID {
		t.Fatalf("duplicate submit = %#v, %t, %v", duplicate, idempotent, err)
	}

	runner := &fakeRunner{results: []Result{{Succeeded: true}}}
	scheduler := newTestScheduler(t, database, runner, clock, account.RetryPolicy{
		MaxAttempts: 2,
		BaseDelay:   time.Minute,
		MaxDelay:    2 * time.Minute,
	})
	outcome, err := scheduler.RunOnce(context.Background())
	if err != nil || outcome.Idle || !outcome.Succeeded || outcome.Request.RequestID != "request-1" {
		t.Fatalf("RunOnce() = %#v, %v", outcome, err)
	}
	if len(runner.works) != 1 || runner.works[0].Request.RequestID != "request-1" {
		t.Fatalf("runner works = %#v", runner.works)
	}
	requireRequestState(t, database, "request-1", account.LoginSucceeded)
	if _, exists, err := database.GetLease("account-1"); err != nil || exists {
		t.Fatalf("completed lease = exists:%t err:%v", exists, err)
	}
}

func TestRunOnceSelectsFIFOAndHonorsGlobalConcurrency(t *testing.T) {
	database := openQueueTestStore(t)
	createAccount(t, database, "account-a")
	createAccount(t, database, "account-b")
	clock := &testClock{now: schedulerTestTime}
	requests := newTestRequestService(t, database, clock)
	for _, input := range []requestservice.SubmitInput{
		{RequestID: "request-b", AccountID: "account-b", IdempotencyKey: "idem-b"},
		{RequestID: "request-a", AccountID: "account-a", IdempotencyKey: "idem-a"},
	} {
		if _, _, err := requests.Submit(input); err != nil {
			t.Fatal(err)
		}
	}

	runner := &fakeRunner{results: []Result{{Succeeded: true}, {Succeeded: true}}}
	scheduler := newTestScheduler(t, database, runner, clock, account.RetryPolicy{MaxAttempts: 1})
	first, err := scheduler.RunOnce(context.Background())
	if err != nil || first.Idle || runner.works[0].Request.RequestID != "request-a" {
		t.Fatalf("first FIFO run = %#v, works=%#v, err=%v", first, runner.works, err)
	}
	second, err := scheduler.RunOnce(context.Background())
	if err != nil || second.Idle || len(runner.works) != 2 || runner.works[1].Request.RequestID != "request-b" {
		t.Fatalf("second FIFO run = %#v, works=%#v, err=%v", second, runner.works, err)
	}
}

func TestRunOnceRetriesTransientFailureAtNotBefore(t *testing.T) {
	database := openQueueTestStore(t)
	createAccount(t, database, "account-1")
	clock := &testClock{now: schedulerTestTime}
	requests := newTestRequestService(t, database, clock)
	if _, _, err := requests.Submit(requestservice.SubmitInput{
		RequestID:      "request-1",
		AccountID:      "account-1",
		IdempotencyKey: "idem-1",
	}); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{results: []Result{
		{Failure: account.TransientFailure},
		{Succeeded: true},
	}}
	scheduler := newTestScheduler(t, database, runner, clock, account.RetryPolicy{
		MaxAttempts: 2,
		BaseDelay:   time.Minute,
		MaxDelay:    2 * time.Minute,
	})
	first, err := scheduler.RunOnce(context.Background())
	if err != nil || first.Idle || !first.Retried {
		t.Fatalf("first failure run = %#v, %v", first, err)
	}
	retry := requireRequestState(t, database, "request-1", account.Queued)
	if retry.Attempt != 1 || !retry.NotBefore.Equal(schedulerTestTime.Add(time.Minute)) {
		t.Fatalf("retry projection = %#v", retry)
	}
	clock.now = schedulerTestTime.Add(30 * time.Second)
	idle, err := scheduler.RunOnce(context.Background())
	if err != nil || !idle.Idle || len(runner.works) != 1 {
		t.Fatalf("before retry = %#v, works=%d, err=%v", idle, len(runner.works), err)
	}
	clock.now = schedulerTestTime.Add(time.Minute)
	second, err := scheduler.RunOnce(context.Background())
	if err != nil || second.Idle || !second.Succeeded || len(runner.works) != 2 {
		t.Fatalf("retry run = %#v, works=%d, err=%v", second, len(runner.works), err)
	}
	final := requireRequestState(t, database, "request-1", account.LoginSucceeded)
	if final.Attempt != 2 {
		t.Fatalf("final attempt = %d, want 2", final.Attempt)
	}
}

func TestRunOnceDoesNotRetryCredentialFailure(t *testing.T) {
	database := openQueueTestStore(t)
	createAccount(t, database, "account-1")
	clock := &testClock{now: schedulerTestTime}
	requests := newTestRequestService(t, database, clock)
	if _, _, err := requests.Submit(requestservice.SubmitInput{
		RequestID:      "request-1",
		AccountID:      "account-1",
		IdempotencyKey: "idem-1",
	}); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{results: []Result{{Failure: account.CredentialFailure}}}
	scheduler := newTestScheduler(t, database, runner, clock, account.RetryPolicy{MaxAttempts: 3, BaseDelay: time.Minute, MaxDelay: time.Minute})
	outcome, err := scheduler.RunOnce(context.Background())
	if err != nil || outcome.Retried || outcome.Succeeded {
		t.Fatalf("credential failure = %#v, %v", outcome, err)
	}
	requireRequestState(t, database, "request-1", account.LoginFailed)
	if len(runner.works) != 1 {
		t.Fatalf("credential runner calls = %d", len(runner.works))
	}
	clock.now = clock.now.Add(time.Minute)
	idle, err := scheduler.RunOnce(context.Background())
	if err != nil || !idle.Idle || len(runner.works) != 1 {
		t.Fatalf("credential retry = %#v, works=%d, err=%v", idle, len(runner.works), err)
	}
}

func TestRunOnceCancelsExpiredQueuedRequestBeforeClaim(t *testing.T) {
	database := openQueueTestStore(t)
	createAccount(t, database, "account-1")
	clock := &testClock{now: schedulerTestTime}
	requests := newTestRequestService(t, database, clock)
	if _, _, err := requests.Submit(requestservice.SubmitInput{
		RequestID:      "request-1",
		AccountID:      "account-1",
		IdempotencyKey: "idem-1",
		Deadline:       schedulerTestTime.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{results: []Result{{Succeeded: true}}}
	scheduler := newTestScheduler(t, database, runner, clock, account.RetryPolicy{MaxAttempts: 1})
	clock.now = schedulerTestTime.Add(time.Minute)
	outcome, err := scheduler.RunOnce(context.Background())
	if err != nil || !outcome.Idle || len(runner.works) != 0 {
		t.Fatalf("expired request = %#v, works=%d, err=%v", outcome, len(runner.works), err)
	}
	requireRequestState(t, database, "request-1", account.Cancelled)
}

func TestRunOnceRejectsCompletionAfterLeaseExpiry(t *testing.T) {
	database := openQueueTestStore(t)
	createAccount(t, database, "account-1")
	clock := &testClock{now: schedulerTestTime}
	requests := newTestRequestService(t, database, clock)
	if _, _, err := requests.Submit(requestservice.SubmitInput{
		RequestID:      "request-1",
		AccountID:      "account-1",
		IdempotencyKey: "idem-1",
	}); err != nil {
		t.Fatal(err)
	}
	runner := &advancingRunner{clock: clock, advance: 11 * time.Minute, result: Result{Succeeded: true}}
	scheduler := newTestScheduler(t, database, runner, clock, account.RetryPolicy{MaxAttempts: 1})
	if _, err := scheduler.RunOnce(context.Background()); !errors.Is(err, account.ErrLeaseExpired) {
		t.Fatalf("completion after lease expiry error = %v, want ErrLeaseExpired", err)
	}
	requireRequestState(t, database, "request-1", account.LoggingIn)
	if _, exists, err := database.GetLease("account-1"); err != nil || !exists {
		t.Fatalf("lease after rejected completion = exists:%t err:%v", exists, err)
	}
}
