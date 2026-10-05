//go:build !windows

package environment

func isReparsePoint(string) (bool, error) { return false, nil }
func isReparsePath(string) (bool, error)  { return false, nil }
