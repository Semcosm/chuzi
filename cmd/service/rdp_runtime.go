package main

import (
	"context"
	"time"

	"github.com/Semcosm/chuzi/internal/coreapi"
	"github.com/Semcosm/chuzi/internal/credential"
)

// rdpCapabilityBridge keeps the credential service behind the Core and
// scheduler interfaces. The default authorizer is deny-by-default; a
// deployment may replace that authorizer at its integration boundary without
// changing the Core/UI contract.
type rdpCapabilityBridge struct {
	service *credential.RDPService
	clock   func() time.Time
}

func newRDPCapabilityBridge(clock func() time.Time) (*rdpCapabilityBridge, error) {
	service, err := credential.NewRDPService(credential.DenyRDPAuthorizer{})
	if err != nil {
		return nil, err
	}
	return &rdpCapabilityBridge{service: service, clock: clock}, nil
}

func (b *rdpCapabilityBridge) Issue(ctx context.Context, accountID, requestID, actor string) (coreapi.RDPCapability, error) {
	return b.issue(ctx, credential.RDPAuthorization{AccountID: accountID, RequestID: requestID, Actor: actor})
}

func (b *rdpCapabilityBridge) IssueBound(ctx context.Context, authorization credential.RDPAuthorization) (coreapi.RDPCapability, error) {
	if authorization.AccountLeaseID == "" || authorization.SlotLeaseID == "" || authorization.SlotID == "" || authorization.EnvironmentGeneration == 0 {
		return coreapi.RDPCapability{}, credential.ErrRDPUnauthorized
	}
	return b.issue(ctx, authorization)
}

func (b *rdpCapabilityBridge) issue(ctx context.Context, authorization credential.RDPAuthorization) (coreapi.RDPCapability, error) {
	if b == nil || b.service == nil || b.clock == nil {
		return coreapi.RDPCapability{}, credential.ErrRDPUnauthorized
	}
	capability, err := b.service.Issue(ctx, authorization, b.clock().UTC(), credential.DefaultRDPLease)
	if err != nil {
		return coreapi.RDPCapability{}, err
	}
	return coreapi.RDPCapability{ID: capability.ID, Token: capability.Token, RequestID: capability.RequestID, ExpiresAt: capability.ExpiresAt}, nil
}

func (b *rdpCapabilityBridge) RevokeRequest(ctx context.Context, requestID string) error {
	if b == nil || b.service == nil {
		return nil
	}
	return b.service.RevokeRequest(ctx, requestID)
}

func (b *rdpCapabilityBridge) RevokeSlotLease(ctx context.Context, leaseID string) error {
	if b == nil || b.service == nil {
		return nil
	}
	return b.service.RevokeSlotLease(ctx, leaseID)
}

func (b *rdpCapabilityBridge) RevokeSlot(ctx context.Context, slotID string) error {
	if b == nil || b.service == nil {
		return nil
	}
	return b.service.RevokeSlot(ctx, slotID)
}

func (b *rdpCapabilityBridge) RevokeAccountLease(ctx context.Context, leaseID string) error {
	if b == nil || b.service == nil {
		return nil
	}
	return b.service.RevokeAccountLease(ctx, leaseID)
}

func (b *rdpCapabilityBridge) RevokeGeneration(ctx context.Context, slotID string, generation uint64) error {
	if b == nil || b.service == nil {
		return nil
	}
	return b.service.RevokeGeneration(ctx, slotID, generation)
}

func (b *rdpCapabilityBridge) Close() error {
	if b == nil || b.service == nil {
		return nil
	}
	return b.service.Close()
}

var _ interface {
	Issue(context.Context, string, string, string) (coreapi.RDPCapability, error)
	IssueBound(context.Context, credential.RDPAuthorization) (coreapi.RDPCapability, error)
	RevokeRequest(context.Context, string) error
	RevokeSlotLease(context.Context, string) error
	RevokeSlot(context.Context, string) error
	RevokeAccountLease(context.Context, string) error
	RevokeGeneration(context.Context, string, uint64) error
	Close() error
} = (*rdpCapabilityBridge)(nil)
