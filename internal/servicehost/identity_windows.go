//go:build windows

package servicehost

import (
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ValidateServiceToken is called immediately before entering the SCM handler.
// It prevents a foreground user process from impersonating the production
// service entrypoint with only the -service flag.
func ValidateServiceToken(identity Identity) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		return ErrHostUnavailable
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return ErrHostUnavailable
	}
	systemSID, err := windows.StringToSid(`S-1-5-18`)
	if err != nil || !user.User.Sid.Equals(systemSID) {
		return ErrInvalidConfig
	}
	sessionID, err := tokenUint32(token, windows.TokenSessionId)
	if err != nil || sessionID != 0 {
		return ErrInvalidConfig
	}
	serviceSID, err := deriveServiceSID(identity.Name)
	if err != nil {
		return ErrHostUnavailable
	}
	groups, err := token.GetTokenGroups()
	if err != nil {
		return ErrHostUnavailable
	}
	foundServiceSID := false
	for _, group := range groups.AllGroups() {
		if group.Sid != nil && group.Sid.Equals(serviceSID) && group.Attributes&windows.SE_GROUP_ENABLED != 0 {
			foundServiceSID = true
			break
		}
	}
	if !foundServiceSID {
		return ErrInvalidConfig
	}
	var size uint32
	_ = windows.GetTokenInformation(token, windows.TokenPrivileges, nil, 0, &size)
	if size == 0 {
		return ErrHostUnavailable
	}
	buffer := make([]byte, size)
	if err := windows.GetTokenInformation(token, windows.TokenPrivileges, &buffer[0], uint32(len(buffer)), &size); err != nil {
		return ErrHostUnavailable
	}
	privileges := (*windows.Tokenprivileges)(unsafe.Pointer(&buffer[0]))
	for _, name := range identity.Privileges {
		namePtr, err := syscall.UTF16PtrFromString(name)
		if err != nil {
			return ErrInvalidConfig
		}
		var luid windows.LUID
		if err := windows.LookupPrivilegeValue(nil, namePtr, &luid); err != nil {
			return ErrHostUnavailable
		}
		found := false
		found = privilegePresent(privileges.AllPrivileges(), luid)
		if !found {
			return ErrInvalidConfig
		}
	}
	return nil
}

// privilegePresent checks that a privilege is carried by the service token.
// LocalSystem commonly carries the session-creation privileges disabled; the
// service enables them only at the native boundary that needs them.
func privilegePresent(privileges []windows.LUIDAndAttributes, wanted windows.LUID) bool {
	for _, privilege := range privileges {
		if privilege.Luid == wanted {
			return true
		}
	}
	return false
}

// EnableServicePrivileges enables the fixed production privileges after the
// service identity has been authenticated. Presence is checked separately by
// ValidateServiceToken so a disabled-but-present LocalSystem privilege is not
// mistaken for a foreign token.
func EnableServicePrivileges(identity Identity) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_ADJUST_PRIVILEGES, &token); err != nil {
		return ErrHostUnavailable
	}
	defer token.Close()
	for _, name := range identity.Privileges {
		namePtr, err := syscall.UTF16PtrFromString(name)
		if err != nil {
			return ErrInvalidConfig
		}
		var luid windows.LUID
		if err := windows.LookupPrivilegeValue(nil, namePtr, &luid); err != nil {
			return ErrHostUnavailable
		}
		state := windows.Tokenprivileges{PrivilegeCount: 1}
		state.Privileges[0] = windows.LUIDAndAttributes{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED}
		if err := windows.AdjustTokenPrivileges(token, false, &state, uint32(unsafe.Sizeof(state)), nil, nil); err != nil {
			return ErrHostUnavailable
		}
		if err := windows.GetLastError(); err != nil {
			if errors.Is(err, windows.ERROR_NOT_ALL_ASSIGNED) {
				return ErrInvalidConfig
			}
			return ErrHostUnavailable
		}
	}
	return nil
}

func deriveServiceSID(serviceName string) (*windows.SID, error) {
	if serviceName == "" {
		return nil, fmt.Errorf("empty service name")
	}
	// Windows derives service SIDs from the upper-case service name.
	encoded := utf16.Encode([]rune(strings.ToUpper(serviceName)))
	bytes := make([]byte, len(encoded)*2)
	for index, value := range encoded {
		binary.LittleEndian.PutUint16(bytes[index*2:], value)
	}
	digest := sha1.Sum(bytes)
	parts := make([]string, 5)
	for index := range parts {
		parts[index] = fmt.Sprintf("%d", binary.LittleEndian.Uint32(digest[index*4:]))
	}
	return windows.StringToSid("S-1-5-80-" + parts[0] + "-" + parts[1] + "-" + parts[2] + "-" + parts[3] + "-" + parts[4])
}

// ServiceSIDString returns the OS-derived service SID used by authenticated
// IPC callers. The value is deterministic for the fixed service name and is
// not read from configuration or request data.
func ServiceSIDString(serviceName string) (string, error) {
	sid, err := deriveServiceSID(serviceName)
	if err != nil {
		return "", ErrHostUnavailable
	}
	return sid.String(), nil
}

func tokenUint32(token windows.Token, class uint32) (uint32, error) {
	var value uint32
	var returned uint32
	if err := windows.GetTokenInformation(token, class, (*byte)(unsafe.Pointer(&value)), uint32(unsafe.Sizeof(value)), &returned); err != nil {
		return 0, err
	}
	if returned < uint32(unsafe.Sizeof(value)) {
		return 0, windows.ERROR_INVALID_DATA
	}
	return value, nil
}
