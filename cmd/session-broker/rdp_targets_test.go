package main

import (
	"context"
	"github.com/Semcosm/chuzi/internal/sessionruntime"
	"github.com/Semcosm/chuzi/internal/slotwindows"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBrokerTargetsSeparatePoolsAndPreserveRestartFence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "targets.json")
	journal, err := slotwindows.NewFileBrokerTargetJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	allocator, err := newBrokerAllocator(journal)
	if err != nil {
		t.Fatal(err)
	}
	first := slotwindows.SessionBrokerOwnership{PoolID: "pool-a", SlotID: "pool-a-001", Ordinal: 1, Generation: 7, SID: "S-1-5-21-1-2-3-4", Owner: "service"}
	second := first
	second.PoolID = "pool-b"
	second.SlotID = "pool-b-001"
	second.SID = "S-1-5-21-1-2-3-5"
	targetA, err := allocator.Allocate(ctx, brokerTargetIdentity(first))
	if err != nil {
		t.Fatal(err)
	}
	targetB, err := allocator.Allocate(ctx, brokerTargetIdentity(second))
	if err != nil {
		t.Fatal(err)
	}
	if targetA.Endpoint() == targetB.Endpoint() {
		t.Fatal("ordinal=1 targets collided")
	}
	restarted, err := slotwindows.NewFileBrokerTargetJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	restored, exists, err := findBrokerTarget(ctx, restarted, allocator, first)
	if err != nil || !exists || restored.Reservation() != targetA.Reservation() {
		t.Fatalf("restart fence lost %v %v", exists, err)
	}
	stale := first
	stale.Generation++
	if _, _, err := findBrokerTarget(ctx, restarted, allocator, stale); err == nil {
		t.Fatal("stale generation adopted target")
	}
	stale = first
	stale.PoolID = "other"
	if _, _, err := findBrokerTarget(ctx, restarted, allocator, stale); err == nil {
		t.Fatal("other pool adopted target")
	}
	if err := allocator.ReclaimExpired(ctx, time.Now().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if entries, err := journal.List(ctx); err != nil || len(entries) != 2 {
		t.Fatalf("expiry freed live native target: %v %v", entries, err)
	}
	if err := allocator.Release(ctx, restored); err != nil {
		t.Fatal(err)
	}
	if _, exists, err := findBrokerTarget(ctx, journal, allocator, first); err != nil || exists {
		t.Fatal("released target remains")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, sensitive := range []string{"127.0.", "TERMSRV", "password", "username"} {
		if strings.Contains(string(raw), sensitive) {
			t.Fatalf("journal leaks %s", sensitive)
		}
	}
	wrong := targetB.Reservation()
	wrong.Owner = "other"
	if err := journal.Release(ctx, wrong); err != sessionruntime.ErrTargetOwnership {
		t.Fatalf("wrong owner release %v", err)
	}
}
