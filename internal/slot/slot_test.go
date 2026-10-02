package slot

import (
	"errors"
	"testing"
	"time"
)

var slotTestTime = time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)

func TestEnvironmentRequirementMatchesManifestAndCapabilities(t *testing.T) {
	value := Slot{SlotID: "pool-001", Ordinal: 1, PoolID: "pool", EnvironmentID: "chuzi-environment/v1", EnvironmentVersion: "1.2.3", EnvironmentGeneration: 7, Capabilities: []string{"cdp", "windows-desktop"}, ManifestDigest: "sha256:abc", Signer: "signer-a", Trusted: true, Status: Ready, CreatedAt: slotTestTime, UpdatedAt: slotTestTime}
	requirement := EnvironmentRequirement{EnvironmentID: "chuzi-environment/v1", Version: "1.2.3", Capabilities: []string{"windows-desktop"}, ManifestDigest: "sha256:abc", Signer: "signer-a", RequireTrusted: true}
	if !value.Matches(requirement) {
		t.Fatal("matching slot was rejected")
	}
	for _, mismatch := range []EnvironmentRequirement{
		{EnvironmentID: "other"},
		{EnvironmentID: "chuzi-environment/v1", Version: "9.9.9"},
		{EnvironmentID: "chuzi-environment/v1", Capabilities: []string{"missing"}},
		{EnvironmentID: "chuzi-environment/v1", ManifestDigest: "sha256:other"},
		{EnvironmentID: "chuzi-environment/v1", Signer: "other-signer"},
	} {
		if value.Matches(mismatch) {
			t.Fatalf("mismatched requirement accepted: %#v", mismatch)
		}
	}
}

func TestSlotLeaseLifecycleRejectsStaleOwner(t *testing.T) {
	lease, err := AcquireLease(nil, slotTestTime, "lease-1", "pool-001", "pool", "request-1", "account-1", "owner-1", 7, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireLease(&lease, slotTestTime.Add(time.Second), "lease-2", "pool-001", "pool", "request-2", "account-2", "owner-2", 7, time.Minute); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("active lease acquire error = %v", err)
	}
	updated, err := HeartbeatLease(lease, slotTestTime.Add(10*time.Second), "lease-1", "owner-1", time.Minute)
	if err != nil || !updated.ExpiresAt.After(lease.ExpiresAt) {
		t.Fatalf("heartbeat = %#v, %v", updated, err)
	}
	if err := ReleaseLease(updated, "lease-2", "owner-2"); !errors.Is(err, ErrLeaseNotOwned) {
		t.Fatalf("stale release error = %v", err)
	}
	if _, err := AcquireLease(&updated, updated.ExpiresAt, "lease-2", "pool-001", "pool", "request-2", "account-2", "owner-2", 7, time.Minute); err != nil {
		t.Fatalf("expired lease should be reclaimable at exact expiry boundary: %v", err)
	}
}

func TestDeletedSlotMayHaveNoEnvironmentGeneration(t *testing.T) {
	value := Slot{
		SlotID:        "pool-001",
		Ordinal:       1,
		PoolID:        "pool",
		EnvironmentID: "chuzi-environment/v1",
		Status:        Deleted,
		CreatedAt:     slotTestTime,
		UpdatedAt:     slotTestTime,
	}
	if err := value.Validate(); err != nil {
		t.Fatalf("deleted slot validation = %v", err)
	}
}

func TestEnvironmentSummaryRejectsPathLikeAgentHandle(t *testing.T) {
	value := EnvironmentSummary{
		EnvironmentID: "chuzi-environment/v1", Generation: 1, UpdatedAt: slotTestTime,
		AgentHandle: `C:\Users\operator\agent.exe`,
	}
	if err := value.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("path-like agent handle validation = %v", err)
	}
}
