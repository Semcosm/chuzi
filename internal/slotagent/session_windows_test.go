//go:build windows

package slotagent

import "testing"

func TestPrepareDesktopRejectsUnmanagedName(t *testing.T) {
	for _, desktop := range []string{
		"",
		"winsta0\\Default",
		"winsta0\\ChuziSlot0001",
		"winsta0\\ChuziSlot0123456789ABCDEG",
		"winsta1\\ChuziSlot0123456789abcdef",
	} {
		if err := PrepareDesktop(desktop, "S-1-5-18"); err == nil {
			t.Fatalf("unmanaged desktop accepted: %q", desktop)
		}
	}
}
