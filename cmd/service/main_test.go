package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/browser"
	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/store"
)

type testFactory struct{}

func (testFactory) Start(context.Context, browser.WorkerSpec) (browser.Worker, error) {
	return nil, errors.New("test factory must not start a worker during assembly")
}

func testServiceOptions() serviceOptions {
	options := defaultServiceOptions()
	options.backend = backendNode
	options.workerCommand = "node"
	options.workerScript = "worker.mjs"
	return options
}

func TestAssembleRuntimeOpensPersistentStoreAndBuildsBoundaries(t *testing.T) {
	cfg, err := config.New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return time.Date(2026, time.September, 11, 12, 0, 0, 0, time.UTC) }
	runtime, err := assembleRuntimeWithFactory(cfg, testServiceOptions(), now, testFactory{})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.store == nil || runtime.requests == nil || runtime.runner == nil || runtime.scheduler == nil {
		t.Fatalf("assembled runtime has missing boundary: %#v", runtime)
	}
	if _, err := runtime.store.SchemaVersion(); err != nil {
		t.Fatalf("store schema version = %v", err)
	}
	storeConfig := runtime.store.Config()
	if err := runtime.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openStoreForTest(storeConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.SchemaVersion(); err != nil {
		t.Fatalf("reopened store schema version = %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func openStoreForTest(cfg config.Config) (*store.Store, error) {
	return store.Open(cfg)
}

func TestNewWorkerFactoryRejectsUnknownBackend(t *testing.T) {
	options := testServiceOptions()
	options.backend = "unknown"
	if _, err := newWorkerFactory(options); !errors.Is(err, errInvalidBackend) {
		t.Fatalf("newWorkerFactory() error = %v, want errInvalidBackend", err)
	}
}

func TestNewWorkerFactorySupportsConfiguredBackends(t *testing.T) {
	for _, backend := range []string{backendNode, backendRust} {
		t.Run(backend, func(t *testing.T) {
			options := testServiceOptions()
			options.backend = backend
			factory, err := newWorkerFactory(options)
			if err != nil {
				t.Fatalf("newWorkerFactory(%q) error = %v", backend, err)
			}
			if factory == nil {
				t.Fatalf("newWorkerFactory(%q) returned nil factory", backend)
			}
		})
	}
}

func TestDefaultServiceOptionsAreValid(t *testing.T) {
	options := defaultServiceOptions()
	if err := options.validate(); err != nil {
		t.Fatal(err)
	}
}
