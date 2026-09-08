package account

import (
	"errors"
	"testing"
	"time"
)

var leaseTestTime = time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)

func TestAcquireLeaseAndRecoverAfterExpiry(t *testing.T) {
	lease, err := AcquireLease(nil, leaseTestTime, "lease-1", "worker-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if lease.LeaseID != "lease-1" || lease.Owner != "worker-1" ||
		!lease.AcquiredAt.Equal(leaseTestTime) || !lease.LastHeartbeat.Equal(leaseTestTime) ||
		!lease.ExpiresAt.Equal(leaseTestTime.Add(time.Minute)) {
		t.Fatalf("unexpected lease: %#v", lease)
	}
	if lease.Expired(leaseTestTime.Add(59 * time.Second)) {
		t.Fatal("lease must remain active before expiry")
	}

	if _, err := AcquireLease(&lease, leaseTestTime.Add(time.Second), "lease-2", "worker-2", time.Minute); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("active lease error = %v, want ErrLeaseHeld", err)
	}

	recovered, err := AcquireLease(&lease, lease.ExpiresAt, "lease-2", "worker-2", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.LeaseID != "lease-2" || !recovered.AcquiredAt.Equal(lease.ExpiresAt) {
		t.Fatalf("unexpected recovered lease: %#v", recovered)
	}
}

func TestHeartbeatLeaseEnforcesOwnershipTimeAndExpiry(t *testing.T) {
	lease, err := AcquireLease(nil, leaseTestTime, "lease-1", "worker-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	heartbeatAt := leaseTestTime.Add(10 * time.Second)
	updated, err := HeartbeatLease(lease, heartbeatAt, "lease-1", "worker-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.LastHeartbeat.Equal(heartbeatAt) || !updated.ExpiresAt.Equal(heartbeatAt.Add(time.Minute)) {
		t.Fatalf("unexpected heartbeat result: %#v", updated)
	}

	if _, err := HeartbeatLease(updated, heartbeatAt, "wrong", "worker-1", time.Minute); !errors.Is(err, ErrLeaseNotOwned) {
		t.Fatalf("wrong lease id error = %v, want ErrLeaseNotOwned", err)
	}
	if _, err := HeartbeatLease(updated, heartbeatAt, "lease-1", "worker-2", time.Minute); !errors.Is(err, ErrLeaseNotOwned) {
		t.Fatalf("wrong owner error = %v, want ErrLeaseNotOwned", err)
	}
	if _, err := HeartbeatLease(updated, leaseTestTime, "lease-1", "worker-1", time.Minute); !errors.Is(err, ErrTimeRegression) {
		t.Fatalf("time regression error = %v, want ErrTimeRegression", err)
	}
	if _, err := HeartbeatLease(updated, updated.ExpiresAt, "lease-1", "worker-1", time.Minute); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("expired heartbeat error = %v, want ErrLeaseExpired", err)
	}
}

func TestReleaseLeaseAndValidateInputs(t *testing.T) {
	lease, err := AcquireLease(nil, leaseTestTime, "lease-1", "worker-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReleaseLease(lease, "lease-1", "worker-2"); !errors.Is(err, ErrLeaseNotOwned) {
		t.Fatalf("wrong owner release error = %v, want ErrLeaseNotOwned", err)
	}
	cleared, err := ReleaseLease(lease, "lease-1", "worker-1")
	if err != nil {
		t.Fatal(err)
	}
	if cleared != (Lease{}) {
		t.Fatalf("cleared lease = %#v, want zero lease", cleared)
	}

	invalid := []Lease{
		{},
		{LeaseID: "lease", Owner: "worker", AcquiredAt: leaseTestTime, LastHeartbeat: leaseTestTime.Add(-time.Second), ExpiresAt: leaseTestTime.Add(time.Minute)},
		{LeaseID: "lease", Owner: "worker", AcquiredAt: leaseTestTime, LastHeartbeat: leaseTestTime, ExpiresAt: leaseTestTime},
	}
	for _, candidate := range invalid {
		if !errors.Is(candidate.Validate(), ErrInvalidLease) {
			t.Errorf("lease %#v should be invalid", candidate)
		}
	}

	invalidAcquires := []struct {
		now   time.Time
		id    string
		owner string
		ttl   time.Duration
	}{
		{time.Time{}, "lease", "worker", time.Minute},
		{leaseTestTime, "", "worker", time.Minute},
		{leaseTestTime, "lease", "", time.Minute},
		{leaseTestTime, "lease", "worker", 0},
	}
	for _, candidate := range invalidAcquires {
		if _, err := AcquireLease(nil, candidate.now, candidate.id, candidate.owner, candidate.ttl); !errors.Is(err, ErrInvalidLease) {
			t.Errorf("invalid acquire %#v error = %v, want ErrInvalidLease", candidate, err)
		}
	}
}

func TestLeaseRecoveryUsesPersistedExpiryAfterRestart(t *testing.T) {
	persisted, err := AcquireLease(nil, leaseTestTime, "lease-before-restart", "old-worker", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	restartTime := leaseTestTime.Add(time.Minute)
	recovered, err := AcquireLease(&persisted, restartTime, "lease-after-restart", "new-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Owner != "new-worker" || !recovered.AcquiredAt.Equal(restartTime) {
		t.Fatalf("restart recovery did not replace expired owner: %#v", recovered)
	}
}
