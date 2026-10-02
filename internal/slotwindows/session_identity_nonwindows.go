//go:build !windows

package slotwindows

import "errors"

// ErrSessionIdentity is kept on non-Windows builds so the shared bootstrap
// contract has the same stable failure classification as the Windows path.
var ErrSessionIdentity = errors.New("slotwindows: session identity mismatch")
