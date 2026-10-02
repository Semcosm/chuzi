package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Semcosm/chuzi/internal/slot"
	"github.com/Semcosm/chuzi/migrations"
	"go.etcd.io/bbolt"
)

// SlotLeaseRecord associates a persisted slot lease with its slot.
type SlotLeaseRecord struct {
	SlotID string     `json:"slot_id"`
	Lease  slot.Lease `json:"lease"`
}

// ReconcileJobPool persists desired capacity and creates only logical slot
// records. It never creates or deletes operating-system users or resources.
func (s *Store) ReconcileJobPool(config slot.PoolConfig, now time.Time) error {
	if now.IsZero() {
		return ErrInvalidQueueOptions
	}
	config.UpdatedAt = now.UTC()
	if err := config.Validate(); err != nil {
		return err
	}
	return s.update(func(tx *bbolt.Tx) error {
		if err := putJSON(tx.Bucket([]byte(migrations.JobPoolsBucket)), config.PoolID, config); err != nil {
			return err
		}
		slots, err := slotsForPoolTx(tx, config.PoolID)
		if err != nil {
			return err
		}
		byOrdinal := make(map[int]slot.Slot, len(slots))
		for _, current := range slots {
			byOrdinal[current.Ordinal] = current
		}
		for ordinal := 1; ordinal <= config.DesiredSlots; ordinal++ {
			current, ok := byOrdinal[ordinal]
			if ok {
				if current.Status == slot.Retiring {
					if current.EnvironmentGeneration == 0 {
						current.Status = slot.Unprovisioned
					} else {
						current.Status = slot.Ready
					}
					current.UpdatedAt = now.UTC()
					if err := putJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), current.SlotID, current); err != nil {
						return err
					}
				}
				continue
			}
			created := slot.Slot{SlotID: fmt.Sprintf("%s-%03d", config.PoolID, ordinal), Ordinal: ordinal, PoolID: config.PoolID, EnvironmentID: config.EnvironmentID, EnvironmentVersion: config.EnvironmentVersion, Capabilities: append([]string(nil), config.Capabilities...), ManifestDigest: config.ManifestDigest, Signer: config.Signer, Status: slot.Unprovisioned, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
			if err := putJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), created.SlotID, created); err != nil {
				return err
			}
		}
		leaseBucket := tx.Bucket([]byte(migrations.SlotLeasesBucket))
		for _, current := range slots {
			if current.Ordinal <= config.DesiredSlots || current.Status == slot.Deleted {
				continue
			}
			if (current.Status == slot.Leased || current.Status == slot.Draining) && leaseBucket.Get([]byte(current.SlotID)) != nil {
				current.Status = slot.Draining
			} else {
				current.Status = slot.Retiring
			}
			current.UpdatedAt = now.UTC()
			if err := putJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), current.SlotID, current); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) GetJobPool(poolID string) (slot.PoolConfig, error) {
	var result slot.PoolConfig
	err := s.view(func(tx *bbolt.Tx) error {
		return getJSON(tx.Bucket([]byte(migrations.JobPoolsBucket)), poolID, &result, slot.ErrPoolNotFound)
	})
	return result, err
}

func (s *Store) ListJobPools() ([]slot.PoolConfig, error) {
	result := make([]slot.PoolConfig, 0)
	err := s.view(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(migrations.JobPoolsBucket)).ForEach(func(key, raw []byte) error {
			if raw == nil {
				return nil
			}
			var config slot.PoolConfig
			if err := decode(raw, &config); err != nil {
				return err
			}
			if err := config.Validate(); err != nil {
				return fmt.Errorf("%w: invalid pool %q", ErrCorruptData, string(key))
			}
			result = append(result, config)
			return nil
		})
	})
	sort.Slice(result, func(i, j int) bool { return result[i].PoolID < result[j].PoolID })
	return result, err
}

func (s *Store) UpsertSlot(value slot.Slot) error {
	if err := value.Validate(); err != nil {
		return err
	}
	return s.update(func(tx *bbolt.Tx) error {
		return putJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), value.SlotID, value)
	})
}

func (s *Store) GetSlot(slotID string) (slot.Slot, error) {
	var result slot.Slot
	err := s.view(func(tx *bbolt.Tx) error {
		return getJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), slotID, &result, slot.ErrSlotNotFound)
	})
	return result, err
}

func (s *Store) ListSlots(poolID string) ([]slot.Slot, error) {
	var result []slot.Slot
	err := s.view(func(tx *bbolt.Tx) error {
		var err error
		result, err = slotsForPoolTx(tx, poolID)
		return err
	})
	return result, err
}

