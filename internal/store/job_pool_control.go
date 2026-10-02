package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Semcosm/chuzi/internal/environment"
	"github.com/Semcosm/chuzi/internal/slot"
	"github.com/Semcosm/chuzi/migrations"
	"go.etcd.io/bbolt"
)

var (
	ErrJobPoolNotFound            = errors.New("store: job pool not found")
	ErrJobPoolOperationNotFound   = errors.New("store: job pool operation not found")
	ErrJobPoolStaleRevision       = errors.New("store: stale job pool revision")
	ErrJobPoolConflict            = errors.New("store: job pool operation conflict")
	ErrJobPoolIdempotencyConflict = errors.New("store: job pool idempotency conflict")
)

type JobPoolOperationState string

const (
	JobPoolRequested    JobPoolOperationState = "requested"
	JobPoolValidating   JobPoolOperationState = "validating"
	JobPoolDraining     JobPoolOperationState = "draining"
	JobPoolProvisioning JobPoolOperationState = "provisioning"
	JobPoolHealthCheck  JobPoolOperationState = "health_check"
	JobPoolCommitting   JobPoolOperationState = "committing"
	JobPoolApplied      JobPoolOperationState = "applied"
	JobPoolFailed       JobPoolOperationState = "failed"
	JobPoolRolledBack   JobPoolOperationState = "rolled_back"
	JobPoolCancelled    JobPoolOperationState = "cancelled"
)

type JobPoolMutation struct {
	Config           slot.PoolConfig
	Operation        string
	IdempotencyKey   string
	Actor            string
	ExpectedRevision uint64
	RequestedAt      time.Time
}

type JobPoolOperation struct {
	OperationID           string
	PoolID                string
	Operation             string
	State                 JobPoolOperationState
	Actor                 string
	ExpectedRevision      uint64
	ConfigRevision        uint64
	RequestedAt           time.Time
	UpdatedAt             time.Time
	CompletedAt           time.Time
	Result                string
	FailureCode           string
	EnvironmentGeneration uint64
	LastSuccessfulAt      time.Time
	ConfigDigest          string
	// PreviousConfig and PreviousSlots make a failed reconcile reversible.
	// They are internal persistence data and are never projected through Core.
	PreviousConfig *slot.PoolConfig `json:"previous_config,omitempty"`
	PreviousSlots  []slot.Slot      `json:"previous_slots,omitempty"`
}

type JobPoolAuditEvent struct {
	EventID               string
	OperationID           string
	Actor                 string
	PoolID                string
	Operation             string
	FromState             JobPoolOperationState
	ToState               JobPoolOperationState
	ConfigRevision        uint64
	EnvironmentGeneration uint64
	Outcome               string
	FailureCode           string
	OccurredAt            time.Time
}

type JobPoolProjection struct {
	Config                    slot.PoolConfig
	Status                    slot.StatusCounts
	EnvironmentReady          bool
	EnvironmentReadiness      string
	ReconcileState            JobPoolOperationState
	OperationID               string
	LastFailureCode           string
	LastSuccessfulReconcileAt time.Time
	EnvironmentGeneration     uint64
}

func (o JobPoolOperation) Validate() error {
	if strings.TrimSpace(o.OperationID) == "" || strings.TrimSpace(o.PoolID) == "" || strings.TrimSpace(o.Operation) == "" || o.State == "" || o.ConfigRevision == 0 || o.RequestedAt.IsZero() || o.UpdatedAt.IsZero() {
		return ErrCorruptData
	}
	return nil
}

func (e JobPoolAuditEvent) Validate() error {
	if strings.TrimSpace(e.EventID) == "" || strings.TrimSpace(e.OperationID) == "" || strings.TrimSpace(e.PoolID) == "" || strings.TrimSpace(e.Operation) == "" || e.ToState == "" || e.ConfigRevision == 0 || e.OccurredAt.IsZero() {
		return ErrCorruptData
	}
	return nil
}

