//go:build !windows

package environment

func sameResolvedPath(a, b string) bool { return a == b }
