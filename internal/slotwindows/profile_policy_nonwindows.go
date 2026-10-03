//go:build !windows

package slotwindows

import "context"

// applyProfilePolicy is unavailable off Windows. Validation remains available
// to cross-platform tests and configuration tooling.
func applyProfilePolicy(context.Context, string, string) error { return ErrUnsupported }

func ReadProfileShellCommand(string) (string, error) { return "", ErrUnsupported }
