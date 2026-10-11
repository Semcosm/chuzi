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
	ErrSlotSessionActionConflict      = errors.New("store: slot session action conflict")
	ErrSlotSessionClaim               = errors.New("store: slot session claim unavailable")
	ErrSlotSessionLateResult          = errors.New("store: stale slot session result")
	ErrSlotSessionDeadline            = errors.New("store: slot session deadline exceeded")
)

const DefaultSlotSessionDeadline = 5 * time.Minute
const SlotSessionFreshnessWindow = time.Minute

type SlotSessionOperationState string

const (
	SlotSessionRequested    SlotSessionOperationState = "requested"
	SlotSessionProvisioning SlotSessionOperationState = "provisioning"
	SlotSessionStopping     SlotSessionOperationState = "stopping"
	SlotSessionCancelling   SlotSessionOperationState = "cancelling"
	SlotSessionRetrying     SlotSessionOperationState = "retrying"
	SlotSessionReady        SlotSessionOperationState = "ready"
	SlotSessionStopped      SlotSessionOperationState = "stopped"
	SlotSessionFailed       SlotSessionOperationState = "failed"
	SlotSessionCancelled    SlotSessionOperationState = "cancelled"
)

type SlotSessionPhase string

const (
	SlotSessionPhaseEnvironment SlotSessionPhase = "environment"
	SlotSessionPhaseHost        SlotSessionPhase = "host_preflight"
	SlotSessionPhaseProfile     SlotSessionPhase = "profile"
	SlotSessionPhaseLogin       SlotSessionPhase = "login"
	SlotSessionPhaseWTS         SlotSessionPhase = "wts"
	SlotSessionPhaseDesktop     SlotSessionPhase = "desktop"
	SlotSessionPhaseShell       SlotSessionPhase = "shell"
	SlotSessionPhaseAgent       SlotSessionPhase = "agent"
	SlotSessionPhaseWorker      SlotSessionPhase = "worker"
	SlotSessionPhaseStopping    SlotSessionPhase = "stopping"
	SlotSessionPhaseStopped     SlotSessionPhase = "stopped"
)

type SlotSessionMutation struct {
	PoolID           string
	SlotID           string
	ExpectedRevision uint64
	IdempotencyKey   string
	Actor            string
	RequestedAt      time.Time
	Attempt          int
	Deadline         time.Time
	RetryBudget      int
}

type SlotSessionOperation struct {
	OperationID             string                    `json:"operation_id"`
	PoolID                  string                    `json:"pool_id"`
	SlotID                  string                    `json:"slot_id"`
	Ordinal                 int                       `json:"ordinal"`
	State                   SlotSessionOperationState `json:"state"`
	Actor                   string                    `json:"actor"`
	ExpectedRevision        uint64                    `json:"expected_revision"`
	RequestedAt             time.Time                 `json:"requested_at"`
	UpdatedAt               time.Time                 `json:"updated_at"`
	CompletedAt             time.Time                 `json:"completed_at,omitempty"`
	FailureCode             string                    `json:"failure_code,omitempty"`
	SessionState            string                    `json:"session_state,omitempty"`
	AgentReady              bool                      `json:"agent_ready"`
	EnvironmentGeneration   uint64                    `json:"environment_generation"`
	Phase                   SlotSessionPhase          `json:"phase,omitempty"`
	Attempt                 int                       `json:"attempt"`
	Revision                uint64                    `json:"revision,omitempty"`
	DesiredState            string                    `json:"desired_state,omitempty"`
	Deadline                time.Time                 `json:"deadline,omitempty"`
	RetryBudget             int                       `json:"retry_budget,omitempty"`
	NextAttemptAt           time.Time                 `json:"next_attempt_at,omitempty"`
	ClaimOwner              string                    `json:"claim_owner,omitempty"`
	ClaimUntil              time.Time                 `json:"claim_until,omitempty"`
	ActionSourceOperationID string                    `json:"action_source_operation_id,omitempty"`
	ActionActor             string                    `json:"action_actor,omitempty"`
	ActionKind              string                    `json:"action_kind,omitempty"`
	ActionExpectedRevision  uint64                    `json:"action_expected_revision,omitempty"`
	ActionGeneration        uint64                    `json:"action_generation,omitempty"`
	ActionAttempt           int                       `json:"action_attempt,omitempty"`
	ActionDeadline          time.Time                 `json:"action_deadline,omitempty"`
	StartIdempotencyKey     string                    `json:"start_idempotency_key,omitempty"`
	StartDeadlineExplicit   bool                      `json:"start_deadline_explicit,omitempty"`
	StartRequestedPoolID    string                    `json:"start_requested_pool_id,omitempty"`
	StartRequestedSlotID    string                    `json:"start_requested_slot_id,omitempty"`
}

