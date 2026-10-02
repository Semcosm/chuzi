// Package slot defines the logical execution-slot domain.
// A slot is a reusable execution resource independent from business accounts.
package slot

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

var (
	ErrInvalidConfig   = errors.New("slot: invalid configuration")
	ErrInvalidSlot     = errors.New("slot: invalid slot")
	ErrInvalidLease    = errors.New("slot: invalid lease")
	ErrLeaseHeld       = errors.New("slot: lease is held")
	ErrLeaseNotOwned   = errors.New("slot: lease is not owned")
	ErrLeaseExpired    = errors.New("slot: lease is expired")
	ErrTimeRegression  = errors.New("slot: lease time moved backwards")
	ErrPoolNotFound    = errors.New("slot: pool not found")
	ErrSlotNotFound    = errors.New("slot: slot not found")
	ErrSlotUnavailable = errors.New("slot: no matching slot is available")
	ErrInvalidStatus   = errors.New("slot: invalid status")
	ErrInvalidRequest  = errors.New("slot: invalid environment requirement")
)

type Status string

const (
	Unprovisioned Status = "unprovisioned"
	Provisioning  Status = "provisioning"
	Ready         Status = "ready"
	Leased        Status = "leased"
	Quarantined   Status = "quarantined"
	Draining      Status = "draining"
	Retiring      Status = "retiring"
	Deleted       Status = "deleted"
)

func (s Status) Valid() bool {
	switch s {
	case Unprovisioned, Provisioning, Ready, Leased, Quarantined, Draining, Retiring, Deleted:
		return true
	default:
		return false
	}
}

// EnvironmentRequirement selects slots using manifest metadata only.
type EnvironmentRequirement struct {
	EnvironmentID  string   `json:"environment_id,omitempty"`
	Version        string   `json:"version,omitempty"`
	Capabilities   []string `json:"capabilities,omitempty"`
	ManifestDigest string   `json:"manifest_digest,omitempty"`
	Signer         string   `json:"signer,omitempty"`
	RequireTrusted bool     `json:"require_trusted,omitempty"`
}

func (r EnvironmentRequirement) Normalize() (EnvironmentRequirement, error) {
	r.EnvironmentID = strings.TrimSpace(r.EnvironmentID)
	r.Version = strings.TrimSpace(r.Version)
	r.ManifestDigest = strings.TrimSpace(r.ManifestDigest)
	r.Signer = strings.TrimSpace(r.Signer)
	if r.EnvironmentID == "" && r.Version == "" && len(r.Capabilities) == 0 && r.ManifestDigest == "" && r.Signer == "" && !r.RequireTrusted {
		return r, nil
	}
	if r.EnvironmentID == "" || len(r.EnvironmentID) > 256 || strings.ContainsAny(r.EnvironmentID, "\r\n\t") || len(r.Version) > 128 || strings.ContainsAny(r.Version, "\r\n") {
		return EnvironmentRequirement{}, ErrInvalidRequest
	}
	if len(r.ManifestDigest) > 256 || len(r.Signer) > 256 || strings.ContainsAny(r.ManifestDigest, "\r\n\t") || strings.ContainsAny(r.Signer, "\r\n\t") {
		return EnvironmentRequirement{}, ErrInvalidRequest
	}
	caps, err := normalizeCapabilities(r.Capabilities)
	if err != nil {
		return EnvironmentRequirement{}, err
	}
	r.Capabilities = caps
	return r, nil
}

// PoolConfig is the persisted desired state for one logical pool.
type PoolConfig struct {
	PoolID             string    `json:"pool_id"`
	EnvironmentID      string    `json:"environment_id,omitempty"`
	EnvironmentVersion string    `json:"environment_version,omitempty"`
	DesiredSlots       int       `json:"desired_slots"`
	Capabilities       []string  `json:"capabilities,omitempty"`
	ManifestDigest     string    `json:"manifest_digest,omitempty"`
	Signer             string    `json:"signer,omitempty"`
	RequireTrusted     bool      `json:"require_trusted,omitempty"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// SlotPool is the descriptive alias used by callers that model the pool as a
// first-class resource. PoolConfig remains the persisted wire shape.
type SlotPool = PoolConfig

func (p PoolConfig) Validate() error {
	if !validID(p.PoolID) || p.DesiredSlots < 0 || p.DesiredSlots > 10000 || p.EnvironmentID == "" {
		return ErrInvalidConfig
	}
	_, err := (EnvironmentRequirement{EnvironmentID: p.EnvironmentID, Version: p.EnvironmentVersion, Capabilities: p.Capabilities, ManifestDigest: p.ManifestDigest, Signer: p.Signer, RequireTrusted: p.RequireTrusted}).Normalize()
	return err
}

func (p PoolConfig) Requirement() EnvironmentRequirement {
	return EnvironmentRequirement{EnvironmentID: p.EnvironmentID, Version: p.EnvironmentVersion, Capabilities: append([]string(nil), p.Capabilities...), ManifestDigest: p.ManifestDigest, Signer: p.Signer, RequireTrusted: p.RequireTrusted}
}

// EnvironmentSummary records only manifest/trust metadata needed for matching.
type EnvironmentSummary struct {
	EnvironmentID  string    `json:"environment_id"`
	Version        string    `json:"version,omitempty"`
	Generation     uint64    `json:"generation"`
	Capabilities   []string  `json:"capabilities,omitempty"`
	ManifestDigest string    `json:"manifest_digest,omitempty"`
	Signer         string    `json:"signer,omitempty"`
	Trusted        bool      `json:"trusted"`
	AgentVersion   string    `json:"agent_version,omitempty"`
	SessionState   string    `json:"session_state,omitempty"`
	DesktopReady   bool      `json:"desktop_ready,omitempty"`
	AgentHandle    string    `json:"agent_handle,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (e EnvironmentSummary) Validate() error {
	if !validID(e.EnvironmentID) || e.Generation == 0 || e.UpdatedAt.IsZero() || len(e.Version) > 128 || len(e.ManifestDigest) > 256 || len(e.Signer) > 256 || len(e.AgentVersion) > 64 || len(e.SessionState) > 32 || len(e.AgentHandle) > 256 || strings.ContainsAny(e.Version+e.ManifestDigest+e.Signer+e.AgentVersion+e.SessionState, "\r\n\t") || strings.ContainsAny(e.AgentHandle, "\r\n\t \\/") {
		return ErrInvalidConfig
	}
	_, err := normalizeCapabilities(e.Capabilities)
	return err
}

