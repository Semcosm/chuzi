package store

// Durable session projections are intentionally metadata-only. Runtime
// credentials, endpoints, profiles and process handles stay behind the
// credential/slot boundaries and are never persisted here.

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Semcosm/chuzi/migrations"
	"go.etcd.io/bbolt"
)

var (
	ErrSessionNotFound = fmt.Errorf("store: session not found")
	ErrInvalidSession  = fmt.Errorf("store: invalid session")
)

// SessionRecord is the redacted durable lifecycle projection. Slot lease and
// environment generation fields provide the restart/generation fence.
type SessionRecord struct {
	SessionID             string    `json:"session_id"`
	RequestID             string    `json:"request_id"`
	AccountID             string    `json:"account_id"`
	PoolID                string    `json:"pool_id"`
	EnvironmentID         string    `json:"environment_id"`
	EnvironmentVersion    string    `json:"environment_version"`
	AdapterID             string    `json:"adapter_id"`
	AdapterVersion        string    `json:"adapter_version"`
	Phase                 string    `json:"phase"`
	FailureCode           string    `json:"failure_code,omitempty"`
	SlotID                string    `json:"slot_id,omitempty"`
	SlotLeaseID           string    `json:"slot_lease_id,omitempty"`
	AccountLeaseID        string    `json:"account_lease_id,omitempty"`
	EnvironmentGeneration uint64    `json:"environment_generation,omitempty"`
	AgentReady            bool      `json:"agent_ready,omitempty"`
	WorkerReady           bool      `json:"worker_ready,omitempty"`
	AdapterReady          bool      `json:"adapter_ready,omitempty"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

func (r SessionRecord) Validate() error {
	values := []string{r.SessionID, r.RequestID, r.AccountID, r.PoolID, r.EnvironmentID, r.EnvironmentVersion, r.AdapterID, r.AdapterVersion, r.Phase, r.FailureCode, r.SlotID, r.SlotLeaseID, r.AccountLeaseID}
	for _, value := range values {
		if value != strings.TrimSpace(value) || strings.ContainsAny(value, "\x00\r\n\t") || len(value) > 256 {
			return ErrInvalidSession
		}
	}
	if r.SessionID == "" || r.RequestID == "" || r.AccountID == "" || r.PoolID == "" || r.EnvironmentID == "" || r.EnvironmentVersion == "" || r.AdapterID == "" || r.AdapterVersion == "" || r.Phase == "" || r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() || r.UpdatedAt.Before(r.CreatedAt) {
		return ErrInvalidSession
	}
	if !validSessionPhase(r.Phase) || !validSessionFailure(r.FailureCode) {
		return ErrInvalidSession
	}
	if (r.SlotID == "") != (r.SlotLeaseID == "") || (r.SlotID != "" && r.AccountLeaseID == "") || (r.EnvironmentGeneration != 0 && (r.SlotID == "" || r.SlotLeaseID == "")) {
		return ErrInvalidSession
	}
	return nil
}

func validSessionPhase(value string) bool {
	switch value {
	case "requested", "slot_claimed", "provisioning", "agent_ready", "worker_starting", "adapter_starting", "running", "rdp_available", "stopping", "stopped", "failed":
		return true
	default:
		return false
	}
}

func validSessionFailure(value string) bool {
	if value == "" {
		return true
	}
	switch value {
	case "no_capacity", "environment_untrusted", "package_unavailable", "slot_provision_failed", "agent_unavailable", "worker_failed", "adapter_failed", "rdp_unavailable", "cancelled", "service_restarted", "timeout":
		return true
	default:
		return false
	}
}

func (s *Store) UpsertSession(value SessionRecord) error {
	if err := value.Validate(); err != nil {
		return err
	}
	return s.update(func(tx *bbolt.Tx) error {
		return putJSON(tx.Bucket([]byte(migrations.SessionsBucket)), value.SessionID, value)
	})
}

func (s *Store) GetSession(sessionID string) (SessionRecord, error) {
	var result SessionRecord
	err := s.view(func(tx *bbolt.Tx) error {
		return getJSON(tx.Bucket([]byte(migrations.SessionsBucket)), sessionID, &result, ErrSessionNotFound)
	})
	if err != nil {
		return SessionRecord{}, err
	}
	if err := result.Validate(); err != nil {
		return SessionRecord{}, fmt.Errorf("%w: %v", ErrCorruptData, err)
	}
	return result, nil
}

func (s *Store) ListSessions() ([]SessionRecord, error) {
	result := make([]SessionRecord, 0)
	err := s.view(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(migrations.SessionsBucket)).ForEach(func(key, raw []byte) error {
			if raw == nil {
				return nil
			}
			var value SessionRecord
			if err := decode(raw, &value); err != nil {
				return err
			}
			if err := value.Validate(); err != nil {
				return fmt.Errorf("%w: session %q: %v", ErrCorruptData, string(key), err)
			}
			result = append(result, value)
			return nil
		})
	})
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].SessionID < result[j].SessionID
		}
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result, err
}