type SlotSessionAction struct {
	OperationID      string
	Actor            string
	IdempotencyKey   string
	ExpectedRevision uint64
	Generation       uint64
	Attempt          int
	Deadline         time.Time
	At               time.Time
}

// SlotSessionCheckpoint is the controller-facing runtime fact boundary. It
// carries only redaction-safe phase/readiness facts; platform identity stays
// in the platform-owned journal.
type SlotSessionCheckpoint struct {
	OperationID  string
	Owner        string
	Generation   uint64
	Phase        SlotSessionPhase
	SessionState string
	AgentReady   bool
	ObservedAt   time.Time
	FreshUntil   time.Time
}

// SlotSessionController is the narrow Store boundary intended for the
// service controller. All methods fence by operation owner and generation.
type SlotSessionController interface {
	ClaimSlotSessionOperation(string, string, time.Time, time.Duration) (SlotSessionOperation, error)
	CheckpointSlotSessionOperation(SlotSessionCheckpoint) (SlotSessionOperation, error)
	CompleteSlotSessionOperation(string, string, uint64, SlotSessionOperationState, SlotSessionPhase, string, time.Time) (SlotSessionOperation, error)
}

func SlotSessionBackoff(attempt int) time.Duration {
	if attempt <= 1 {
		return 0
	}
	shift := attempt - 2
	if shift > 5 {
		shift = 5
	}
	return time.Duration(1<<shift) * time.Second
}

func (s *SlotSessionMutation) normalize(now time.Time) error {
	s.PoolID, s.SlotID, s.IdempotencyKey, s.Actor = strings.TrimSpace(s.PoolID), strings.TrimSpace(s.SlotID), strings.TrimSpace(s.IdempotencyKey), strings.TrimSpace(s.Actor)
	if s.RequestedAt.IsZero() {
		s.RequestedAt = now.UTC()
	} else {
		s.RequestedAt = s.RequestedAt.UTC()
	}
	if (s.PoolID == "") == (s.SlotID == "") || s.IdempotencyKey == "" || s.Actor == "" || s.Attempt < 0 || s.RetryBudget < 0 || strings.ContainsAny(s.PoolID+s.SlotID+s.IdempotencyKey+s.Actor, "\r\n\t") {
		return ErrInvalidRequest
	}
	if !s.Deadline.IsZero() {
		s.Deadline = s.Deadline.UTC()
		if !s.Deadline.After(s.RequestedAt) {
			return ErrInvalidRequest
		}
	} else {
		s.Deadline = s.RequestedAt.Add(DefaultSlotSessionDeadline)
	}
	return nil
}

func slotSessionTerminal(state SlotSessionOperationState) bool {
	return state == SlotSessionReady || state == SlotSessionStopped || state == SlotSessionFailed || state == SlotSessionCancelled
}

func slotSessionStateValid(state SlotSessionOperationState) bool {
	switch state {
	case SlotSessionRequested, SlotSessionProvisioning, SlotSessionStopping, SlotSessionCancelling, SlotSessionRetrying, SlotSessionReady, SlotSessionStopped, SlotSessionFailed, SlotSessionCancelled:
		return true
	default:
		return false
	}
}

func normalizedSlotSessionAttempt(attempt int) int {
	if attempt == 0 {
		return 1
	}
	return attempt
}

