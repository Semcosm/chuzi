package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Semcosm/chuzi/internal/slot"
	"github.com/Semcosm/chuzi/migrations"
	"go.etcd.io/bbolt"
)

var (
	ErrSlotSessionOperationNotFound   = errors.New("store: slot session operation not found")
	ErrSlotSessionIdempotencyConflict = errors.New("store: slot session idempotency conflict")
	ErrSlotSessionRevision            = errors.New("store: stale slot session revision")
)

type SlotSessionOperationState string

const (
	SlotSessionRequested    SlotSessionOperationState = "requested"
	SlotSessionProvisioning SlotSessionOperationState = "provisioning"
	SlotSessionReady        SlotSessionOperationState = "ready"
	SlotSessionFailed       SlotSessionOperationState = "failed"
	SlotSessionCancelled    SlotSessionOperationState = "cancelled"
)

type SlotSessionMutation struct {
	PoolID           string
	SlotID           string
	ExpectedRevision uint64
	IdempotencyKey   string
	Actor            string
	RequestedAt      time.Time
}

type SlotSessionOperation struct {
	OperationID           string                    `json:"operation_id"`
	PoolID                string                    `json:"pool_id"`
	SlotID                string                    `json:"slot_id"`
	Ordinal               int                       `json:"ordinal"`
	State                 SlotSessionOperationState `json:"state"`
	Actor                 string                    `json:"actor"`
	ExpectedRevision      uint64                    `json:"expected_revision"`
	RequestedAt           time.Time                 `json:"requested_at"`
	UpdatedAt             time.Time                 `json:"updated_at"`
	CompletedAt           time.Time                 `json:"completed_at,omitempty"`
	FailureCode           string                    `json:"failure_code,omitempty"`
	SessionState          string                    `json:"session_state,omitempty"`
	AgentReady            bool                      `json:"agent_ready"`
	EnvironmentGeneration uint64                    `json:"environment_generation"`
}

func (s *SlotSessionMutation) normalize(now time.Time) error {
	s.PoolID, s.SlotID, s.IdempotencyKey, s.Actor = strings.TrimSpace(s.PoolID), strings.TrimSpace(s.SlotID), strings.TrimSpace(s.IdempotencyKey), strings.TrimSpace(s.Actor)
	if s.RequestedAt.IsZero() {
		s.RequestedAt = now.UTC()
	} else {
		s.RequestedAt = s.RequestedAt.UTC()
	}
	if (s.PoolID == "") == (s.SlotID == "") || s.IdempotencyKey == "" || s.Actor == "" || strings.ContainsAny(s.PoolID+s.SlotID+s.IdempotencyKey+s.Actor, "\r\n\t") {
		return ErrInvalidRequest
	}
	return nil
}

func slotSessionTerminal(state SlotSessionOperationState) bool {
	return state == SlotSessionReady || state == SlotSessionFailed || state == SlotSessionCancelled
}

func slotSessionOperationID(m SlotSessionMutation, slotID string, tx *bbolt.Tx) string {
	h := sha256.Sum256([]byte(m.PoolID + "|" + slotID + "|" + m.IdempotencyKey + "|" + m.RequestedAt.Format(time.RFC3339Nano)))
	base := "slotop_" + hex.EncodeToString(h[:8])
	if tx.Bucket([]byte(migrations.SlotSessionOperationsBucket)).Get([]byte(base)) == nil {
		return base
	}
	for i := 1; ; i++ {
		candidate := fmt.Sprintf("%s_%d", base, i)
		if tx.Bucket([]byte(migrations.SlotSessionOperationsBucket)).Get([]byte(candidate)) == nil {
			return candidate
		}
	}
}

