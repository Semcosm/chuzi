package slotwindows

import (
	"context"
	"time"

	"github.com/Semcosm/chuzi/internal/sessionruntime"
)

// SessionRuntimeLoginAdapter bridges the frozen slotwindows credential
// boundary to sessionruntime. It deliberately accepts only a deployment-owned
// SessionLoginAdapter; callers cannot provide an endpoint, command, profile,
// or RDP file. The native adapter is responsible for the registered-helper
// and local-RDP policy described by the Windows session contract.
type SessionRuntimeLoginAdapter struct {
	adapter SessionLoginAdapter
	now     func() time.Time
}

// NewSessionRuntimeLoginAdapter creates the credential handoff used by the
// platform-neutral runtime. The adapter remains the only code that receives a
// temporary UTF-16 password view.
func NewSessionRuntimeLoginAdapter(adapter SessionLoginAdapter, now func() time.Time) (*SessionRuntimeLoginAdapter, error) {
	if adapter == nil {
		return nil, ErrSessionBootstrapUnavailable
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &SessionRuntimeLoginAdapter{adapter: adapter, now: now}, nil
}

func (a *SessionRuntimeLoginAdapter) Login(ctx context.Context, identity sessionruntime.Identity, lease sessionruntime.SecretLease) (sessionruntime.Session, error) {
	if a == nil || a.adapter == nil || ctx == nil {
		return sessionruntime.Session{}, ErrSessionBootstrapUnavailable
	}
	if err := ctx.Err(); err != nil {
		return sessionruntime.Session{}, err
	}
	managed, err := managedIdentityFromRuntime(identity)
	if err != nil {
		return sessionruntime.Session{}, err
	}
	var candidate BootstrapSession
	err = lease.WithPassword(func(password []uint16) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(password) == 0 || lease.Username() == "" {
			return ErrSessionIdentity
		}
		var callErr error
		candidate, callErr = a.adapter.Start(ctx, managed, lease.Username(), password)
		return callErr
	})
	if err != nil {
		return sessionruntime.Session{}, err
	}
	if candidate.ID == 0 || candidate.State != "active" {
		return sessionruntime.Session{}, ErrSessionUnavailable
	}
	return sessionruntime.Session{ID: candidate.ID, SID: identity.SID, State: candidate.State, ObservedAt: a.now().UTC()}, nil
}

func (a *SessionRuntimeLoginAdapter) Logout(ctx context.Context, identity sessionruntime.Identity, session sessionruntime.Session) error {
	if a == nil || a.adapter == nil || ctx == nil {
		return ErrSessionBootstrapUnavailable
	}
	if err := identity.Validate(); err != nil || session.ID == 0 || session.SID != identity.SID {
		return ErrSessionIdentity
	}
	managed, err := managedIdentityFromRuntime(identity)
	if err != nil {
		return err
	}
	return a.adapter.Stop(ctx, managed, BootstrapSession{ID: session.ID, State: session.State})
}

func managedIdentityFromRuntime(identity sessionruntime.Identity) (ManagedIdentity, error) {
	if err := identity.Validate(); err != nil {
		return ManagedIdentity{}, ErrInvalidOptions
	}
	managed := ManagedIdentity{PoolID: identity.PoolID, SlotID: identity.SlotID, Ordinal: identity.Ordinal, Generation: identity.Generation, SID: identity.SID, Owner: identity.Owner}
	if err := managed.validate(); err != nil {
		return ManagedIdentity{}, err
	}
	return managed, nil
}
