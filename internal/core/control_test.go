package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/coreapi"
	"github.com/Semcosm/chuzi/internal/environment"
	"github.com/Semcosm/chuzi/internal/slot"
	"github.com/Semcosm/chuzi/internal/store"
)

var controlTestTime = time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)

type controlJobPools struct {
	projection store.JobPoolProjection
	err        error
}

func (p controlJobPools) ListJobPoolProjections(time.Time) ([]store.JobPoolProjection, error) {
	if p.err != nil {
		return nil, p.err
	}
	return []store.JobPoolProjection{p.projection}, nil
}
func (p controlJobPools) GetJobPoolProjection(string, time.Time) (store.JobPoolProjection, error) {
	return p.projection, p.err
}
func (p controlJobPools) ApplyJobPool(store.JobPoolMutation) (store.JobPoolOperation, bool, error) {
	return store.JobPoolOperation{}, false, p.err
}
func (p controlJobPools) ScaleJobPool(string, int, uint64, string, string, time.Time) (store.JobPoolOperation, bool, error) {
	return store.JobPoolOperation{}, false, p.err
}
func (p controlJobPools) DrainJobPool(string, uint64, string, string, time.Time) (store.JobPoolOperation, bool, error) {
	return store.JobPoolOperation{}, false, p.err
}
func (p controlJobPools) ResumeJobPool(string, uint64, string, string, time.Time) (store.JobPoolOperation, bool, error) {
	return store.JobPoolOperation{}, false, p.err
}
func (p controlJobPools) GetJobPoolOperation(string) (store.JobPoolOperation, error) {
	return store.JobPoolOperation{}, p.err
}
func (p controlJobPools) ReconcileJobPoolControl(string, time.Time) (store.JobPoolProjection, error) {
	return p.projection, p.err
}

type controlEnvironments struct {
	record environment.Record
	err    error
}

type recordingEnvironmentExecutor struct {
	mutation store.EnvironmentMutation
	err      error
}

func (e *recordingEnvironmentExecutor) Execute(_ context.Context, mutation store.EnvironmentMutation) (environment.Record, error) {
	e.mutation = mutation
	return environment.Record{EnvironmentID: mutation.EnvironmentID, Version: mutation.Version, ManifestDigest: strings.Repeat("a", 64), Signer: "signer", Installed: true, Verified: true, Trusted: true, Enabled: true, Healthy: true, Ready: true, Generation: 1, UpdatedAt: controlTestTime}, e.err
}

func (e controlEnvironments) ListEnvironmentRecords() ([]environment.Record, error) {
	return []environment.Record{e.record}, e.err
}
func (e controlEnvironments) GetEnvironmentRecord(string, string) (environment.Record, error) {
	if e.err != nil {
		return environment.Record{}, e.err
	}
	return e.record, nil
}
func (e controlEnvironments) ApplyEnvironmentOperation(store.EnvironmentMutation) (store.EnvironmentOperationRecord, bool, error) {
	return store.EnvironmentOperationRecord{OperationID: "envop-1", EnvironmentID: "env/v1", Version: "1.0.0", Operation: "trust", State: "requested", RequestedAt: controlTestTime, UpdatedAt: controlTestTime}, false, e.err
}
func (e controlEnvironments) GetEnvironmentOperation(string) (store.EnvironmentOperationRecord, error) {
	return store.EnvironmentOperationRecord{}, e.err
}
func (e controlEnvironments) UpdateEnvironmentOperation(string, string, string, time.Time) (store.EnvironmentOperationRecord, error) {
	return store.EnvironmentOperationRecord{OperationID: "envop-1", EnvironmentID: "env/v1", Version: "1.0.0", Operation: "install", State: "applied", RequestedAt: controlTestTime, UpdatedAt: controlTestTime}, e.err
}
func (e controlEnvironments) ApplyEnvironmentGate(string, string, string, time.Time) (environment.Record, error) {
	return e.record, e.err
}

