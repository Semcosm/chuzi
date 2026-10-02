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
	"github.com/Semcosm/chuzi/migrations"
	"go.etcd.io/bbolt"
)

var (
	ErrEnvironmentOperationNotFound   = errors.New("store: environment operation not found")
	ErrEnvironmentIdempotencyConflict = errors.New("store: environment idempotency conflict")
	ErrEnvironmentStaleRevision       = errors.New("store: stale environment revision")
)

type EnvironmentMutation struct {
	EnvironmentID    string
	Version          string
	Operation        string
	PackageRef       string
	ExpectedRevision uint64
	IdempotencyKey   string
	Actor            string
	RequestedAt      time.Time
}

type EnvironmentOperationRecord struct {
	OperationID           string
	EnvironmentID         string
	Version               string
	Operation             string
	State                 string
	FailureCode           string
	Actor                 string
	PackageRef            string
	ExpectedRevision      uint64
	EnvironmentGeneration uint64
	RequestedAt           time.Time
	UpdatedAt             time.Time
}

type EnvironmentAuditEvent struct {
	EventID               string
	OperationID           string
	EnvironmentID         string
	Version               string
	Operation             string
	Actor                 string
	FromState             string
	ToState               string
	Outcome               string
	FailureCode           string
	ConfigRevision        uint64
	EnvironmentGeneration uint64
	OccurredAt            time.Time
}

func terminalEnvironmentOperation(state string) bool {
	return state == "applied" || state == "failed" || state == "rolled_back" || state == "cancelled"
}

func (e EnvironmentAuditEvent) Validate() error {
	if e.EventID == "" || e.OperationID == "" || e.EnvironmentID == "" || e.Version == "" || e.Operation == "" || e.ToState == "" || e.OccurredAt.IsZero() {
		return ErrCorruptData
	}
	return nil
}

func (o EnvironmentOperationRecord) Validate() error {
	if strings.TrimSpace(o.OperationID) == "" || strings.TrimSpace(o.EnvironmentID) == "" || strings.TrimSpace(o.Version) == "" || strings.TrimSpace(o.Operation) == "" || strings.TrimSpace(o.State) == "" || o.RequestedAt.IsZero() || o.UpdatedAt.IsZero() {
		return ErrCorruptData
	}
	return nil
}

func (s *Store) ApplyEnvironmentOperation(m EnvironmentMutation) (EnvironmentOperationRecord, bool, error) {
	if m.RequestedAt.IsZero() {
		m.RequestedAt = time.Now().UTC()
	}
	m.RequestedAt = m.RequestedAt.UTC()
	m.EnvironmentID, m.Version, m.Operation, m.IdempotencyKey, m.Actor = strings.TrimSpace(m.EnvironmentID), strings.TrimSpace(m.Version), strings.TrimSpace(m.Operation), strings.TrimSpace(m.IdempotencyKey), strings.TrimSpace(m.Actor)
	if m.EnvironmentID == "" || m.Version == "" || m.Operation == "" || m.IdempotencyKey == "" || m.Actor == "" || strings.ContainsAny(m.EnvironmentID+m.Version+m.Operation+m.IdempotencyKey+m.Actor, "\r\n\t") {
		return EnvironmentOperationRecord{}, false, ErrInvalidRequest
	}
	var result EnvironmentOperationRecord
	var idempotent bool
	err := s.update(func(tx *bbolt.Tx) error {
		ops := tx.Bucket([]byte(migrations.EnvironmentOperationsBucket))
		index := tx.Bucket([]byte(migrations.EnvironmentIdempotencyBucket))
		if raw := index.Get([]byte(m.IdempotencyKey)); raw != nil {
			if err := decode(ops.Get(raw), &result); err != nil {
				return fmt.Errorf("%w: invalid environment operation", ErrCorruptData)
			}
			if result.EnvironmentID != m.EnvironmentID || result.Version != m.Version || result.Operation != m.Operation || result.PackageRef != m.PackageRef || result.ExpectedRevision != m.ExpectedRevision || result.Actor != m.Actor {
				return ErrEnvironmentIdempotencyConflict
			}
			idempotent = true
			return nil
		}
		if m.ExpectedRevision > 0 {
			var record environment.Record
			raw := tx.Bucket([]byte(migrations.EnvironmentPackagesBucket)).Get([]byte(environmentKey(m.EnvironmentID, m.Version)))
			if raw == nil || decode(raw, &record) != nil || record.Generation != m.ExpectedRevision {
				return ErrEnvironmentStaleRevision
			}
		}
		generation := uint64(0)
		if raw := tx.Bucket([]byte(migrations.EnvironmentPackagesBucket)).Get([]byte(environmentKey(m.EnvironmentID, m.Version))); raw != nil {
			var record environment.Record
			if decode(raw, &record) == nil {
				generation = record.Generation
			}
		}
		id := environmentOperationID(m, tx)
		result = EnvironmentOperationRecord{OperationID: id, EnvironmentID: m.EnvironmentID, Version: m.Version, Operation: m.Operation, State: "requested", Actor: m.Actor, PackageRef: m.PackageRef, ExpectedRevision: m.ExpectedRevision, EnvironmentGeneration: generation, RequestedAt: m.RequestedAt, UpdatedAt: m.RequestedAt}
		if err := putJSON(ops, id, result); err != nil {
			return err
		}
		if err := index.Put([]byte(m.IdempotencyKey), []byte(id)); err != nil {
			return err
		}
		return putEnvironmentAuditTx(tx, EnvironmentAuditEvent{EventID: "audit_" + id, OperationID: id, EnvironmentID: m.EnvironmentID, Version: m.Version, Operation: m.Operation, Actor: m.Actor, ToState: "requested", Outcome: "requested", ConfigRevision: m.ExpectedRevision, EnvironmentGeneration: generation, OccurredAt: m.RequestedAt})
	})
	return result, idempotent, err
}

