package main

import (
	"context"
	"fmt"
	"github.com/Semcosm/chuzi/internal/sessionruntime"
	"github.com/Semcosm/chuzi/internal/slotwindows"
)

func brokerTargetIdentity(o slotwindows.SessionBrokerOwnership) sessionruntime.Identity {
	return sessionruntime.Identity{PoolID: o.PoolID, SlotID: o.SlotID, Ordinal: o.Ordinal, Generation: o.Generation, SID: o.SID, Owner: o.Owner}
}

func newBrokerAllocator(journal sessionruntime.TargetJournal) (*sessionruntime.LoopbackAllocator, error) {
	// Fixed aliases are installation-owned, never request-selected.
	var ids, addresses []string
	for i := 0; i < 1024; i++ {
		ids = append(ids, fmt.Sprintf("rdp-%04d", i+1))
		addresses = append(addresses, fmt.Sprintf("127.0.%d.%d", 1+i/253, 2+i%253))
	}
	return sessionruntime.NewLoopbackAllocator(journal, sessionruntime.LoopbackAllocatorOptions{TargetIDs: ids, Addresses: addresses, RetainUntilRelease: true})
}

func findBrokerTarget(ctx context.Context, journal sessionruntime.TargetJournal, allocator *sessionruntime.LoopbackAllocator, ownership slotwindows.SessionBrokerOwnership) (sessionruntime.LoopbackTarget, bool, error) {
	reservations, err := journal.List(ctx)
	if err != nil {
		return sessionruntime.LoopbackTarget{}, false, err
	}
	for _, r := range reservations {
		if r.SlotID != ownership.SlotID {
			continue
		}
		if r.PoolID != ownership.PoolID || r.SID != ownership.SID || r.Ordinal != ownership.Ordinal || r.Generation != ownership.Generation || r.Owner != ownership.Owner {
			return sessionruntime.LoopbackTarget{}, false, sessionruntime.ErrTargetOwnership
		}
		target, err := allocator.Restore(r)
		return target, true, err
	}
	return sessionruntime.LoopbackTarget{}, false, nil
}
