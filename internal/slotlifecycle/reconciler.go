// Package slotlifecycle connects the durable logical slot pool to an OS
// provisioner without moving lease or business-state ownership out of store.
package slotlifecycle

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/Semcosm/chuzi/internal/environment"
	"github.com/Semcosm/chuzi/internal/slot"
)

var (
	ErrInvalidConfig = errors.New("slotlifecycle: invalid configuration")
	ErrLeaseMismatch = errors.New("slotlifecycle: slot has an inconsistent lease")
)

const (
	prewarmMaxAttempts = 5
	prewarmRetryBase   = 5 * time.Second
	prewarmRetryMax    = 2 * time.Minute
)

type Store interface {
	ListSlots(string) ([]slot.Slot, error)
	GetSlotLease(string) (slot.Lease, bool, error)
	SetSlotStatus(string, slot.Status, time.Time) error
	MarkSlotReady(string, slot.EnvironmentSummary, time.Time) error
}

type LeaseHealthChecker interface {
	Health(context.Context, slot.ProvisionRequest, slot.Lease) error
}

type SlotLeaseQuarantiner interface {
	QuarantineSlotLease(string, string, string, time.Time, string) error
}

type CapabilityRevoker interface {
	RevokeSlotLease(context.Context, string) error
}

type GenerationRevoker interface {
	RevokeGeneration(context.Context, string, uint64) error
}

// EnvironmentAuthority is the trusted package registry consulted before a
// pool is provisioned. It keeps caller supplied slot requirements from
// becoming an alternate environment source.
type EnvironmentAuthority interface {
	GetEnvironmentRecord(string, string) (environment.Record, error)
}

// PoolConfigSource lets a long-lived reconciler follow the durable desired
// pool after a Core update instead of retaining startup configuration.
type PoolConfigSource interface {
	GetJobPool(string) (slot.PoolConfig, error)
}

// PoolRuntimeUpdater refreshes service-owned runtime paths after a durable
// environment change. Implementations must resolve only signed package
// metadata and must never accept caller paths or commands.
type PoolRuntimeUpdater interface {
	UpdatePoolRuntime(context.Context, slot.PoolConfig) error
}

// SessionOperationStateLookup lets the pool reconciler yield to the durable
// session controller for an explicit operation on the same slot. The lookup
// is injected as a small callback to keep this package independent from the
// bbolt store's operation DTO.
type SessionOperationStateLookup func(string) (string, error)

// slotTargetUpdater is optional so focused lifecycle fakes do not need to
// persist target metadata. The durable store implements it to record the
// generation fence before provisioning starts.
type slotTargetUpdater interface {
	UpsertSlot(slot.Slot) error
}

type Provisioner interface {
	Provision(context.Context, slot.ProvisionRequest) (slot.ProvisionResult, error)
	Inspect(context.Context, slot.ProvisionRequest) (slot.EnvironmentSummary, error)
	Retire(context.Context, slot.ProvisionRequest) error
}

// Shutdowner is an optional process-lifecycle boundary. OS provisioners use
// it to close agent Job Objects during graceful service shutdown while
// leaving durable slot ownership intact for the next reconcile pass.
type Shutdowner interface {
	Shutdown(context.Context) error
}

// SessionStopper fences a running prewarm session while retaining the
// service-owned identity, Profile, and slot root for a later explicit Start.
// It is distinct from Retire, which permanently removes those resources.
type SessionStopper interface {
	StopSession(context.Context, slot.ProvisionRequest) error
}

type Reconciler struct {
	mu               sync.Mutex
	store            Store
	provisioner      Provisioner
	pool             slot.PoolConfig
	clock            func() time.Time
	provisionTimeout time.Duration
	cleanupTimeout   time.Duration
	revoker          CapabilityRevoker
	authority        EnvironmentAuthority
	poolSource       PoolConfigSource
	operationState   SessionOperationStateLookup
	owner            string
}