func (m JobPoolMutation) normalize(now time.Time) (JobPoolMutation, error) {
	m.Config = m.Config.Normalized()
	if m.RequestedAt.IsZero() {
		m.RequestedAt = now.UTC()
	}
	m.RequestedAt = m.RequestedAt.UTC()
	m.Actor = strings.TrimSpace(m.Actor)
	m.IdempotencyKey = strings.TrimSpace(m.IdempotencyKey)
	m.Operation = strings.TrimSpace(m.Operation)
	if m.Operation == "" {
		m.Operation = "apply"
	}
	if m.IdempotencyKey == "" || len(m.IdempotencyKey) > 256 || strings.ContainsAny(m.IdempotencyKey, "\r\n\t") || m.Actor == "" || len(m.Actor) > 256 || strings.ContainsAny(m.Actor, "\r\n\t") {
		return JobPoolMutation{}, ErrInvalidRequest
	}
	if err := m.Config.Validate(); err != nil {
		return JobPoolMutation{}, err
	}
	return m, nil
}

func jobPoolConfigDigest(config slot.PoolConfig) string {
	config = config.Normalized()
	value := fmt.Sprintf("%s|%d|%d|%s|%s|%s|%s|%t|%s|%s", config.PoolID, config.DesiredSlots, config.MaxConcurrency, config.EnvironmentID, config.EnvironmentVersion, config.ManifestDigest, config.Signer, config.RequireTrusted, config.DesiredState, strings.Join(config.Capabilities, ","))
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}

func jobPoolEnvironmentChanged(previous, next slot.PoolConfig) bool {
	return previous.EnvironmentID != next.EnvironmentID || previous.EnvironmentVersion != next.EnvironmentVersion ||
		!strings.EqualFold(previous.ManifestDigest, next.ManifestDigest) || previous.Signer != next.Signer ||
		previous.RequireTrusted != next.RequireTrusted || !containsCapabilities(previous.Capabilities, next.Capabilities) ||
		!containsCapabilities(next.Capabilities, previous.Capabilities)
}

func terminalJobPoolOperation(state JobPoolOperationState) bool {
	return state == JobPoolApplied || state == JobPoolFailed || state == JobPoolRolledBack || state == JobPoolCancelled
}

