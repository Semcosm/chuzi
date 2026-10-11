package sessionruntime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type targetJournalFake struct {
	mu           sync.Mutex
	reservations map[string]TargetReservation
}

func newTargetJournalFake() *targetJournalFake {
	return &targetJournalFake{reservations: make(map[string]TargetReservation)}
}
func (j *targetJournalFake) List(context.Context) ([]TargetReservation, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	result := make([]TargetReservation, 0, len(j.reservations))
	for _, reservation := range j.reservations {
		result = append(result, reservation)
	}
	return result, nil
}
func (j *targetJournalFake) Claim(_ context.Context, reservation TargetReservation) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, exists := j.reservations[reservation.TargetID]; exists {
		return ErrTargetCollision
	}
	j.reservations[reservation.TargetID] = reservation
	return nil
}
func (j *targetJournalFake) Release(_ context.Context, reservation TargetReservation) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	current, exists := j.reservations[reservation.TargetID]
	if !exists || current != reservation {
		return ErrTargetOwnership
	}
	delete(j.reservations, reservation.TargetID)
	return nil
}

func TestLoopbackAllocatorSeparatesConcurrentSlotsAndReclaimsOwnedLease(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	journal := newTargetJournalFake()
	allocator, err := NewLoopbackAllocator(journal, LoopbackAllocatorOptions{
		Now:       func() time.Time { return now },
		Addresses: []string{"127.0.0.2", "127.0.0.3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := allocator.Allocate(context.Background(), validIdentity())
	if err != nil {
		t.Fatal(err)
	}
	secondIdentity := validIdentity()
	secondIdentity.SlotID = "slot-002"
	second, err := allocator.Allocate(context.Background(), secondIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if first.Endpoint() == second.Endpoint() || first.Endpoint() != "127.0.0.2:3389" || second.Endpoint() != "127.0.0.3:3389" {
		t.Fatalf("target collision or unexpected endpoint: %s / %s", first.Endpoint(), second.Endpoint())
	}
	if _, err := allocator.Allocate(context.Background(), validIdentity()); !errors.Is(err, ErrTargetUnavailable) {
		t.Fatalf("full target pool error=%v", err)
	}
	if err := allocator.Release(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	third, err := allocator.Allocate(context.Background(), validIdentity())
	if err != nil {
		t.Fatal(err)
	}
	if third.Endpoint() != first.Endpoint() {
		t.Fatalf("released target was not reused: %s vs %s", third.Endpoint(), first.Endpoint())
	}
}

func TestLoopbackAllocatorRejectsNonLoopbackAndStaleRelease(t *testing.T) {
	journal := newTargetJournalFake()
	if _, err := NewLoopbackAllocator(journal, LoopbackAllocatorOptions{Addresses: []string{"192.0.2.1"}}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("non-loopback address accepted: %v", err)
	}
	now := time.Unix(100, 0).UTC()
	allocator, err := NewLoopbackAllocator(journal, LoopbackAllocatorOptions{Now: func() time.Time { return now }, Addresses: []string{"127.0.0.2"}})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := allocator.Allocate(context.Background(), validIdentity())
	if err != nil {
		t.Fatal(err)
	}
	stale := lease
	stale.reservation.Owner = "other-owner"
	if err := allocator.Release(context.Background(), stale); !errors.Is(err, ErrTargetOwnership) {
		t.Fatalf("stale ownership release error=%v", err)
	}
	if err := allocator.ReclaimExpired(context.Background(), now.Add(11*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := allocator.Allocate(context.Background(), validIdentity()); err != nil {
		t.Fatalf("expired reservation was not reclaimed: %v", err)
	}
}

func TestLoopbackAllocatorReclaimsExpiredReservationBeforeClaim(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	journal := newTargetJournalFake()
	allocator, err := NewLoopbackAllocator(journal, LoopbackAllocatorOptions{Now: func() time.Time { return now }, Lease: time.Minute, Addresses: []string{"127.0.0.2"}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := allocator.Allocate(context.Background(), validIdentity())
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	second, err := allocator.Allocate(context.Background(), validIdentity())
	if err != nil {
		t.Fatal(err)
	}
	if second.Endpoint() != first.Endpoint() || second.Reservation().LeaseID == first.Reservation().LeaseID {
		t.Fatal("expired reservation was not replaced atomically")
	}
}

func TestLoopbackAllocatorConcurrentSlotsDoNotShareTarget(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	journal := newTargetJournalFake()
	allocator, err := NewLoopbackAllocator(journal, LoopbackAllocatorOptions{
		Now:       func() time.Time { return now },
		Addresses: []string{"127.0.0.2", "127.0.0.3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	identities := []Identity{validIdentity(), validIdentity()}
	identities[1].SlotID = "slot-002"
	results := make(chan LoopbackTarget, len(identities))
	errs := make(chan error, len(identities))
	var wait sync.WaitGroup
	for _, identity := range identities {
		wait.Add(1)
		go func(identity Identity) {
			defer wait.Done()
			target, allocateErr := allocator.Allocate(context.Background(), identity)
			if allocateErr != nil {
				errs <- allocateErr
				return
			}
			results <- target
		}(identity)
	}
	wait.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var endpoints []string
	for target := range results {
		endpoints = append(endpoints, target.Endpoint())
	}
	if len(endpoints) != 2 || endpoints[0] == endpoints[1] {
		t.Fatalf("concurrent target assignment collided: %v", endpoints)
	}
}
