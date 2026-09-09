package browser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/queue"
	requestservice "github.com/Semcosm/chuzi/internal/request"
	"github.com/Semcosm/chuzi/internal/store"
)

var browserTestTime = time.Date(2026, time.September, 10, 12, 0, 0, 0, time.UTC)

type fakeFactory struct {
	worker *fakeWorker
	specs  []WorkerSpec
	started chan WorkerSpec
}

func (f *fakeFactory) Start(_ context.Context, spec WorkerSpec) (Worker, error) {
	f.specs = append(f.specs, spec)
	if f.started != nil {
		f.started <- spec
	}
	return f.worker, nil
}

type fakeWorker struct {
	result     WorkerResult
	runErr     error
	block      bool
	cancelOnce sync.Once
	closeOnce  sync.Once
	cancelled  chan struct{}
	closed     chan struct{}
}

func (w *fakeWorker) Run(ctx context.Context) (WorkerResult, error) {
	if !w.block {
		return w.result, w.runErr
	}
	select {
	case <-ctx.Done():
		return WorkerResult{}, ctx.Err()
	case <-w.cancelled:
		return WorkerResult{Failure: account.TransientFailure}, context.Canceled
	}
}

func (w *fakeWorker) Cancel(ctx context.Context) error {
	if ctx == nil {
		return errors.New("nil cancel context")
	}
	w.cancelOnce.Do(func() { close(w.cancelled) })
	return nil
}

func (w *fakeWorker) Close(_ context.Context) error {
	w.closeOnce.Do(func() { close(w.closed) })
	return nil
}

type fakeLeases struct {
	mu     sync.Mutex
	calls  int
	latest account.Lease
	err    error
}

func (l *fakeLeases) HeartbeatLease(_ string, now time.Time, leaseID, owner string, ttl time.Duration) (account.Lease, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	if l.err != nil {
		return account.Lease{}, l.err
	}
	updated, err := account.HeartbeatLease(l.latest, now, leaseID, owner, ttl)
	if err == nil {
		l.latest = updated
	}
	return updated, err
}