// Slot is the durable logical resource record.
type Slot struct {
	SlotID                string    `json:"slot_id"`
	Ordinal               int       `json:"ordinal"`
	PoolID                string    `json:"pool_id"`
	EnvironmentID         string    `json:"environment_id"`
	EnvironmentVersion    string    `json:"environment_version,omitempty"`
	EnvironmentGeneration uint64    `json:"environment_generation"`
	Capabilities          []string  `json:"capabilities,omitempty"`
	ManifestDigest        string    `json:"manifest_digest,omitempty"`
	Signer                string    `json:"signer,omitempty"`
	Trusted               bool      `json:"trusted"`
	AgentHandle           string    `json:"agent_handle,omitempty"`
	Status                Status    `json:"status"`
	HealthAt              time.Time `json:"health_at,omitempty"`
	FailureCount          int       `json:"failure_count"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

func (s Slot) Validate() error {
	if !validID(s.SlotID) || !validID(s.PoolID) || s.Ordinal < 1 || s.EnvironmentID == "" || !s.Status.Valid() || s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() || s.UpdatedAt.Before(s.CreatedAt) || s.FailureCount < 0 || len(s.EnvironmentID) > 256 || len(s.EnvironmentVersion) > 128 || len(s.ManifestDigest) > 256 || len(s.Signer) > 256 || len(s.AgentHandle) > 256 || strings.ContainsAny(s.EnvironmentID, "\r\n\t") || strings.ContainsAny(s.EnvironmentVersion, "\r\n\t") || strings.ContainsAny(s.ManifestDigest, "\r\n\t") || strings.ContainsAny(s.Signer+s.AgentHandle, "\r\n\t") {
		return ErrInvalidSlot
	}
	// Retiring can represent a logical slot that never reached provisioning;
	// it therefore may still have no environment generation.
	if s.EnvironmentGeneration == 0 && s.Status != Unprovisioned && s.Status != Provisioning && s.Status != Retiring && s.Status != Deleted {
		return fmt.Errorf("%w: active slot has no environment generation", ErrInvalidSlot)
	}
	_, err := normalizeCapabilities(s.Capabilities)
	return err
}

func (s Slot) Matches(requirement EnvironmentRequirement) bool {
	r, err := requirement.Normalize()
	if err != nil || s.Status != Ready || s.EnvironmentID != r.EnvironmentID {
		return false
	}
	if r.Version != "" && s.EnvironmentVersion != r.Version {
		return false
	}
	if r.ManifestDigest != "" && s.ManifestDigest != r.ManifestDigest {
		return false
	}
	if r.Signer != "" && s.Signer != r.Signer {
		return false
	}
	if r.RequireTrusted && !s.Trusted {
		return false
	}
	available := make(map[string]struct{}, len(s.Capabilities))
	for _, capability := range s.Capabilities {
		available[capability] = struct{}{}
	}
	for _, capability := range r.Capabilities {
		if _, ok := available[capability]; !ok {
			return false
		}
	}
	return true
}

// Lease is a slot-specific ownership interval bound to one request/account.
type Lease struct {
	LeaseID               string    `json:"lease_id"`
	SlotID                string    `json:"slot_id"`
	PoolID                string    `json:"pool_id"`
	RequestID             string    `json:"request_id"`
	AccountID             string    `json:"account_id"`
	Owner                 string    `json:"owner"`
	EnvironmentGeneration uint64    `json:"environment_generation"`
	AcquiredAt            time.Time `json:"acquired_at"`
	LastHeartbeat         time.Time `json:"last_heartbeat"`
	ExpiresAt             time.Time `json:"expires_at"`
}

func (l Lease) Validate() error {
	if !validID(l.LeaseID) || !validID(l.SlotID) || !validID(l.PoolID) || !validID(l.RequestID) || !validID(l.AccountID) || !validID(l.Owner) || l.EnvironmentGeneration == 0 || l.AcquiredAt.IsZero() || l.LastHeartbeat.IsZero() || l.ExpiresAt.IsZero() || l.LastHeartbeat.Before(l.AcquiredAt) || !l.ExpiresAt.After(l.LastHeartbeat) {
		return ErrInvalidLease
	}
	return nil
}

func (l Lease) Expired(now time.Time) bool { return !now.Before(l.ExpiresAt) }

func AcquireLease(current *Lease, now time.Time, leaseID, slotID, poolID, requestID, accountID, owner string, generation uint64, ttl time.Duration) (Lease, error) {
	if now.IsZero() || ttl <= 0 || generation == 0 || !validID(leaseID) || !validID(slotID) || !validID(poolID) || !validID(requestID) || !validID(accountID) || !validID(owner) {
		return Lease{}, ErrInvalidLease
	}
	if current != nil {
		if err := current.Validate(); err != nil {
			return Lease{}, err
		}
		if !current.Expired(now) {
			return Lease{}, fmt.Errorf("%w: %s", ErrLeaseHeld, current.LeaseID)
		}
	}
	expires := now.Add(ttl)
	if !expires.After(now) {
		return Lease{}, ErrInvalidLease
	}
	return Lease{LeaseID: leaseID, SlotID: slotID, PoolID: poolID, RequestID: requestID, AccountID: accountID, Owner: owner, EnvironmentGeneration: generation, AcquiredAt: now, LastHeartbeat: now, ExpiresAt: expires}, nil
}

// AcquireSlotLease is the explicit resource-named spelling of AcquireLease.
func AcquireSlotLease(current *Lease, now time.Time, leaseID, slotID, poolID, requestID, accountID, owner string, generation uint64, ttl time.Duration) (Lease, error) {
	return AcquireLease(current, now, leaseID, slotID, poolID, requestID, accountID, owner, generation, ttl)
}

func HeartbeatLease(lease Lease, now time.Time, leaseID, owner string, ttl time.Duration) (Lease, error) {
	if err := lease.Validate(); err != nil {
		return Lease{}, err
	}
	if now.IsZero() || ttl <= 0 {
		return Lease{}, ErrInvalidLease
	}
	if lease.LeaseID != leaseID || lease.Owner != owner {
		return Lease{}, ErrLeaseNotOwned
	}
	if lease.Expired(now) {
		return Lease{}, ErrLeaseExpired
	}
	if now.Before(lease.LastHeartbeat) {
		return Lease{}, ErrTimeRegression
	}
	lease.LastHeartbeat = now
	lease.ExpiresAt = now.Add(ttl)
	if !lease.ExpiresAt.After(now) {
		return Lease{}, ErrInvalidLease
	}
	return lease, nil
}

// HeartbeatSlotLease is the explicit resource-named spelling of HeartbeatLease.
func HeartbeatSlotLease(lease Lease, now time.Time, leaseID, owner string, ttl time.Duration) (Lease, error) {
	return HeartbeatLease(lease, now, leaseID, owner, ttl)
}

func ReleaseLease(lease Lease, leaseID, owner string) error {
	if err := lease.Validate(); err != nil {
		return err
	}
	if lease.LeaseID != leaseID || lease.Owner != owner {
		return ErrLeaseNotOwned
	}
	return nil
}

// ReleaseSlotLease is the explicit resource-named spelling of ReleaseLease.
func ReleaseSlotLease(lease Lease, leaseID, owner string) error {
	return ReleaseLease(lease, leaseID, owner)
}

// StatusCounts is a redaction-safe pool projection.
type StatusCounts struct {
	PoolID             string `json:"pool_id"`
	EnvironmentID      string `json:"environment_id"`
	EnvironmentVersion string `json:"environment_version"`
	Desired            int    `json:"desired"`
	Ready              int    `json:"ready"`
	Leased             int    `json:"leased"`
	Quarantined        int    `json:"quarantined"`
	Draining           int    `json:"draining"`
	Provisioning       int    `json:"provisioning"`
	Retiring           int    `json:"retiring"`
	Unprovisioned      int    `json:"unprovisioned"`
}

func SortSlots(slots []Slot) {
	sort.Slice(slots, func(i, j int) bool {
		if slots[i].Ordinal != slots[j].Ordinal {
			return slots[i].Ordinal < slots[j].Ordinal
		}
		return slots[i].SlotID < slots[j].SlotID
	})
}

func normalizeCapabilities(values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 128 || strings.ContainsAny(value, "\r\n\t") {
			return nil, ErrInvalidConfig
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func validID(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && len(value) <= 256 && !strings.ContainsAny(value, "\r\n\t ")
}
