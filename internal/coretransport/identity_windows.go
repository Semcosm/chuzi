//go:build windows

package coretransport

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                        = windows.NewLazySystemDLL("kernel32.dll")
	getNamedPipeClientProcessIDProc = kernel32.NewProc("GetNamedPipeClientProcessId")
	getNamedPipeServerProcessIDProc = kernel32.NewProc("GetNamedPipeServerProcessId")
)

// serviceCallerSID is the deterministic SID assigned to the fixed ChuziCore
// service name. The service token must carry this enabled group in addition
// to LocalSystem; this prevents an unrelated SYSTEM process from becoming a
// Core caller merely because it can open the pipe.
const serviceCallerSID = "S-1-5-80-1936457352-483135088-2315033984-64399253-3431197935"

// WindowsPipeIdentityProvider derives the caller identity from the accepted
// named-pipe handle and its kernel-reported client PID. Request actor/owner
// fields never participate in this decision. The provider intentionally
// exposes no mutating process controls.
type WindowsPipeIdentityProvider struct{}

func NewWindowsPipeIdentityProvider() WindowsPipeIdentityProvider {
	return WindowsPipeIdentityProvider{}
}

type pipeHandleConn interface {
	Fd() uintptr
}

func (WindowsPipeIdentityProvider) Identity(ctx context.Context, conn net.Conn) (CallerIdentity, error) {
	if ctx == nil || conn == nil {
		return CallerIdentity{}, ErrUnauthorized
	}
	select {
	case <-ctx.Done():
		return CallerIdentity{}, ErrUnauthorized
	default:
	}
	pipe, ok := conn.(pipeHandleConn)
	if !ok || pipe.Fd() == 0 || pipe.Fd() == ^uintptr(0) {
		return CallerIdentity{}, ErrUnauthorized
	}

	handle := windows.Handle(pipe.Fd())
	pid, err := namedPipeClientProcessID(handle)
	if err != nil || pid == 0 {
		return CallerIdentity{}, ErrUnauthorized
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return CallerIdentity{}, ErrUnauthorized
	}
	defer windows.CloseHandle(process)
	processToken, err := openProcessToken(process)
	if err != nil {
		return CallerIdentity{}, ErrUnauthorized
	}
	defer processToken.Close()
	user, err := processToken.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return CallerIdentity{}, ErrUnauthorized
	}
	sid := user.User.Sid.String()
	sessionID, err := tokenUint32(processToken, windows.TokenSessionId)
	if err != nil {
		return CallerIdentity{}, ErrUnauthorized
	}
	executable, err := processImagePath(process)
	if err != nil || strings.TrimSpace(executable) == "" {
		return CallerIdentity{}, ErrUnauthorized
	}
	if currentPID, err := namedPipeClientProcessID(handle); err != nil || currentPID != pid {
		return CallerIdentity{}, ErrUnauthorized
	}
	if sessionID == 0 {
		systemSID, sidErr := windows.StringToSid("S-1-5-18")
		serviceSID, serviceErr := windows.StringToSid(serviceCallerSID)
		if sidErr != nil || serviceErr != nil || !user.User.Sid.Equals(systemSID) || !tokenHasEnabledSID(processToken, serviceSID) {
			return CallerIdentity{}, ErrUnauthorized
		}
		return CallerIdentity{SID: serviceCallerSID, SessionID: 0, Executable: executable, Authenticated: true, Service: true}, nil
	}
	return CallerIdentity{SID: sid, SessionID: sessionID, Executable: executable, Authenticated: true, Interactive: true}, nil
}

func tokenHasEnabledSID(token windows.Token, expected *windows.SID) bool {
	if expected == nil {
		return false
	}
	groups, err := token.GetTokenGroups()
	if err != nil {
		return false
	}
	for _, group := range groups.AllGroups() {
		if group.Sid != nil && group.Sid.Equals(expected) && group.Attributes&windows.SE_GROUP_ENABLED != 0 {
			return true
		}
	}
	return false
}

func namedPipeClientProcessID(handle windows.Handle) (uint32, error) {
	var pid uint32
	r1, _, callErr := getNamedPipeClientProcessIDProc.Call(uintptr(handle), uintptr(unsafe.Pointer(&pid)))
	if r1 == 0 {
		if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
			return 0, callErr
		}
		return 0, windows.ERROR_INVALID_HANDLE
	}
	return pid, nil
}

func namedPipeServerProcessID(handle windows.Handle) (uint32, error) {
	var pid uint32
	r1, _, callErr := getNamedPipeServerProcessIDProc.Call(uintptr(handle), uintptr(unsafe.Pointer(&pid)))
	if r1 == 0 {
		if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
			return 0, callErr
		}
		return 0, windows.ERROR_INVALID_HANDLE
	}
	return pid, nil
}

