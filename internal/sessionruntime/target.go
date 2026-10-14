package sessionruntime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	targetReservationVersion = 1
	defaultRDPPort           = 3389
	defaultTargetLease       = 10 * time.Minute
)

var (
	ErrTargetUnavailable = errors.New("sessionruntime: loopback target unavailable")
	ErrTargetCollision   = errors.New("sessionruntime: loopback target collision")
	ErrTargetOwnership   = errors.New("sessionruntime: loopback target ownership mismatch")
)

// TargetReservation is the durable, endpoint-free part of a loopback target
// lease. TargetID resolves through the installation-owned address list; an
// arbitrary host or RDP profile never enters a request or journal record.
type TargetReservation struct {
	SID        string    `json:"sid"`
	Ordinal    int       `json:"ordinal"`
	Version    int       `json:"version"`
	TargetID   string    `json:"target_id"`
	PoolID     string    `json:"pool_id"`
	SlotID     string    `json:"slot_id"`
	Generation uint64    `json:"generation"`
	Owner      string    `json:"owner"`
	LeaseID    string    `json:"lease_id"`
	ExpiresAt  time.Time `json:"expires_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (r TargetReservation) Validate() error {
	if r.Version != targetReservationVersion || !validID(r.TargetID) || !validID(r.PoolID) || !validID(r.SlotID) || r.Generation == 0 || !validID(r.Owner) || !validID(r.LeaseID) || r.ExpiresAt.IsZero() || r.UpdatedAt.IsZero() {
		return ErrInvalidArgument
	}
	return nil
}

// TargetJournal is the durable compare-and-claim boundary supplied by the
// service owner. Claim must be atomic with respect to all slots. It is not a
// second Core database.
type TargetJournal interface {
	List(context.Context) ([]TargetReservation, error)
	Claim(context.Context, TargetReservation) error
	Release(context.Context, TargetReservation) error
}

// LoopbackTarget is an ephemeral credential-boundary view of a claimed target.
// Its address and port are deliberately unexported so it cannot be serialized
// into Core/UI or broker JSON by accident.
type LoopbackTarget struct {
	reservation TargetReservation
	address     string
	port        uint16
}

func (t LoopbackTarget) Reservation() TargetReservation { return t.reservation }
func (t LoopbackTarget) Endpoint() string               { return net.JoinHostPort(t.address, fmt.Sprint(t.port)) }

type LoopbackAllocatorOptions struct {
	Now       func() time.Time
	Lease     time.Duration
	TargetIDs []string
	Addresses []string
	Port      uint16
	// RetainUntilRelease prevents time-based reuse while native side effects may exist.
	RetainUntilRelease bool
}

// LoopbackAllocator assigns only installation-configured loopback aliases.
// The default address set is 127.0.0.2 through 127.0.0.254; callers cannot
// supply an arbitrary endpoint through the slot identity request.
type LoopbackAllocator struct {
	journal            TargetJournal
	now                func() time.Time
	lease              time.Duration
	retainUntilRelease bool
	port               uint16
	targets            []loopbackTarget
	known              map[string]struct{}
	mu                 sync.Mutex
}

type loopbackTarget struct {
	id      string
	address string
}

func NewLoopbackAllocator(journal TargetJournal, options LoopbackAllocatorOptions) (*LoopbackAllocator, error) {
	if journal == nil {
		return nil, ErrInvalidArgument
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	lease := options.Lease
	if lease <= 0 {
		lease = defaultTargetLease
	}
	port := options.Port
	if port == 0 {
		port = defaultRDPPort
	}
	if port != defaultRDPPort {
		return nil, ErrUnsupported
	}
	addresses := options.Addresses
	if len(addresses) == 0 {
		addresses = make([]string, 0, 253)
		for octet := 2; octet <= 254; octet++ {
			addresses = append(addresses, fmt.Sprintf("127.0.0.%d", octet))
		}
	}
	ids := options.TargetIDs
	if len(ids) != 0 && len(ids) != len(addresses) {
		return nil, ErrInvalidArgument
	}
	targets := make([]loopbackTarget, 0, len(addresses))
	seen := make(map[string]struct{}, len(addresses))
	for index, address := range addresses {
		address = strings.TrimSpace(address)
		ip := net.ParseIP(address).To4()
		if ip == nil || ip[0] != 127 || ip[3] < 2 || ip[3] > 254 {
			return nil, ErrInvalidArgument
		}
		id := fmt.Sprintf("loopback-%03d", int(ip[3]))
		if len(ids) != 0 {
			id = strings.TrimSpace(ids[index])
		}
		if !validID(id) {
			return nil, ErrInvalidArgument
		}
		if _, ok := seen[id]; ok {
			return nil, ErrInvalidArgument
		}
		seen[id] = struct{}{}
		targets = append(targets, loopbackTarget{id: id, address: address})
	}
	known := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		known[target.id] = struct{}{}
	}
	return &LoopbackAllocator{journal: journal, now: now, lease: lease, port: port, retainUntilRelease: options.RetainUntilRelease, targets: targets, known: known}, nil
}

// Allocate claims the lowest available alias. Ownership is checked against
// every durable reservation before the atomic Claim call, so collisions and
// stale records fail closed.
func (a *LoopbackAllocator) Allocate(ctx context.Context, identity Identity) (LoopbackTarget, error) {
	if a == nil || a.journal == nil || identity.Validate() != nil {
		return LoopbackTarget{}, ErrInvalidArgument
	}
	if ctx == nil {
		return LoopbackTarget{}, ErrInvalidArgument
	}
	select {
	case <-ctx.Done():
		return LoopbackTarget{}, ctx.Err()
	default:
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	reservations, err := a.journal.List(ctx)
	if err != nil {
		return LoopbackTarget{}, ErrTargetUnavailable
	}
	used := make(map[string]struct{}, len(reservations))
	now := a.now().UTC()
	for _, reservation := range reservations {
		if reservation.Validate() != nil {
			return LoopbackTarget{}, ErrTargetUnavailable
		}
		if _, ok := a.known[reservation.TargetID]; !ok {
			return LoopbackTarget{}, ErrTargetUnavailable
		}
		if !a.retainUntilRelease && !now.Before(reservation.ExpiresAt) {
			// Reclaim only the exact observed journal record. A compare
			// failure is not permission to delete a newer allocation.
			if err := a.journal.Release(ctx, reservation); err != nil {
				return LoopbackTarget{}, ErrTargetUnavailable
			}
			continue
		}
		used[reservation.TargetID] = struct{}{}
	}
	for _, target := range a.targets {
		if _, ok := used[target.id]; ok {
			continue
		}
		leaseID, err := randomLeaseID()
		if err != nil {
			return LoopbackTarget{}, ErrTargetUnavailable
		}
		reservation := TargetReservation{SID: identity.SID, Ordinal: identity.Ordinal, Version: targetReservationVersion, TargetID: target.id, PoolID: identity.PoolID, SlotID: identity.SlotID, Generation: identity.Generation, Owner: identity.Owner, LeaseID: leaseID, ExpiresAt: now.Add(a.lease), UpdatedAt: now}
		if err := a.journal.Claim(ctx, reservation); err != nil {
			if errors.Is(err, ErrTargetCollision) {
				used[target.id] = struct{}{}
				continue
			}
			return LoopbackTarget{}, ErrTargetUnavailable
		}
		return LoopbackTarget{reservation: reservation, address: target.address, port: a.port}, nil
	}
	return LoopbackTarget{}, ErrTargetUnavailable
}

func (a *LoopbackAllocator) Release(ctx context.Context, target LoopbackTarget) error {
	if a == nil || a.journal == nil || target.reservation.Validate() != nil || ctx == nil {
		return ErrTargetOwnership
	}
	if err := a.journal.Release(ctx, target.reservation); err != nil {
		return ErrTargetOwnership
	}
	return nil
}

// ReclaimExpired removes only expired reservations and preserves every active
// lease. A journal implementation must make Release compare all ownership
// fields atomically.
func (a *LoopbackAllocator) ReclaimExpired(ctx context.Context, now time.Time) error {
	if a == nil || a.journal == nil || ctx == nil || now.IsZero() {
		return ErrInvalidArgument
	}
	reservations, err := a.journal.List(ctx)
	if err != nil {
		return ErrTargetUnavailable
	}
	sort.Slice(reservations, func(i, j int) bool { return reservations[i].TargetID < reservations[j].TargetID })
	for _, reservation := range reservations {
		if reservation.Validate() != nil {
			return ErrTargetUnavailable
		}
		if _, ok := a.known[reservation.TargetID]; !ok {
			return ErrTargetUnavailable
		}
		if !a.retainUntilRelease && !now.Before(reservation.ExpiresAt) {
			if err := a.journal.Release(ctx, reservation); err != nil {
				return ErrTargetOwnership
			}
		}
	}
	return nil
}

func randomLeaseID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "lease-" + hex.EncodeToString(raw[:]), nil
}

// Restore resolves an exact durable reservation using only installation aliases.
func (a *LoopbackAllocator) Restore(reservation TargetReservation) (LoopbackTarget, error) {
	if a == nil || reservation.Validate() != nil {
		return LoopbackTarget{}, ErrTargetOwnership
	}
	for _, target := range a.targets {
		if target.id == reservation.TargetID {
			return LoopbackTarget{reservation: reservation, address: target.address, port: a.port}, nil
		}
	}
	return LoopbackTarget{}, ErrTargetOwnership
}