// SlotSessionTransitionAllowed is the deterministic operation state machine
// used by controllers and recovery. Runtime adapters may report facts, but
// they cannot skip a durable intent or revive a terminal attempt.
func SlotSessionTransitionAllowed(from, to SlotSessionOperationState) bool {
	if from == to {
		return true
	}
	switch from {
	case SlotSessionRequested:
		return to == SlotSessionProvisioning || to == SlotSessionStopping || to == SlotSessionCancelling || to == SlotSessionFailed
	case SlotSessionProvisioning, SlotSessionRetrying:
		return to == SlotSessionProvisioning || to == SlotSessionReady || to == SlotSessionStopping || to == SlotSessionCancelling || to == SlotSessionFailed
	case SlotSessionReady:
		return to == SlotSessionStopping || to == SlotSessionCancelling
	case SlotSessionStopping:
		return to == SlotSessionStopped || to == SlotSessionFailed
	case SlotSessionCancelling:
		return to == SlotSessionCancelled || to == SlotSessionFailed
	default:
		return false
	}
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

func slotSessionActionIndex(key string) string { return "action:" + key }

func slotSessionActionTime(action SlotSessionAction) time.Time {
	if action.At.IsZero() {
		return time.Now().UTC()
	}
	return action.At.UTC()
}

func (s *Store) StartSlotSession(m SlotSessionMutation) (SlotSessionOperation, bool, error) {
	deadlineExplicit := !m.Deadline.IsZero()
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
			selectorConflict := result.StartIdempotencyKey != "" && (result.StartRequestedPoolID != m.PoolID || result.StartRequestedSlotID != m.SlotID)
			if selectorConflict || (m.PoolID != "" && result.PoolID != m.PoolID) || (m.SlotID != "" && result.SlotID != m.SlotID) || result.Actor != m.Actor || result.ExpectedRevision != m.ExpectedRevision || result.Attempt != normalizedSlotSessionAttempt(m.Attempt) || result.RetryBudget != m.RetryBudget || result.StartDeadlineExplicit != deadlineExplicit || (result.StartDeadlineExplicit && result.Deadline != m.Deadline) || (result.StartIdempotencyKey != "" && result.StartIdempotencyKey != m.IdempotencyKey) {
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
				if candidate.Status == slot.Unprovisioned || candidate.Status == slot.Provisioning || candidate.Status == slot.Ready || candidate.Status == slot.Stopped {
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
		if pool.DesiredSlots == 0 || pool.DesiredState == "disabled" || pool.DesiredState == "draining" {
			return slot.ErrSlotUnavailable
		}
		if m.ExpectedRevision != 0 && pool.ConfigRevision != m.ExpectedRevision {
			return ErrSlotSessionRevision
		}
		if selected.Status != slot.Ready && selected.Status != slot.Unprovisioned && selected.Status != slot.Provisioning && selected.Status != slot.Stopped {
			return slot.ErrSlotUnavailable
		}
		if selected.SessionOperationID != "" {
			var existing SlotSessionOperation
			if err := getJSON(ops, selected.SessionOperationID, &existing, ErrSlotSessionOperationNotFound); err == nil {
				if !slotSessionTerminal(existing.State) || existing.DesiredState == "running" {
					return ErrSlotSessionActionConflict
				}
			} else if !errors.Is(err, ErrSlotSessionOperationNotFound) {
				return err
			}
		}
		state := SlotSessionRequested
		completedAt := time.Time{}
		phase := SlotSessionPhaseEnvironment
		if selected.Status == slot.Ready {
			state = SlotSessionReady
			phase = SlotSessionPhaseWorker
			completedAt = m.RequestedAt
		}
		if selected.Status == slot.Stopped {
			// An explicit Start is the only path that revives a manually stopped slot.
			selected.Status = slot.Provisioning
		}
		attempt := m.Attempt
		if attempt == 0 {
			attempt = 1
		}
		result = SlotSessionOperation{OperationID: slotSessionOperationID(m, selected.SlotID, tx), PoolID: selected.PoolID, SlotID: selected.SlotID, Ordinal: selected.Ordinal, State: state, Actor: m.Actor, ExpectedRevision: m.ExpectedRevision, RequestedAt: m.RequestedAt, UpdatedAt: m.RequestedAt, CompletedAt: completedAt, SessionState: string(selected.Status), AgentReady: selected.Status == slot.Ready, EnvironmentGeneration: selected.EnvironmentGeneration, Phase: phase, Attempt: attempt, Revision: selected.SessionRevision + 1, DesiredState: "running", Deadline: m.Deadline, RetryBudget: m.RetryBudget, StartIdempotencyKey: m.IdempotencyKey, StartDeadlineExplicit: deadlineExplicit, StartRequestedPoolID: m.PoolID, StartRequestedSlotID: m.SlotID}
		selected.SessionDesiredState = "running"
		selected.SessionOperationID = result.OperationID
		selected.SessionRevision = result.Revision
		selected.UpdatedAt = m.RequestedAt
		if err := putJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), selected.SlotID, selected); err != nil {
			return err
		}
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

func slotLeaseForOperationTx(tx *bbolt.Tx, slotID string) (slot.Lease, bool, error) {
	raw := tx.Bucket([]byte(migrations.SlotLeasesBucket)).Get([]byte(slotID))
	if raw == nil {
		return slot.Lease{}, false, nil
	}
	var lease slot.Lease
	if err := decode(raw, &lease); err != nil {
		return slot.Lease{}, false, err
	}
	if err := lease.Validate(); err != nil {
		return slot.Lease{}, false, fmt.Errorf("%w: invalid slot lease", ErrCorruptData)
	}
	return lease, true, nil
}

// applyStoppedIntentTx projects a durable stop intent onto the slot without
// claiming that cleanup completed while a lease is still present.
func applyStoppedIntentTx(tx *bbolt.Tx, current *slot.Slot) error {
	if current == nil || current.Status == slot.Deleted || current.Status == slot.Retiring {
		return nil
	}
	_, leased, err := slotLeaseForOperationTx(tx, current.SlotID)
	if err != nil {
		return err
	}
	if leased {
		current.Status = slot.Draining
	} else {
		current.Status = slot.Stopped
	}
	return nil
}

func (s *Store) mutateSlotSessionAction(action SlotSessionAction, target SlotSessionOperationState, phase SlotSessionPhase, desired string) (SlotSessionOperation, bool, error) {
	now := slotSessionActionTime(action)
	if strings.TrimSpace(action.OperationID) == "" || strings.TrimSpace(action.Actor) == "" || strings.TrimSpace(action.IdempotencyKey) == "" || strings.ContainsAny(action.OperationID+action.Actor+action.IdempotencyKey, "\r\n\t ") || action.Attempt < 0 {
		return SlotSessionOperation{}, false, ErrInvalidRequest
	}
	var result SlotSessionOperation
	idempotent := false
	err := s.update(func(tx *bbolt.Tx) error {
		ops := tx.Bucket([]byte(migrations.SlotSessionOperationsBucket))
		index := tx.Bucket([]byte(migrations.SlotSessionIdempotencyBucket))
		indexKey := []byte(slotSessionActionIndex(action.IdempotencyKey))
		if raw := index.Get(indexKey); raw != nil {
			if err := decode(ops.Get(raw), &result); err != nil {
				return ErrCorruptData
			}
			if string(raw) != action.OperationID || (result.ActionActor != "" && result.ActionActor != action.Actor) || (result.ActionKind != "" && result.ActionKind != string(target)) || result.ActionExpectedRevision != action.ExpectedRevision || result.ActionGeneration != action.Generation || result.ActionAttempt != action.Attempt || !result.ActionDeadline.Equal(action.Deadline) {
				return ErrSlotSessionActionConflict
			}
			idempotent = true
			return nil
		}
		if err := getJSON(ops, action.OperationID, &result, ErrSlotSessionOperationNotFound); err != nil {
			return err
		}
		if action.ExpectedRevision != 0 && action.ExpectedRevision != result.Revision {
			return ErrSlotSessionRevision
		}
		if action.Attempt != 0 && action.Attempt != result.Attempt {
			return ErrSlotSessionActionConflict
		}
		if action.Generation != 0 && result.EnvironmentGeneration != 0 && action.Generation != result.EnvironmentGeneration {
			return ErrSlotSessionLateResult
		}
		if slotSessionTerminal(result.State) && !(result.State == SlotSessionReady && (target == SlotSessionStopping || target == SlotSessionCancelling)) {
			return ErrSlotSessionActionConflict
		}
		if !SlotSessionTransitionAllowed(result.State, target) {
			return ErrSlotSessionActionConflict
		}
		var current slot.Slot
		if err := getJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), result.SlotID, &current, slot.ErrSlotNotFound); err != nil {
			return err
		}
		result.State, result.Phase, result.DesiredState, result.UpdatedAt = target, phase, desired, now
		result.CompletedAt = time.Time{}
		result.ActionSourceOperationID, result.ActionActor, result.ActionKind = action.OperationID, action.Actor, string(target)
		result.ActionExpectedRevision, result.ActionGeneration, result.ActionAttempt, result.ActionDeadline = action.ExpectedRevision, action.Generation, action.Attempt, action.Deadline.UTC()
		if !result.Deadline.IsZero() && !now.Before(result.Deadline) {
			result.State, result.FailureCode, result.CompletedAt = SlotSessionFailed, "deadline_exceeded", now
		}
		current.SessionDesiredState = desired
		if desired == "stopped" && (target == SlotSessionStopping || target == SlotSessionCancelling) {
			if err := applyStoppedIntentTx(tx, &current); err != nil {
				return err
			}
		}
		current.SessionOperationID = result.OperationID
		current.SessionRevision = result.Revision
		current.UpdatedAt = now
		if err := putJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), current.SlotID, current); err != nil {
			return err
		}
		if err := putJSON(ops, result.OperationID, result); err != nil {
			return err
		}
		return index.Put(indexKey, []byte(result.OperationID))
	})
	return result, idempotent, err
}

