//go:build windows

package slotwindows

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	brokerAuthAdvapi32             = windows.NewLazySystemDLL("advapi32.dll")
	brokerAuthKernel32             = windows.NewLazySystemDLL("kernel32.dll")
	brokerAuthImpersonateNamedPipe = brokerAuthAdvapi32.NewProc("ImpersonateNamedPipeClient")
	brokerAuthGetClientProcessID   = brokerAuthKernel32.NewProc("GetNamedPipeClientProcessId")
)

type sessionBrokerCallerIdentity struct {
	SID         string
	ServiceSIDs []string
	SessionID   uint32
	Executable  string
}

type sessionBrokerNativeCallerValidator struct {
	acl                SessionBrokerACL
	serviceExecutable  string
	expectedExecutable string
	serviceOnly        bool
}

func newSessionBrokerCallerValidator(acl SessionBrokerACL, serviceExecutable, expectedExecutable string) sessionBrokerCallerValidator {
	return sessionBrokerNativeCallerValidator{acl: acl, serviceExecutable: serviceExecutable, expectedExecutable: expectedExecutable}
}

func newSessionBrokerServiceCallerValidator(acl SessionBrokerACL, serviceExecutable, expectedExecutable string) sessionBrokerCallerValidator {
	return sessionBrokerNativeCallerValidator{acl: acl, serviceExecutable: serviceExecutable, expectedExecutable: expectedExecutable, serviceOnly: true}
}

func (v sessionBrokerNativeCallerValidator) Validate(ctx context.Context, conn net.Conn) error {
	if ctx == nil || conn == nil {
		return ErrSessionBrokerInvalidRequest
	}
	select {
	case <-ctx.Done():
		return ErrSessionBrokerInvalidRequest
	default:
	}
	identity, err := authenticatedSessionBrokerCaller(conn)
	serviceCaller := identity.SID == "S-1-5-18" && identity.SessionID == 0 && containsSessionBrokerSID(identity.ServiceSIDs, v.acl.ServiceSID)
	if err != nil || (v.serviceOnly && !serviceCaller) || (!v.acl.Allows(identity.SID) && !serviceCaller) {
		return ErrSessionBrokerInvalidRequest
	}
	expectedExecutable := v.expectedExecutable
	if serviceCaller && v.serviceExecutable != "" {
		expectedExecutable = v.serviceExecutable
	}
	if expectedExecutable != "" && !strings.EqualFold(normalizeBrokerPath(identity.Executable), normalizeBrokerPath(expectedExecutable)) {
		return ErrSessionBrokerInvalidRequest
	}
	return nil
}

// authenticatedSessionBrokerCaller obtains the caller SID from the pipe
// token and confirms it against the current PID token and image. The repeated
// PID read closes the PID-reuse window between token and image inspection.
func authenticatedSessionBrokerCaller(conn net.Conn) (sessionBrokerCallerIdentity, error) {
	pipe, ok := conn.(interface{ Fd() uintptr })
	if !ok || pipe.Fd() == 0 || pipe.Fd() == ^uintptr(0) {
		return sessionBrokerCallerIdentity{}, ErrSessionBrokerInvalidRequest
	}
	handle := windows.Handle(pipe.Fd())
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := impersonateBrokerPipeClient(handle); err != nil {
		return sessionBrokerCallerIdentity{}, ErrSessionBrokerInvalidRequest
	}
	defer windows.RevertToSelf()
	var token windows.Token
	if err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &token); err != nil {
		return sessionBrokerCallerIdentity{}, ErrSessionBrokerInvalidRequest
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return sessionBrokerCallerIdentity{}, ErrSessionBrokerInvalidRequest
	}
	sid := user.User.Sid.String()
	sessionID, err := brokerTokenUint32(token, windows.TokenSessionId)
	if err != nil {
		return sessionBrokerCallerIdentity{}, ErrSessionBrokerInvalidRequest
	}
	pid, err := brokerNamedPipeClientProcessID(handle)
	if err != nil || pid == 0 {
		return sessionBrokerCallerIdentity{}, ErrSessionBrokerInvalidRequest
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return sessionBrokerCallerIdentity{}, ErrSessionBrokerInvalidRequest
	}
	defer windows.CloseHandle(process)
	var processToken windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &processToken); err != nil {
		return sessionBrokerCallerIdentity{}, ErrSessionBrokerInvalidRequest
	}
	defer processToken.Close()
	processUser, err := processToken.GetTokenUser()
	if err != nil || processUser == nil || processUser.User.Sid == nil || !processUser.User.Sid.Equals(user.User.Sid) {
		return sessionBrokerCallerIdentity{}, ErrSessionBrokerInvalidRequest
	}
	processSession, err := brokerTokenUint32(processToken, windows.TokenSessionId)
	if err != nil || processSession != sessionID {
		return sessionBrokerCallerIdentity{}, ErrSessionBrokerInvalidRequest
	}
	executable, err := brokerProcessImagePath(process)
	if err != nil || strings.TrimSpace(executable) == "" {
		return sessionBrokerCallerIdentity{}, ErrSessionBrokerInvalidRequest
	}
	if currentPID, err := brokerNamedPipeClientProcessID(handle); err != nil || currentPID != pid {
		return sessionBrokerCallerIdentity{}, ErrSessionBrokerInvalidRequest
	}
	serviceSIDs := []string(nil)
	if sid == "S-1-5-18" && sessionID == 0 {
		if groups, groupErr := processToken.GetTokenGroups(); groupErr == nil {
			for _, group := range groups.AllGroups() {
				if group.Sid != nil && group.Attributes&windows.SE_GROUP_ENABLED != 0 && strings.HasPrefix(group.Sid.String(), "S-1-5-80-") {
					serviceSIDs = append(serviceSIDs, group.Sid.String())
				}
			}
		}
	}
	return sessionBrokerCallerIdentity{SID: sid, ServiceSIDs: serviceSIDs, SessionID: sessionID, Executable: executable}, nil
}

func containsSessionBrokerSID(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func impersonateBrokerPipeClient(handle windows.Handle) error {
	r1, _, callErr := brokerAuthImpersonateNamedPipe.Call(uintptr(handle))
	if r1 == 0 {
		if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
			return callErr
		}
		return windows.ERROR_ACCESS_DENIED
	}
	return nil
}

func brokerNamedPipeClientProcessID(handle windows.Handle) (uint32, error) {
	var pid uint32
	r1, _, callErr := brokerAuthGetClientProcessID.Call(uintptr(handle), uintptr(unsafe.Pointer(&pid)))
	if r1 == 0 {
		if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
			return 0, callErr
		}
		return 0, windows.ERROR_INVALID_HANDLE
	}
	return pid, nil
}

func brokerTokenUint32(token windows.Token, class uint32) (uint32, error) {
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

func brokerProcessImagePath(process windows.Handle) (string, error) {
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &size); err != nil {
		return "", err
	}
	return syscall.UTF16ToString(buffer[:size]), nil
}

func normalizeBrokerPath(value string) string {
	path := strings.TrimSpace(strings.ReplaceAll(value, "/", "\\"))
	path = strings.TrimPrefix(path, "\x5c\x5c?\x5c")
	return strings.ToLower(strings.TrimRight(filepath.Clean(path), "\\"))
}
