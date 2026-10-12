package coretransport

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
)

var ErrUnauthorized = errors.New("core transport: unauthorized caller")

type CallerIdentity struct {
	SID           string
	SessionID     uint32
	Executable    string
	Authenticated bool
	Interactive   bool
	Service       bool
}

// CallerPolicy is the policy applied after a platform adapter obtains the
// caller token/SID from the endpoint handle. It rejects slot users and
// arbitrary executables even when the request contains a plausible actor.
type CallerPolicy struct {
	ServiceSID   string
	LauncherRoot string
	// LauncherExecutable is optional for compatibility with callers that only
	// know the installation root. When omitted, the fixed launcher name is
	// derived from LauncherRoot; arbitrary executables in the signed tree are
	// not accepted as Core callers.
	LauncherExecutable string
}

func (p CallerPolicy) Validate(identity CallerIdentity) error {
	if !identity.Authenticated || strings.TrimSpace(identity.SID) == "" {
		return ErrUnauthorized
	}
	if identity.Service {
		if identity.SID != p.ServiceSID || identity.SessionID != 0 {
			return ErrUnauthorized
		}
		return nil
	}
	if !identity.Interactive || identity.SessionID == 0 || strings.TrimSpace(p.LauncherRoot) == "" || identity.Executable == "" {
		return ErrUnauthorized
	}
	root := filepath.Clean(p.LauncherRoot)
	executable := filepath.Clean(identity.Executable)
	if !filepath.IsAbs(root) || !filepath.IsAbs(executable) || executable == root {
		return ErrUnauthorized
	}
	launcherExecutable := strings.TrimSpace(p.LauncherExecutable)
	if launcherExecutable == "" {
		launcherExecutable = filepath.Join(root, "chuzi-launcher.exe")
	}
	launcherExecutable = filepath.Clean(launcherExecutable)
	if !filepath.IsAbs(launcherExecutable) || !withinPath(root, launcherExecutable) || !strings.EqualFold(executable, launcherExecutable) {
		return ErrUnauthorized
	}
	rel, err := filepath.Rel(root, executable)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ErrUnauthorized
	}
	return nil
}

func withinPath(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

type ConnectionIdentityProvider interface {
	Identity(context.Context, net.Conn) (CallerIdentity, error)
}

type IdentityAuthorizer struct {
	Provider ConnectionIdentityProvider
	Policy   CallerPolicy
}

func (a IdentityAuthorizer) Authorize(ctx context.Context, conn net.Conn) error {
	if a.Provider == nil {
		return ErrUnauthorized
	}
	identity, err := a.Provider.Identity(ctx, conn)
	if err != nil {
		return ErrUnauthorized
	}
	return a.Policy.Validate(identity)
}

// ConnectionAuthorizer is the OS-authenticated caller boundary. The Windows
// implementation derives caller SID/session/PID/image from the pipe handle;
// request actor text is never a substitute for this check.
type ConnectionAuthorizer interface {
	Authorize(context.Context, net.Conn) error
}

type ConnectionAuthorizerFunc func(context.Context, net.Conn) error

func (f ConnectionAuthorizerFunc) Authorize(ctx context.Context, conn net.Conn) error {
	if f == nil {
		return ErrUnauthorized
	}
	if err := f(ctx, conn); err != nil {
		return ErrUnauthorized
	}
	return nil
}
