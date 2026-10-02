package main

import (
	"context"
	"crypto/sha256"
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

	adapterpkg "github.com/Semcosm/chuzi/internal/adapter"
	"github.com/Semcosm/chuzi/internal/browser"
	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/observability"
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

func TestSlotLifecycleFailureIsClassifiedAndAffectsReadiness(t *testing.T) {
	metrics := observability.NewMetrics()
	if err := metrics.Register(observability.MetricDefinition{Name: "chuzi_slot_reconcile_errors_total", Kind: observability.Counter}); err != nil {
		t.Fatal(err)
	}
	healthState := newSlotLifecycleHealth()
	events := observability.NewEventBuffer(8)
	runtime := &serviceRuntime{metrics: metrics, eventBuffer: events, slotHealth: healthState}
	runtime.recordSlotReconcile(time.Date(2026, time.September, 12, 1, 2, 3, 0, time.UTC), errors.New("SID and password must never leave the OS boundary"))
	if err := healthState.probe(context.Background()); err == nil {
		t.Fatal("failed reconcile did not mark slot lifecycle unhealthy")
	}
	if got := metrics.Prometheus(); !strings.Contains(got, "chuzi_slot_reconcile_errors_total 1") {
		t.Fatalf("reconcile metric = %q", got)
	}
	window := events.Snapshot(8)
	if len(window) != 1 || window[0].ErrorClass != "reconcile_failed" || strings.Contains(window[0].ErrorClass, "password") {
		t.Fatalf("classified reconcile event = %#v", window)
	}
	runtime.recordSlotReconcile(time.Date(2026, time.September, 12, 1, 2, 4, 0, time.UTC), nil)
	if err := healthState.probe(context.Background()); err != nil {
		t.Fatalf("successful reconcile left readiness unhealthy: %v", err)
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
	for _, backend := range []string{backendNode, backendHeadless, backendHeaded} {
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

func TestHeadedBackendCarriesDesktopConfiguration(t *testing.T) {
	options := testServiceOptions()
	options.backend = backendHeaded
	options.windowsDesktop = "ChuziDesktop"
	options.windowsLauncherCommand = "chuzi-browser-launcher.exe"
	if err := options.validate(); err != nil {
		t.Fatalf("headed desktop options rejected: %v", err)
	}
	factory, err := newWorkerFactory(options)
	if err != nil || factory == nil {
		t.Fatalf("headed worker factory = %#v, %v", factory, err)
	}
}

func TestGenshinAutomationAdapterRequiresExplicitCDPBackend(t *testing.T) {
	options := testServiceOptions()
	options.automationAdapter = "genshin-cloudgame"
	if err := options.validate(); !errors.Is(err, errInvalidOptions) {
		t.Fatalf("node backend adapter validation = %v, want errInvalidOptions", err)
	}
	options.backend = backendHeadless
	if err := options.validate(); err != nil {
		t.Fatalf("headless adapter validation = %v", err)
	}
	options.backend = backendHeaded
	if err := options.validate(); err != nil {
		t.Fatalf("headed adapter validation = %v", err)
	}
	options.automationAdapter = "unsupported"
	if err := options.validate(); !errors.Is(err, errInvalidOptions) {
		t.Fatalf("unsupported adapter validation = %v, want errInvalidOptions", err)
	}
}

func TestResolveAutomationPackageUsesManagedRegistryAndLifecycleGates(t *testing.T) {
	cfg, err := config.New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	target := serviceTarget()
	if target == "" {
		t.Skip("the test host is outside the release target matrix")
	}
	root := filepath.Join(cfg.DataDir, "plugins", "genshin-cloudgame")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("export default {};\n")
	digest := sha256.Sum256(content)
	manifest := adapterpkg.Manifest{
		Format: adapterpkg.ManifestFormat, ID: "genshin-cloudgame", API: adapterpkg.AdapterAPI, Version: "1.0.0",
		Entry: "adapter.mjs", Capabilities: []string{"genshin-cloudgame@1"},
		Permissions: []string{"browser.cdp.loopback"}, Targets: []string{target}, SignedBy: "test-signer",
		Resources: []adapterpkg.Resource{{Path: "adapter.mjs", SHA256: fmt.Sprintf("%x", digest[:]), Size: int64(len(content))}},
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "adapter.mjs"), content, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, adapterpkg.ManifestName), manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(cfg.DataDir, ".chuzi", "launcher-state.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	writeState := func(state adapterpkg.State) {
		t.Helper()
		data, marshalErr := json.Marshal(struct {
			Plugins map[string]adapterpkg.State `json:"plugins"`
		}{Plugins: map[string]adapterpkg.State{manifest.ID: state}})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if writeErr := os.WriteFile(statePath, data, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	base := adapterpkg.State{Installed: true, Verified: true, Trusted: true, Enabled: true, Version: manifest.Version}
	for _, test := range []struct {
		name  string
		state adapterpkg.State
		want  error
	}{
		{name: "not installed", state: adapterpkg.State{}, want: adapterpkg.ErrNotInstalled},
		{name: "not verified", state: adapterpkg.State{Installed: true, Version: manifest.Version}, want: adapterpkg.ErrNotVerified},
		{name: "not trusted", state: adapterpkg.State{Installed: true, Verified: true, Version: manifest.Version}, want: adapterpkg.ErrNotTrusted},
		{name: "disabled", state: adapterpkg.State{Installed: true, Verified: true, Trusted: true, Version: manifest.Version}, want: adapterpkg.ErrDisabled},
	} {
		t.Run(test.name, func(t *testing.T) {
			writeState(test.state)
			_, resolveErr := resolveAutomationPackage(cfg, manifest.ID)
			if !errors.Is(resolveErr, test.want) {
				t.Fatalf("resolveAutomationPackage() = %v, want %v", resolveErr, test.want)
			}
			if strings.Contains(resolveErr.Error(), cfg.DataDir) || strings.Contains(resolveErr.Error(), "test-signer") {
				t.Fatalf("resolution error leaked managed path or signer: %v", resolveErr)
			}
		})
	}
	writeState(base)
	resolved, err := resolveAutomationPackage(cfg, manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Manifest.ID != manifest.ID || resolved.Entry != filepath.Join(root, manifest.Entry) {
		t.Fatalf("resolved package = %#v, want managed entry", resolved)
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