func (s *Store) ApplyJobPool(mutation JobPoolMutation) (JobPoolOperation, bool, error) {
	now := mutation.RequestedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	mutation, err := mutation.normalize(now)
	if err != nil {
		return JobPoolOperation{}, false, err
	}
	configDigest := jobPoolConfigDigest(mutation.Config)
	var result JobPoolOperation
	var idempotent bool
	err = s.update(func(tx *bbolt.Tx) error {
		idempotency := tx.Bucket([]byte(migrations.JobPoolIdempotencyBucket))
		operations := tx.Bucket([]byte(migrations.JobPoolOperationsBucket))
		if raw := idempotency.Get([]byte(mutation.IdempotencyKey)); raw != nil {
			var existing JobPoolOperation
			if err := decode(operations.Get(raw), &existing); err != nil {
				return fmt.Errorf("%w: invalid job pool operation", ErrCorruptData)
			}
			if existing.PoolID != mutation.Config.PoolID || existing.Operation != mutation.Operation || existing.Actor != mutation.Actor || existing.ExpectedRevision != mutation.ExpectedRevision || (existing.ConfigDigest != "" && existing.ConfigDigest != configDigest) {
				return ErrJobPoolIdempotencyConflict
			}
			result, idempotent = existing, true
			return nil
		}
		var current slot.PoolConfig
		hasCurrent := false
		if raw := tx.Bucket([]byte(migrations.JobPoolsBucket)).Get([]byte(mutation.Config.PoolID)); raw != nil {
			if err := decode(raw, &current); err != nil {
				return fmt.Errorf("%w: invalid job pool", ErrCorruptData)
			}
			current = current.Normalized()
			hasCurrent = true
		}
		currentRevision := uint64(0)
		if hasCurrent {
			currentRevision = current.ConfigRevision
		}
		if mutation.ExpectedRevision != currentRevision {
			return ErrJobPoolStaleRevision
		}
		previousSlots, err := slotsForPoolTx(tx, mutation.Config.PoolID)
		if err != nil {
			return err
		}
		environmentGeneration := environmentGenerationTx(tx, mutation.Config.EnvironmentID, mutation.Config.EnvironmentVersion)
		mutation.Config.ConfigRevision = currentRevision + 1
		mutation.Config.UpdatedAt = mutation.RequestedAt
		mutation.Config.UpdatedBy = mutation.Actor
		if err := putJSON(tx.Bucket([]byte(migrations.JobPoolsBucket)), mutation.Config.PoolID, mutation.Config); err != nil {
			return err
		}
		// Environment identity changes are committed as durable desired state
		// first. ReconcileJobPoolControl validates the new signed environment
		// before touching the old ready generation. This keeps a failed update
		// from making the only usable generation unavailable. Capacity-only and
		// lifecycle changes can update logical slot targets immediately.
		if !hasCurrent || !jobPoolEnvironmentChanged(current, mutation.Config) {
			if err := reconcilePoolSlotsTx(tx, mutation.Config, mutation.RequestedAt); err != nil {
				return err
			}
		}
		operationID := newJobPoolOperationID(mutation, tx)
		var previousConfig *slot.PoolConfig
		if hasCurrent {
			copy := current
			previousConfig = &copy
		}
		result = JobPoolOperation{OperationID: operationID, PoolID: mutation.Config.PoolID, Operation: mutation.Operation, State: JobPoolRequested, Actor: mutation.Actor, ExpectedRevision: mutation.ExpectedRevision, ConfigRevision: mutation.Config.ConfigRevision, EnvironmentGeneration: environmentGeneration, RequestedAt: mutation.RequestedAt, UpdatedAt: mutation.RequestedAt, ConfigDigest: configDigest, PreviousConfig: previousConfig, PreviousSlots: previousSlots}
		if err := putJSON(operations, operationID, result); err != nil {
			return err
		}
		if err := idempotency.Put([]byte(mutation.IdempotencyKey), []byte(operationID)); err != nil {
			return err
		}
		return putJobPoolAuditTx(tx, JobPoolAuditEvent{EventID: "audit_" + operationID, OperationID: operationID, Actor: mutation.Actor, PoolID: mutation.Config.PoolID, Operation: mutation.Operation, ToState: JobPoolRequested, ConfigRevision: mutation.Config.ConfigRevision, EnvironmentGeneration: environmentGeneration, Outcome: "requested", OccurredAt: mutation.RequestedAt})
	})
	return result, idempotent, err
}

func newJobPoolOperationID(m JobPoolMutation, tx *bbolt.Tx) string {
	h := sha256.Sum256([]byte(m.Config.PoolID + "|" + m.Operation + "|" + m.IdempotencyKey + "|" + m.RequestedAt.Format(time.RFC3339Nano)))
	base := "op_" + hex.EncodeToString(h[:8])
	if tx.Bucket([]byte(migrations.JobPoolOperationsBucket)).Get([]byte(base)) == nil {
		return base
	}
	for i := 1; ; i++ {
		candidate := fmt.Sprintf("%s_%d", base, i)
		if tx.Bucket([]byte(migrations.JobPoolOperationsBucket)).Get([]byte(candidate)) == nil {
			return candidate
		}
	}
}

func (s *Store) ScaleJobPool(poolID string, desiredSlots int, expectedRevision uint64, idempotencyKey, actor string, at time.Time) (JobPoolOperation, bool, error) {
	config, err := s.GetJobPool(poolID)
	if err != nil {
		return JobPoolOperation{}, false, err
	}
	config.DesiredSlots = desiredSlots
	return s.ApplyJobPool(JobPoolMutation{Config: config, Operation: "scale", ExpectedRevision: expectedRevision, IdempotencyKey: idempotencyKey, Actor: actor, RequestedAt: at})
}

func (s *Store) DrainJobPool(poolID string, expectedRevision uint64, idempotencyKey, actor string, at time.Time) (JobPoolOperation, bool, error) {
	config, err := s.GetJobPool(poolID)
	if err != nil {
		return JobPoolOperation{}, false, err
	}
	config.DesiredState = "draining"
	return s.ApplyJobPool(JobPoolMutation{Config: config, Operation: "drain", ExpectedRevision: expectedRevision, IdempotencyKey: idempotencyKey, Actor: actor, RequestedAt: at})
}

