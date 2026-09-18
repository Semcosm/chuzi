package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/browser"
	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/store"
)

type testFactory struct{}

func TestWorkerStderrRequiresExplicitDebugOptIn(t *testing.T) {
	t.Setenv("CHUZI_WORKER_DEBUG", "")
	if got := workerStderr(); got != io.Discard {
		t.Fatalf("workerStderr() without opt-in = %T, want io.Discard", got)
	}
	t.Setenv("CHUZI_WORKER_DEBUG", "1")
	if got := workerStderr(); got != os.Stderr {
		t.Fatalf("workerStderr() with opt-in = %T, want os.Stderr", got)
	}
}

func TestCLIErrorMessageDoesNotExposeWrappedDetails(t *testing.T) {
	secret := "account-private /credential-secret"
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{name: "generic", err: errors.New(secret), want: "operation failed"},
		{name: "restore", err: fmt.Errorf("%w: %s", store.ErrInvalidRestore, secret), want: "invalid restore source"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := cliErrorMessage(test.err); got != test.want || strings.Contains(got, secret) {
				t.Fatalf("cliErrorMessage() = %q, want %q", got, test.want)
			}
		})
	}
}

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
	if runtime.store == nil || runtime.requests == nil || runtime.runner == nil || runtime.scheduler == nil || runtime.coreAPI == nil {
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
	for _, backend := range []string{backendNode, backendHeadless} {
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

func TestHeadlessWorkerFactoryUsesExplicitBrowserCommand(t *testing.T) {
	options := testServiceOptions()
	options.backend = backendHeadless
	options.headlessBrowserCommand = "/usr/bin/chromium"
	factory, err := newWorkerFactory(options)
	if err != nil {
		t.Fatal(err)
	}
	if factory == nil {
		t.Fatal("headless factory is nil")
	}
}

func TestHeadlessBackendRejectsEmptyBrowserCommand(t *testing.T) {
	options := testServiceOptions()
	options.backend = backendHeadless
	options.headlessBrowserCommand = "  "
	if _, err := newWorkerFactory(options); !errors.Is(err, errInvalidOptions) {
		t.Fatalf("newWorkerFactory() error = %v, want errInvalidOptions", err)
	}
}

func TestGenshinAutomationAdapterRequiresExplicitHeadlessBackend(t *testing.T) {
	options := testServiceOptions()
	options.automationAdapter = "genshin-cloudgame"
	if err := options.validate(); !errors.Is(err, errInvalidOptions) {
		t.Fatalf("node backend adapter validation = %v, want errInvalidOptions", err)
	}
	options.backend = backendHeadless
	if err := options.validate(); err != nil {
		t.Fatalf("headless adapter validation = %v", err)
	}
	options.automationAdapter = "unsupported"
	if err := options.validate(); !errors.Is(err, errInvalidOptions) {
		t.Fatalf("unsupported adapter validation = %v, want errInvalidOptions", err)
	}
}

func TestDefaultServiceOptionsAreValid(t *testing.T) {
	options := defaultServiceOptions()
	if err := options.validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLogPathMayUseConfiguredDefaultRotationLimits(t *testing.T) {
	options := testServiceOptions()
	options.logPath = filepath.Join(t.TempDir(), "service.log")
	if err := options.validate(); err != nil {
		t.Fatalf("log path with default limits rejected: %v", err)
	}
	options.logMaxBytes = -1
	if err := options.validate(); !errors.Is(err, errInvalidOptions) {
		t.Fatalf("negative log size error = %v, want errInvalidOptions", err)
	}
}

func TestAssembleRuntimeWiresConfiguredMatrixAndCredentialBoundaries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"user_id":"@bot:example.org"}`))
	}))
	defer server.Close()
	t.Setenv("CHUZI_MATRIX_TOKEN", "test-token")
	cfg, err := config.New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Matrix = config.MatrixConfig{HomeserverURL: server.URL, AccessTokenEnv: "CHUZI_MATRIX_TOKEN"}
	runtime, err := assembleRuntimeWithFactory(cfg, testServiceOptions(), func() time.Time { return time.Date(2026, time.September, 11, 12, 0, 0, 0, time.UTC) }, testFactory{})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.credentials == nil || runtime.notifier == nil || runtime.matrixClient == nil || runtime.health == nil {
		t.Fatalf("runtime production boundaries missing: %#v", runtime)
	}
	if err := runtime.store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialMaintenanceLifecycleUsesRedactedProductionBoundary(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	cfg, err := config.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateAccount("maintenance-account"); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.json")
	raw, err := json.Marshal(struct {
		DataDir     string                  `json:"data_dir"`
		Credentials config.CredentialConfig `json:"credentials"`
	}{DataDir: dataDir, Credentials: config.CredentialConfig{KeyEnv: "CHUZI_MAINT_KEY", KeyIDEnv: "CHUZI_MAINT_KEY_ID", HistoryEnv: "CHUZI_MAINT_KEYS"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	options := defaultServiceOptions()
	options.configPath = configPath
	t.Setenv("CHUZI_MAINT_KEY_ID", "old-key")
	t.Setenv("CHUZI_MAINT_KEY", strings.Repeat("11", 32))
	t.Setenv("CHUZI_MAINT_SECRET", "maintenance-secret")
	if err := runMaintenance(context.Background(), options, false, "", "maintenance-account", "", "", "CHUZI_MAINT_SECRET", "operator", false, false, "", "", 100); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHUZI_MAINT_KEY_ID", "new-key")
	t.Setenv("CHUZI_MAINT_KEY", strings.Repeat("22", 32))
	t.Setenv("CHUZI_MAINT_KEYS", `{"old-key":"1111111111111111111111111111111111111111111111111111111111111111"}`)
	if err := runMaintenance(context.Background(), options, false, "", "", "maintenance-account", "", "", "operator", false, false, "", "", 100); err != nil {
		t.Fatal(err)
	}
	if err := runMaintenance(context.Background(), options, false, "", "", "", "maintenance-account", "", "operator", false, false, "", "", 100); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	record, found, err := reopened.GetCredential("maintenance-account")
	if err != nil || !found || record.RevokedAt == nil || len(record.Ciphertext) != 0 {
		t.Fatalf("maintenance credential = %#v/%t: %v", record, found, err)
	}
}