func (s *Store) PutEnvironmentSummary(summary slot.EnvironmentSummary) error {
	if err := summary.Validate(); err != nil {
		return err
	}
	return s.update(func(tx *bbolt.Tx) error {
		return putJSON(tx.Bucket([]byte(migrations.EnvironmentSummariesBucket)), summary.EnvironmentID, summary)
	})
}

func (s *Store) GetEnvironmentSummary(environmentID string) (slot.EnvironmentSummary, error) {
	var result slot.EnvironmentSummary
	err := s.view(func(tx *bbolt.Tx) error {
		return getJSON(tx.Bucket([]byte(migrations.EnvironmentSummariesBucket)), environmentID, &result, slot.ErrPoolNotFound)
	})
	return result, err
}

func (s *Store) ListEnvironmentSummaries() ([]slot.EnvironmentSummary, error) {
	result := make([]slot.EnvironmentSummary, 0)
	err := s.view(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(migrations.EnvironmentSummariesBucket)).ForEach(func(key, raw []byte) error {
			if raw == nil {
				return nil
			}
			var summary slot.EnvironmentSummary
			if err := decode(raw, &summary); err != nil {
				return err
			}
			if err := summary.Validate(); err != nil {
				return fmt.Errorf("%w: invalid environment summary", ErrCorruptData)
			}
			result = append(result, summary)
			return nil
		})
	})
	sort.Slice(result, func(i, j int) bool { return result[i].EnvironmentID < result[j].EnvironmentID })
	return result, err
}

// MarkSlotReady attaches a validated environment summary and makes a logical
// slot schedulable. This does not provision a Windows user.
func (s *Store) MarkSlotReady(slotID string, summary slot.EnvironmentSummary, now time.Time) error {
	if now.IsZero() {
		return ErrInvalidQueueOptions
	}
	if err := summary.Validate(); err != nil {
		return err
	}
	return s.update(func(tx *bbolt.Tx) error {
		var value slot.Slot
		if err := getJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), slotID, &value, slot.ErrSlotNotFound); err != nil {
			return err
		}
		if value.Status == slot.Leased || value.Status == slot.Draining {
			return slot.ErrLeaseHeld
		}
		value.EnvironmentID, value.EnvironmentVersion, value.EnvironmentGeneration = summary.EnvironmentID, summary.Version, summary.Generation
		value.Capabilities = append([]string(nil), summary.Capabilities...)
		value.ManifestDigest, value.Signer, value.Trusted = summary.ManifestDigest, summary.Signer, summary.Trusted
		value.AgentHandle = summary.AgentHandle
		value.Status, value.HealthAt, value.UpdatedAt = slot.Ready, now.UTC(), now.UTC()
		if err := putJSON(tx.Bucket([]byte(migrations.EnvironmentSummariesBucket)), summary.EnvironmentID, summary); err != nil {
			return err
		}
		return putJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), value.SlotID, value)
	})
}

func (s *Store) SetSlotStatus(slotID string, status slot.Status, now time.Time) error {
	if !status.Valid() || now.IsZero() {
		return slot.ErrInvalidStatus
	}
	return s.update(func(tx *bbolt.Tx) error {
		var value slot.Slot
		if err := getJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), slotID, &value, slot.ErrSlotNotFound); err != nil {
			return err
		}
		if value.EnvironmentGeneration == 0 && status != slot.Unprovisioned && status != slot.Provisioning && status != slot.Retiring && status != slot.Deleted {
			return slot.ErrInvalidSlot
		}
		raw := tx.Bucket([]byte(migrations.SlotLeasesBucket)).Get([]byte(slotID))
		if raw != nil && status != slot.Leased && status != slot.Draining {
			return slot.ErrLeaseHeld
		}
		if raw == nil && (status == slot.Leased || status == slot.Draining) {
			return ErrLeaseNotFound
		}
		value.Status, value.UpdatedAt = status, now.UTC()
		return putJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), value.SlotID, value)
	})
}