func New(database Store, provisioner Provisioner, pool slot.PoolConfig, clock func() time.Time, provisionTimeout, cleanupTimeout time.Duration, extras ...any) (*Reconciler, error) {
	if database == nil || provisioner == nil || clock == nil || pool.Validate() != nil || provisionTimeout <= 0 || cleanupTimeout <= 0 {
		return nil, ErrInvalidConfig
	}
	var revoker CapabilityRevoker
	var authority EnvironmentAuthority
	var poolSource PoolConfigSource
	var operationState SessionOperationStateLookup
	owner := "service"
	ownerSet := false
	for _, extra := range extras {
		switch value := extra.(type) {
		case nil:
			continue
		case CapabilityRevoker:
			if revoker != nil {
				return nil, ErrInvalidConfig
			}
			revoker = value
		case EnvironmentAuthority:
			if authority != nil {
				return nil, ErrInvalidConfig
			}
			authority = value
		case PoolConfigSource:
			if poolSource != nil {
				return nil, ErrInvalidConfig
			}
			poolSource = value
		case SessionOperationStateLookup:
			if operationState != nil {
				return nil, ErrInvalidConfig
			}
			operationState = value
		case string:
			if ownerSet || strings.TrimSpace(value) != value || value == "" || len(value) > 160 || strings.ContainsAny(value, "\x00\r\n\t/\\") {
				return nil, ErrInvalidConfig
			}
			owner = value
			ownerSet = true
		default:
			return nil, ErrInvalidConfig
		}
		if candidate, ok := extra.(PoolConfigSource); ok {
			if poolSource == nil {
				poolSource = candidate
			}
		}
	}
	return &Reconciler{store: database, provisioner: provisioner, pool: pool, clock: clock, provisionTimeout: provisionTimeout, cleanupTimeout: cleanupTimeout, revoker: revoker, authority: authority, poolSource: poolSource, operationState: operationState, owner: owner}, nil
}

