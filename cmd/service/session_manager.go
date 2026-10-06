package main

import (
	"context"
	"time"

	"github.com/Semcosm/chuzi/internal/credential"
	"github.com/Semcosm/chuzi/internal/session"
	"github.com/Semcosm/chuzi/internal/store"
)

type sessionRDPProvider struct{ bridge *rdpCapabilityBridge }

func (p sessionRDPProvider) Issue(ctx context.Context, b session.Binding) error {
	if p.bridge == nil {
		return credential.ErrRDPUnauthorized
	}
	_, err := p.bridge.IssueBound(ctx, credential.RDPAuthorization{AccountID: b.AccountID, RequestID: b.RequestID, Actor: "core-ui", AccountLeaseID: b.AccountLeaseID, SlotLeaseID: b.SlotLeaseID, SlotID: b.SlotID, EnvironmentGeneration: b.EnvironmentGeneration})
	return err
}
func (p sessionRDPProvider) Revoke(ctx context.Context, b session.Binding) error {
	if p.bridge == nil {
		return nil
	}
	return p.bridge.RevokeSlotLease(ctx, b.SlotLeaseID)
}

func newSessionManager(database *store.Store, resolver slotAgentResolver, owner string, newID func(string) string, clock func() time.Time, bridge *rdpCapabilityBridge) (*session.Manager, error) {
	rdp := session.RDPProvider(sessionRDPProvider{bridge: bridge})
	return session.New(session.Config{Store: database, Runtime: newServiceSessionRuntime(database, resolver, owner), RDP: rdp, Owner: owner, NewID: newID, Clock: clock})
}