func (s *Store) ResumeJobPool(poolID string, expectedRevision uint64, idempotencyKey, actor string, at time.Time) (JobPoolOperation, bool, error) {
	config, err := s.GetJobPool(poolID)
	if err != nil {
		return JobPoolOperation{}, false, err
	}
	config.DesiredState = "enabled"
	return s.ApplyJobPool(JobPoolMutation{Config: config, Operation: "resume", ExpectedRevision: expectedRevision, IdempotencyKey: idempotencyKey, Actor: actor, RequestedAt: at})
}

func (s *Store) GetJobPoolOperation(operationID string) (JobPoolOperation, error) {
	var result JobPoolOperation
	err := s.view(func(tx *bbolt.Tx) error {
		if err := getJSON(tx.Bucket([]byte(migrations.JobPoolOperationsBucket)), operationID, &result, ErrJobPoolOperationNotFound); err != nil {
			return err
		}
		return result.Validate()
	})
	return result, err
}

func (s *Store) ListJobPoolOperations(poolID string, limit int) ([]JobPoolOperation, error) {
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 1000 {
		return nil, ErrInvalidRequest
	}
	result := make([]JobPoolOperation, 0)
	err := s.view(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(migrations.JobPoolOperationsBucket)).ForEach(func(_, raw []byte) error {
			if raw == nil {
				return nil
			}
			var value JobPoolOperation
			if err := decode(raw, &value); err != nil {
				return err
			}
			if value.PoolID == poolID {
				result = append(result, value)
			}
			return nil
		})
	})
	sort.Slice(result, func(i, j int) bool { return result[i].RequestedAt.Before(result[j].RequestedAt) })
	if len(result) > limit {
		result = result[len(result)-limit:]
	}
	return result, err
}

func (s *Store) UpdateJobPoolOperation(operationID string, state JobPoolOperationState, result, failureCode string, at time.Time) (JobPoolOperation, error) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	var updated JobPoolOperation
	err := s.update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte(migrations.JobPoolOperationsBucket))
		var current JobPoolOperation
		if err := getJSON(bucket, operationID, &current, ErrJobPoolOperationNotFound); err != nil {
			return err
		}
		if current.State == JobPoolApplied || current.State == JobPoolFailed || current.State == JobPoolRolledBack || current.State == JobPoolCancelled {
			updated = current
			return nil
		}
		previous := current.State
		current.State, current.Result, current.FailureCode, current.UpdatedAt = state, result, failureCode, at.UTC()
		if state == JobPoolApplied || state == JobPoolFailed || state == JobPoolRolledBack || state == JobPoolCancelled {
			current.CompletedAt = at.UTC()
		}
		if state == JobPoolApplied {
			current.LastSuccessfulAt = at.UTC()
		}
		if current.EnvironmentGeneration == 0 {
			var config slot.PoolConfig
			if err := getJSON(tx.Bucket([]byte(migrations.JobPoolsBucket)), current.PoolID, &config, slot.ErrPoolNotFound); err == nil {
				current.EnvironmentGeneration = environmentGenerationTx(tx, config.EnvironmentID, config.EnvironmentVersion)
			}
		}
		if err := putJSON(bucket, operationID, current); err != nil {
			return err
		}
		if err := putJobPoolAuditTx(tx, JobPoolAuditEvent{EventID: nextJobPoolAuditID(tx, operationID, at), OperationID: operationID, Actor: current.Actor, PoolID: current.PoolID, Operation: current.Operation, FromState: previous, ToState: state, ConfigRevision: current.ConfigRevision, EnvironmentGeneration: current.EnvironmentGeneration, Outcome: result, FailureCode: failureCode, OccurredAt: at}); err != nil {
			return err
		}
		updated = current
		return nil
	})
	return updated, err
}