func validatePipeServerIdentity(conn net.Conn, expectedExecutable string) error {
	pipe, ok := conn.(pipeHandleConn)
	if !ok || pipe.Fd() == 0 || pipe.Fd() == ^uintptr(0) {
		return ErrUnauthorized
	}
	pid, err := namedPipeServerProcessID(windows.Handle(pipe.Fd()))
	if err != nil || pid == 0 {
		return ErrUnauthorized
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ErrUnauthorized
	}
	defer windows.CloseHandle(process)
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return ErrUnauthorized
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return ErrUnauthorized
	}
	systemSID, err := windows.StringToSid("S-1-5-18")
	if err != nil || !user.User.Sid.Equals(systemSID) {
		return ErrUnauthorized
	}
	session, err := tokenUint32(token, windows.TokenSessionId)
	if err != nil || session != 0 {
		return ErrUnauthorized
	}
	executable, err := processImagePath(process)
	if err != nil || !validServerExecutablePath(executable, expectedExecutable) {
		return ErrUnauthorized
	}
	if currentPID, err := namedPipeServerProcessID(windows.Handle(pipe.Fd())); err != nil || currentPID != pid {
		return ErrUnauthorized
	}
	return nil
}

// ValidateWindowsPipeServer authenticates the server side of a named pipe by
// PID, SYSTEM token, session 0, and exact installed executable path. Clients
// use this before accepting any protocol response from a fixed endpoint.
func ValidateWindowsPipeServer(conn net.Conn, expectedExecutable string) error {
	return validatePipeServerIdentity(conn, expectedExecutable)
}

// ValidateWindowsPipeExecutable authenticates a non-service named-pipe peer
// by its live PID and exact installed image. The caller supplies the expected
// SID/session policy separately when the peer is an interactive helper.
func ValidateWindowsPipeExecutable(conn net.Conn, expectedExecutable string) error {
	pipe, ok := conn.(pipeHandleConn)
	if !ok || pipe.Fd() == 0 || pipe.Fd() == ^uintptr(0) {
		return ErrUnauthorized
	}
	handle := windows.Handle(pipe.Fd())
	pid, err := namedPipeServerProcessID(handle)
	if err != nil || pid == 0 {
		return ErrUnauthorized
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ErrUnauthorized
	}
	defer windows.CloseHandle(process)
	executable, err := processImagePath(process)
	if err != nil || !validServerExecutablePath(executable, expectedExecutable) {
		return ErrUnauthorized
	}
	if currentPID, err := namedPipeServerProcessID(handle); err != nil || currentPID != pid {
		return ErrUnauthorized
	}
	return nil
}

// ValidateWindowsPipePeer authenticates an interactive helper by the live
// server PID, token SID/session, and exact installed image. It closes the
// pipe-squatting and PID-reuse windows before a caller accepts the endpoint as
// the registered operator helper.
func ValidateWindowsPipePeer(conn net.Conn, expectedSID string, expectedSession uint32, expectedExecutable string) error {
	if conn == nil || expectedSID == "" || expectedSession == 0 || strings.TrimSpace(expectedExecutable) == "" {
		return ErrUnauthorized
	}
	pipe, ok := conn.(pipeHandleConn)
	if !ok || pipe.Fd() == 0 || pipe.Fd() == ^uintptr(0) {
		return ErrUnauthorized
	}
	handle := windows.Handle(pipe.Fd())
	pid, err := namedPipeServerProcessID(handle)
	if err != nil || pid == 0 {
		return ErrUnauthorized
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ErrUnauthorized
	}
	defer windows.CloseHandle(process)
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return ErrUnauthorized
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil || user.User.Sid.String() != expectedSID {
		return ErrUnauthorized
	}
	session, err := tokenUint32(token, windows.TokenSessionId)
	if err != nil || session != expectedSession {
		return ErrUnauthorized
	}
	executable, err := processImagePath(process)
	if err != nil || !validServerExecutablePath(executable, expectedExecutable) {
		return ErrUnauthorized
	}
	if currentPID, err := namedPipeServerProcessID(handle); err != nil || currentPID != pid {
		return ErrUnauthorized
	}
	return nil
}

func validServerExecutablePath(actual, expected string) bool {
	actual = normalizeWindowsPath(actual)
	if strings.TrimSpace(expected) == "" {
		return false
	}
	expected = normalizeWindowsPath(expected)
	return filepath.IsAbs(expected) && strings.EqualFold(actual, expected)
}

func normalizeWindowsPath(value string) string {
	path := strings.TrimSpace(strings.ReplaceAll(value, "/", "\\"))
	if stripped := strings.TrimPrefix(path, "\\\\?\\UNC\\"); stripped != path {
		path = "\\\\" + stripped
	} else if stripped := strings.TrimPrefix(path, "\\\\?\\"); stripped != path {
		path = stripped
	}
	return strings.ToLower(strings.TrimRight(filepath.Clean(path), "\\"))
}

func openProcessToken(process windows.Handle) (windows.Token, error) {
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return 0, err
	}
	return token, nil
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

func processImagePath(process windows.Handle) (string, error) {
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &size); err != nil {
		return "", err
	}
	return syscall.UTF16ToString(buffer[:size]), nil
}
