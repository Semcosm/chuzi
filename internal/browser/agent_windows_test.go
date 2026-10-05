//go:build windows

package browser

import "testing"

func TestLeaseCommandIDBindsRetryToSlotLease(t *testing.T) {
	first := leaseCommandID("prepare", "request-1", "slot-lease-1")
	if first != leaseCommandID("prepare", "request-1", "slot-lease-1") {
		t.Fatal("lease command ID is not deterministic")
	}
	if first == leaseCommandID("prepare", "request-1", "slot-lease-2") {
		t.Fatal("different slot leases share a command ID")
	}
	if first == leaseCommandID("start", "request-1", "slot-lease-1") {
		t.Fatal("different command kinds share a command ID")
	}
}