// Reconcile processes one pass. Failures are returned as a stable category;
// callers must not log the provisioner's underlying OS error text.
func (r *Reconciler) Reconcile(ctx context.Context) error {
	if r == nil || ctx == nil {
		return ErrInvalidConfig
	}
	now := r.clock().UTC()
	r.mu.Lock()
	defer r.mu.Unlock()
	pool := r.pool
	if r.poolSource != nil {
		current, sourceErr := r.poolSource.GetJobPool(pool.PoolID)
		if sourceErr != nil {
			return errors.New("slotlifecycle: pool configuration unavailable")
		}
		pool = current
	}
	slots, err := r.store.ListSlots(pool.PoolID)
	if err != nil {
		return err
	}
	cleanupOnly := pool.DesiredSlots == 0 || pool.DesiredState == "draining" || pool.DesiredState == "disabled"
	targetSlots := pool.DesiredSlots
	if cleanupOnly {
		targetSlots = 0
	}
	var packageGeneration uint64
	if cleanupOnly {
		// Cleanup must remain available when the signed package is missing or no
		// longer trusted, so do not resolve it through EnvironmentAuthority here.
		// A provisioner that already holds a verified runtime can still require
		// its request fence to match that handoff before Retire touches resources.
		if provider, ok := r.provisioner.(interface{ PackageGeneration() uint64 }); ok {
			packageGeneration = provider.PackageGeneration()
		}
	}
	if r.authority != nil && !cleanupOnly {
		record, authorityErr := r.authority.GetEnvironmentRecord(pool.EnvironmentID, pool.EnvironmentVersion)
		if authorityErr != nil || !record.IsReady() || record.EnvironmentID != pool.EnvironmentID || record.Version != pool.EnvironmentVersion || pool.ManifestDigest == "" || !strings.EqualFold(record.ManifestDigest, pool.ManifestDigest) || pool.Signer == "" || record.Signer != pool.Signer || !containsAll(record.Capabilities, pool.Capabilities) {
			return errors.New("slotlifecycle: trusted environment unavailable")
		}
		packageGeneration = record.Generation
	}
	if updater, ok := r.provisioner.(PoolRuntimeUpdater); ok && !cleanupOnly {
		if err := updater.UpdatePoolRuntime(ctx, pool); err != nil {
			return errors.New("slotlifecycle: trusted environment runtime unavailable")
		}
	}
	var first error
	for _, value := range slots {
		if err := ctx.Err(); err != nil {
			return err
		}
		if value.Status == slot.Deleted {
			continue
		}
		if value.SessionOperationID != "" && r.operationState != nil {
			operationState, lookupErr := r.operationState(value.SessionOperationID)
			if lookupErr != nil {
				if first == nil {
					first = errors.New("slotlifecycle: session operation unavailable")
				}
				continue
			}
			if operationState != "ready" && operationState != "stopped" && operationState != "failed" && operationState != "cancelled" {
				// The durable controller owns this slot until it checkpoints or
				// completes. Starting prewarm or issuing StopSession here would
				// create a second runtime owner for the same generation.
				continue
			}
		}
		lease, leased, err := r.store.GetSlotLease(value.SlotID)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		environmentChanged := !matchesSlotTarget(value, pool)
		generation := value.EnvironmentGeneration
		if generation == 0 {
			// A newly created logical slot has no runtime generation yet. The
			// first provision is generation one.
			generation = 1
		} else if environmentChanged && value.Ordinal <= targetSlots {
			if generation == ^uint64(0) {
				if first == nil {
					first = errors.New("slotlifecycle: environment generation exhausted")
				}
				continue
			}
			generation++
		}
		requestGeneration := generation
		if value.SessionDesiredState == "stopped" || value.Status == slot.Stopped {
			// A pool target update changes the next start generation, but a
			// stop must fence the currently owned identity/runtime first.
			requestGeneration = value.EnvironmentGeneration
			if requestGeneration == 0 {
				requestGeneration = generation
			}
		}
		request := slot.ProvisionRequest{SlotID: value.SlotID, PoolID: value.PoolID, Ordinal: value.Ordinal, Owner: r.owner, EnvironmentGeneration: requestGeneration, PackageGeneration: packageGeneration, Requirement: pool.Requirement()}
		if value.Ordinal > targetSlots {
			if leased {
				if value.Status != slot.Draining {
					if err := r.store.SetSlotStatus(value.SlotID, slot.Draining, r.clock().UTC()); err != nil && first == nil {
						first = err
					}
				}
				continue
			}
			if value.Status != slot.Retiring {
				if err := r.store.SetSlotStatus(value.SlotID, slot.Retiring, r.clock().UTC()); err != nil {
					if first == nil {
						first = err
					}
					continue
				}
			}
			cleanupCtx, cancel := context.WithTimeout(ctx, r.cleanupTimeout)
			err := r.provisioner.Retire(cleanupCtx, request)
			cancel()
			if err != nil {
				_ = r.store.SetSlotStatus(value.SlotID, slot.Quarantined, r.clock().UTC())
				if first == nil {
					first = errors.New("slotlifecycle: cleanup failed")
				}
				continue
			}
			if err := r.store.SetSlotStatus(value.SlotID, slot.Deleted, r.clock().UTC()); err != nil && first == nil {
				first = err
			}
			continue
		}
		// Quarantine is an explicit operator/recovery boundary. Keep the slot
		// isolated until a separate action moves it back to provisioning.
		if value.Status == slot.Quarantined {
			continue
		}
		if value.SessionDesiredState == "stopped" || value.Status == slot.Stopped {
			if leased {
				// A stop intent drains an active lease but must not tear down its
				// runtime while work still owns the slot. Cleanup resumes after
				// the exact lease is released.
				if value.Status != slot.Draining {
					if err := r.store.SetSlotStatus(value.SlotID, slot.Draining, r.clock().UTC()); err != nil && first == nil {
						first = err
					}
				}
				continue
			}
			// Manual Stop is durable intent. Fence any existing runtime before
			// leaving the logical slot stopped, while retaining its identity and
			// Profile for an explicit later Start. A missing optional hook is safe
			// for logical slots; native provisioners must implement it.
			if stopper, ok := r.provisioner.(SessionStopper); ok {
				stopCtx, cancel := context.WithTimeout(ctx, r.cleanupTimeout)
				stopErr := stopper.StopSession(stopCtx, request)
				cancel()
				if stopErr != nil {
					if first == nil {
						first = errors.New("slotlifecycle: session stop failed")
					}
					continue
				}
			}
			continue
		}
		if value.Status == slot.Provisioning && value.FailureCount > 0 && now.Before(value.UpdatedAt.Add(prewarmRetryDelay(value.FailureCount))) {
			continue
		}
		if leased {
			// An active lease keeps running the old environment until its owner
			// releases it. Drain it before health checks so no new work can
			// select a slot whose target no longer matches the pool.
			if environmentChanged && value.Status != slot.Draining {
				if generationRevoker, ok := r.revoker.(GenerationRevoker); ok {
					if revokeErr := generationRevoker.RevokeGeneration(context.Background(), value.SlotID, value.EnvironmentGeneration); revokeErr != nil && first == nil {
						first = errors.New("slotlifecycle: generation capability revocation failed")
					}
				}
				if err := r.store.SetSlotStatus(value.SlotID, slot.Draining, r.clock().UTC()); err != nil {
					if first == nil {
						first = err
					}
				} else {
					value.Status = slot.Draining
				}
			}
			if lease.SlotID != value.SlotID || lease.EnvironmentGeneration != value.EnvironmentGeneration || (value.Status != slot.Leased && value.Status != slot.Draining) {
				if first == nil {
					first = ErrLeaseMismatch
				}
			}
			// A leased slot keeps its old generation isolated until the owner
			// releases it. Do not probe it with the new pool requirement: that
			// would turn a valid drain into a false health failure or quarantine.
			if environmentChanged {
				continue
			}
			if checker, ok := r.provisioner.(LeaseHealthChecker); ok {
				healthCtx, cancel := context.WithTimeout(ctx, r.provisionTimeout)
				healthErr := checker.Health(healthCtx, slot.ProvisionRequest{SlotID: value.SlotID, PoolID: value.PoolID, Ordinal: value.Ordinal, Owner: r.owner, EnvironmentGeneration: value.EnvironmentGeneration, PackageGeneration: packageGeneration, Requirement: pool.Requirement()}, lease)
				cancel()
				if healthErr != nil {
					// A failed health probe means the leased runtime no longer has
					// independently proven Agent/worker readiness. Revoke the exact
					// lease and quarantine immediately, including retryable session
					// disconnects; leaving a live lease would keep unavailable work
					// schedulable until a later heartbeat.
					if r.revoker != nil {
						if revokeErr := r.revoker.RevokeSlotLease(context.Background(), lease.LeaseID); revokeErr != nil {
							if first == nil {
								first = errors.New("slotlifecycle: capability revocation failed")
							}
							continue
						}
					}
					if quarantine, quarantineOK := r.store.(SlotLeaseQuarantiner); quarantineOK {
						if quarantineErr := quarantine.QuarantineSlotLease(value.SlotID, lease.LeaseID, lease.Owner, r.clock().UTC(), "agent health failure"); quarantineErr != nil && first == nil {
							first = errors.New("slotlifecycle: slot quarantine failed")
						} else if first == nil {
							first = errors.New("slotlifecycle: agent health failed")
						}
					} else if first == nil {
						first = errors.New("slotlifecycle: agent health failed")
					}
				}
			}
			continue
		}
		if value.Status == slot.Leased || value.Status == slot.Draining {
			if first == nil {
				first = ErrLeaseMismatch
			}
			continue
		}
		if value.Status == slot.Ready {
			healthCtx, cancel := context.WithTimeout(ctx, r.provisionTimeout)
			summary, inspectErr := r.provisioner.Inspect(healthCtx, request)
			cancel()
			if !environmentChanged && inspectErr == nil && matches(summary, pool, value.EnvironmentGeneration) {
				continue
			}
		}
		if value.Status != slot.Provisioning {
			if err := r.store.SetSlotStatus(value.SlotID, slot.Provisioning, r.clock().UTC()); err != nil {
				if first == nil {
					first = err
				}
				continue
			}
			// Keep the local projection aligned with the durable transition.
			// Retry bookkeeping below may persist this value again; retaining a
			// stale Ready status would bypass the backoff fence on the next pass.
			value.Status = slot.Provisioning
			value.AgentHandle = ""
			value.HealthAt = time.Time{}
		}
		if environmentChanged || value.EnvironmentGeneration == 0 {
			if updater, ok := r.store.(slotTargetUpdater); ok {
				value.Status = slot.Provisioning
				value.EnvironmentID = pool.EnvironmentID
				value.EnvironmentVersion = pool.EnvironmentVersion
				value.EnvironmentGeneration = generation
				value.Capabilities = append([]string(nil), pool.Capabilities...)
				value.ManifestDigest = pool.ManifestDigest
				value.Signer = pool.Signer
				value.Trusted = false
				value.RequireTrusted = pool.RequireTrusted
				value.AgentHandle = ""
				value.HealthAt = time.Time{}
				value.FailureCount = 0
				value.UpdatedAt = r.clock().UTC()
				if err := updater.UpsertSlot(value); err != nil {
					if first == nil {
						first = err
					}
					continue
				}
			}
		}
		provisionCtx, cancel := context.WithTimeout(ctx, r.provisionTimeout)
		result, provisionErr := r.provisioner.Provision(provisionCtx, request)
		cancel()
		cleanupRuntime := func() error {
			stopper, ok := r.provisioner.(SessionStopper)
			if !ok {
				return nil
			}
			cleanupCtx, cleanupCancel := context.WithTimeout(ctx, r.cleanupTimeout)
			err := stopper.StopSession(cleanupCtx, request)
			cleanupCancel()
			return err
		}
		if provisionErr != nil || result.AgentHandle == "" || !matches(result.Summary, pool, generation) {
			// Provision may have created a session before returning an error or an
			// incomplete summary. Fence that runtime before leaving the slot in a
			// retryable or quarantined state; a status projection alone cannot
			// prove that native resources are gone.
			cleanupErr := cleanupRuntime()
			if provisionErr != nil {
				if cleanupErr != nil || func() bool {
					retryable, ok := provisionErr.(interface{ Retryable() bool })
					return !ok || !retryable.Retryable()
				}() {
					_ = r.store.SetSlotStatus(value.SlotID, slot.Quarantined, r.clock().UTC())
				} else {
					r.recordPrewarmFailure(value)
				}
			} else {
				_ = r.store.SetSlotStatus(value.SlotID, slot.Quarantined, r.clock().UTC())
			}
			if first == nil {
				first = errors.New("slotlifecycle: provisioning unavailable")
			}
			continue
		}
		// Provisioning establishes the runtime, but Inspect is the health gate.
		// A successful process start must not become schedulable until the
		// provisioner reports the same generation and trusted environment.
		inspectCtx, inspectCancel := context.WithTimeout(ctx, r.provisionTimeout)
		inspected, inspectErr := r.provisioner.Inspect(inspectCtx, request)
		inspectCancel()
		if inspectErr != nil || !matches(inspected, pool, generation) {
			cleanupErr := cleanupRuntime()
			if cleanupErr != nil || inspectErr == nil || func() bool {
				retryable, ok := inspectErr.(interface{ Retryable() bool })
				return !ok || !retryable.Retryable()
			}() {
				_ = r.store.SetSlotStatus(value.SlotID, slot.Quarantined, r.clock().UTC())
			} else {
				r.recordPrewarmFailure(value)
			}
			if first == nil {
				first = errors.New("slotlifecycle: health check failed")
			}
			continue
		}
		if inspected.AgentHandle == "" {
			inspected.AgentHandle = result.AgentHandle
		}
		inspected.UpdatedAt = r.clock().UTC()
		if err := r.store.MarkSlotReady(value.SlotID, inspected, inspected.UpdatedAt); err != nil {
			// A durable Stop may win after Inspect but before readiness is
			// published. Once the store rejects that late Ready fact, fence the
			// runtime created by this pass before returning. Cleanup is limited to
			// the explicit stopped intent so unrelated persistence failures retain
			// their existing recovery behavior.
			currentStopped := false
			if latest, listErr := r.store.ListSlots(pool.PoolID); listErr == nil {
				for _, current := range latest {
					if current.SlotID == value.SlotID {
						currentStopped = current.SessionDesiredState == "stopped" || current.Status == slot.Stopped
						break
					}
				}
			}
			if currentStopped {
				if cleanupErr := cleanupRuntime(); cleanupErr != nil {
					_ = r.store.SetSlotStatus(value.SlotID, slot.Quarantined, r.clock().UTC())
					if first == nil {
						first = errors.New("slotlifecycle: session stop failed")
					}
					continue
				}
			}
			if first == nil {
				first = err
			}
		}
	}
	return first
}

