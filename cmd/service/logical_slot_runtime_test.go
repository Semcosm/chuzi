package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/coreapi"
	"github.com/Semcosm/chuzi/internal/slot"
	"github.com/Semcosm/chuzi/internal/store"
)

func TestCoreLogicalPoolApplyWithoutEnvironmentRemainsAvailable(t *testing.T) {
	cfg, err := config.New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := assembleRuntimeWithFactory(cfg, testServiceOptions(), time.Now, testFactory{})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.store.Close()
	ctx := context.Background()
	pools := runtime.coreAPI.(coreapi.JobPoolAPI)
	_, err = pools.ApplyJobPool(ctx, coreapi.JobPoolApplyRequest{Config: coreapi.JobPoolConfig{PoolID: "test", EnvironmentID: "logical-test", EnvironmentVersion: "1.0.0", DesiredSlots: 1, MaxConcurrency: 1, RequireTrusted: true, DesiredState: "enabled", Enabled: true}, IdempotencyKey: "logical-apply", Actor: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.slotReconciler.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	items, err := pools.ListJobPools(ctx)
	if err != nil || len(items) != 1 || items[0].Status.ExecutionMode != "logical" || items[0].Status.Ready != 1 || items[0].Status.EffectiveCapacity != 1 || items[0].Config.ManifestDigest != "" || items[0].Config.Signer != "" {
		t.Fatalf("logical capacity = %#v, error = %v", items, err)
	}
	_, err = pools.ScaleJobPool(ctx, coreapi.JobPoolScaleRequest{PoolID: "test", DesiredSlots: 0, ExpectedRevision: items[0].Config.ConfigRevision, IdempotencyKey: "logical-zero", Actor: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.slotReconciler.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	items, err = pools.ListJobPools(ctx)
	if err != nil || len(items) != 1 || items[0].Status.Desired != 0 || items[0].Status.Ready != 0 || items[0].Status.ReconcileState != "applied" {
		t.Fatalf("logical scale-to-zero = %#v, error = %v", items, err)
	}
}

func TestLogicalSlotReconcilerCompletesApplyAndDelete(t *testing.T) {
	cfg, err := config.New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := func() time.Time { return time.Date(2026, time.October, 9, 12, 0, 0, 0, time.UTC) }
	pool := slot.PoolConfig{PoolID: "logical-pool", EnvironmentID: "environment", DesiredSlots: 3, RequireTrusted: true}
	created, _, err := database.ApplyJobPool(store.JobPoolMutation{Config: pool, Operation: "apply", IdempotencyKey: "apply-logical", Actor: "operator", RequestedAt: now()})
	if err != nil {
		t.Fatal(err)
	}
	reconciler, err := newLogicalSlotReconciler(database, now, "service")
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err := database.SlotPoolStatus(pool.PoolID, now())
	if err != nil || status.Ready != 3 || status.Unprovisioned != 0 {
		t.Fatalf("logical apply status = %#v, err=%v", status, err)
	}
	applied, err := database.GetJobPoolOperation(created.OperationID)
	if err != nil || applied.State != store.JobPoolApplied {
		t.Fatalf("logical apply operation = %#v, err=%v", applied, err)
	}
	current, err := database.GetJobPool(pool.PoolID)
	if err != nil {
		t.Fatal(err)
	}
	deletion, _, err := database.DeleteJobPool(pool.PoolID, current.ConfigRevision, "delete-logical", "operator", now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetJobPool(pool.PoolID); !errors.Is(err, slot.ErrPoolNotFound) {
		t.Fatalf("logical pool after delete = %v", err)
	}
	finished, err := database.GetJobPoolOperation(deletion.OperationID)
	if err != nil || finished.State != store.JobPoolApplied || finished.Result != "deleted" {
		t.Fatalf("logical delete operation = %#v, err=%v", finished, err)
	}
}
