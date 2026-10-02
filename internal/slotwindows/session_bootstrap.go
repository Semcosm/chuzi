package slotwindows

import (
	"context"
	"errors"
)

// ManagedIdentity is the narrow identity contract passed to a session
// bootstrapper. It deliberately contains no password, endpoint, executable,
// profile path, or command text. The provisioner derives every field from the
// validated slot request and its ownership record.
type ManagedIdentity struct {
	SlotID     string
	Ordinal    int
	Generation uint64
	SID        string
}

// BootstrapSession is the opaque result of a controlled session provider. A
// provider must establish a real WTS session; creating a process with a
// duplicated token is not sufficient. The provisioner re-queries WTS and
// checks the SID before it starts an agent.
type BootstrapSession struct {
	ID    uint32
	State string
}

// SessionBootstrapper is optional. Production deployments normally leave it
// nil and depend on an externally managed interactive session. Native smoke
// may inject a runner-owned implementation, but the implementation must not
// accept arbitrary user, endpoint, command, or executable input.
type SessionBootstrapper interface {
	Start(context.Context, ManagedIdentity) (BootstrapSession, error)
	Stop(context.Context, ManagedIdentity, BootstrapSession) error
}

// credentialedSessionBootstrapper is an internal extension for a provider
// owned by the same controlled Windows process. The password is passed only
// as an in-memory UTF-16 slice for the initial disposable-user bootstrap and
// is cleared by the provisioner immediately after Start returns. It is never
// part of the public contract or a Core/API DTO.
type credentialedSessionBootstrapper interface {
	StartWithCredential(context.Context, ManagedIdentity, string, []uint16) (BootstrapSession, error)
}

var ErrSessionBootstrapUnavailable = errors.New("slotwindows: session bootstrap provider unavailable")

func startManagedSession(ctx context.Context, provider SessionBootstrapper, identity ManagedIdentity, username string, password []uint16) (BootstrapSession, error) {
	if provider == nil {
		return BootstrapSession{}, ErrSessionBootstrapUnavailable
	}
	if credentialed, ok := provider.(credentialedSessionBootstrapper); ok && len(password) > 0 {
		copyPassword := append([]uint16(nil), password...)
		defer clear(copyPassword)
		return credentialed.StartWithCredential(ctx, identity, username, copyPassword)
	}
	return provider.Start(ctx, identity)
}

func (i ManagedIdentity) validate() error {
	if !slotIDPattern.MatchString(i.SlotID) || i.Ordinal < 1 || i.Ordinal > 256 || i.Generation == 0 || i.SID == "" {
		return ErrInvalidOptions
	}
	return nil
}