func prewarmRetryDelay(failures int) time.Duration {
	if failures <= 0 {
		return 0
	}
	if failures > 8 {
		failures = 8
	}
	delay := prewarmRetryBase
	for attempt := 1; attempt < failures && delay < prewarmRetryMax; attempt++ {
		delay *= 2
	}
	if delay > prewarmRetryMax {
		return prewarmRetryMax
	}
	return delay
}

func (r *Reconciler) recordPrewarmFailure(value slot.Slot) {
	updater, ok := r.store.(slotTargetUpdater)
	if !ok {
		return
	}
	if value.FailureCount < prewarmMaxAttempts {
		value.FailureCount++
	}
	if value.FailureCount >= prewarmMaxAttempts {
		value.Status = slot.Quarantined
	}
	value.UpdatedAt = r.clock().UTC()
	_ = updater.UpsertSlot(value)
}

// Shutdown fences transient OS processes without changing durable slot state.
// Provisioners that do not own processes can safely omit the optional hook.
func (r *Reconciler) Shutdown(ctx context.Context) error {
	if r == nil || ctx == nil {
		return ErrInvalidConfig
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if shutdowner, ok := r.provisioner.(Shutdowner); ok {
		return shutdowner.Shutdown(ctx)
	}
	return nil
}

func matches(summary slot.EnvironmentSummary, pool slot.PoolConfig, generation uint64) bool {
	if summary.Validate() != nil || summary.Generation == 0 || (generation != 0 && summary.Generation != generation) ||
		summary.EnvironmentID != pool.EnvironmentID || summary.Version != pool.EnvironmentVersion || !summary.Trusted {
		return false
	}
	if summary.ManifestDigest != pool.ManifestDigest || summary.Signer != pool.Signer {
		return false
	}
	available := make(map[string]struct{}, len(summary.Capabilities))
	for _, capability := range summary.Capabilities {
		available[capability] = struct{}{}
	}
	for _, required := range pool.Capabilities {
		if _, ok := available[required]; !ok {
			return false
		}
	}
	return summary.SessionState == "ready" && summary.DesktopReady && summary.WorkerReady && summary.AgentVersion != ""
}

func containsAll(available, required []string) bool {
	set := make(map[string]struct{}, len(available))
	for _, value := range available {
		set[value] = struct{}{}
	}
	for _, value := range required {
		if _, ok := set[value]; !ok {
			return false
		}
	}
	return true
}

func matchesSlotTarget(value slot.Slot, pool slot.PoolConfig) bool {
	if value.EnvironmentID != pool.EnvironmentID || value.EnvironmentVersion != pool.EnvironmentVersion ||
		value.ManifestDigest != pool.ManifestDigest || value.Signer != pool.Signer || value.RequireTrusted != pool.RequireTrusted {
		return false
	}
	if pool.RequireTrusted && !value.Trusted && (value.Status == slot.Ready || value.Status == slot.Leased || value.Status == slot.Draining) {
		return false
	}
	available := make(map[string]struct{}, len(value.Capabilities))
	for _, capability := range value.Capabilities {
		available[capability] = struct{}{}
	}
	for _, capability := range pool.Capabilities {
		if _, ok := available[capability]; !ok {
			return false
		}
	}
	return true
}