// AcquireSlotLease atomically selects a matching ready slot and persists the
// request-bound lease. Repeating the exact lease identity is idempotent.
func (s *Store) AcquireSlotLease(now time.Time, poolID string, requirement slot.EnvironmentRequirement, requestID, accountID, owner, leaseID string, ttl time.Duration) (slot.Lease, slot.Slot, error) {
	if now.IsZero() || strings.TrimSpace(poolID) == "" || strings.TrimSpace(requestID) == "" || strings.TrimSpace(accountID) == "" || strings.TrimSpace(owner) == "" || strings.TrimSpace(leaseID) == "" || ttl <= 0 {
		return slot.Lease{}, slot.Slot{}, ErrInvalidQueueOptions
	}
	requirement, err := requirement.Normalize()
	if err != nil {
		return slot.Lease{}, slot.Slot{}, err
	}
	var leaseResult slot.Lease
	var slotResult slot.Slot
	err = s.update(func(tx *bbolt.Tx) error {
		var config slot.PoolConfig
		if err := getJSON(tx.Bucket([]byte(migrations.JobPoolsBucket)), poolID, &config, slot.ErrPoolNotFound); err != nil {
			return err
		}
		if requirement.EnvironmentID == "" {
			requirement = config.Requirement()
		}
		all, err := slotsForPoolTx(tx, poolID)
		if err != nil {
			return err
		}
		leases := tx.Bucket([]byte(migrations.SlotLeasesBucket))
		for index := range all {
			current := &all[index]
			var existing slot.Lease
			raw := leases.Get([]byte(current.SlotID))
			if raw == nil {
				continue
			}
			if err := decode(raw, &existing); err != nil {
				return err
			}
			if err := existing.Validate(); err != nil {
				return fmt.Errorf("%w: invalid slot lease", ErrCorruptData)
			}
			if existing.LeaseID == leaseID && existing.RequestID == requestID && existing.AccountID == accountID && existing.Owner == owner && !existing.Expired(now) {
				leaseResult, slotResult = existing, *current
				return nil
			}
			if existing.Expired(now) {
				if err := leases.Delete([]byte(current.SlotID)); err != nil {
					return err
				}
				if current.Status == slot.Leased || current.Status == slot.Draining {
					status := slot.Ready
					if current.Ordinal > config.DesiredSlots {
						status = slot.Retiring
					}
					current.Status, current.UpdatedAt = status, now.UTC()
					if err := putJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), current.SlotID, *current); err != nil {
						return err
					}
				}
			}
		}
		slot.SortSlots(all)
		for _, candidate := range all {
			if !candidate.Matches(requirement) {
				continue
			}
			var current *slot.Lease
			if raw := leases.Get([]byte(candidate.SlotID)); raw != nil {
				var value slot.Lease
				if err := decode(raw, &value); err != nil {
					return err
				}
				current = &value
			}
			created, err := slot.AcquireLease(current, now, leaseID, candidate.SlotID, poolID, requestID, accountID, owner, candidate.EnvironmentGeneration, ttl)
			if err != nil {
				if errors.Is(err, slot.ErrLeaseHeld) {
					continue
				}
				return err
			}
			candidate.Status, candidate.UpdatedAt = slot.Leased, now.UTC()
			if err := putJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), candidate.SlotID, candidate); err != nil {
				return err
			}
			if err := putJSON(leases, candidate.SlotID, created); err != nil {
				return err
			}
			leaseResult, slotResult = created, candidate
			return nil
		}
		return slot.ErrSlotUnavailable
	})
	return leaseResult, slotResult, err
}

func (s *Store) GetSlotLease(slotID string) (slot.Lease, bool, error) {
	var result slot.Lease
	var exists bool
	err := s.view(func(tx *bbolt.Tx) error {
		raw := tx.Bucket([]byte(migrations.SlotLeasesBucket)).Get([]byte(slotID))
		if raw == nil {
			return nil
		}
		exists = true
		if err := decode(raw, &result); err != nil {
			return err
		}
		return result.Validate()
	})
	return result, exists, err
}

func (s *Store) ListSlotLeases() ([]SlotLeaseRecord, error) {
	result := make([]SlotLeaseRecord, 0)
	err := s.view(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(migrations.SlotLeasesBucket)).ForEach(func(key, raw []byte) error {
			if raw == nil {
				return nil
			}
			var lease slot.Lease
			if err := decode(raw, &lease); err != nil {
				return err
			}
			if err := lease.Validate(); err != nil {
				return fmt.Errorf("%w: invalid slot lease", ErrCorruptData)
			}
			result = append(result, SlotLeaseRecord{SlotID: string(key), Lease: lease})
			return nil
		})
	})
	sort.Slice(result, func(i, j int) bool { return result[i].SlotID < result[j].SlotID })
	return result, err
}