func (s *Store) StartSlotSession(m SlotSessionMutation) (SlotSessionOperation, bool, error) {
	now := m.RequestedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if err := m.normalize(now); err != nil {
		return SlotSessionOperation{}, false, err
	}
	var result SlotSessionOperation
	var idempotent bool
	err := s.update(func(tx *bbolt.Tx) error {
		ops, index := tx.Bucket([]byte(migrations.SlotSessionOperationsBucket)), tx.Bucket([]byte(migrations.SlotSessionIdempotencyBucket))
		if raw := index.Get([]byte(m.IdempotencyKey)); raw != nil {
			if err := decode(ops.Get(raw), &result); err != nil {
				return ErrCorruptData
			}
			if (m.PoolID != "" && result.PoolID != m.PoolID) || (m.SlotID != "" && result.SlotID != m.SlotID) || result.Actor != m.Actor || result.ExpectedRevision != m.ExpectedRevision {
				return ErrSlotSessionIdempotencyConflict
			}
			idempotent = true
			return nil
		}
		var selected slot.Slot
		if m.SlotID != "" {
			if err := getJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), m.SlotID, &selected, slot.ErrSlotNotFound); err != nil {
				return err
			}
			if m.PoolID != "" && selected.PoolID != m.PoolID {
				return slot.ErrSlotNotFound
			}
		} else {
			values, err := slotsForPoolTx(tx, m.PoolID)
			if err != nil {
				return err
			}
			for _, candidate := range values {
				if candidate.Status != slot.Deleted && candidate.Status != slot.Retiring {
					selected = candidate
					break
				}
			}
			if selected.SlotID == "" {
				return slot.ErrSlotUnavailable
			}
		}
		var pool slot.PoolConfig
		if err := getJSON(tx.Bucket([]byte(migrations.JobPoolsBucket)), selected.PoolID, &pool, slot.ErrPoolNotFound); err != nil {
			return err
		}
		if m.ExpectedRevision != 0 && pool.ConfigRevision != m.ExpectedRevision {
			return ErrSlotSessionRevision
		}
		result = SlotSessionOperation{OperationID: slotSessionOperationID(m, selected.SlotID, tx), PoolID: selected.PoolID, SlotID: selected.SlotID, Ordinal: selected.Ordinal, State: SlotSessionRequested, Actor: m.Actor, ExpectedRevision: m.ExpectedRevision, RequestedAt: m.RequestedAt, UpdatedAt: m.RequestedAt, SessionState: string(selected.Status), AgentReady: selected.Status == slot.Ready, EnvironmentGeneration: selected.EnvironmentGeneration}
		if err := putJSON(ops, result.OperationID, result); err != nil {
			return err
		}
		return index.Put([]byte(m.IdempotencyKey), []byte(result.OperationID))
	})
	return result, idempotent, err
}

func (s *Store) GetSlotSessionOperation(id string) (SlotSessionOperation, error) {
	var result SlotSessionOperation
	err := s.view(func(tx *bbolt.Tx) error {
		return getJSON(tx.Bucket([]byte(migrations.SlotSessionOperationsBucket)), id, &result, ErrSlotSessionOperationNotFound)
	})
	return result, err
}

func (s *Store) ReconcileSlotSessionOperations(at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	at = at.UTC()
	return s.update(func(tx *bbolt.Tx) error {
		ops := tx.Bucket([]byte(migrations.SlotSessionOperationsBucket))
		return ops.ForEach(func(key, raw []byte) error {
			if raw == nil {
				return nil
			}
			var operation SlotSessionOperation
			if err := decode(raw, &operation); err != nil {
				return err
			}
			if slotSessionTerminal(operation.State) {
				return nil
			}
			var value slot.Slot
			if err := getJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), operation.SlotID, &value, slot.ErrSlotNotFound); err != nil {
				operation.State, operation.FailureCode = SlotSessionFailed, "slot_not_found"
			} else {
				operation.SessionState, operation.EnvironmentGeneration = string(value.Status), value.EnvironmentGeneration
				operation.AgentReady = value.Status == slot.Ready
				switch value.Status {
				case slot.Ready:
					operation.State, operation.FailureCode = SlotSessionReady, ""
				case slot.Quarantined:
					operation.State, operation.FailureCode = SlotSessionFailed, "slot_quarantined"
				default:
					operation.State = SlotSessionProvisioning
				}
			}
			operation.UpdatedAt = at
			if slotSessionTerminal(operation.State) {
				operation.CompletedAt = at
			}
			return putJSON(ops, string(key), operation)
		})
	})
}