// CancelSlotSession revokes the desired running intent. Cleanup is completed
// by the runtime and must later call CompleteSlotSessionOperation.
func (s *Store) CancelSlotSession(action SlotSessionAction) (SlotSessionOperation, bool, error) {
	return s.mutateSlotSessionAction(action, SlotSessionCancelling, SlotSessionPhaseStopping, "stopped")
}

// StopSlotSession drains an active session while preserving its slot record.
func (s *Store) StopSlotSession(action SlotSessionAction) (SlotSessionOperation, bool, error) {
	return s.mutateSlotSessionAction(action, SlotSessionStopping, SlotSessionPhaseStopping, "stopped")
}

// RetrySlotSession creates a new fenced attempt for a failed/cancelled op.
func (s *Store) RetrySlotSession(action SlotSessionAction) (SlotSessionOperation, bool, error) {
	now := slotSessionActionTime(action)
	if action.Attempt < 0 {
		return SlotSessionOperation{}, false, ErrInvalidRequest
	}
	if !action.Deadline.IsZero() {
		action.Deadline = action.Deadline.UTC()
		if !action.Deadline.After(now) {
			return SlotSessionOperation{}, false, ErrSlotSessionDeadline
		}
	}
	var result SlotSessionOperation
	idempotent := false
	err := s.update(func(tx *bbolt.Tx) error {
		ops := tx.Bucket([]byte(migrations.SlotSessionOperationsBucket))
		index := tx.Bucket([]byte(migrations.SlotSessionIdempotencyBucket))
		indexKey := []byte(slotSessionActionIndex(action.IdempotencyKey))
		if action.OperationID == "" || action.Actor == "" || action.IdempotencyKey == "" || strings.ContainsAny(action.OperationID+action.Actor+action.IdempotencyKey, "\r\n\t ") {
			return ErrInvalidRequest
		}
		if raw := index.Get(indexKey); raw != nil {
			if err := decode(ops.Get(raw), &result); err != nil {
				return ErrCorruptData
			}
			if result.ActionSourceOperationID != action.OperationID || (result.ActionActor != "" && result.ActionActor != action.Actor) || (result.ActionKind != "" && result.ActionKind != "retry") || result.ActionExpectedRevision != action.ExpectedRevision || result.ActionGeneration != action.Generation || result.ActionAttempt != action.Attempt || !result.ActionDeadline.Equal(action.Deadline) {
				return ErrSlotSessionActionConflict
			}
			idempotent = true
			return nil
		}
		var previous SlotSessionOperation
		if err := getJSON(ops, action.OperationID, &previous, ErrSlotSessionOperationNotFound); err != nil {
			return err
		}
		if previous.State != SlotSessionFailed && previous.State != SlotSessionCancelled {
			return ErrSlotSessionActionConflict
		}
		if action.ExpectedRevision != 0 && action.ExpectedRevision != previous.Revision {
			return ErrSlotSessionRevision
		}
		if action.Attempt != 0 && action.Attempt != previous.Attempt {
			return ErrSlotSessionActionConflict
		}
		if action.Generation != 0 && previous.EnvironmentGeneration != 0 && action.Generation != previous.EnvironmentGeneration {
			return ErrSlotSessionLateResult
		}
		if previous.RetryBudget == 0 || (previous.RetryBudget > 0 && previous.Attempt >= previous.RetryBudget+1) {
			return ErrSlotSessionActionConflict
		}
		if action.Deadline.IsZero() && !previous.Deadline.IsZero() && !now.Before(previous.Deadline) {
			return ErrSlotSessionDeadline
		}
		var current slot.Slot
		if err := getJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), previous.SlotID, &current, slot.ErrSlotNotFound); err != nil {
			return err
		}
		if current.Status == slot.Leased || current.Status == slot.Draining || current.Status == slot.Quarantined || current.Status == slot.Retiring || current.Status == slot.Deleted {
			return slot.ErrSlotUnavailable
		}
		result = previous
		result.OperationID = slotSessionOperationID(SlotSessionMutation{PoolID: previous.PoolID, SlotID: previous.SlotID, IdempotencyKey: action.IdempotencyKey, RequestedAt: now}, previous.SlotID, tx)
		result.StartIdempotencyKey, result.StartDeadlineExplicit = "", false
		result.State, result.Phase = SlotSessionRetrying, SlotSessionPhaseEnvironment
		result.Attempt++
		result.RequestedAt, result.UpdatedAt, result.CompletedAt = now, now, time.Time{}
		result.FailureCode, result.DesiredState = "", "running"
		result.NextAttemptAt = now.Add(SlotSessionBackoff(result.Attempt))
		if current.Status == slot.Stopped {
			current.Status = slot.Provisioning
		}
		if !action.Deadline.IsZero() {
			result.Deadline = action.Deadline.UTC()
		}
		result.ClaimOwner, result.ClaimUntil = "", time.Time{}
		result.ActionSourceOperationID, result.ActionActor, result.ActionKind = action.OperationID, action.Actor, "retry"
		result.ActionExpectedRevision, result.ActionGeneration, result.ActionAttempt, result.ActionDeadline = action.ExpectedRevision, action.Generation, action.Attempt, action.Deadline.UTC()
		current.SessionDesiredState, current.SessionOperationID = "running", result.OperationID
		current.SessionRevision = previous.Revision + 1
		current.UpdatedAt = now
		result.Revision = current.SessionRevision
		if err := putJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), current.SlotID, current); err != nil {
			return err
		}
		if err := putJSON(ops, result.OperationID, result); err != nil {
			return err
		}
		return index.Put(indexKey, []byte(result.OperationID))
	})
	return result, idempotent, err
}

