package account

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrInvalidLease   = errors.New("account: invalid lease")
	ErrLeaseHeld      = errors.New("account: lease is held")
	ErrLeaseNotOwned  = errors.New("account: lease is not owned")
	ErrLeaseExpired   = errors.New("account: lease is expired")
	ErrTimeRegression = errors.New("account: lease time moved backwards")
)

// Lease is a persisted ownership interval for a running account operation.
type Lease struct {
	LeaseID       string    `json:"lease_id"`
	Owner         string    `json:"owner"`
	AcquiredAt    time.Time `json:"acquired_at"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
	ExpiresAt     time.Time `json:"expires_at"`
}

func (l Lease) validate() error {
	if strings.TrimSpace(l.LeaseID) == "" || strings.TrimSpace(l.Owner) == "" ||
		l.AcquiredAt.IsZero() || l.LastHeartbeat.IsZero() || l.ExpiresAt.IsZero() {
		return ErrInvalidLease
	}
	if l.LastHeartbeat.Before(l.AcquiredAt) || !l.ExpiresAt.After(l.LastHeartbeat) {
		return ErrInvalidLease
	}
	return nil
}

// Validate checks a lease loaded from persistence.
func (l Lease) Validate() error {
	return l.validate()
}

// Expired reports whether now is at or after the lease expiry.
func (l Lease) Expired(now time.Time) bool {
	return !now.Before(l.ExpiresAt)
}

// AcquireLease obtains a new lease when no active lease exists. An expired
// lease is recoverable, which is the restart/recovery primitive for later
// persistence and scheduling layers.
func AcquireLease(current *Lease, now time.Time, leaseID, owner string, ttl time.Duration) (Lease, error) {
	if now.IsZero() || strings.TrimSpace(leaseID) == "" || strings.TrimSpace(owner) == "" || ttl <= 0 {
		return Lease{}, ErrInvalidLease
	}
	if current != nil {
		if err := current.validate(); err != nil {
			return Lease{}, err
		}
		if !current.Expired(now) {
			return Lease{}, fmt.Errorf("%w: %s", ErrLeaseHeld, current.LeaseID)
		}
	}
	expiresAt := now.Add(ttl)
	if !expiresAt.After(now) {
		return Lease{}, ErrInvalidLease
	}
	return Lease{
		LeaseID:       leaseID,
		Owner:         owner,
		AcquiredAt:    now,
		LastHeartbeat: now,
		ExpiresAt:     expiresAt,
	}, nil
}

// HeartbeatLease extends a lease only for its current owner and before expiry.
func HeartbeatLease(lease Lease, now time.Time, leaseID, owner string, ttl time.Duration) (Lease, error) {
	if err := lease.validate(); err != nil {
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
	expiresAt := now.Add(ttl)
	if !expiresAt.After(now) {
		return Lease{}, ErrInvalidLease
	}
	lease.LastHeartbeat = now
	lease.ExpiresAt = expiresAt
	return lease, nil
}

// ReleaseLease clears a lease for its current owner. Expired leases may also
// be explicitly released after recovery bookkeeping has completed.
func ReleaseLease(lease Lease, leaseID, owner string) (Lease, error) {
	if err := lease.validate(); err != nil {
		return Lease{}, err
	}
	if lease.LeaseID != leaseID || lease.Owner != owner {
		return Lease{}, ErrLeaseNotOwned
	}
	return Lease{}, nil
}