func newBrowserProfiles(t *testing.T) *Profiles {
	t.Helper()
	cfg, err := config.New(filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := NewProfiles(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return profiles
}

func newBrowserWork(t *testing.T, at time.Time, ttl time.Duration) queue.Work {
	t.Helper()
	request, err := store.NewRequest("request-1", "account-1", "idem-1", at)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := account.AcquireLease(nil, at, "lease-1", "runner-1", ttl)
	if err != nil {
		t.Fatal(err)
	}
	return queue.Work{Request: request, Lease: lease}
}

func newBrowserRunner(t *testing.T, factory WorkerFactory, leases LeaseKeeper, profiles *Profiles, clock Clock) *Runner {
	t.Helper()
	runner, err := New(factory, leases, profiles, Config{
		LeaseTTL:        time.Minute,
		CancelTimeout:   100 * time.Millisecond,
		ShutdownTimeout: 100 * time.Millisecond,
		Clock:           clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func TestRunnerUsesIsolatedGeneratedProfileAndReportsSuccess(t *testing.T) {
	worker := &fakeWorker{result: WorkerResult{Succeeded: true}, cancelled: make(chan struct{}), closed: make(chan struct{})}
	factory := &fakeFactory{worker: worker}
	profiles := newBrowserProfiles(t)
	runner := newBrowserRunner(t, factory, nil, profiles, func() time.Time { return browserTestTime })

	result, err := runner.Run(context.Background(), newBrowserWork(t, browserTestTime, time.Minute))
	if err != nil || !result.Succeeded || result.Failure != "" {
		t.Fatalf("Run() = %#v, %v", result, err)
	}
	if len(factory.specs) != 1 {
		t.Fatalf("factory specs = %#v", factory.specs)
	}
	spec := factory.specs[0]
	if spec.AccountID != "account-1" || spec.RequestID != "request-1" || spec.LeaseID != "lease-1" {
		t.Fatalf("unexpected worker spec: %#v", spec)
	}
	if spec.ProfileDir == "" || filepath.Base(spec.ProfileDir) == "account-1" || filepath.Dir(spec.ProfileDir) != profiles.Root() {
		t.Fatalf("profile path is not service-derived: %q root=%q", spec.ProfileDir, profiles.Root())
	}
	info, err := os.Stat(spec.ProfileDir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("profile path is not a directory: %s", spec.ProfileDir)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Fatalf("profile permissions = %o, want 700", info.Mode().Perm())
	}
	select {
	case <-worker.closed:
	default:
		t.Fatal("runner did not close worker")
	}
}

func TestRunnerMapsWorkerFailureAndCrashToRedactedFacts(t *testing.T) {
	profiles := newBrowserProfiles(t)
	cases := []struct {
		name       string
		result     WorkerResult
		runErr     error
		wantResult queue.Result
		wantErr    error
	}{
		{
			name:       "credential-failure",
			result:     WorkerResult{Failure: account.CredentialFailure},
			wantResult: queue.Result{Failure: account.CredentialFailure},
		},
		{
			name:       "worker-crash",
			runErr:     ErrWorkerCrashed,
			wantResult: queue.Result{Failure: account.TransientFailure},
			wantErr:    ErrWorkerCrashed,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			worker := &fakeWorker{result: test.result, runErr: test.runErr, cancelled: make(chan struct{}), closed: make(chan struct{})}
			runner := newBrowserRunner(t, &fakeFactory{worker: worker}, nil, profiles, func() time.Time { return browserTestTime })
			got, err := runner.Run(context.Background(), newBrowserWork(t, browserTestTime, time.Minute))
			if !errors.Is(err, test.wantErr) || got != test.wantResult {
				t.Fatalf("Run() = %#v, %v, want %#v, %v", got, err, test.wantResult, test.wantErr)
			}
		})
	}
}

func TestRunnerCancelsBlockedWorkerOnContextCancellation(t *testing.T) {
	worker := &fakeWorker{block: true, cancelled: make(chan struct{}), closed: make(chan struct{})}
	runner := newBrowserRunner(t, &fakeFactory{worker: worker}, nil, newBrowserProfiles(t), func() time.Time { return browserTestTime })
	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan struct {
		result queue.Result
		err    error
	}, 1)
	go func() {
		result, err := runner.Run(ctx, newBrowserWork(t, browserTestTime, time.Minute))
		resultCh <- struct {
			result queue.Result
			err    error
		}{result: result, err: err}
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case outcome := <-resultCh:
		if !errors.Is(outcome.err, context.Canceled) || outcome.result.Failure != account.TransientFailure {
			t.Fatalf("cancelled Run() = %#v, %v", outcome.result, outcome.err)
		}
	case <-time.After(time.Second):
		t.Fatal("runner did not finish after cancellation")
	}
	select {
	case <-worker.cancelled:
	case <-time.After(time.Second):
		t.Fatal("runner did not send worker cancellation")
	}
	select {
	case <-worker.closed:
	case <-time.After(time.Second):
		t.Fatal("runner did not close cancelled worker")
	}
}

func TestRunnerRejectsExpiredLeaseBeforeStartingWorker(t *testing.T) {
	started := false
	factory := &fakeFactory{worker: &fakeWorker{cancelled: make(chan struct{}), closed: make(chan struct{})}}
	wrapped := WorkerFactoryFunc(func(ctx context.Context, spec WorkerSpec) (Worker, error) {
		started = true
		return factory.Start(ctx, spec)
	})
	runner := newBrowserRunner(t, wrapped, nil, newBrowserProfiles(t), func() time.Time {
		return browserTestTime.Add(time.Minute)
	})
	result, err := runner.Run(context.Background(), newBrowserWork(t, browserTestTime, time.Minute))
	if !errors.Is(err, account.ErrLeaseExpired) || result.Failure != account.TransientFailure || started {
		t.Fatalf("expired lease Run() = %#v, %v, started=%t", result, err, started)
	}
}

func TestRunnerCancelsWorkerWhenDurableRequestIsCancelled(t *testing.T) {
	cfg, err := config.New(filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.CreateAccount("account-1"); err != nil {
		t.Fatal(err)
	}
	clock := browserTestTime
	idCounts := make(map[string]int)
	var idMu sync.Mutex
	newID := func(kind string) string {
		idMu.Lock()
		defer idMu.Unlock()
		idCounts[kind]++
		return fmt.Sprintf("%s-%d", kind, idCounts[kind])
	}
	requests, err := requestservice.New(database, func() time.Time { return clock }, requestservice.IDGenerator(newID), "browser-test")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := requests.Submit(requestservice.SubmitInput{
		RequestID:      "request-1",
		AccountID:      "account-1",
		IdempotencyKey: "idem-1",
	}); err != nil {
		t.Fatal(err)
	}
	worker := &fakeWorker{block: true, cancelled: make(chan struct{}), closed: make(chan struct{})}
	factory := &fakeFactory{worker: worker, started: make(chan WorkerSpec, 1)}
	profiles, err := NewProfiles(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sessionRunner, err := New(factory, database, profiles, Config{
		LeaseTTL:          time.Minute,
		HeartbeatInterval: time.Millisecond,
		CancelTimeout:     100 * time.Millisecond,
		ShutdownTimeout:   100 * time.Millisecond,
		Clock:             func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	scheduler, err := queue.New(database, sessionRunner, queue.Config{
		Owner:                "browser-test",
		LeaseTTL:             time.Minute,
		RunTimeout:           time.Second,
		MaxGlobalConcurrency: 1,
		RetryPolicy:          account.RetryPolicy{MaxAttempts: 1},
		Clock:                func() time.Time { return clock },
		NewID:                newID,
	})
	if err != nil {
		t.Fatal(err)
	}
	runResult := make(chan struct {
		outcome queue.Outcome
		err     error
	}, 1)
	go func() {
		outcome, runErr := scheduler.RunOnce(context.Background())
		runResult <- struct {
			outcome queue.Outcome
			err     error
		}{outcome: outcome, err: runErr}
	}()
	select {
	case <-factory.started:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not start worker")
	}
	if _, err := requests.Cancel("request-1", "test", "cancel while running"); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-runResult:
		if result.err != nil || result.outcome.Request.State != account.Cancelled {
			t.Fatalf("scheduler cancellation = %#v, %v", result.outcome, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler did not finish cancelled worker")
	}
	select {
	case <-worker.cancelled:
	case <-time.After(time.Second):
		t.Fatal("worker was not cancelled")
	}
	if _, exists, err := database.GetLease("account-1"); err != nil || exists {
		t.Fatalf("cancelled lease = exists:%t err:%v", exists, err)
	}
}

// WorkerFactoryFunc makes small lifecycle fakes readable without introducing
// another production abstraction.
type WorkerFactoryFunc func(context.Context, WorkerSpec) (Worker, error)

func (f WorkerFactoryFunc) Start(ctx context.Context, spec WorkerSpec) (Worker, error) {
	return f(ctx, spec)
}
