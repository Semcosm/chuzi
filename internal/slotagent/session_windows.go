//go:build windows

package slotagent

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	currentSessionActive       = 0
	currentSessionDisconnected = 4
)

// CurrentSessionState reports only the bounded state enum needed by health;
// it never exposes a Windows session identifier or user identity.
func CurrentSessionState() string {
	var sessionID uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &sessionID); err != nil {
		return "unavailable"
	}
	var raw *windows.WTS_SESSION_INFO
	var count uint32
	if err := windows.WTSEnumerateSessions(0, 0, 1, &raw, &count); err != nil {
		return "unavailable"
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(raw)))
	for _, entry := range unsafe.Slice(raw, count) {
		if entry.SessionID != sessionID {
			continue
		}
		switch entry.State {
		case currentSessionActive:
			return "ready"
		case currentSessionDisconnected:
			return "disconnected"
		default:
			return "logged_off"
		}
	}
	return "logged_off"
}
