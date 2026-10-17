//go:build windows

package servicehost

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestPrivilegePresentAcceptsDisabledPrivilege(t *testing.T) {
	wanted := windows.LUID{LowPart: 7}
	if !privilegePresent([]windows.LUIDAndAttributes{{Luid: wanted, Attributes: 0}}, wanted) {
		t.Fatal("disabled privilege was treated as absent")
	}
	if privilegePresent([]windows.LUIDAndAttributes{{Luid: windows.LUID{LowPart: 8}}}, wanted) {
		t.Fatal("different privilege was treated as present")
	}
}
