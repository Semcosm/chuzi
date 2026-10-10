package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/coreapi"
	"github.com/Semcosm/chuzi/internal/environment"
)

func TestTestEnvironmentLifecyclePoolBindingAndRestart(t *testing.T) {
	cfg, err := config.New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	runtimeRoot := t.TempDir()
	workerRoot := filepath.Join(runtimeRoot, "browser-worker", "src")
	if err := os.MkdirAll(workerRoot, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"worker.mjs", "headless.mjs", "headless-adapter.mjs", "cdp-runtime.mjs", "local-test-page.html"} {
		if err := os.WriteFile(filepath.Join(workerRoot, name), []byte("process.stdin.resume();\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	runtime, err := assembleRuntimeWithFactory(cfg, testServiceOptions(), time.Now, testFactory{})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.store.Close()
	if err := prepareTestEnvironmentPackage(cfg, runtimeRoot, serviceTarget(), runtime.environment); err != nil {
		t.Fatal(err)
	}
	if err := prepareTestEnvironmentPackage(cfg, runtimeRoot, serviceTarget(), runtime.environment); err != nil {
		t.Fatalf("repeated bootstrap: %v", err)
	}
	trust, err := environment.LoadTrustStore(environmentTrustStorePath(cfg))
	if err != nil || len(trust) != 1 {
		t.Fatalf("persisted public signer: %v", err)
	}
	ctx := context.Background()
	environments := runtime.coreAPI.(coreapi.EnvironmentAPI)
	pools := runtime.coreAPI.(coreapi.JobPoolAPI)
	missing, err := environments.EnvironmentOperation(ctx, coreapi.EnvironmentOperationRequest{EnvironmentID: testEnvironmentID, Version: testEnvironmentVersion, Operation: "install", PackageRef: "missing-catalog-key", IdempotencyKey: "missing-package", Actor: "operator"})
	if err != nil || missing.State != "failed" || missing.FailureCode != "package_unavailable" {
		t.Fatalf("missing package: %#v %v", missing, err)
	}
	installed, err := runtime.environment.InstallReferenceFor(ctx, testEnvironmentPackageRef, testEnvironmentID, testEnvironmentVersion)
	if err != nil {
		t.Fatalf("install catalog package: %v", err)
	}
	if !installed.Installed || !installed.Verified || installed.Trusted || installed.Enabled || installed.Healthy || installed.Ready {
		t.Fatalf("install bypassed lifecycle gates: %#v", installed)
	}
	if err := runtime.store.PutEnvironmentRecord(installed); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"verify", "trust", "enable", "health"} {
		input := coreapi.EnvironmentOperationRequest{EnvironmentID: testEnvironmentID, Version: testEnvironmentVersion, Operation: operation, IdempotencyKey: "test-environment-" + operation, Actor: "operator"}
		result, err := environments.EnvironmentOperation(ctx, input)
		if err != nil || result.State != "applied" {
			t.Fatalf("%s: %#v %v", operation, result, err)
		}
		if operation == "verify" {
			record, err := runtime.store.GetEnvironmentRecord(testEnvironmentID, testEnvironmentVersion)
			if err != nil || !record.Verified || record.Trusted || record.Enabled || record.Healthy || record.Ready {
				t.Fatalf("verify bypassed later lifecycle gates: %#v %v", record, err)
			}
		}
	}
	items, err := environments.ListEnvironments(ctx)
	if err != nil || len(items) != 1 || !items[0].Ready || items[0].ManifestDigest == "" || !strings.HasPrefix(items[0].Signer, testEnvironmentSignerPrefix) || len(trust[items[0].Signer]) != 32 {
		t.Fatalf("ready environment: %#v %v", items, err)
	}
	poolOperation, err := pools.ApplyJobPool(ctx, coreapi.JobPoolApplyRequest{Config: coreapi.JobPoolConfig{PoolID: "test", EnvironmentID: testEnvironmentID, EnvironmentVersion: testEnvironmentVersion, DesiredSlots: 0, MaxConcurrency: 1, DesiredState: "enabled", Enabled: true, RequireTrusted: true}, IdempotencyKey: "test-pool-apply", Actor: "operator"})
	if err != nil || poolOperation.OperationID == "" {
		t.Fatalf("pool apply: %#v %v", poolOperation, err)
	}
	pool, err := runtime.store.GetJobPool("test")
	if err != nil || pool.ManifestDigest != items[0].ManifestDigest || pool.Signer != items[0].Signer || !pool.RequireTrusted {
		t.Fatalf("bound pool: %#v %v", pool, err)
	}
	if err := runtime.store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := assembleRuntimeWithFactory(cfg, testServiceOptions(), time.Now, testFactory{})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.store.Close()
	items, err = restarted.coreAPI.(coreapi.EnvironmentAPI).ListEnvironments(ctx)
	if err != nil || len(items) != 1 || !items[0].Ready {
		t.Fatalf("restarted environment: %#v %v", items, err)
	}
	pool, err = restarted.store.GetJobPool("test")
	if err != nil {
		t.Fatal(err)
	}
	windowCfg := cfg
	windowCfg.WindowsJobPool.Enabled = true
	if err := verifyWindowsModeEnvironment(restarted.store, restarted.environment, windowCfg, pool); err != nil {
		t.Fatalf("ready signed mode candidate: %v", err)
	}
	originalPool := pool
	pool.ManifestDigest = strings.Repeat("0", 64)
	if err := verifyWindowsModeEnvironment(restarted.store, restarted.environment, windowCfg, pool); err == nil {
		t.Fatal("mismatched pool digest accepted")
	}
	pool = originalPool
	pool.Signer = "other-signer"
	if err := verifyWindowsModeEnvironment(restarted.store, restarted.environment, windowCfg, pool); err == nil {
		t.Fatal("mismatched pool signer accepted")
	}
	record, err := restarted.store.GetEnvironmentRecord(testEnvironmentID, testEnvironmentVersion)
	if err != nil {
		t.Fatal(err)
	}
	record.Ready, record.Healthy = false, false
	if err := restarted.store.PutEnvironmentRecord(record); err != nil {
		t.Fatal(err)
	}
	if err := verifyWindowsModeEnvironment(restarted.store, restarted.environment, windowCfg, originalPool); err == nil {
		t.Fatal("unready Store projection accepted despite ready manager state")
	}
	record, err = restarted.environment.Get(testEnvironmentID, testEnvironmentVersion)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.store.PutEnvironmentRecord(record); err != nil {
		t.Fatal(err)
	}
	installedResource := filepath.Join(cfg.DataDir, "environments", filepath.FromSlash(testEnvironmentID), testEnvironmentVersion, "worker.mjs")
	if err := os.WriteFile(installedResource, []byte("tampered installed resource"), 0600); err != nil {
		t.Fatal(err)
	}
	failed, err := restarted.coreAPI.(coreapi.EnvironmentAPI).EnvironmentOperation(ctx, coreapi.EnvironmentOperationRequest{EnvironmentID: testEnvironmentID, Version: testEnvironmentVersion, Operation: "verify", IdempotencyKey: "verify-tampered", Actor: "operator"})
	if err != nil || failed.State != "failed" || failed.FailureCode != "environment_unverified" {
		t.Fatalf("verify tampered install: %#v %v", failed, err)
	}
	items, err = restarted.coreAPI.(coreapi.EnvironmentAPI).ListEnvironments(ctx)
	if err != nil || len(items) != 1 || items[0].Verified || items[0].Trusted || items[0].Enabled || items[0].Healthy || items[0].Ready {
		t.Fatalf("failed verify left ready Store projection: %#v %v", items, err)
	}
	if _, err := restarted.coreAPI.(coreapi.JobPoolAPI).ApplyJobPool(ctx, coreapi.JobPoolApplyRequest{Config: coreapi.JobPoolConfig{PoolID: "rejected", EnvironmentID: testEnvironmentID, EnvironmentVersion: testEnvironmentVersion, DesiredSlots: 1, MaxConcurrency: 1, DesiredState: "enabled", Enabled: true, RequireTrusted: true}, IdempotencyKey: "unready-apply", Actor: "operator"}); err == nil {
		t.Fatal("failed verification still allowed pool binding")
	}
	reloaded, err := newConfiguredEnvironmentManager(cfg, serviceTarget())
	if err != nil {
		t.Fatal(err)
	}
	record, err = reloaded.Get(testEnvironmentID, testEnvironmentVersion)
	if err != nil || record.Verified || record.Trusted || record.Enabled || record.Healthy || record.Ready {
		t.Fatalf("failed verification did not persist revoked gates: %#v %v", record, err)
	}
}

func TestTestEnvironmentBootstrapRejectsMissingAndRedirectedResources(t *testing.T) {
	cfg, err := config.New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := newConfiguredEnvironmentManager(cfg, serviceTarget())
	if err != nil {
		t.Fatal(err)
	}
	runtimeRoot := t.TempDir()
	if err := prepareTestEnvironmentPackage(cfg, runtimeRoot, serviceTarget(), manager); !errors.Is(err, environment.ErrPackageReference) {
		t.Fatalf("missing bundled files: %v", err)
	}
	if _, err := os.Stat(environmentTrustStorePath(cfg)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing resources changed trust store: %v", err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	source := t.TempDir()
	for _, name := range []string{"worker.mjs", "headless.mjs", "headless-adapter.mjs", "cdp-runtime.mjs", "local-test-page.html"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte("resource"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(runtimeRoot, "browser-worker"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(source, filepath.Join(runtimeRoot, "browser-worker", "src")); err != nil {
		t.Fatal(err)
	}
	if err := prepareTestEnvironmentPackage(cfg, runtimeRoot, serviceTarget(), manager); !errors.Is(err, environment.ErrPackageReference) {
		t.Fatalf("redirected source directory: %v", err)
	}
}

func TestTestEnvironmentBootstrapRecoversOrphanPublicSignerAndRejectsTamperedCatalog(t *testing.T) {
	cfg, err := config.New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	public, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveTestTrustStore(environmentTrustStorePath(cfg), environment.TrustStore{"orphan-test-signer": public}); err != nil {
		t.Fatal(err)
	}
	manager, err := newConfiguredEnvironmentManager(cfg, serviceTarget())
	if err != nil {
		t.Fatal(err)
	}
	runtimeRoot := t.TempDir()
	source := filepath.Join(runtimeRoot, "browser-worker", "src")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"worker.mjs", "headless.mjs", "headless-adapter.mjs", "cdp-runtime.mjs", "local-test-page.html"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte("resource"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := prepareTestEnvironmentPackage(cfg, runtimeRoot, serviceTarget(), manager); err != nil {
		t.Fatal(err)
	}
	trust, err := environment.LoadTrustStore(environmentTrustStorePath(cfg))
	if err != nil || len(trust) != 2 || len(trust["orphan-test-signer"]) != ed25519.PublicKeySize {
		t.Fatalf("orphan signer recovery: %v, keys=%d", err, len(trust))
	}
	resource := filepath.Join(cfg.DataDir, ".chuzi", "environment-catalog", testEnvironmentPackageRef, "worker.mjs")
	if err := os.WriteFile(resource, []byte("tampered resource"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := prepareTestEnvironmentPackage(cfg, runtimeRoot, serviceTarget(), manager); !errors.Is(err, environment.ErrPackageReference) {
		t.Fatalf("tampered catalog reused: %v", err)
	}
	if data, err := os.ReadFile(resource); err != nil || string(data) != "tampered resource" {
		t.Fatalf("failed validation silently replaced catalog: %v", err)
	}
}

func TestConfiguredEnvironmentManagerRejectsMissingTrustForPersistedState(t *testing.T) {
	cfg, err := config.New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := newConfiguredEnvironmentManager(cfg, serviceTarget())
	if err != nil {
		t.Fatal(err)
	}
	runtimeRoot := t.TempDir()
	source := filepath.Join(runtimeRoot, "browser-worker", "src")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"worker.mjs", "headless.mjs", "headless-adapter.mjs", "cdp-runtime.mjs", "local-test-page.html"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte("resource"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := prepareTestEnvironmentPackage(cfg, runtimeRoot, serviceTarget(), manager); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.InstallReferenceFor(context.Background(), testEnvironmentPackageRef, testEnvironmentID, testEnvironmentVersion); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(environmentTrustStorePath(cfg)); err != nil {
		t.Fatal(err)
	}
	if _, err := newConfiguredEnvironmentManager(cfg, serviceTarget()); err == nil {
		t.Fatal("persisted environment loaded without its trust store")
	}
}
