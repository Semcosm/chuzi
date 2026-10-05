//go:build windows

package slotwindows

import (
	"errors"
	"testing"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

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

func TestReadTokenEnvironmentKeepsOnlyRuntimeSafeVariables(t *testing.T) {
	encoded := make([]uint16, 0, 128)
	for _, entry := range []string{"PATH=C:\\Windows\\System32", "CHUZI_SERVICE_SECRET=redacted", "USERPROFILE=C:\\Users\\smoke"} {
		encoded = append(encoded, utf16.Encode([]rune(entry))...)
		encoded = append(encoded, 0)
	}
	encoded = append(encoded, 0)
	values := make(map[string]string)
	readTokenEnvironment(&encoded[0], values)
	if values["PATH"] != "PATH=C:\\Windows\\System32" || values["USERPROFILE"] != "USERPROFILE=C:\\Users\\smoke" {
		t.Fatalf("safe token environment was not retained: %#v", values)
	}
	if _, ok := values["CHUZI_SERVICE_SECRET"]; ok {
		t.Fatal("unapproved token environment variable crossed the process boundary")
	}
}

func TestClassifyProcessStartWin32ErrorUsesSafeCategories(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
	}{
		{name: "access denied", err: windows.ERROR_ACCESS_DENIED, want: errProcessStartWin32AccessDenied},
		{name: "file missing", err: windows.ERROR_FILE_NOT_FOUND, want: errProcessStartWin32FileMissing},
		{name: "invalid parameter", err: windows.ERROR_INVALID_PARAMETER, want: errProcessStartWin32InvalidArg},
		{name: "unknown", err: errors.New("unpublished native error"), want: errProcessStartWin32Unknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyProcessStartWin32Error(test.err); !errors.Is(got, test.want) {
				t.Fatalf("category = %v, want %v", got, test.want)
			}
		})
	}
}
