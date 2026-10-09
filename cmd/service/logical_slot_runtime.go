package main

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Semcosm/chuzi/internal/environment"
	"github.com/Semcosm/chuzi/internal/slot"
	"github.com/Semcosm/chuzi/internal/slotlifecycle"
	"github.com/Semcosm/chuzi/internal/store"
)

// logicalSlotReconciler is the platform-neutral lifecycle for a logical pool.
// It is used when Windows job-pool provisioning is disabled. Logical slots
// still need a lifecycle pass: otherwise Core can create unprovisioned slots
// that can never become ready, and retiring slots can never finish deletion.
//
// The durable Store remains the source of truth and the pool source is read on
// every pass, so pools created or changed through Core are handled after the
// service has already started.
type logicalSlotReconciler struct {
	database *store.Store
	now      func() time.Time
	owner    string
	mu       sync.Mutex
	items    map[string]logicalSlotItem
}

type logicalSlotItem struct {
	reconciler        *slotlifecycle.Reconciler
	requiresAuthority bool
}

func newLogicalSlotReconciler(database *store.Store, now func() time.Time, owner string) (*logicalSlotReconciler, error) {
	if database == nil || now == nil || owner == "" {
		return nil, errInvalidOptions
	}
	return &logicalSlotReconciler{database: database, now: now, owner: owner, items: make(map[string]logicalSlotItem)}, nil
}

// Reconcile is the slotReconciler compatibility entry point.
func (r *logicalSlotReconciler) Reconcile(ctx context.Context) error {
	return r.ReconcileAll(ctx)
}

// ReconcileAll runs the control projection before and after the lifecycle pass.
// The second projection lets short logical operations, including delete, reach
// their terminal state in one tick instead of waiting for the next scheduler
// interval. A completed delete removes the pool between the two projections;
// that expected not-found result is therefore ignored.
func (r *logicalSlotReconciler) ReconcileAll(ctx context.Context) error {
	if r == nil || ctx == nil {
		return errors.New("logical slot reconciler: invalid configuration")
	}
	pools, err := r.database.ListJobPools()
	if err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(pools))
	var first error
	for _, pool := range pools {
		if err := ctx.Err(); err != nil {
			return err
		}
		seen[pool.PoolID] = struct{}{}
		if _, err := r.database.ReconcileJobPoolControl(pool.PoolID, r.now().UTC()); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		reconciler, err := r.forPool(pool)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if err := reconciler.Reconcile(ctx); err != nil && first == nil {
			first = err
		}
		if _, err := r.database.ReconcileJobPoolControl(pool.PoolID, r.now().UTC()); err != nil && !errors.Is(err, slot.ErrPoolNotFound) && first == nil {
			first = err
		}
	}
	r.mu.Lock()
	for poolID := range r.items {
		if _, ok := seen[poolID]; !ok {
			delete(r.items, poolID)
		}
	}
	r.mu.Unlock()
	return first
}

func (r *logicalSlotReconciler) forPool(pool slot.PoolConfig) (*slotlifecycle.Reconciler, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	requiresAuthority := pool.ManifestDigest != "" || pool.Signer != ""
	if current, ok := r.items[pool.PoolID]; ok && current.requiresAuthority == requiresAuthority {
		return current.reconciler, nil
	}
	provisioner := logicalSlotProvisioner{now: r.now}
	// A logical pool without a manifest digest/signer is the intentionally
	// unbacked foundation mode used by the launcher. Once signed metadata is
	// supplied, require the durable environment authority before provisioning.
	extras := []any{logicalPoolSource{database: r.database}, r.owner}
	if requiresAuthority {
		extras = append(extras, logicalEnvironmentAuthority{database: r.database})
	}
	value, err := slotlifecycle.New(r.database, provisioner, pool, r.now, time.Second, time.Second, extras...)
	if err != nil {
		return nil, err
	}
	r.items[pool.PoolID] = logicalSlotItem{reconciler: value, requiresAuthority: requiresAuthority}
	return value, nil
}

// The Store implements both lifecycle optional interfaces. Small wrappers keep
// the logical mode from accidentally enabling the environment authority for a
// pool that intentionally has no signed package metadata.
type logicalPoolSource struct{ database *store.Store }

func (s logicalPoolSource) GetJobPool(poolID string) (slot.PoolConfig, error) {
	return s.database.GetJobPool(poolID)
}

type logicalEnvironmentAuthority struct{ database *store.Store }

func (s logicalEnvironmentAuthority) GetEnvironmentRecord(environmentID, version string) (environment.Record, error) {
	return s.database.GetEnvironmentRecord(environmentID, version)
}

func (r *logicalSlotReconciler) Shutdown(ctx context.Context) error {
	if r == nil || ctx == nil {
		return nil
	}
	return ctx.Err()
}

// logicalSlotProvisioner models a ready local execution slot without creating
// an OS user, process, profile, endpoint, or credential. It is deliberately
// limited to the non-Windows/disabled-Windows logical pool mode.
type logicalSlotProvisioner struct {
	now func() time.Time
}

func (p logicalSlotProvisioner) summary(request slot.ProvisionRequest) slot.EnvironmentSummary {
	generation := request.EnvironmentGeneration
	if generation == 0 {
		generation = 1
	}
	return slot.EnvironmentSummary{
		EnvironmentID:  request.Requirement.EnvironmentID,
		Version:        request.Requirement.Version,
		Generation:     generation,
		Capabilities:   append([]string(nil), request.Requirement.Capabilities...),
		ManifestDigest: request.Requirement.ManifestDigest,
		Signer:         request.Requirement.Signer,
		Trusted:        true,
		AgentVersion:   "logical",
		SessionState:   "ready",
		DesktopReady:   true,
		AgentHandle:    "logical",
		UpdatedAt:      p.now().UTC(),
	}
}

func (p logicalSlotProvisioner) Provision(ctx context.Context, request slot.ProvisionRequest) (slot.ProvisionResult, error) {
	if err := ctx.Err(); err != nil {
		return slot.ProvisionResult{}, err
	}
	summary := p.summary(request)
	return slot.ProvisionResult{AgentHandle: summary.AgentHandle, Summary: summary}, nil
}

func (p logicalSlotProvisioner) Inspect(ctx context.Context, request slot.ProvisionRequest) (slot.EnvironmentSummary, error) {
	if err := ctx.Err(); err != nil {
		return slot.EnvironmentSummary{}, err
	}
	return p.summary(request), nil
}

func (p logicalSlotProvisioner) Retire(ctx context.Context, _ slot.ProvisionRequest) error {
	return ctx.Err()
}
