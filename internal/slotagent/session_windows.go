//go:build windows

package slotagent

import (
	"errors"
	"regexp"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	currentSessionActive       = 0
	currentSessionDisconnected = 4
	winstaAllAccess            = 0x000F037F
	desktopAllAccess           = 0x000F01FF
)

var managedDesktopPattern = regexp.MustCompile("^winsta0\\\\ChuziSlot[0-9a-f]{16}$")

var ErrDesktopBootstrap = errors.New("slotagent: desktop bootstrap failed")

// PrepareDesktop creates or opens the service-derived desktop from inside the
// target user's session. The bootstrap may start without a desktop binding,
// so this function explicitly attaches the process to that session's WinSta0.
func PrepareDesktop(desktop, serviceSID string) error {
	if !managedDesktopPattern.MatchString(desktop) || serviceSID == "" {
		return ErrDesktopBootstrap
	}
	service, err := windows.StringToSid(serviceSID)
	if err != nil {
		return ErrDesktopBootstrap
	}
	station, closeStation, err := openInteractiveWindowStation()
	if err != nil {
		return ErrDesktopBootstrap
	}
	defer closeStation()
	if err := grantWindowObjectAccess(station, service, windows.ACCESS_MASK(winstaAllAccess)); err != nil {
		return ErrDesktopBootstrap
	}
	setProcessWindowStation := windows.NewLazySystemDLL("user32.dll").NewProc("SetProcessWindowStation")
	if result, _, _ := setProcessWindowStation.Call(uintptr(station)); result == 0 {
		return ErrDesktopBootstrap
	}
	name := desktop[strings.LastIndexByte(desktop, '\\')+1:]
	nameUTF16, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return ErrDesktopBootstrap
	}
	create := windows.NewLazySystemDLL("user32.dll").NewProc("CreateDesktopW")
	close := windows.NewLazySystemDLL("user32.dll").NewProc("CloseDesktop")
	handle, _, _ := create.Call(uintptr(unsafe.Pointer(nameUTF16)), 0, 0, 0, desktopAllAccess, 0)
	if handle != 0 {
		if err := grantDesktopAccess(windows.Handle(handle), service); err != nil {
			_, _, _ = close.Call(handle)
			return ErrDesktopBootstrap
		}
		_, _, _ = close.Call(handle)
		return nil
	}
	open := windows.NewLazySystemDLL("user32.dll").NewProc("OpenDesktopW")
	handle, _, _ = open.Call(uintptr(unsafe.Pointer(nameUTF16)), 0, 0, 0, desktopAllAccess)
	if handle == 0 {
		return ErrDesktopBootstrap
	}
	if err := grantDesktopAccess(windows.Handle(handle), service); err != nil {
		_, _, _ = close.Call(handle)
		return ErrDesktopBootstrap
	}
	_, _, _ = close.Call(handle)
	return nil
}

func grantDesktopAccess(handle windows.Handle, serviceSID *windows.SID) error {
	return grantWindowObjectAccess(handle, serviceSID, windows.ACCESS_MASK(desktopAllAccess))
}

func openInteractiveWindowStation() (windows.Handle, func(), error) {
	name, err := windows.UTF16PtrFromString("WinSta0")
	if err != nil {
		return 0, func() {}, err
	}
	user32 := windows.NewLazySystemDLL("user32.dll")
	open := user32.NewProc("OpenWindowStationW")
	close := user32.NewProc("CloseWindowStation")
	handle, _, callErr := open.Call(uintptr(unsafe.Pointer(name)), 0, winstaAllAccess)
	if handle == 0 {
		return 0, func() {}, callErr
	}
	return windows.Handle(handle), func() { _, _, _ = close.Call(handle) }, nil
}

func grantWindowObjectAccess(handle windows.Handle, serviceSID *windows.SID, access windows.ACCESS_MASK) error {
	security, err := windows.GetSecurityInfo(handle, windows.SE_WINDOW_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	entry := windows.EXPLICIT_ACCESS{
		AccessPermissions: access,
		AccessMode:        windows.SET_ACCESS,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(serviceSID),
		},
	}
	updated, err := windows.BuildSecurityDescriptor(nil, nil, []windows.EXPLICIT_ACCESS{entry}, nil, security)
	if err != nil {
		return err
	}
	dacl, _, err := updated.DACL()
	if err != nil || dacl == nil {
		if err == nil {
			err = windows.ERROR_INVALID_ACL
		}
		return err
	}
	return windows.SetSecurityInfo(handle, windows.SE_WINDOW_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

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