// ClaimSlotSessionOperation fences one executor. Expired claims can be
// recovered after a crash; a live claim from another owner is rejected.
func (s *Store) ClaimSlotSessionOperation(operationID, owner string, at time.Time, ttl time.Duration) (SlotSessionOperation, error) {
	if operationID == "" || owner == "" || strings.ContainsAny(operationID+owner, "\r\n\t ") || ttl <= 0 {
		return SlotSessionOperation{}, ErrInvalidRequest
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	at = at.UTC()
	var result SlotSessionOperation
	err := s.update(func(tx *bbolt.Tx) error {
		ops := tx.Bucket([]byte(migrations.SlotSessionOperationsBucket))
		if err := getJSON(ops, operationID, &result, ErrSlotSessionOperationNotFound); err != nil {
			return err
		}
		if slotSessionTerminal(result.State) {
			return ErrSlotSessionClaim
		}
		if !result.NextAttemptAt.IsZero() && at.Before(result.NextAttemptAt) {
			return ErrSlotSessionClaim
		}
		if result.ClaimOwner != "" && result.ClaimOwner != owner && at.Before(result.ClaimUntil) {
			return ErrSlotSessionClaim
		}
		result.ClaimOwner, result.ClaimUntil = owner, at.Add(ttl)
		if result.State == SlotSessionRequested || result.State == SlotSessionRetrying {
			result.State = SlotSessionProvisioning
		}
		result.UpdatedAt = at
		return putJSON(ops, operationID, result)
	})
	return result, err
}

// CompleteSlotSessionOperation accepts only the currently claimed, matching
// generation result. Late completions cannot mutate a newer attempt.
func (s *Store) CompleteSlotSessionOperation(operationID, owner string, generation uint64, state SlotSessionOperationState, phase SlotSessionPhase, failure string, at time.Time) (SlotSessionOperation, error) {
	if operationID == "" || owner == "" || strings.ContainsAny(operationID+owner, "\r\n\t ") || at.IsZero() {
		return SlotSessionOperation{}, ErrInvalidRequest
	}
	at = at.UTC()
	var result SlotSessionOperation
	err := s.update(func(tx *bbolt.Tx) error {
		ops := tx.Bucket([]byte(migrations.SlotSessionOperationsBucket))
		if err := getJSON(ops, operationID, &result, ErrSlotSessionOperationNotFound); err != nil {
			return err
		}
		if result.ClaimOwner != owner || !at.Before(result.ClaimUntil) {
			return ErrSlotSessionClaim
		}
		if !result.Deadline.IsZero() && !at.Before(result.Deadline) {
			var current slot.Slot
			if err := getJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), result.SlotID, &current, slot.ErrSlotNotFound); err != nil {
				return err
			}
			result.State, result.Phase, result.FailureCode = SlotSessionFailed, SlotSessionPhaseStopping, "deadline_exceeded"
			result.DesiredState = "stopped"
			current.SessionDesiredState = "stopped"
			if err := applyStoppedIntentTx(tx, &current); err != nil {
				return err
			}
			current.SessionOperationID, current.SessionRevision, current.UpdatedAt = operationID, result.Revision, at
			if err := putJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), current.SlotID, current); err != nil {
				return err
			}
			result.CompletedAt, result.UpdatedAt = at, at
			result.ClaimOwner, result.ClaimUntil = "", time.Time{}
			if err := putJSON(ops, operationID, result); err != nil {
				return err
			}
			return ErrSlotSessionDeadline
		}
		if generation != 0 && result.EnvironmentGeneration != 0 && generation != result.EnvironmentGeneration {
			return ErrSlotSessionLateResult
		}
		if !slotSessionStateValid(state) {
			return ErrInvalidRequest
		}
		if !SlotSessionTransitionAllowed(result.State, state) {
			return ErrSlotSessionActionConflict
		}
		result.State, result.Phase, result.FailureCode, result.UpdatedAt = state, phase, failure, at
		if generation != 0 && result.EnvironmentGeneration == 0 {
			result.EnvironmentGeneration = generation
		}
		result.ClaimOwner, result.ClaimUntil = "", time.Time{}
		if slotSessionTerminal(state) {
			result.CompletedAt = at
		}
		var current slot.Slot
		if err := getJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), result.SlotID, &current, slot.ErrSlotNotFound); err != nil {
			return err
		}
		if state == SlotSessionReady {
			result.DesiredState = "running"
			current.SessionDesiredState = "running"
		} else if state == SlotSessionStopped || state == SlotSessionCancelled || state == SlotSessionFailed {
			result.DesiredState = "stopped"
			current.SessionDesiredState = "stopped"
			if state == SlotSessionStopped || state == SlotSessionCancelled || state == SlotSessionFailed {
				if err := applyStoppedIntentTx(tx, &current); err != nil {
					return err
				}
			}
		}
		current.SessionOperationID, current.SessionRevision, current.UpdatedAt = operationID, result.Revision, at
		if err := putJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), current.SlotID, current); err != nil {
			return err
		}
		return putJSON(ops, operationID, result)
	})
	return result, err
}

