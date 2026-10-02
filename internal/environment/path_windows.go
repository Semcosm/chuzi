//go:build windows

package environment

import "strings"

func sameResolvedPath(a, b string) bool { return strings.EqualFold(a, b) }
