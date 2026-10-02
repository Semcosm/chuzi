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
	if err := c.manager.SyncRecords(c.store); err != nil {
		return environment.Record{}, err
	}
	return record, nil
}

func (e serviceEnvironmentExecutor) Execute(ctx context.Context, mutation store.EnvironmentMutation) error {
	if e.manager == nil || e.store == nil {
		return environment.ErrPackageReference
	}
	switch mutation.Operation {
	case "install":
		if _, err := e.manager.InstallReference(ctx, mutation.PackageRef); err != nil {
			return err
		}
	case "upgrade":
		if _, err := e.manager.UpgradeReference(ctx, mutation.PackageRef); err != nil {
			return err
		}
	case "rollback":
		if err := e.manager.Rollback(mutation.EnvironmentID, mutation.Version); err != nil {
			return err
		}
	default:
		return environment.ErrPackageReference
	}
	return e.manager.SyncRecords(e.store)
}
