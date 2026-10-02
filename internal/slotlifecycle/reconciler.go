// Package slotlifecycle connects the durable logical slot pool to an OS
// provisioner without moving lease or business-state ownership out of store.
package slotlifecycle

import (
	"context"
	"errors"
	"time"

	"github.com/Semcosm/chuzi/internal/slot"
)

var (
	ErrInvalidConfig = errors.New("slotlifecycle: invalid configuration")
	ErrLeaseMismatch = errors.New("slotlifecycle: slot has an inconsistent lease")
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

type Reconciler struct {
	store            Store
	provisioner      Provisioner
	pool             slot.PoolConfig
	clock            func() time.Time
	provisionTimeout time.Duration
	cleanupTimeout   time.Duration
	revoker          CapabilityRevoker
}

func New(database Store, provisioner Provisioner, pool slot.PoolConfig, clock func() time.Time, provisionTimeout, cleanupTimeout time.Duration, revokers ...CapabilityRevoker) (*Reconciler, error) {
	if database == nil || provisioner == nil || clock == nil || pool.Validate() != nil || provisionTimeout <= 0 || cleanupTimeout <= 0 {
		return nil, ErrInvalidConfig
	}
	var revoker CapabilityRevoker
	if len(revokers) > 1 {
		return nil, ErrInvalidConfig
	}
	if len(revokers) == 1 {
		revoker = revokers[0]
	}
	return &Reconciler{store: database, provisioner: provisioner, pool: pool, clock: clock, provisionTimeout: provisionTimeout, cleanupTimeout: cleanupTimeout, revoker: revoker}, nil
}

// Reconcile processes one pass. Failures are returned as a stable category;
// callers must not log the provisioner's underlying OS error text.
func (r *Reconciler) Reconcile(ctx context.Context) error {
	if r == nil || ctx == nil {
		return ErrInvalidConfig
	}
	slots, err := r.store.ListSlots(r.pool.PoolID)
	if err != nil {
		return err
	}
	var first error
	for _, value := range slots {
		if err := ctx.Err(); err != nil {
			return err
		}
		if value.Status == slot.Deleted {
			continue
		}
		lease, leased, err := r.store.GetSlotLease(value.SlotID)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		environmentChanged := !matchesSlotTarget(value, r.pool)
		generation := value.EnvironmentGeneration
		if generation == 0 {
			// A newly created logical slot has no runtime generation yet. The
			// first provision is generation one.
			generation = 1
		} else if environmentChanged && value.Ordinal <= r.pool.DesiredSlots {
			if generation == ^uint64(0) {
				if first == nil {
					first = errors.New("slotlifecycle: environment generation exhausted")
				}
				continue
			}
			generation++
		}
		request := slot.ProvisionRequest{SlotID: value.SlotID, PoolID: value.PoolID, Ordinal: value.Ordinal, EnvironmentGeneration: generation, Requirement: r.pool.Requirement()}
		if value.Ordinal > r.pool.DesiredSlots {
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
		if leased {
			// An active lease keeps running the old environment until its owner
			// releases it. Drain it before health checks so no new work can
			// select a slot whose target no longer matches the pool.
			if environmentChanged && value.Status != slot.Draining {
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
			if checker, ok := r.provisioner.(LeaseHealthChecker); ok {
				healthCtx, cancel := context.WithTimeout(ctx, r.provisionTimeout)
				healthErr := checker.Health(healthCtx, slot.ProvisionRequest{SlotID: value.SlotID, PoolID: value.PoolID, Ordinal: value.Ordinal, EnvironmentGeneration: value.EnvironmentGeneration, Requirement: r.pool.Requirement()}, lease)
				cancel()
				if healthErr != nil {
					if retryable, retryableOK := healthErr.(interface{ Retryable() bool }); retryableOK && retryable.Retryable() {
						if first == nil {
							first = errors.New("slotlifecycle: session temporarily unavailable")
						}
						continue
					}
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
			if !environmentChanged && inspectErr == nil && matches(summary, r.pool, value.EnvironmentGeneration) {
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
		}
		if environmentChanged || value.EnvironmentGeneration == 0 {
			if updater, ok := r.store.(slotTargetUpdater); ok {
				value.Status = slot.Provisioning
				value.EnvironmentID = r.pool.EnvironmentID
				value.EnvironmentVersion = r.pool.EnvironmentVersion
				value.EnvironmentGeneration = generation
				value.Capabilities = append([]string(nil), r.pool.Capabilities...)
				value.ManifestDigest = r.pool.ManifestDigest
				value.Signer = r.pool.Signer
				value.Trusted = false
				value.AgentHandle = ""
				value.HealthAt = time.Time{}
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
		if provisionErr != nil || result.AgentHandle == "" || !matches(result.Summary, r.pool, generation) {
			// A transient session boundary (for example an RDP disconnect)
			// remains provisioning so the next pass can retry. Runtime,
			// identity, and ACL failures isolate the slot before it can be
			// selected for a lease.
			if provisionErr != nil {
				if retryable, ok := provisionErr.(interface{ Retryable() bool }); !ok || !retryable.Retryable() {
					_ = r.store.SetSlotStatus(value.SlotID, slot.Quarantined, r.clock().UTC())
				}
			} else {
				_ = r.store.SetSlotStatus(value.SlotID, slot.Quarantined, r.clock().UTC())
			}
			if first == nil {
				first = errors.New("slotlifecycle: provisioning unavailable")
			}
			continue
		}
		result.Summary.UpdatedAt = r.clock().UTC()
		if err := r.store.MarkSlotReady(value.SlotID, result.Summary, result.Summary.UpdatedAt); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Shutdown fences transient OS processes without changing durable slot state.
// Provisioners that do not own processes can safely omit the optional hook.
func (r *Reconciler) Shutdown(ctx context.Context) error {
	if r == nil || ctx == nil {
		return ErrInvalidConfig
	}
	if shutdowner, ok := r.provisioner.(Shutdowner); ok {
		return shutdowner.Shutdown(ctx)
	}
	return nil
}

func matches(summary slot.EnvironmentSummary, pool slot.PoolConfig, generation uint64) bool {
	if summary.Validate() != nil || summary.Generation == 0 || (generation != 0 && summary.Generation != generation) ||
		summary.EnvironmentID != pool.EnvironmentID || summary.Version != pool.EnvironmentVersion || !summary.Trusted && pool.RequireTrusted {
		return false
	}
	if pool.ManifestDigest != "" && summary.ManifestDigest != pool.ManifestDigest || pool.Signer != "" && summary.Signer != pool.Signer {
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
	return summary.SessionState == "ready" && summary.DesktopReady && summary.AgentVersion != ""
}

func matchesSlotTarget(value slot.Slot, pool slot.PoolConfig) bool {
	if value.EnvironmentID != pool.EnvironmentID || value.EnvironmentVersion != pool.EnvironmentVersion ||
		value.ManifestDigest != pool.ManifestDigest || value.Signer != pool.Signer {
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
