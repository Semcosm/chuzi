package main

import (
	"context"
	"errors"
)

// revokeRuntimeCapabilities fences every durable capability projection before
// worker and session cleanup. Any lookup or revoke failure keeps shutdown in a
// forced/recovery state instead of silently claiming that cleanup completed.
func revokeRuntimeCapabilities(ctx context.Context, runtime *serviceRuntime) error {
	if runtime == nil || runtime.rdp == nil || runtime.store == nil {
		return nil
	}
	var first error
	requests, err := runtime.store.ListRequests()
	if err != nil {
		first = errors.Join(first, err)
	} else {
		for _, request := range requests {
			if err := runtime.rdp.RevokeRequest(ctx, request.RequestID); err != nil {
				first = errors.Join(first, err)
			}
		}
	}
	leases, err := runtime.store.ListLeases()
	if err != nil {
		first = errors.Join(first, err)
	} else {
		for _, record := range leases {
			if err := runtime.rdp.RevokeAccountLease(ctx, record.Lease.LeaseID); err != nil {
				first = errors.Join(first, err)
			}
		}
	}
	slotLeases, err := runtime.store.ListSlotLeases()
	if err != nil {
		first = errors.Join(first, err)
	} else {
		for _, record := range slotLeases {
			if err := runtime.rdp.RevokeSlotLease(ctx, record.Lease.LeaseID); err != nil {
				first = errors.Join(first, err)
			}
		}
	}
	return first
}
