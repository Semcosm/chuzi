package main

import (
	"context"
	"time"

	"github.com/Semcosm/chuzi/internal/environment"
	"github.com/Semcosm/chuzi/internal/store"
)

// serviceEnvironmentExecutor is the controlled package boundary used by the
// Core operation API. PackageRef is an opaque catalog key; the Manager owns
// the catalog root and performs signature, digest, and tree validation.
type serviceEnvironmentExecutor struct {
	manager *environment.Manager
	store   *store.Store
}

// serviceEnvironmentControl keeps lifecycle gates on the same signed-manager
// authority used by the slot runtime. Operation records still live in Store.
type serviceEnvironmentControl struct {
	store   *store.Store
	manager *environment.Manager
}

func (c serviceEnvironmentControl) ListEnvironmentRecords() ([]environment.Record, error) {
	return c.store.ListEnvironmentRecords()
}

func (c serviceEnvironmentControl) ApplyEnvironmentOperation(mutation store.EnvironmentMutation) (store.EnvironmentOperationRecord, bool, error) {
	return c.store.ApplyEnvironmentOperation(mutation)
}

func (c serviceEnvironmentControl) GetEnvironmentOperation(id string) (store.EnvironmentOperationRecord, error) {
	return c.store.GetEnvironmentOperation(id)
}

func (c serviceEnvironmentControl) UpdateEnvironmentOperation(id, state, failureCode string, at time.Time) (store.EnvironmentOperationRecord, error) {
	return c.store.UpdateEnvironmentOperation(id, state, failureCode, at)
}

func (c serviceEnvironmentControl) CompleteEnvironmentOperation(id, state, failureCode string, record environment.Record, at time.Time) (store.EnvironmentOperationRecord, error) {
	return c.store.CompleteEnvironmentOperation(id, state, failureCode, record, at)
}

func (c serviceEnvironmentControl) ApplyEnvironmentGate(id, version, operation string, at time.Time) (environment.Record, error) {
	if c.manager == nil {
		return c.store.ApplyEnvironmentGate(id, version, operation, at)
	}
	var (
		record environment.Record
		err    error
	)
	switch operation {
	case "trust":
		record, err = c.manager.SetTrusted(id, version, true)
	case "enable":
		record, err = c.manager.SetEnabled(id, version, true)
	case "disable":
		record, err = c.manager.SetEnabled(id, version, false)
	case "health", "verify":
		record, err = c.manager.HealthCheck(context.Background(), id, version)
	default:
		return c.store.ApplyEnvironmentGate(id, version, operation, at)
	}
	if err != nil {
		return environment.Record{}, err
	}
	return record, nil
}

func (e serviceEnvironmentExecutor) Execute(ctx context.Context, mutation store.EnvironmentMutation) (environment.Record, error) {
	if e.manager == nil || e.store == nil {
		return environment.Record{}, environment.ErrPackageReference
	}
	switch mutation.Operation {
	case "install":
		record, err := e.manager.InstallReferenceFor(ctx, mutation.PackageRef, mutation.EnvironmentID, mutation.Version)
		if err != nil {
			return environment.Record{}, err
		}
		if record.EnvironmentID != mutation.EnvironmentID || record.Version != mutation.Version {
			return environment.Record{}, environment.ErrInvalidManifest
		}
		return record, nil
	case "upgrade":
		record, err := e.manager.UpgradeReferenceFor(ctx, mutation.PackageRef, mutation.EnvironmentID, mutation.Version)
		if err != nil {
			return environment.Record{}, err
		}
		if record.EnvironmentID != mutation.EnvironmentID || record.Version != mutation.Version {
			return environment.Record{}, environment.ErrInvalidManifest
		}
		return record, nil
	case "rollback":
		if err := e.manager.Rollback(mutation.EnvironmentID, mutation.Version); err != nil {
			return environment.Record{}, err
		}
		return e.manager.Get(mutation.EnvironmentID, mutation.Version)
	default:
		return environment.Record{}, environment.ErrPackageReference
	}
}