func environmentOperationID(m EnvironmentMutation, tx *bbolt.Tx) string {
	h := sha256.Sum256([]byte(m.EnvironmentID + "|" + m.Version + "|" + m.Operation + "|" + m.IdempotencyKey + "|" + m.RequestedAt.Format(time.RFC3339Nano)))
	base := "envop_" + hex.EncodeToString(h[:8])
	if tx.Bucket([]byte(migrations.EnvironmentOperationsBucket)).Get([]byte(base)) == nil {
		return base
	}
	for i := 1; ; i++ {
		id := fmt.Sprintf("%s_%d", base, i)
		if tx.Bucket([]byte(migrations.EnvironmentOperationsBucket)).Get([]byte(id)) == nil {
			return id
		}
	}
}

func (s *Store) GetEnvironmentOperation(id string) (EnvironmentOperationRecord, error) {
	var result EnvironmentOperationRecord
	err := s.view(func(tx *bbolt.Tx) error {
		if err := getJSON(tx.Bucket([]byte(migrations.EnvironmentOperationsBucket)), id, &result, ErrEnvironmentOperationNotFound); err != nil {
			return err
		}
		return result.Validate()
	})
	return result, err
}

func (s *Store) UpdateEnvironmentOperation(id, state, failureCode string, at time.Time) (EnvironmentOperationRecord, error) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	var result EnvironmentOperationRecord
	err := s.update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte(migrations.EnvironmentOperationsBucket))
		if err := getJSON(bucket, id, &result, ErrEnvironmentOperationNotFound); err != nil {
			return err
		}
		if terminalEnvironmentOperation(result.State) {
			return nil
		}
		previous := result.State
		if raw := tx.Bucket([]byte(migrations.EnvironmentPackagesBucket)).Get([]byte(environmentKey(result.EnvironmentID, result.Version))); raw != nil {
			var record environment.Record
			if decode(raw, &record) == nil {
				result.EnvironmentGeneration = record.Generation
			}
		}
		result.State, result.FailureCode, result.UpdatedAt = state, failureCode, at.UTC()
		if err := putJSON(bucket, id, result); err != nil {
			return err
		}
		return putEnvironmentAuditTx(tx, EnvironmentAuditEvent{EventID: nextEnvironmentAuditID(tx, id, at), OperationID: id, EnvironmentID: result.EnvironmentID, Version: result.Version, Operation: result.Operation, Actor: result.Actor, FromState: previous, ToState: state, Outcome: state, FailureCode: failureCode, ConfigRevision: result.ExpectedRevision, EnvironmentGeneration: result.EnvironmentGeneration, OccurredAt: at})
	})
	return result, err
}

// CompleteEnvironmentOperation commits a manager-validated lifecycle record,
// operation state, and metadata-only audit event in one bbolt transaction.
func (s *Store) CompleteEnvironmentOperation(id, state, failureCode string, record environment.Record, at time.Time) (EnvironmentOperationRecord, error) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if err := record.Validate(); err != nil {
		return EnvironmentOperationRecord{}, err
	}
	var result EnvironmentOperationRecord
	err := s.update(func(tx *bbolt.Tx) error {
		operations := tx.Bucket([]byte(migrations.EnvironmentOperationsBucket))
		if err := getJSON(operations, id, &result, ErrEnvironmentOperationNotFound); err != nil {
			return err
		}
		if terminalEnvironmentOperation(result.State) {
			return nil
		}
		if record.EnvironmentID != result.EnvironmentID || record.Version != result.Version {
			return ErrInvalidRequest
		}
		previous := result.State
		result.State, result.FailureCode, result.EnvironmentGeneration, result.UpdatedAt = state, failureCode, record.Generation, at.UTC()
		if err := putJSON(tx.Bucket([]byte(migrations.EnvironmentPackagesBucket)), environmentKey(record.EnvironmentID, record.Version), record); err != nil {
			return err
		}
		if err := putJSON(operations, id, result); err != nil {
			return err
		}
		return putEnvironmentAuditTx(tx, EnvironmentAuditEvent{EventID: nextEnvironmentAuditID(tx, id, at), OperationID: id, EnvironmentID: result.EnvironmentID, Version: result.Version, Operation: result.Operation, Actor: result.Actor, FromState: previous, ToState: state, Outcome: state, FailureCode: failureCode, ConfigRevision: result.ExpectedRevision, EnvironmentGeneration: result.EnvironmentGeneration, OccurredAt: at})
	})
	return result, err
}

