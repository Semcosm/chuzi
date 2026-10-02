package store

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/internal/slot"
	"github.com/Semcosm/chuzi/migrations"
	"go.etcd.io/bbolt"
)

// SlotClaim is the atomic result of claiming one account request and one
// matching execution slot.
type SlotClaim struct {
	Claim     Claim
	SlotLease slot.Lease
	Slot      slot.Slot
}

// ClaimNextWithSlot keeps account and execution-resource concurrency in one
// bbolt transaction. It never marks a request as credential-failed when no
// matching slot is ready.
func (s *Store) ClaimNextWithSlot(now time.Time, leaseID, owner string, ttl time.Duration, eventID, actor, reason string, options QueueOptions, poolID string, requirement slot.EnvironmentRequirement) (SlotClaim, error) {
	if now.IsZero() || strings.TrimSpace(leaseID) == "" || strings.TrimSpace(owner) == "" || ttl <= 0 || strings.TrimSpace(eventID) == "" || strings.TrimSpace(actor) == "" || strings.TrimSpace(reason) == "" || strings.TrimSpace(poolID) == "" || options.MaxGlobalConcurrency < 1 {
		return SlotClaim{}, ErrInvalidQueueOptions
	}
	var result SlotClaim
	err := s.update(func(tx *bbolt.Tx) error {
		if existing, found, err := existingClaimTx(tx, now, eventID, leaseID, owner, actor, reason); err != nil {
			return err
		} else if found {
			lease, value, err := slotLeaseForRequestTx(tx, existing.Request.RequestID, existing.Request.AccountID, owner)
			if err != nil {
				return err
			}
			result = SlotClaim{Claim: existing, SlotLease: lease, Slot: value}
			return nil
		}
		activeLeases, err := activeLeaseCountTx(tx, now)
		if err != nil {
			return err
		}
		if activeLeases >= options.MaxGlobalConcurrency {
			return ErrQueueCapacity
		}
		var pool slot.PoolConfig
		if err := getJSON(tx.Bucket([]byte(migrations.JobPoolsBucket)), poolID, &pool, slot.ErrPoolNotFound); err != nil {
			return err
		}
		if err := validatePoolEnvironmentTx(tx, pool); err != nil {
			return err
		}
		requirement, err = constrainSlotRequirement(requirement, pool)
		if err != nil {
			return err
		}
		queued, err := queuedRequestsTx(tx)
		if err != nil {
			return err
		}
		if len(queued) == 0 {
			return ErrQueueEmpty
		}
		candidates, err := slotsForPoolTx(tx, poolID)
		if err != nil {
			return err
		}
		leases := tx.Bucket([]byte(migrations.SlotLeasesBucket))
		for index := range candidates {
			candidate := &candidates[index]
			raw := leases.Get([]byte(candidate.SlotID))
			if raw == nil {
				if candidate.Status == slot.Leased || candidate.Status == slot.Draining {
					status := slot.Ready
					if candidate.Ordinal > pool.DesiredSlots {
						status = slot.Retiring
					} else if !slotMatchesPoolTarget(*candidate, pool) {
						status = slot.Provisioning
					}
					candidate.Status, candidate.UpdatedAt = status, now.UTC()
					if err := putJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), candidate.SlotID, *candidate); err != nil {
						return err
					}
				}
				continue
			}
			var current slot.Lease
			if err := decode(raw, &current); err != nil {
				return err
			}
			if err := current.Validate(); err != nil {
				return fmt.Errorf("%w: invalid slot lease", ErrCorruptData)
			}
			if current.Expired(now) {
				if err := leases.Delete([]byte(candidate.SlotID)); err != nil {
					return err
				}
				if candidate.Status == slot.Leased || candidate.Status == slot.Draining {
					status := slot.Ready
					if candidate.Ordinal > pool.DesiredSlots {
						status = slot.Retiring
					} else if !slotMatchesPoolTarget(*candidate, pool) {
						status = slot.Provisioning
					}
					candidate.Status, candidate.UpdatedAt = status, now.UTC()
					if err := putJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), candidate.SlotID, *candidate); err != nil {
						return err
					}
				}
			}
		}

		for _, request := range queued {
			if request.NotBefore.After(now) || (!request.Deadline.IsZero() && !now.Before(request.Deadline)) {
				continue
			}
			state, err := snapshotFromTx(tx, request.AccountID)
			if err != nil {
				return err
			}
			if state.Status != account.Queued || state.RequestID != request.RequestID {
				return fmt.Errorf("%w: queued request %q does not match account state", ErrRequestStateMismatch, request.RequestID)
			}
			var selected *slot.Slot
			for index := range candidates {
				candidate := &candidates[index]
				if candidate.Matches(requirement) {
					selected = candidate
					break
				}
			}
			if selected == nil {
				continue
			}
			accountLease, err := account.AcquireLease(nil, now, leaseID, owner, ttl)
			if current, exists, err := leaseFromTx(tx, request.AccountID); err != nil {
				return err
			} else if exists {
				accountLease, err = account.AcquireLease(&current, now, leaseID, owner, ttl)
				if err != nil {
					if errors.Is(err, account.ErrLeaseHeld) {
						continue
					}
					return err
				}
			}
			slotLeaseID := leaseID + "-slot"
			slotLease, err := slot.AcquireLease(nil, now, slotLeaseID, selected.SlotID, poolID, request.RequestID, request.AccountID, owner, selected.EnvironmentGeneration, ttl)
			if err != nil {
				if errors.Is(err, slot.ErrLeaseHeld) {
					continue
				}
				return err
			}
			event := account.Event{EventID: eventID, AccountID: request.AccountID, RequestID: request.RequestID, From: account.Queued, ExpectedRevision: state.Revision, To: account.Starting, Reason: reason, Actor: actor, OccurredAt: now}
			transition, err := applyEventTx(tx, event)
			if err != nil {
				return err
			}
			updated, err := requestFromTx(tx, request.RequestID)
			if err != nil {
				return err
			}
			if updated.Attempt == int(^uint(0)>>1) {
				return ErrRequestAttemptOverflow
			}
			updated.Attempt++
			updated.NotBefore = time.Time{}
			updated.UpdatedAt = now
			if err := updated.Validate(); err != nil {
				return fmt.Errorf("%w: claimed request: %v", ErrCorruptData, err)
			}
			if err := putRequest(tx.Bucket([]byte(migrations.RequestsBucket)), updated); err != nil {
				return err
			}
			if err := putLease(tx.Bucket([]byte(migrations.LeasesBucket)), request.AccountID, accountLease); err != nil {
				return err
			}
			selected.Status, selected.UpdatedAt = slot.Leased, now.UTC()
			if err := putJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), selected.SlotID, *selected); err != nil {
				return err
			}
			if err := putJSON(leases, selected.SlotID, slotLease); err != nil {
				return err
			}
			result = SlotClaim{Claim: Claim{Request: updated, Lease: accountLease, Transition: transition}, SlotLease: slotLease, Slot: *selected}
			return nil
		}
		return slot.ErrSlotUnavailable
	})
	return result, err
}

func slotLeaseForRequestTx(tx *bbolt.Tx, requestID, accountID, owner string) (slot.Lease, slot.Slot, error) {
	var result slot.Lease
	var value slot.Slot
	err := tx.Bucket([]byte(migrations.SlotLeasesBucket)).ForEach(func(key, raw []byte) error {
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
		if lease.RequestID == requestID && lease.AccountID == accountID && lease.Owner == owner {
			result = lease
			return getJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), string(key), &value, slot.ErrSlotNotFound)
		}
		return nil
	})
	if err != nil {
		return slot.Lease{}, slot.Slot{}, err
	}
	if result.LeaseID == "" {
		return slot.Lease{}, slot.Slot{}, fmt.Errorf("%w: slot lease is missing", ErrCorruptData)
	}
	return result, value, nil
}
