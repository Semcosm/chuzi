//go:build windows

package slotwindows

import (
	"context"
	"time"

	"github.com/Semcosm/chuzi/internal/sessionruntime"
)

// NewSessionRuntimeWTSProvider exposes the SYSTEM-side WTS fact checks to the
// platform-neutral runtime. Start remains unsupported here: only the selected
// LoginAdapter may create a session.
func NewSessionRuntimeWTSProvider() sessionruntime.SessionProvider {
	return wtsRuntimeProvider{}
}

type wtsRuntimeProvider struct{}

func (wtsRuntimeProvider) Start(context.Context, sessionruntime.Identity) (sessionruntime.Session, error) {
	return sessionruntime.Session{}, ErrSessionBootstrapUnavailable
}

func (wtsRuntimeProvider) Find(ctx context.Context, sid string) (sessionruntime.Session, error) {
	if ctx == nil {
		return sessionruntime.Session{}, ErrSessionUnavailable
	}
	if err := ctx.Err(); err != nil {
		return sessionruntime.Session{}, err
	}
	native, err := FindSession(sid)
	observed := time.Now().UTC()
	fact := sessionruntime.Session{ID: native.ID, SID: sid, State: native.State, ObservedAt: observed}
	if err != nil {
		return fact, err
	}
	return fact, nil
}

func (wtsRuntimeProvider) Stop(ctx context.Context, identity sessionruntime.Identity, session sessionruntime.Session) error {
	if ctx == nil || ctx.Err() != nil {
		return ErrCleanup
	}
	managed, err := managedIdentityFromRuntime(identity)
	if err != nil || session.ID == 0 || session.SID != managed.SID {
		return ErrSessionIdentity
	}
	current, err := VerifySession(managed.SID, session.ID)
	if err != nil {
		return err
	}
	if current.ID != session.ID {
		return ErrSessionChanged
	}
	if err := wtsLogoffSession(session.ID); err != nil {
		return ErrCleanup
	}
	if err := waitForSessionGone(ctx, managed.SID); err != nil {
		return ErrCleanup
	}
	return nil
}