// RecoverEnvironmentOperations closes operations left in requested or
// provisioning state by a process crash. A newer ready generation proves a
// package lifecycle operation completed before the crash; all other records
// receive the stable service_restarted failure classification.
func (s *Store) RecoverEnvironmentOperations(at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return s.update(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(migrations.EnvironmentOperationsBucket)).ForEach(func(key, raw []byte) error {
			if raw == nil {
				return nil
			}
			var operation EnvironmentOperationRecord
			if err := decode(raw, &operation); err != nil {
				return err
			}
			if terminalEnvironmentOperation(operation.State) || (operation.State != "requested" && operation.State != "provisioning") {
				return nil
			}
			state, failure := "failed", "service_restarted"
			if operation.Operation == "install" || operation.Operation == "upgrade" || operation.Operation == "rollback" {
				if packageRaw := tx.Bucket([]byte(migrations.EnvironmentPackagesBucket)).Get([]byte(environmentKey(operation.EnvironmentID, operation.Version))); packageRaw != nil {
					var record environment.Record
					if decode(packageRaw, &record) == nil && record.IsReady() && record.Generation > operation.EnvironmentGeneration {
						state, failure = "applied", ""
					}
				}
			}
			previous := operation.State
			operation.State, operation.FailureCode, operation.UpdatedAt = state, failure, at.UTC()
			if err := putJSON(tx.Bucket([]byte(migrations.EnvironmentOperationsBucket)), string(key), operation); err != nil {
				return err
			}
			return putEnvironmentAuditTx(tx, EnvironmentAuditEvent{EventID: nextEnvironmentAuditID(tx, operation.OperationID, at), OperationID: operation.OperationID, EnvironmentID: operation.EnvironmentID, Version: operation.Version, Operation: operation.Operation, Actor: operation.Actor, FromState: previous, ToState: state, Outcome: state, FailureCode: failure, ConfigRevision: operation.ExpectedRevision, EnvironmentGeneration: operation.EnvironmentGeneration, OccurredAt: at})
		})
	})
}

func nextEnvironmentAuditID(tx *bbolt.Tx, operationID string, at time.Time) string {
	bucket := tx.Bucket([]byte(migrations.EnvironmentAuditBucket))
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

func putEnvironmentAuditTx(tx *bbolt.Tx, event EnvironmentAuditEvent) error {
	if err := event.Validate(); err != nil {
		return err
	}
	return putJSON(tx.Bucket([]byte(migrations.EnvironmentAuditBucket)), event.EventID, event)
}

func (s *Store) ListEnvironmentAudit(id, version string, limit int) ([]EnvironmentAuditEvent, error) {
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 1000 {
		return nil, ErrInvalidRequest
	}
	result := make([]EnvironmentAuditEvent, 0)
	err := s.view(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(migrations.EnvironmentAuditBucket)).ForEach(func(_, raw []byte) error {
			if raw == nil {
				return nil
			}
			var value EnvironmentAuditEvent
			if err := decode(raw, &value); err != nil {
				return err
			}
			if value.EnvironmentID == id && value.Version == version {
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

func (s *Store) ListEnvironmentOperations(limit int) ([]EnvironmentOperationRecord, error) {
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 1000 {
		return nil, ErrInvalidRequest
	}
	result := make([]EnvironmentOperationRecord, 0)
	err := s.view(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(migrations.EnvironmentOperationsBucket)).ForEach(func(_, raw []byte) error {
			if raw == nil {
				return nil
			}
			var value EnvironmentOperationRecord
			if err := decode(raw, &value); err != nil {
				return err
			}
			result = append(result, value)
			return nil
		})
	})
	sort.Slice(result, func(i, j int) bool { return result[i].RequestedAt.Before(result[j].RequestedAt) })
	if len(result) > limit {
		result = result[len(result)-limit:]
	}
	return result, err
}

func (s *Store) ApplyEnvironmentGate(id, version, operation string, at time.Time) (environment.Record, error) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	record, err := s.GetEnvironmentRecord(id, version)
	if err != nil {
		return environment.Record{}, err
	}
	switch operation {
	case "trust":
		record.Trusted = true
	case "disable":
		record.Enabled, record.Ready = false, false
	case "enable":
		if !record.Trusted {
			return environment.Record{}, environment.ErrNotTrusted
		}
		record.Enabled = true
	case "health":
		if !record.Verified {
			return environment.Record{}, environment.ErrNotVerified
		}
	case "verify":
		if !record.Installed || !record.Verified {
			return environment.Record{}, environment.ErrNotVerified
		}
	case "install", "upgrade", "rollback":
		return record, environment.ErrNotReady
	default:
		return environment.Record{}, ErrInvalidRequest
	}
	if operation == "health" {
		record.Healthy = true
		record.Ready = record.Installed && record.Verified && record.Trusted && record.Enabled
	}
	record.UpdatedAt = at.UTC()
	if err := record.Validate(); err != nil {
		return environment.Record{}, err
	}
	return record, s.PutEnvironmentRecord(record)
}