// CheckpointSlotSessionOperation stores a bounded runtime fact while retaining
// the operation's control state. A stale or expired owner cannot overwrite a
// newer generation.
func (s *Store) CheckpointSlotSessionOperation(checkpoint SlotSessionCheckpoint) (SlotSessionOperation, error) {
	if strings.TrimSpace(checkpoint.OperationID) == "" || strings.TrimSpace(checkpoint.Owner) == "" || strings.ContainsAny(checkpoint.OperationID+checkpoint.Owner+checkpoint.SessionState, "\r\n\t ") || checkpoint.ObservedAt.IsZero() {
		return SlotSessionOperation{}, ErrInvalidRequest
	}
	at := checkpoint.ObservedAt.UTC()
	freshUntil := checkpoint.FreshUntil.UTC()
	if freshUntil.IsZero() {
		freshUntil = at.Add(SlotSessionFreshnessWindow)
	}
	if !freshUntil.After(at) {
		return SlotSessionOperation{}, ErrInvalidRequest
	}
	var result SlotSessionOperation
	err := s.update(func(tx *bbolt.Tx) error {
		ops := tx.Bucket([]byte(migrations.SlotSessionOperationsBucket))
		if err := getJSON(ops, checkpoint.OperationID, &result, ErrSlotSessionOperationNotFound); err != nil {
			return err
		}
		if result.ClaimOwner != checkpoint.Owner || !at.Before(result.ClaimUntil) {
			return ErrSlotSessionClaim
		}
		if checkpoint.Generation != 0 && result.EnvironmentGeneration != 0 && checkpoint.Generation != result.EnvironmentGeneration {
			return ErrSlotSessionLateResult
		}
		result.Phase, result.SessionState, result.AgentReady, result.UpdatedAt = checkpoint.Phase, checkpoint.SessionState, checkpoint.AgentReady, at
		if result.EnvironmentGeneration == 0 {
			result.EnvironmentGeneration = checkpoint.Generation
		}
		var current slot.Slot
		if err := getJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), result.SlotID, &current, slot.ErrSlotNotFound); err != nil {
			return err
		}
		current.HealthAt, current.UpdatedAt = at, at
		if err := putJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), current.SlotID, current); err != nil {
			return err
		}
		return putJSON(ops, result.OperationID, result)
	})
	return result, err
}