func (s *Store) HeartbeatSlotLease(slotID string, now time.Time, leaseID, owner string, ttl time.Duration) (slot.Lease, error) {
	var result slot.Lease
	err := s.update(func(tx *bbolt.Tx) error {
		raw := tx.Bucket([]byte(migrations.SlotLeasesBucket)).Get([]byte(slotID))
		if raw == nil {
			return slot.ErrSlotNotFound
		}
		var current slot.Lease
		if err := decode(raw, &current); err != nil {
			return err
		}
		updated, heartbeatErr := slot.HeartbeatLease(current, now, leaseID, owner, ttl)
		if heartbeatErr != nil {
			return heartbeatErr
		}
		var value slot.Slot
		if err := getJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), slotID, &value, slot.ErrSlotNotFound); err != nil {
			return err
		}
		if value.HealthAt.IsZero() || !now.Before(value.HealthAt) {
			value.HealthAt = now.UTC()
		}
		if !now.Before(value.UpdatedAt) {
			value.UpdatedAt = now.UTC()
		}
		if err := putJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), slotID, value); err != nil {
			return err
		}
		result = updated
		return putJSON(tx.Bucket([]byte(migrations.SlotLeasesBucket)), slotID, updated)
	})
	return result, err
}

// ReleaseSlotLease is idempotent after the same lease has already been
// released, while a stale release cannot clear a newer lease.
func (s *Store) ReleaseSlotLease(slotID, leaseID, owner string) error {
	return s.update(func(tx *bbolt.Tx) error {
		leases, slots := tx.Bucket([]byte(migrations.SlotLeasesBucket)), tx.Bucket([]byte(migrations.ExecutionSlotsBucket))
		raw := leases.Get([]byte(slotID))
		if raw == nil {
			return nil
		}
		var current slot.Lease
		if err := decode(raw, &current); err != nil {
			return err
		}
		if err := slot.ReleaseLease(current, leaseID, owner); err != nil {
			return err
		}
		if err := leases.Delete([]byte(slotID)); err != nil {
			return err
		}
		var value slot.Slot
		if err := getJSON(slots, slotID, &value, slot.ErrSlotNotFound); err != nil {
			return err
		}
		if value.Status == slot.Leased || value.Status == slot.Draining {
			status := slot.Ready
			var config slot.PoolConfig
			if err := getJSON(tx.Bucket([]byte(migrations.JobPoolsBucket)), value.PoolID, &config, slot.ErrPoolNotFound); err != nil {
				return err
			}
			if value.Ordinal > config.DesiredSlots {
				status = slot.Retiring
			}
			value.Status, value.UpdatedAt = status, current.LastHeartbeat
			return putJSON(slots, slotID, value)
		}
		return nil
	})
}

func (s *Store) QuarantineSlot(slotID string, now time.Time, reason string) error {
	if now.IsZero() || strings.TrimSpace(reason) == "" {
		return slot.ErrInvalidStatus
	}
	return s.update(func(tx *bbolt.Tx) error {
		var value slot.Slot
		if err := getJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), slotID, &value, slot.ErrSlotNotFound); err != nil {
			return err
		}
		if value.FailureCount < int(^uint(0)>>1) {
			value.FailureCount++
		}
		leases := tx.Bucket([]byte(migrations.SlotLeasesBucket))
		if raw := leases.Get([]byte(slotID)); raw != nil {
			var lease slot.Lease
			if err := decode(raw, &lease); err != nil {
				return err
			}
			if err := lease.Validate(); err != nil {
				return fmt.Errorf("%w: invalid slot lease", ErrCorruptData)
			}
			if !lease.Expired(now) {
				return slot.ErrLeaseHeld
			}
		}
		value.Status, value.HealthAt, value.UpdatedAt = slot.Quarantined, now.UTC(), now.UTC()
		if err := leases.Delete([]byte(slotID)); err != nil {
			return err
		}
		return putJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), slotID, value)
	})
}

func (s *Store) QuarantineSlotLease(slotID, leaseID, owner string, now time.Time, reason string) error {
	if now.IsZero() || strings.TrimSpace(reason) == "" {
		return slot.ErrInvalidStatus
	}
	return s.update(func(tx *bbolt.Tx) error {
		var current slot.Lease
		raw := tx.Bucket([]byte(migrations.SlotLeasesBucket)).Get([]byte(slotID))
		if raw == nil {
			return slot.ErrSlotNotFound
		}
		if err := decode(raw, &current); err != nil {
			return err
		}
		if current.LeaseID != leaseID || current.Owner != owner {
			return slot.ErrLeaseNotOwned
		}
		var value slot.Slot
		if err := getJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), slotID, &value, slot.ErrSlotNotFound); err != nil {
			return err
		}
		if value.FailureCount < int(^uint(0)>>1) {
			value.FailureCount++
		}
		value.Status, value.HealthAt, value.UpdatedAt = slot.Quarantined, now.UTC(), now.UTC()
		if err := tx.Bucket([]byte(migrations.SlotLeasesBucket)).Delete([]byte(slotID)); err != nil {
			return err
		}
		return putJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), slotID, value)
	})
}

