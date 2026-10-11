//go:build !windows

package servicehost

// ServiceSIDString is unavailable outside Windows because service SIDs are an
// operating-system token property. Keeping the boundary explicit prevents a
// Unix caller from manufacturing a value accepted by Windows IPC policy.
func ServiceSIDString(string) (string, error) {
	return "", ErrHostUnavailable
}