// RollbackJobPoolOperation restores the last committed pool configuration and
// slot generation captured when the operation was requested. It is atomic with
// the terminal audit event and never exposes the snapshot through Core.
func (s *Store) RollbackJobPoolOperation(operationID, failureCode string, at time.Time) (JobPoolOperation, error) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	var updated JobPoolOperation
	err := s.update(func(tx *bbolt.Tx) error {
		operations := tx.Bucket([]byte(migrations.JobPoolOperationsBucket))
		if err := getJSON(operations, operationID, &updated, ErrJobPoolOperationNotFound); err != nil {
			return err
		}
		if terminalJobPoolOperation(updated.State) {
			return nil
		}
		if updated.PreviousConfig == nil {
			if err := tx.Bucket([]byte(migrations.JobPoolsBucket)).Delete([]byte(updated.PoolID)); err != nil {
				return err
			}
		} else if err := putJSON(tx.Bucket([]byte(migrations.JobPoolsBucket)), updated.PoolID, *updated.PreviousConfig); err != nil {
			return err
		}
		previousByID := make(map[string]slot.Slot, len(updated.PreviousSlots))
		for _, value := range updated.PreviousSlots {
			previousByID[value.SlotID] = value
		}
		slots, err := slotsForPoolTx(tx, updated.PoolID)
		if err != nil {
			return err
		}
		leases := tx.Bucket([]byte(migrations.SlotLeasesBucket))
		seen := make(map[string]struct{}, len(slots))
		for _, current := range slots {
			seen[current.SlotID] = struct{}{}
			previous, existed := previousByID[current.SlotID]
			if !existed {
				if leases.Get([]byte(current.SlotID)) != nil {
					current.Status = slot.Draining
					current.UpdatedAt = at.UTC()
					if err := putJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), current.SlotID, current); err != nil {
						return err
					}
					continue
				}
				if err := tx.Bucket([]byte(migrations.ExecutionSlotsBucket)).Delete([]byte(current.SlotID)); err != nil {
					return err
				}
				continue
			}
			if leases.Get([]byte(current.SlotID)) != nil && current.EnvironmentGeneration != previous.EnvironmentGeneration {
				current.Status = slot.Draining
				current.UpdatedAt = at.UTC()
				if err := putJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), current.SlotID, current); err != nil {
					return err
				}
				continue
			}
			previous.UpdatedAt = at.UTC()
			if err := putJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), previous.SlotID, previous); err != nil {
				return err
			}
		}
		for _, previous := range updated.PreviousSlots {
			if _, ok := seen[previous.SlotID]; ok || leases.Get([]byte(previous.SlotID)) != nil {
				continue
			}
			previous.UpdatedAt = at.UTC()
			if err := putJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), previous.SlotID, previous); err != nil {
				return err
			}
		}
		previousState := updated.State
		updated.State, updated.Result, updated.FailureCode, updated.UpdatedAt, updated.CompletedAt = JobPoolRolledBack, "rolled_back", failureCode, at.UTC(), at.UTC()
		if err := putJSON(operations, operationID, updated); err != nil {
			return err
		}
		return putJobPoolAuditTx(tx, JobPoolAuditEvent{EventID: nextJobPoolAuditID(tx, operationID, at), OperationID: operationID, Actor: updated.Actor, PoolID: updated.PoolID, Operation: updated.Operation, FromState: previousState, ToState: JobPoolRolledBack, ConfigRevision: updated.ConfigRevision, EnvironmentGeneration: updated.EnvironmentGeneration, Outcome: "rolled_back", FailureCode: failureCode, OccurredAt: at})
	})
	return updated, err
}