func TestJobPoolProjectionRedactsActorAndPreservesCapacityFields(t *testing.T) {
	projection := store.JobPoolProjection{
		Config:           slot.PoolConfig{PoolID: "pool-1", EnvironmentID: "env/v1", EnvironmentVersion: "1.0.0", DesiredSlots: 4, MaxConcurrency: 2, ConfigRevision: 3, UpdatedBy: "operator@example", DesiredState: "enabled"},
		Status:           slot.StatusCounts{PoolID: "pool-1", Desired: 4, Ready: 3, Leased: 1, Quarantined: 1, Draining: 1, Provisioning: 1, Retiring: 1, Unprovisioned: 1},
		EnvironmentReady: true, EnvironmentReadiness: "ready", ReconcileState: store.JobPoolApplied, OperationID: "op-1",
	}
	service, err := New(Dependencies{Requests: viewServiceRequests{}, Store: viewServiceStore{}, JobPoolControl: controlJobPools{projection: projection}, Clock: func() time.Time { return controlTestTime }})
	if err != nil {
		t.Fatal(err)
	}
	items, err := service.ListJobPools(context.Background())
	if err != nil || len(items) != 1 {
		t.Fatalf("items = %#v, err=%v", items, err)
	}
	item := items[0]
	if item.Status.EffectiveCapacity != 2 || item.Config.UpdatedBy == "operator@example" || !strings.HasPrefix(item.Config.UpdatedBy, "id_") {
		t.Fatalf("projection = %#v", item)
	}
}

func TestJobPoolStatusRejectsInvalidPoolIDAndMapsStaleRevision(t *testing.T) {
	service, err := New(Dependencies{Requests: viewServiceRequests{}, Store: viewServiceStore{}, JobPoolControl: controlJobPools{err: store.ErrJobPoolStaleRevision}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetJobPoolStatus(context.Background(), "bad pool"); coreapi.CodeOf(err) != coreapi.CodeInvalidArgument {
		t.Fatalf("invalid pool error = %v, code=%q", err, coreapi.CodeOf(err))
	}
	_, err = service.ScaleJobPool(context.Background(), coreapi.JobPoolScaleRequest{PoolID: "pool-1", DesiredSlots: 2, IdempotencyKey: "idem", Actor: "operator"})
	if coreapi.CodeOf(err) != coreapi.CodeConflict || errors.Is(err, store.ErrJobPoolStaleRevision) {
		t.Fatalf("stale revision error = %v, code=%q", err, coreapi.CodeOf(err))
	}
}

func TestEnvironmentOperationRejectsUncontrolledPackageRef(t *testing.T) {
	service, err := New(Dependencies{Requests: viewServiceRequests{}, Store: viewServiceStore{}, Environments: controlEnvironments{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.EnvironmentOperation(context.Background(), coreapi.EnvironmentOperationRequest{EnvironmentID: "env/v1", Version: "1.0.0", Operation: "install", PackageRef: "../package", IdempotencyKey: "idem", Actor: "operator"})
	if coreapi.CodeOf(err) != coreapi.CodeInvalidArgument {
		t.Fatalf("package ref error = %v, code=%q", err, coreapi.CodeOf(err))
	}
}

func TestEnvironmentPackageOperationUsesControlledExecutor(t *testing.T) {
	executor := &recordingEnvironmentExecutor{}
	service, err := New(Dependencies{Requests: viewServiceRequests{}, Store: viewServiceStore{}, Environments: controlEnvironments{}, EnvironmentExecutor: executor})
	if err != nil {
		t.Fatal(err)
	}
	operation, err := service.EnvironmentOperation(context.Background(), coreapi.EnvironmentOperationRequest{EnvironmentID: "env/v1", Version: "1.0.0", Operation: "install", PackageRef: "catalog-v1", IdempotencyKey: "idem", Actor: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	if operation.State != "applied" || executor.mutation.PackageRef != "catalog-v1" || executor.mutation.Operation != "install" {
		t.Fatalf("operation = %#v, mutation = %#v", operation, executor.mutation)
	}
}

func TestApplyJobPoolClassifiesInvalidConfiguration(t *testing.T) {
	service, err := New(Dependencies{Requests: viewServiceRequests{}, Store: viewServiceStore{}, JobPoolControl: controlJobPools{err: slot.ErrInvalidConfig}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.ApplyJobPool(context.Background(), coreapi.JobPoolApplyRequest{
		Config:         coreapi.JobPoolConfig{PoolID: "pool-1", EnvironmentID: "env/v1", DesiredState: "unknown"},
		IdempotencyKey: "idem", Actor: "operator",
	})
	if coreapi.CodeOf(err) != coreapi.CodeInvalidArgument {
		t.Fatalf("invalid pool configuration error = %v, code=%q", err, coreapi.CodeOf(err))
	}
}
