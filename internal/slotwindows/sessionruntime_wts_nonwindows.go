//go:build !windows

package slotwindows

import (
	"context"

	"github.com/Semcosm/chuzi/internal/sessionruntime"
)

// NewSessionRuntimeWTSProvider keeps non-Windows builds explicit: the WTS
// adapter is unavailable rather than silently substituting a process token.
func NewSessionRuntimeWTSProvider() sessionruntime.SessionProvider {
	return unsupportedWTSProvider{}
}

type unsupportedWTSProvider struct{}

func (unsupportedWTSProvider) Start(context.Context, sessionruntime.Identity) (sessionruntime.Session, error) {
	return sessionruntime.Session{}, ErrSessionBootstrapUnavailable
}
func (unsupportedWTSProvider) Stop(context.Context, sessionruntime.Identity, sessionruntime.Session) error {
	return ErrSessionBootstrapUnavailable
}
func (unsupportedWTSProvider) Find(context.Context, string) (sessionruntime.Session, error) {
	return sessionruntime.Session{}, ErrSessionBootstrapUnavailable
}