// RecoverStaleJobPoolOperations closes abandoned operations superseded by a
// newer durable config revision. The latest revision remains for reconciliation
// after a process restart.
func (s *Store) RecoverStaleJobPoolOperations(poolID string, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return s.update(func(tx *bbolt.Tx) error {
		var config slot.PoolConfig
		if err := getJSON(tx.Bucket([]byte(migrations.JobPoolsBucket)), poolID, &config, ErrJobPoolNotFound); err != nil {
			return err
		}
		return tx.Bucket([]byte(migrations.JobPoolOperationsBucket)).ForEach(func(key, raw []byte) error {
			if raw == nil {
				return nil
			}
			var operation JobPoolOperation
			if err := decode(raw, &operation); err != nil {
				return err
			}
			if operation.PoolID != poolID || terminalJobPoolOperation(operation.State) || operation.ConfigRevision >= config.ConfigRevision {
				return nil
			}
			previous := operation.State
			operation.State, operation.Result, operation.FailureCode, operation.UpdatedAt, operation.CompletedAt = JobPoolCancelled, "superseded", "superseded", at.UTC(), at.UTC()
			if err := putJSON(tx.Bucket([]byte(migrations.JobPoolOperationsBucket)), string(key), operation); err != nil {
				return err
			}
			return putJobPoolAuditTx(tx, JobPoolAuditEvent{EventID: nextJobPoolAuditID(tx, operation.OperationID, at), OperationID: operation.OperationID, Actor: operation.Actor, PoolID: operation.PoolID, Operation: operation.Operation, FromState: previous, ToState: JobPoolCancelled, ConfigRevision: operation.ConfigRevision, EnvironmentGeneration: operation.EnvironmentGeneration, Outcome: "superseded", FailureCode: "superseded", OccurredAt: at})
		})
	})
}

func environmentGenerationTx(tx *bbolt.Tx, environmentID, version string) uint64 {
	raw := tx.Bucket([]byte(migrations.EnvironmentPackagesBucket)).Get([]byte(environmentKey(environmentID, version)))
	if raw == nil {
		return 0
	}
	var record environment.Record
	if decode(raw, &record) != nil {
		return 0
	}
	return record.Generation
}

func putJobPoolAuditTx(tx *bbolt.Tx, event JobPoolAuditEvent) error {
	if err := event.Validate(); err != nil {
		return err
	}
	return putJSON(tx.Bucket([]byte(migrations.JobPoolAuditBucket)), event.EventID, event)
}

func nextJobPoolAuditID(tx *bbolt.Tx, operationID string, at time.Time) string {
	bucket := tx.Bucket([]byte(migrations.JobPoolAuditBucket))
	base := fmt.Sprintf("audit_%s_%d", operationID, at.UnixNano())
	if bucket.Get([]byte(base)) == nil {
		return base
	}
	for suffix := 1; ; suffix++ {
		candidate := fmt.Sprintf("%s_%d", base, suffix)
		if bucket.Get([]byte(candidate)) == nil {
			return candidate
		}
	}
}

func (s *Store) ListJobPoolAudit(poolID string, limit int) ([]JobPoolAuditEvent, error) {
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 1000 {
		return nil, ErrInvalidRequest
	}
	result := make([]JobPoolAuditEvent, 0)
	err := s.view(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(migrations.JobPoolAuditBucket)).ForEach(func(_, raw []byte) error {
			if raw == nil {
				return nil
			}
			var value JobPoolAuditEvent
			if err := decode(raw, &value); err != nil {
				return err
			}
			if value.PoolID == poolID {
				result = append(result, value)
			}
			return nil
		})
	})
	sort.Slice(result, func(i, j int) bool {
		if result[i].OccurredAt.Equal(result[j].OccurredAt) {
			return result[i].EventID < result[j].EventID
		}
		return result[i].OccurredAt.Before(result[j].OccurredAt)
	})
	if len(result) > limit {
		result = result[len(result)-limit:]
	}
	return result, err
}

func (s *Store) GetJobPoolProjection(poolID string, at time.Time) (JobPoolProjection, error) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	config, err := s.GetJobPool(poolID)
	if err != nil {
		return JobPoolProjection{}, err
	}
	status, err := s.SlotPoolStatus(poolID, at)
	if err != nil {
		return JobPoolProjection{}, err
	}
	ready, readiness, generation := s.environmentReadiness(config)
	operations, err := s.ListJobPoolOperations(poolID, 1000)
	if err != nil {
		return JobPoolProjection{}, err
	}
	var operation JobPoolOperation
	if len(operations) > 0 {
		operation = operations[len(operations)-1]
	}
	return JobPoolProjection{Config: config, Status: status, EnvironmentReady: ready, EnvironmentReadiness: readiness, ReconcileState: operation.State, OperationID: operation.OperationID, LastFailureCode: operation.FailureCode, LastSuccessfulReconcileAt: operation.LastSuccessfulAt, EnvironmentGeneration: generation}, nil
}

