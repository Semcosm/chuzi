//go:build !windows

package coretransport

import (
	"errors"
	"net"
)

var errWindowsPipeUnavailable = errors.New("core transport: Windows pipe identity unavailable")

// WindowsPipeIdentityProvider is intentionally unavailable off Windows. Unix
// endpoints use filesystem ownership and mode enforcement instead.
type WindowsPipeIdentityProvider struct{}

func NewWindowsPipeIdentityProvider() WindowsPipeIdentityProvider {
	return WindowsPipeIdentityProvider{}
}

func ValidateWindowsPipeServer(net.Conn, string) error     { return errWindowsPipeUnavailable }
func ValidateWindowsPipeExecutable(net.Conn, string) error { return errWindowsPipeUnavailable }
