package browser

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/config"
)

func newProcessTestFactory(t *testing.T, mode string) (*ProcessFactory, *Profiles) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is required for the worker process integration test")
	}
	cfg, err := config.New(filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := NewProfiles(cfg)
	if err != nil {
		t.Fatal(err)
	}
	factory, err := NewProcessFactory(ProcessConfig{
		Command:    "node",
		Script:     filepath.Join("..", "..", "browser-worker", "src", "worker.mjs"),
		WorkerMode: mode,
	})
	if err != nil {
		t.Fatal(err)
	}
	return factory, profiles
}

func TestNewProcessFactorySupportsScriptAndArgumentListModes(t *testing.T) {
	scriptFactory, err := NewProcessFactory(ProcessConfig{Command: "node", Script: "worker.mjs"})
	if err != nil {
		t.Fatal(err)
	}
	if len(scriptFactory.args) != 2 || scriptFactory.args[0] != "worker.mjs" || scriptFactory.args[1] != "--stdio" {
		t.Fatalf("script process args = %#v", scriptFactory.args)
	}

	runtimeFactory, err := NewProcessFactory(ProcessConfig{Command: "chuzi-browser-runtime", Args: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if runtimeFactory.args == nil || len(runtimeFactory.args) != 0 {
		t.Fatalf("runtime process args = %#v, want explicit empty argument list", runtimeFactory.args)
	}

	if _, err := NewProcessFactory(ProcessConfig{Command: "node", Script: "worker.mjs", Args: []string{}}); !errors.Is(err, ErrInvalidProcessConfig) {
		t.Fatalf("mixed process arguments error = %v, want ErrInvalidProcessConfig", err)
	}
}

func processTestSpec(t *testing.T, profiles *Profiles) WorkerSpec {
	t.Helper()
	profile, err := profiles.Prepare("account-1")
	if err != nil {
		t.Fatal(err)
	}
	return WorkerSpec{
		SessionID:  "session-1",
		AccountID:  "account-1",
		RequestID:  "request-1",
		ProfileDir: profile,
		LeaseID:    "lease-1",
		Owner:      "test",
	}
}

func closeProcessTestWorker(t *testing.T, worker Worker) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := worker.Close(ctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("worker.Close() error = %v", err)
	}
}

func TestProcessWorkerRunsSessionLifecycle(t *testing.T) {
	factory, profiles := newProcessTestFactory(t, "success")
	worker, err := factory.Start(context.Background(), processTestSpec(t, profiles))
	if err != nil {
		t.Fatal(err)
	}
	result, err := worker.Run(context.Background())
	if err != nil || !result.Succeeded {
		t.Fatalf("worker.Run() = %#v, %v", result, err)
	}
	closeProcessTestWorker(t, worker)
}

func TestProcessWorkerReportsCrashAndAcceptsCancellation(t *testing.T) {
	t.Run("crash", func(t *testing.T) {
		factory, profiles := newProcessTestFactory(t, "crash")
		worker, err := factory.Start(context.Background(), processTestSpec(t, profiles))
		if err != nil {
			t.Fatal(err)
		}
		_, err = worker.Run(context.Background())
		if !errors.Is(err, ErrWorkerCrashed) {
			t.Fatalf("crashed worker error = %v, want ErrWorkerCrashed", err)
		}
		closeProcessTestWorker(t, worker)
	})

	t.Run("cancel", func(t *testing.T) {
		factory, profiles := newProcessTestFactory(t, "hold")
		worker, err := factory.Start(context.Background(), processTestSpec(t, profiles))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			_, runErr := worker.Run(ctx)
			result <- runErr
		}()
		time.Sleep(20 * time.Millisecond)
		cancel()
		if err := worker.Cancel(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled worker error = %v, want context.Canceled", err)
		}
		closeProcessTestWorker(t, worker)
	})
}