func (s *Store) ListJobPoolProjections(at time.Time) ([]JobPoolProjection, error) {
	configs, err := s.ListJobPools()
	if err != nil {
		return nil, err
	}
	result := make([]JobPoolProjection, 0, len(configs))
	for _, config := range configs {
		value, err := s.GetJobPoolProjection(config.PoolID, at)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func (s *Store) environmentReadiness(config slot.PoolConfig) (bool, string, uint64) {
	record, err := s.GetEnvironmentRecord(config.EnvironmentID, config.EnvironmentVersion)
	if err != nil {
		if len(config.ManifestDigest) == 64 && config.Signer != "" {
			return false, "environment_unavailable", 0
		}
		return true, "ready", 0
	}
	if !record.Installed {
		return false, "environment_unavailable", record.Generation
	}
	if !record.Verified {
		return false, "environment_unverified", record.Generation
	}
	if !record.Trusted || (config.RequireTrusted && !record.Trusted) {
		return false, "environment_untrusted", record.Generation
	}
	if !record.Enabled {
		return false, "environment_disabled", record.Generation
	}
	if !record.Healthy {
		return false, "environment_unhealthy", record.Generation
	}
	if !record.IsReady() || !strings.EqualFold(record.ManifestDigest, config.ManifestDigest) || record.Signer != config.Signer {
		return false, "environment_unready", record.Generation
	}
	return true, "ready", record.Generation
}

func reconcilePoolSlotsTx(tx *bbolt.Tx, config slot.PoolConfig, now time.Time) error {
	if config.DesiredState == "" {
		config.DesiredState = "enabled"
	}
	if err := config.Validate(); err != nil {
		return err
	}
	target := config.DesiredSlots
	if config.DesiredState == "draining" || config.DesiredState == "disabled" {
		target = 0
	}
	slots, err := slotsForPoolTx(tx, config.PoolID)
	if err != nil {
		return err
	}
	byOrdinal := make(map[int]slot.Slot, len(slots))
	for _, current := range slots {
		byOrdinal[current.Ordinal] = current
	}
	for ordinal := 1; ordinal <= target; ordinal++ {
		current, ok := byOrdinal[ordinal]
		if !ok {
			created := slot.Slot{SlotID: fmt.Sprintf("%s-%03d", config.PoolID, ordinal), Ordinal: ordinal, PoolID: config.PoolID, EnvironmentID: config.EnvironmentID, EnvironmentVersion: config.EnvironmentVersion, Capabilities: append([]string(nil), config.Capabilities...), ManifestDigest: config.ManifestDigest, Signer: config.Signer, Status: slot.Unprovisioned, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
			if err := putJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), created.SlotID, created); err != nil {
				return err
			}
			continue
		}
		if current.Status == slot.Deleted {
			continue
		}
		targetChanged := current.EnvironmentID != config.EnvironmentID || current.EnvironmentVersion != config.EnvironmentVersion || current.ManifestDigest != config.ManifestDigest || current.Signer != config.Signer || !capabilitiesEqual(current.Capabilities, config.Capabilities)
		if targetChanged {
			if current.Status == slot.Quarantined {
				// Quarantine is an explicit recovery boundary. A desired config
				// update must not silently make the slot schedulable again.
				continue
			}
			if current.Status == slot.Leased || current.Status == slot.Draining {
				current.Status = slot.Draining
			} else {
				current.Status = slot.Provisioning
			}
			current.UpdatedAt = now.UTC()
			if err := putJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), current.SlotID, current); err != nil {
				return err
			}
		}
	}
	leaseBucket := tx.Bucket([]byte(migrations.SlotLeasesBucket))
	for _, current := range slots {
		if current.Ordinal <= target || current.Status == slot.Deleted {
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
}

func (s *Store) ReconcileJobPoolControl(poolID string, at time.Time) (JobPoolProjection, error) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	config, err := s.GetJobPool(poolID)
	if err != nil {
		return JobPoolProjection{}, err
	}
	if err := s.RecoverStaleJobPoolOperations(poolID, at); err != nil && !errors.Is(err, ErrJobPoolNotFound) {
		return JobPoolProjection{}, err
	}
	operations, err := s.ListJobPoolOperations(poolID, 1000)
	if err != nil {
		return JobPoolProjection{}, err
	}
	var operation JobPoolOperation
	if len(operations) > 0 {
		operation = operations[len(operations)-1]
	}
	ready, readiness, _ := s.environmentReadiness(config)
	needsEnvironment := config.DesiredState != "draining" && config.DesiredState != "disabled"
	if !terminalJobPoolOperation(operation.State) && needsEnvironment && !ready {
		updated, rollbackErr := s.RollbackJobPoolOperation(operation.OperationID, readiness, at)
		if rollbackErr != nil {
			return JobPoolProjection{}, rollbackErr
		}
		if updated.PreviousConfig == nil {
			return JobPoolProjection{ReconcileState: updated.State, OperationID: updated.OperationID, LastFailureCode: updated.FailureCode}, nil
		}
		return s.GetJobPoolProjection(poolID, at)
	}
	if !terminalJobPoolOperation(operation.State) {
		if err := s.ReconcileJobPool(config, at); err != nil {
			return JobPoolProjection{}, err
		}
	}
	projection, err := s.GetJobPoolProjection(poolID, at)
	if err != nil {
		return JobPoolProjection{}, err
	}
	if projection.OperationID == "" {
		return projection, nil
	}
	if !terminalJobPoolOperation(operation.State) && projection.Status.Quarantined > 0 {
		updated, rollbackErr := s.RollbackJobPoolOperation(operation.OperationID, "slot_quarantined", at)
		if rollbackErr != nil {
			return JobPoolProjection{}, rollbackErr
		}
		projection, err = s.GetJobPoolProjection(poolID, at)
		if err != nil {
			return JobPoolProjection{}, err
		}
		projection.ReconcileState, projection.LastFailureCode, projection.LastSuccessfulReconcileAt = updated.State, updated.FailureCode, updated.LastSuccessfulAt
		return projection, nil
	}
	// Persist each visible reconcile phase in order. This keeps the operation
	// queryable during a long-running Windows-backed reconcile and leaves an
	// audit trail of the drain -> provision -> health -> commit path.
	updated, err := s.UpdateJobPoolOperation(projection.OperationID, JobPoolValidating, "validating", "", at)
	if err != nil {
		return JobPoolProjection{}, err
	}
	if !projection.EnvironmentReady && config.DesiredState != "disabled" && config.DesiredState != "draining" {
		updated, err = s.UpdateJobPoolOperation(projection.OperationID, JobPoolFailed, "failed", projection.EnvironmentReadiness, at)
	} else if config.DesiredState == "draining" || config.DesiredState == "disabled" {
		updated, err = s.UpdateJobPoolOperation(projection.OperationID, JobPoolDraining, "draining", "", at)
		if err == nil && projection.Status.Leased == 0 && projection.Status.Draining == 0 && projection.Status.Retiring == 0 {
			updated, err = s.UpdateJobPoolOperation(projection.OperationID, JobPoolApplied, "applied", "", at)
		}
	} else {
		updated, err = s.UpdateJobPoolOperation(projection.OperationID, JobPoolProvisioning, "provisioning", "", at)
		if err == nil && projection.EnvironmentReady && projection.Status.Ready >= config.DesiredSlots && projection.Status.Provisioning == 0 && projection.Status.Unprovisioned == 0 && projection.Status.Draining == 0 {
			updated, err = s.UpdateJobPoolOperation(projection.OperationID, JobPoolHealthCheck, "health_check", "", at)
			if err == nil {
				updated, err = s.UpdateJobPoolOperation(projection.OperationID, JobPoolCommitting, "committing", "", at)
			}
			if err == nil {
				updated, err = s.UpdateJobPoolOperation(projection.OperationID, JobPoolApplied, "applied", "", at)
			}
		}
	}
	if err != nil {
		return JobPoolProjection{}, err
	}
	projection.ReconcileState, projection.LastFailureCode, projection.LastSuccessfulReconcileAt = updated.State, updated.FailureCode, updated.LastSuccessfulAt
	return projection, nil
}