func (s *Store) RecoverExpiredSlotLeases(now time.Time) error {
	if now.IsZero() {
		return ErrInvalidQueueOptions
	}
	return s.update(func(tx *bbolt.Tx) error {
		leases, slots := tx.Bucket([]byte(migrations.SlotLeasesBucket)), tx.Bucket([]byte(migrations.ExecutionSlotsBucket))
		var expired []string
		if err := leases.ForEach(func(key, raw []byte) error {
			if raw == nil {
				return nil
			}
			var lease slot.Lease
			if err := decode(raw, &lease); err != nil {
				return err
			}
			if err := lease.Validate(); err != nil {
				return err
			}
			if lease.Expired(now) {
				expired = append(expired, string(key))
			}
			return nil
		}); err != nil {
			return err
		}
		for _, slotID := range expired {
			if err := leases.Delete([]byte(slotID)); err != nil {
				return err
			}
			var value slot.Slot
			if err := getJSON(slots, slotID, &value, slot.ErrSlotNotFound); err != nil {
				return err
			}
			if value.Status == slot.Leased || value.Status == slot.Draining {
				var config slot.PoolConfig
				if err := getJSON(tx.Bucket([]byte(migrations.JobPoolsBucket)), value.PoolID, &config, slot.ErrPoolNotFound); err != nil {
					return err
				}
				status := slot.Ready
				if value.Ordinal > config.DesiredSlots {
					status = slot.Retiring
				}
				value.Status, value.UpdatedAt = status, now.UTC()
				if err := putJSON(slots, slotID, value); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (s *Store) SlotPoolStatus(poolID string, now time.Time) (slot.StatusCounts, error) {
	if now.IsZero() {
		return slot.StatusCounts{}, ErrInvalidQueueOptions
	}
	// Lease recovery is a scheduler operation because it must revoke any
	// capability bound to an expired lease before the durable record is removed.
	// Status reads remain side-effect free and report the persisted lifecycle.
	config, err := s.GetJobPool(poolID)
	if err != nil {
		return slot.StatusCounts{}, err
	}
	items, err := s.ListSlots(poolID)
	if err != nil {
		return slot.StatusCounts{}, err
	}
	result := slot.StatusCounts{PoolID: poolID, Desired: config.DesiredSlots}
	for _, value := range items {
		switch value.Status {
		case slot.Ready:
			result.Ready++
		case slot.Leased:
			result.Leased++
		case slot.Quarantined:
			result.Quarantined++
		case slot.Draining:
			result.Draining++
		case slot.Provisioning:
			result.Provisioning++
		case slot.Retiring:
			result.Retiring++
		case slot.Unprovisioned:
			result.Unprovisioned++
		}
	}
	return result, nil
}

func (s *Store) EffectiveSlotCapacity(poolID string, maxConcurrency int, now time.Time) (int, error) {
	if maxConcurrency < 1 {
		return 0, ErrInvalidQueueOptions
	}
	status, err := s.SlotPoolStatus(poolID, now)
	if err != nil {
		return 0, err
	}
	if status.Ready < maxConcurrency {
		return status.Ready, nil
	}
	return maxConcurrency, nil
}

func slotsForPoolTx(tx *bbolt.Tx, poolID string) ([]slot.Slot, error) {
	if strings.TrimSpace(poolID) == "" {
		return nil, slot.ErrPoolNotFound
	}
	result := make([]slot.Slot, 0)
	err := tx.Bucket([]byte(migrations.ExecutionSlotsBucket)).ForEach(func(key, raw []byte) error {
		if raw == nil {
			return nil
		}
		var value slot.Slot
		if err := decode(raw, &value); err != nil {
			return err
		}
		if value.PoolID != poolID {
			return nil
		}
		if err := value.Validate(); err != nil {
			return fmt.Errorf("%w: invalid slot %q", ErrCorruptData, string(key))
		}
		result = append(result, value)
		return nil
	})
	slot.SortSlots(result)
	return result, err
}

func getJSON(bucket *bbolt.Bucket, key string, target any, missing error) error {
	if bucket == nil {
		return fmt.Errorf("%w: slot bucket is missing", ErrCorruptData)
	}
	raw := bucket.Get([]byte(key))
	if raw == nil {
		return missing
	}
	return decode(raw, target)
}

func putJSON(bucket *bbolt.Bucket, key string, value any) error {
	if bucket == nil {
		return fmt.Errorf("%w: slot bucket is missing", ErrCorruptData)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return bucket.Put([]byte(key), raw)
}
