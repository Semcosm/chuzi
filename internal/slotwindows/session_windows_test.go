//go:build windows

package slotwindows

import "testing"

func TestManagedDesktopNameIsClosedAndSlotScoped(t *testing.T) {
	for _, value := range []string{`winsta0\ChuziSlot0123456789abcdef`, `winsta0\ChuziSlotfedcba9876543210`} {
		if !managedDesktopPattern.MatchString(value) {
			t.Fatalf("managed desktop rejected: %q", value)
		}
	}
	for _, value := range []string{`Default`, `winsta0\Default`, `winsta0\ChuziSlot0001`, `winsta0\ChuziSlot0123456789ABCDEG`, `winsta0\Other0001`} {
		if managedDesktopPattern.MatchString(value) {
			t.Fatalf("uncontrolled desktop accepted: %q", value)
		}
	}
}