// RecordSlotSessionFacts is a convenience spelling for controllers that
// report a readiness heartbeat without changing the operation phase.
func (s *Store) RecordSlotSessionFacts(operationID, owner string, generation uint64, sessionState string, agentReady bool, at time.Time) (SlotSessionOperation, error) {
	return s.CheckpointSlotSessionOperation(SlotSessionCheckpoint{OperationID: operationID, Owner: owner, Generation: generation, SessionState: sessionState, AgentReady: agentReady, ObservedAt: at})
}

// FinishSlotSessionOperation is the controller-facing completion spelling.
func (s *Store) FinishSlotSessionOperation(operationID, owner string, generation uint64, state SlotSessionOperationState, phase SlotSessionPhase, failure string, at time.Time) (SlotSessionOperation, error) {
	return s.CompleteSlotSessionOperation(operationID, owner, generation, state, phase, failure, at)
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
			if !operation.Deadline.IsZero() && !at.Before(operation.Deadline) {
				var current slot.Slot
				if err := getJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), operation.SlotID, &current, slot.ErrSlotNotFound); err == nil {
					current.SessionDesiredState = "stopped"
					if stopErr := applyStoppedIntentTx(tx, &current); stopErr != nil {
						return stopErr
					}
					current.SessionOperationID, current.SessionRevision, current.UpdatedAt = operation.OperationID, operation.Revision, at
					if putErr := putJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), current.SlotID, current); putErr != nil {
						return putErr
					}
				} else if !errors.Is(err, slot.ErrSlotNotFound) {
					return err
				}
				operation.State, operation.FailureCode, operation.CompletedAt = SlotSessionFailed, "deadline_exceeded", at
				operation.ClaimOwner, operation.ClaimUntil, operation.UpdatedAt = "", time.Time{}, at
				return putJSON(ops, string(key), operation)
			}
			if operation.ClaimOwner != "" && !at.Before(operation.ClaimUntil) {
				// A crashed controller loses its claim. The operation remains
				// retryable and a fresh controller may claim it.
				operation.ClaimOwner, operation.ClaimUntil = "", time.Time{}
			}
			var value slot.Slot
			if err := getJSON(tx.Bucket([]byte(migrations.ExecutionSlotsBucket)), operation.SlotID, &value, slot.ErrSlotNotFound); err != nil {
				operation.State, operation.FailureCode = SlotSessionFailed, "slot_not_found"
			} else {
				operation.SessionState, operation.EnvironmentGeneration = string(value.Status), value.EnvironmentGeneration
				operation.AgentReady = value.Status == slot.Ready
				if operation.DesiredState == "stopped" || operation.State == SlotSessionStopping || operation.State == SlotSessionCancelling {
					if value.Status == slot.Stopped || value.Status == slot.Unprovisioned || value.Status == slot.Retiring || value.Status == slot.Deleted {
						if operation.State == SlotSessionCancelling {
							operation.State = SlotSessionCancelled
						} else {
							operation.State = SlotSessionStopped
						}
						operation.Phase, operation.CompletedAt = SlotSessionPhaseStopped, at
					}
				} else {
					switch value.Status {
					case slot.Ready:
						operation.State, operation.FailureCode, operation.Phase = SlotSessionReady, "", SlotSessionPhaseWorker
					case slot.Quarantined:
						operation.State, operation.FailureCode = SlotSessionFailed, "slot_quarantined"
					default:
						if operation.State != SlotSessionRetrying {
							operation.State = SlotSessionProvisioning
						}
					}
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
