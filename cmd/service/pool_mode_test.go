package main

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/slot"
	"github.com/Semcosm/chuzi/internal/store"
)

func TestPoolModeRequiresCleanupAcrossEveryPool(t *testing.T) {
	at := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	for _, status := range []slot.Status{slot.Unprovisioned, slot.Provisioning, slot.Ready, slot.Leased, slot.Quarantined, slot.Draining, slot.Retiring, slot.Deleted} {
		t.Run(string(status), func(t *testing.T) {
			cfg, _ := config.New(t.TempDir())
			db, err := store.Open(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			pool := slot.PoolConfig{PoolID: "other-pool", EnvironmentID: "env", DesiredSlots: 1}
			if err := db.ReconcileJobPool(pool, at); err != nil {
				t.Fatal(err)
			}
			slots, err := db.ListSlots(pool.PoolID)
			if err != nil || len(slots) != 1 {
				t.Fatalf("slots: %v %v", slots, err)
			}
			// Scale the pool target to zero, but retain a resource in an arbitrary state.
			pool.DesiredSlots = 0
			if err := db.ReconcileJobPool(pool, at); err != nil {
				t.Fatal(err)
			}
			resource := slots[0]
			resource.Status = status
			resource.EnvironmentGeneration = 1
			if err := db.UpsertSlot(resource); err != nil {
				t.Fatal(err)
			}
			_, err = poolModeCandidate(cfg, db, "logical", "", 0, at)
			if status == slot.Deleted {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, poolModeError("pool_cleanup_required")) {
				t.Fatalf("cleanup allowed: %v", err)
			}
		})
	}
}

func TestPoolModeChecksRevisionSignatureAndPendingOperations(t *testing.T) {
	cfg, _ := config.New(t.TempDir())
	db, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	pool := slot.PoolConfig{PoolID: "pool-a", EnvironmentID: "env", EnvironmentVersion: "1.0.0", RequireTrusted: true}
	op, _, err := db.ApplyJobPool(store.JobPoolMutation{Config: pool, Operation: "apply", Actor: "operator", IdempotencyKey: "mode-apply", RequestedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := poolModeCandidate(cfg, db, "logical", "", 0, at); !errors.Is(err, poolModeError("pool_cleanup_required")) {
		t.Fatalf("pending op: %v", err)
	}
	if _, err := db.UpdateJobPoolOperation(op.OperationID, store.JobPoolApplied, "applied", "", at); err != nil {
		t.Fatal(err)
	}
	pool, err = db.GetJobPool(pool.PoolID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := poolModeCandidate(cfg, db, "windows", pool.PoolID, pool.ConfigRevision+1, at); !errors.Is(err, poolModeError("stale_revision")) {
		t.Fatalf("revision: %v", err)
	}
	if _, err := poolModeCandidate(cfg, db, "windows", pool.PoolID, pool.ConfigRevision, at); !errors.Is(err, poolModeError("signed_environment_required")) {
		t.Fatalf("signature: %v", err)
	}
	pool.ManifestDigest = strings.Repeat("a", 64)
	pool.Signer = "signer"
	if err := db.ReconcileJobPool(pool, at); err != nil {
		t.Fatal(err)
	}
	pool, _ = db.GetJobPool(pool.PoolID)
	candidate, err := poolModeCandidate(cfg, db, "windows", pool.PoolID, pool.ConfigRevision, at)
	if err != nil {
		t.Fatal(err)
	}
	if !candidate.WindowsJobPool.Enabled || candidate.WindowsJobPool.DesiredSlots != 0 || candidate.JobPool.ManifestDigest != pool.ManifestDigest {
		t.Fatalf("candidate: %#v", candidate)
	}
	candidate.Credentials.KeyEnv = "DEPLOYMENT_KEY"
	disabled, err := poolModeCandidate(candidate, db, "logical", "", 0, at)
	if err != nil || disabled.WindowsJobPool.Enabled || disabled.Credentials.KeyEnv != "DEPLOYMENT_KEY" {
		t.Fatalf("disabled: %#v %v", disabled, err)
	}
}

func TestPoolModeMaintenanceRejectsNonWindowsAndPreservesConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("non-Windows boundary")
	}
	cfg, _ := config.New(t.TempDir())
	path := filepath.Join(cfg.DataDir, "core-config.json")
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	if err := runPoolModeMaintenance(context.Background(), path, "windows", "pool-a", 1); !errors.Is(err, poolModeError("windows_required")) {
		t.Fatalf("err=%v", err)
	}
	loaded, err := config.Load(path)
	if err != nil || loaded.WindowsJobPool.Enabled {
		t.Fatalf("config changed: %#v %v", loaded, err)
	}
}

func TestPoolModeRejectsActiveAndExpiredAccountLeases(t *testing.T) {
	cfg, _ := config.New(t.TempDir())
	db, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	if _, err := db.CreateAccount("account-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AcquireLease("account-a", at, "lease-a", "owner-a", time.Minute); err != nil {
		t.Fatal(err)
	}
	for _, now := range []time.Time{at, at.Add(2 * time.Minute)} {
		if _, err := poolModeCandidate(cfg, db, "logical", "", 0, now); !errors.Is(err, poolModeError("pool_cleanup_required")) {
			t.Fatalf("lease allowed at %s: %v", now, err)
		}
	}
	if err := db.ReleaseLease("account-a", "lease-a", "owner-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := poolModeCandidate(cfg, db, "logical", "", 0, at); err != nil {
		t.Fatal(err)
	}
}

func TestPoolModeMaintenanceRequiresExclusiveStoreLock(t *testing.T) {
	cfg, _ := config.New(t.TempDir())
	path := filepath.Join(cfg.DataDir, "core-config.json")
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := runPoolModeMaintenance(context.Background(), path, "logical", "", 0); !errors.Is(err, poolModeError("core_stop_required")) {
		t.Fatalf("locked store accepted: %v", err)
	}
}
