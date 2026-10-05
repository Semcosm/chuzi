//go:build windows

package slotwindows

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	ErrProcessStart                   = errors.New("slotwindows: process start failed")
	ErrProcessTerminate               = errors.New("slotwindows: process tree termination failed")
	errProcessStartValidation         = errors.New("slotwindows: process start validation failed")
	errProcessStartExecutablePath     = errors.New("slotwindows: process start executable path failed")
	errProcessStartWorkingDirectory   = errors.New("slotwindows: process start working directory failed")
	errProcessStartEnvironment        = errors.New("slotwindows: process start environment failed")
	errProcessStartTokenDuplicate     = errors.New("slotwindows: process start token duplication failed")
	errProcessStartTokenPrivilege     = errors.New("slotwindows: process start token privilege failed")
	errProcessStartCommandLine        = errors.New("slotwindows: process start command line failed")
	errProcessStartJobCreate          = errors.New("slotwindows: process start job creation failed")
	errProcessStartJobConfigure       = errors.New("slotwindows: process start job configuration failed")
	errProcessStartDesktopValidation  = errors.New("slotwindows: process start desktop validation failed")
	errProcessStartDesktopImpersonate = errors.New("slotwindows: process start desktop impersonation failed")
	errProcessStartDesktopOpen        = errors.New("slotwindows: process start desktop open failed")
	errProcessStartDesktopAuthorize   = errors.New("slotwindows: process start desktop authorization failed")
	errProcessStartCreateProcess      = errors.New("slotwindows: CreateProcessAsUser failed")
	errProcessStartBootstrapCreate    = errors.New("slotwindows: bootstrap CreateProcessAsUser failed")
	errProcessStartAgentCreate        = errors.New("slotwindows: agent CreateProcessAsUser failed")
	errProcessStartWin32AccessDenied  = errors.New("slotwindows: process start win32 access denied")
	errProcessStartWin32FileMissing   = errors.New("slotwindows: process start win32 file missing")
	errProcessStartWin32PathMissing   = errors.New("slotwindows: process start win32 path missing")
	errProcessStartWin32InvalidArg    = errors.New("slotwindows: process start win32 invalid parameter")
	errProcessStartWin32BadExecutable = errors.New("slotwindows: process start win32 bad executable")
	errProcessStartWin32Privilege     = errors.New("slotwindows: process start win32 privilege not held")
	errProcessStartWin32Environment   = errors.New("slotwindows: process start win32 environment missing")
	errProcessStartWin32Token         = errors.New("slotwindows: process start win32 token invalid")
	errProcessStartWin32Unknown       = errors.New("slotwindows: process start win32 error unknown")
	errProcessStartJobAssign          = errors.New("slotwindows: process job assignment failed")
	errProcessStartResume             = errors.New("slotwindows: process thread resume failed")
)

var processStartDiagnosticsMu sync.Mutex

type processStartError struct {
	stage  error
	detail error
}

func (e processStartError) Error() string {
	return ErrProcessStart.Error()
}

func (e processStartError) Unwrap() []error {
	if e.detail == nil {
		return []error{ErrProcessStart, e.stage}
	}
	return []error{ErrProcessStart, e.stage, e.detail}
}

func processStartFailure(stage error) error {
	return processStartError{stage: stage}
}

func processStartFailureWithWin32Error(stage error, cause error) error {
	return processStartError{stage: stage, detail: classifyProcessStartWin32Error(cause)}
}

func classifyProcessStartWin32Error(err error) error {
	switch {
	case errors.Is(err, windows.ERROR_ACCESS_DENIED), errors.Is(err, windows.ERROR_ELEVATION_REQUIRED):
		return errProcessStartWin32AccessDenied
	case errors.Is(err, windows.ERROR_FILE_NOT_FOUND):
		return errProcessStartWin32FileMissing
	case errors.Is(err, windows.ERROR_PATH_NOT_FOUND):
		return errProcessStartWin32PathMissing
	case errors.Is(err, windows.ERROR_INVALID_PARAMETER):
		return errProcessStartWin32InvalidArg
	case errors.Is(err, windows.ERROR_BAD_EXE_FORMAT):
		return errProcessStartWin32BadExecutable
	case errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD):
		return errProcessStartWin32Privilege
	case errors.Is(err, windows.ERROR_ENVVAR_NOT_FOUND):
		return errProcessStartWin32Environment
	case errors.Is(err, windows.ERROR_INVALID_HANDLE), errors.Is(err, windows.ERROR_BAD_TOKEN_TYPE), errors.Is(err, windows.ERROR_NO_SUCH_LOGON_SESSION):
		return errProcessStartWin32Token
	default:
		return errProcessStartWin32Unknown
	}
}

var managedDesktopPattern = regexp.MustCompile(`^winsta0\\ChuziSlot[0-9a-f]{16}$`)

const (
	wtsActive                         = 0
	wtsDisconnected                   = 4
	createUnicodeEnvironment          = 0x00000400
	createSuspended                   = 0x00000004
	bootstrapCreateNoWindow           = 0x08000000
	jobObjectLimitKillOnJobClose      = 0x00002000
	jobObjectLimitBreakawayOK         = 0x00000800
	jobObjectExtendedLimitInformation = 9
	tokenAdjustDefault                = 0x0080
	tokenAdjustSessionID              = 0x0100
	tokenPrimaryProcessAccess         = windows.TOKEN_ASSIGN_PRIMARY | windows.TOKEN_DUPLICATE | windows.TOKEN_QUERY | tokenAdjustDefault | tokenAdjustSessionID
)

const createBreakawayFromJob = windows.CREATE_BREAKAWAY_FROM_JOB

// CreateProcessWithTokenW is the last managed-user launch fallback. It is
// intentionally loaded lazily so the normal CreateProcessAsUser boundary
// remains unchanged on hosts where the API is unavailable.
const createProcessWithTokenLogonFlags = 0

// tokenEnvironmentAllowed is the only part of the user environment that a
// managed agent may receive. The agent environment also contains the fixed
// CHUZI_AGENT_* values supplied by the provisioner; no other service variable
// is allowed to cross the session boundary.
var tokenEnvironmentAllowed = map[string]struct{}{
	"ALLUSERSPROFILE":    {},
	"APPDATA":            {},
	"COMSPEC":            {},
	"COMMONPROGRAMFILES": {},
	"HOMEDRIVE":          {},
	"HOMEPATH":           {},
	"LOCALAPPDATA":       {},
	"PATH":               {},
	"PATHEXT":            {},
	"PROGRAMDATA":        {},
	"PROGRAMFILES":       {},
	"PROGRAMFILES(X86)":  {},
	"SYSTEMDRIVE":        {},
	"SYSTEMROOT":         {},
	"TEMP":               {},
	"TMP":                {},
	"USERPROFILE":        {},
	"WINDIR":             {},
}

type Session struct {
	ID    uint32
	State string
}

// FindSession returns only an active session whose token SID exactly matches
// the managed slot identity. Session 0 is never returned.
func FindSession(expectedSID string) (Session, error) {
	if expectedSID == "" {
		return Session{}, ErrSessionIdentity
	}
	var raw *windows.WTS_SESSION_INFO
	var count uint32
	if err := windows.WTSEnumerateSessions(0, 0, 1, &raw, &count); err != nil {
		return Session{}, ErrSessionUnavailable
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(raw)))
	entries := unsafe.Slice(raw, count)
	var disconnected bool
	for _, entry := range entries {
		if entry.SessionID == 0 {
			continue
		}
		if entry.State != wtsActive && entry.State != wtsDisconnected {
			continue
		}
		var token windows.Token
		if err := windows.WTSQueryUserToken(entry.SessionID, &token); err != nil {
			continue
		}
		user, err := token.GetTokenUser()
		_ = token.Close()
		if err != nil || user.User.Sid.String() != expectedSID {
			continue
		}
		if entry.State == wtsDisconnected {
			disconnected = true
			continue
		}
		return Session{ID: entry.SessionID, State: "active"}, nil
	}
	if disconnected {
		return Session{State: "disconnected"}, ErrSessionDisconnected
	}
	return Session{State: "logged_off"}, ErrSessionUnavailable
}

// VerifySession catches disconnect, logoff and session-ID changes before a
// previously captured session is reused.
func VerifySession(expectedSID string, previousID uint32) (Session, error) {
	current, err := FindSession(expectedSID)
	if err != nil {
		return current, err
	}
	if previousID != 0 && current.ID != previousID {
		return current, ErrSessionChanged
	}
	return current, nil
}

// Process owns one process tree. Closing its job handle is a bounded tree-wide
// termination fence scoped to this slot. The service keeps this handle in
// memory only; JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE makes service shutdown (or
// restart) terminate an orphaned agent before the next reconciliation starts
// a replacement.
type Process struct {
	mu      sync.Mutex
	waitMu  sync.Mutex
	process windows.Handle
	thread  windows.Handle
	job     windows.Handle
	closed  bool
}

// BootstrapDesktopAsSlotUser starts a fixed bootstrap agent in the target
// user's session without binding the service to a cross-session desktop. The
// agent attaches itself to that session's WinSta0 and creates the service-
// derived desktop there, then exits before the real agent is launched on it.
func BootstrapDesktopAsSlotUser(expectedSID string, sessionID uint32, executable string, workingDirectory string, desktop string, serviceSID string) error {
	if sessionID == 0 || executable == "" || !filepath.IsAbs(executable) || !strings.EqualFold(filepath.Base(executable), "chuzi-user-agent.exe") || workingDirectory == "" || !filepath.IsAbs(workingDirectory) || !managedDesktopPattern.MatchString(desktop) || serviceSID == "" {
		return processStartFailure(errProcessStartValidation)
	}
	if err := inspectNoReparseChain(executable); err != nil {
		return processStartFailure(errProcessStartExecutablePath)
	}
	if err := inspectDirectoryNoReparseChain(workingDirectory); err != nil {
		return processStartFailure(errProcessStartWorkingDirectory)
	}
	if err := enableCreateProcessPrivileges(); err != nil {
		return processStartFailure(errProcessStartTokenPrivilege)
	}
	session, err := VerifySession(expectedSID, sessionID)
	if err != nil {
		return err
	}
	var token windows.Token
	if err := windows.WTSQueryUserToken(session.ID, &token); err != nil {
		return ErrSessionUnavailable
	}
	defer token.Close()
	var primary windows.Token
	if err := windows.DuplicateTokenEx(token, tokenPrimaryProcessAccess, nil, windows.SecurityImpersonation, windows.TokenPrimary, &primary); err != nil {
		return processStartFailure(errProcessStartTokenDuplicate)
	}
	defer func() { _ = primary.Close() }()
	user, err := primary.GetTokenUser()
	if err != nil || user.User.Sid.String() != expectedSID {
		return ErrSessionIdentity
	}
	// Use the fixed system command interpreter as the CreateProcessAsUser
	// image. The target agent is then a child of that process, where the target
	// user's session and filesystem access are applied to the validated agent
	// path. No caller-provided command text enters this boundary.
	systemDirectory, err := windows.GetSystemDirectory()
	if err != nil || systemDirectory == "" {
		return processStartFailure(errProcessStartExecutablePath)
	}
	commandInterpreter := filepath.Join(systemDirectory, "cmd.exe")
	if err := inspectNoReparseChain(commandInterpreter); err != nil {
		return processStartFailure(errProcessStartExecutablePath)
	}
	application, err := windows.UTF16PtrFromString(commandInterpreter)
	if err != nil {
		return processStartFailure(errProcessStartExecutablePath)
	}
	command, err := windows.UTF16FromString(windows.ComposeCommandLine([]string{commandInterpreter, "/d", "/s", "/c", executable}))
	if err != nil || len(command) == 0 {
		return processStartFailure(errProcessStartCommandLine)
	}
	working, err := windows.UTF16PtrFromString(systemDirectory)
	if err != nil {
		return processStartFailure(errProcessStartWorkingDirectory)
	}
	environment := map[string]string{
		"CHUZI_AGENT_DESKTOP_BOOTSTRAP":   "1",
		"CHUZI_AGENT_DESKTOP":             desktop,
		"CHUZI_AGENT_DESKTOP_SERVICE_SID": serviceSID,
	}
	// Bind the bootstrap to the target session's interactive station. A nil
	// desktop makes CreateProcessAsUser inherit the service's session-0 station,
	// which can reject the target user's token with ERROR_ACCESS_DENIED before
	// the bootstrap gets a chance to attach to WinSta0 itself.
	defaultDesktop, err := windows.UTF16PtrFromString("winsta0\\default")
	if err != nil {
		return processStartFailure(errProcessStartDesktopValidation)
	}
	startup := windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{})), Desktop: defaultDesktop}
	create := func(attempt string, candidate windows.Token, extraFlags uint32, parent windows.Handle) (windows.ProcessInformation, error) {
		env, cleanupEnv, envErr := tokenEnvironment(candidate, environment)
		if envErr != nil {
			createErr := processStartFailure(errProcessStartEnvironment)
			writeProcessLaunchAttempt("bootstrap", attempt, false, parent, windows.ProcessInformation{}, createErr)
			return windows.ProcessInformation{}, createErr
		}
		defer cleanupEnv()
		// CreateProcessAsUserW may modify its command-line buffer. Each attempt
		// therefore receives a fresh copy so the fallback is independent.
		commandCopy := append([]uint16(nil), command...)
		defer clearUTF16(commandCopy)
		var processInfo windows.ProcessInformation
		err := createProcessAsUserWithParent(candidate, application, &commandCopy[0], env, working, &startup, createUnicodeEnvironment|bootstrapCreateNoWindow|extraFlags, parent, &processInfo)
		writeProcessLaunchAttempt("bootstrap", attempt, false, parent, processInfo, err)
		return processInfo, err
	}
	info, createErr := create("wts_primary", primary, 0, 0)
	if errors.Is(createErr, windows.ERROR_ACCESS_DENIED) {
		if fallback, fallbackErr := interactiveProcessToken(expectedSID, session.ID); fallbackErr == nil {
			if fallbackInfo, retryErr := create("interactive_token", fallback, 0, 0); retryErr == nil {
				_ = primary.Close()
				primary = fallback
				info = fallbackInfo
				createErr = nil
			} else {
				writeProcessStartDiagnostics("bootstrap", expectedSID, session.ID, token, fallback, retryErr)
				_ = fallback.Close()
				createErr = retryErr
			}
		}
	}
	if errors.Is(createErr, windows.ERROR_ACCESS_DENIED) {
		if retryInfo, retryErr := create("breakaway", primary, createBreakawayFromJob, 0); retryErr == nil {
			info = retryInfo
			createErr = nil
		}
	}
	if errors.Is(createErr, windows.ERROR_ACCESS_DENIED) {
		if parent, parentErr := interactivePowerShellProcess(expectedSID, session.ID); parentErr == nil {
			if retryInfo, retryErr := create("powershell_parent", primary, 0, parent); retryErr == nil {
				info = retryInfo
				createErr = nil
			} else if retryInfo, retryErr := create("powershell_parent_breakaway", primary, createBreakawayFromJob, parent); retryErr == nil {
				info = retryInfo
				createErr = nil
			}
			_ = windows.CloseHandle(parent)
		}
	}
	if createErr != nil {
		if errors.Is(createErr, errProcessStartEnvironment) {
			return createErr
		}
		writeProcessStartDiagnostics("bootstrap", expectedSID, session.ID, token, primary, createErr)
		return processStartFailureWithWin32Error(errors.Join(errProcessStartCreateProcess, errProcessStartBootstrapCreate), createErr)
	}
	defer windows.CloseHandle(info.Thread)
	defer windows.CloseHandle(info.Process)
	result, waitErr := windows.WaitForSingleObject(info.Process, uint32((30*time.Second)/time.Millisecond))
	if waitErr != nil || result == uint32(windows.WAIT_TIMEOUT) {
		_ = windows.TerminateProcess(info.Process, 1)
		return processStartFailure(errProcessStartDesktopOpen)
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.Process, &code); err != nil || code != 0 {
		return processStartFailure(errProcessStartDesktopOpen)
	}
	return nil
}

// interactiveProcessToken returns a primary token copied from an already
// running process owned by the target user in the target session. The WTS
// token remains the first choice; this fallback is used only when Windows
// rejects the desktop-bound CreateProcessAsUser call with ERROR_ACCESS_DENIED.
// Requiring both the exact SID and session prevents borrowing a token from an
// unrelated interactive user.
func interactiveProcessToken(expectedSID string, sessionID uint32) (windows.Token, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err := windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		process, openErr := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, entry.ProcessID)
		if openErr != nil {
			continue
		}
		var source windows.Token
		openErr = windows.OpenProcessToken(process, windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &source)
		_ = windows.CloseHandle(process)
		if openErr != nil {
			continue
		}
		user, userErr := source.GetTokenUser()
		session, sessionErr := tokenInfoUint32(source, windows.TokenSessionId)
		if userErr != nil || user == nil || user.User.Sid == nil || user.User.Sid.String() != expectedSID || sessionErr != nil || session != sessionID {
			_ = source.Close()
			continue
		}
		var primary windows.Token
		duplicateErr := windows.DuplicateTokenEx(source, tokenPrimaryProcessAccess, nil, windows.SecurityImpersonation, windows.TokenPrimary, &primary)
		_ = source.Close()
		if duplicateErr == nil {
			return primary, nil
		}
	}
	return 0, windows.ERROR_FILE_NOT_FOUND
}

// interactivePowerShellProcess returns a handle suitable for
// PROC_THREAD_ATTRIBUTE_PARENT_PROCESS. It accepts only the fixed Windows
// PowerShell image from the exact managed SID and Session; arbitrary
// same-user processes must never become a service launch parent.
func interactivePowerShellProcess(expectedSID string, sessionID uint32) (windows.Handle, error) {
	systemRoot, err := windows.GetWindowsDirectory()
	if err != nil || systemRoot == "" {
		return 0, err
	}
	expectedImage := filepath.Clean(filepath.Join(systemRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe"))
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err := windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		process, openErr := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_CREATE_PROCESS, false, entry.ProcessID)
		if openErr != nil {
			continue
		}
		keep := false
		var source windows.Token
		if openErr = windows.OpenProcessToken(process, windows.TOKEN_QUERY, &source); openErr == nil {
			user, userErr := source.GetTokenUser()
			tokenSession, sessionErr := tokenInfoUint32(source, windows.TokenSessionId)
			image, imageErr := queryProcessImage(process)
			keep = userErr == nil && user != nil && user.User.Sid != nil &&
				user.User.Sid.String() == expectedSID && sessionErr == nil && tokenSession == sessionID &&
				imageErr == nil && strings.EqualFold(filepath.Clean(image), expectedImage)
			_ = source.Close()
		}
		if keep {
			return process, nil
		}
		_ = windows.CloseHandle(process)
	}
	return 0, windows.ERROR_FILE_NOT_FOUND
}

func queryProcessImage(process windows.Handle) (string, error) {
	buffer := make([]uint16, windows.MAX_PATH)
	for {
		size := uint32(len(buffer))
		err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &size)
		if err == nil {
			return windows.UTF16ToString(buffer[:size]), nil
		}
		if err != windows.ERROR_INSUFFICIENT_BUFFER || len(buffer) >= 32768 {
			return "", err
		}
		buffer = make([]uint16, len(buffer)*2)
	}
}

// StartProcessAsSlotUser is intentionally limited to the packaged user agent;
// the service never exposes a general executable launcher to request callers.
func StartProcessAsSlotUser(expectedSID string, sessionID uint32, executable string, args []string, environment map[string]string, workingDirectory string, desktop string) (*Process, error) {
	return startProcessAsSlotUser(expectedSID, sessionID, executable, args, environment, workingDirectory, desktop, false, false, false, false)
}

func startProcessAsSlotUser(expectedSID string, sessionID uint32, executable string, args []string, environment map[string]string, workingDirectory string, desktop string, forceParentBreakaway bool, commandInterpreter bool, useTokenProcess bool, desktopless bool) (*Process, error) {
	if sessionID == 0 || executable == "" || !filepath.IsAbs(executable) || !strings.EqualFold(filepath.Base(executable), "chuzi-user-agent.exe") || workingDirectory == "" || !filepath.IsAbs(workingDirectory) || !managedDesktopPattern.MatchString(desktop) || len(args) == 0 || args[0] != executable {
		return nil, processStartFailure(errProcessStartValidation)
	}
	if err := inspectNoReparseChain(executable); err != nil {
		return nil, processStartFailure(errProcessStartExecutablePath)
	}
	if err := inspectDirectoryNoReparseChain(workingDirectory); err != nil {
		return nil, processStartFailure(errProcessStartWorkingDirectory)
	}
	if err := enableCreateProcessPrivileges(); err != nil {
		return nil, processStartFailure(errProcessStartTokenPrivilege)
	}
	for key := range environment {
		if !strings.HasPrefix(key, "CHUZI_AGENT_") || strings.ContainsAny(key, "=\x00") {
			return nil, processStartFailure(errProcessStartEnvironment)
		}
	}
	session, err := VerifySession(expectedSID, sessionID)
	if err != nil {
		return nil, err
	}
	var token windows.Token
	if err := windows.WTSQueryUserToken(session.ID, &token); err != nil {
		return nil, ErrSessionUnavailable
	}
	defer token.Close()
	var primary windows.Token
	if err := windows.DuplicateTokenEx(token, tokenPrimaryProcessAccess, nil, windows.SecurityImpersonation, windows.TokenPrimary, &primary); err != nil {
		return nil, processStartFailure(errProcessStartTokenDuplicate)
	}
	defer func() { _ = primary.Close() }()
	user, err := primary.GetTokenUser()
	if err != nil || user.User.Sid.String() != expectedSID {
		return nil, ErrSessionIdentity
	}
	launchApplication := executable
	launchArgs := args
	if commandInterpreter {
		systemDirectory, systemErr := windows.GetSystemDirectory()
		if systemErr != nil || systemDirectory == "" {
			return nil, processStartFailure(errProcessStartExecutablePath)
		}
		launchApplication = filepath.Join(systemDirectory, "cmd.exe")
		if err := inspectNoReparseChain(launchApplication); err != nil {
			return nil, processStartFailure(errProcessStartExecutablePath)
		}
		// The interpreter command is fixed and contains only the validated
		// packaged agent path. This wrapper is used solely for a Windows
		// loader failure fallback.
		launchArgs = []string{launchApplication, "/d", "/s", "/c", executable}
	}
	command, err := windows.UTF16FromString(windows.ComposeCommandLine(launchArgs))
	if err != nil || len(command) == 0 {
		return nil, processStartFailure(errProcessStartCommandLine)
	}
	application, err := windows.UTF16PtrFromString(launchApplication)
	if err != nil {
		return nil, processStartFailure(errProcessStartExecutablePath)
	}
	working, err := windows.UTF16PtrFromString(workingDirectory)
	if err != nil {
		return nil, processStartFailure(errProcessStartWorkingDirectory)
	}
	var defaultDesktop *uint16
	if !desktopless {
		defaultDesktop, err = windows.UTF16PtrFromString("winsta0\\default")
		if err != nil {
			return nil, processStartFailure(errProcessStartDesktopValidation)
		}
	}
	launchEnvironment := make(map[string]string, len(environment)+1)
	for key, value := range environment {
		launchEnvironment[key] = value
	}
	launchEnvironment["CHUZI_AGENT_DESKTOP_BOOTSTRAP"] = "0"
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, processStartFailure(errProcessStartJobCreate)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	// The agent owns worker Job Objects. Allow those workers to break away
	// from this service-owned agent Job while retaining kill-on-close cleanup.
	limits.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose | jobObjectLimitBreakawayOK
	if _, err := windows.SetInformationJobObject(job, jobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		_ = windows.CloseHandle(job)
		return nil, processStartFailure(errProcessStartJobConfigure)
	}
	// The worker is later started explicitly on the managed desktop from inside
	// the target session. A desktopless agent does not need a window station.
	startup := windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{})), Desktop: defaultDesktop}
	suspended := true
	create := func(attempt string, candidate windows.Token, extraFlags uint32, parent windows.Handle, createSuspendedProcess bool) (windows.ProcessInformation, error) {
		if commandInterpreter {
			attempt = "cmd_" + attempt
		}
		if useTokenProcess {
			attempt = "token_" + attempt
		}
		if desktopless {
			attempt = "desktopless_" + attempt
		}
		env, cleanupEnv, envErr := tokenEnvironment(candidate, launchEnvironment)
		if envErr != nil {
			createErr := processStartFailure(errProcessStartEnvironment)
			writeProcessLaunchAttempt("agent", attempt, createSuspendedProcess, parent, windows.ProcessInformation{}, createErr)
			return windows.ProcessInformation{}, createErr
		}
		defer cleanupEnv()
		commandCopy := append([]uint16(nil), command...)
		defer clearUTF16(commandCopy)
		var processInfo windows.ProcessInformation
		flags := uint32(createUnicodeEnvironment) | extraFlags
		if desktopless {
			flags |= bootstrapCreateNoWindow
		}
		if createSuspendedProcess {
			flags |= createSuspended
		}
		var err error
		if useTokenProcess && parent == 0 {
			// CreateProcessWithTokenW uses the token's session directly and
			// requires SeImpersonatePrivilege instead of the
			// CreateProcessAsUser privilege pair.
			err = createProcessWithToken(candidate, application, &commandCopy[0], env, working, &startup, flags, &processInfo)
		} else {
			err = createProcessAsUserWithParent(candidate, application, &commandCopy[0], env, working, &startup, flags, parent, &processInfo)
		}
		if err == nil && useTokenProcess {
			if launchedSession, sessionErr := processSessionID(processInfo.ProcessId); sessionErr != nil || launchedSession != sessionID {
				_ = windows.TerminateProcess(processInfo.Process, 1)
				_ = windows.CloseHandle(processInfo.Thread)
				_ = windows.CloseHandle(processInfo.Process)
				if sessionErr != nil {
					err = sessionErr
				} else {
					err = ErrSessionChanged
				}
			}
		}
		writeProcessLaunchAttempt("agent", attempt, createSuspendedProcess, parent, processInfo, err)
		return processInfo, err
	}
	info, createErr := create("wts_primary", primary, 0, 0, suspended)
	if errors.Is(createErr, windows.ERROR_ACCESS_DENIED) {
		if fallback, fallbackErr := interactiveProcessToken(expectedSID, session.ID); fallbackErr == nil {
			if fallbackInfo, retryErr := create("interactive_token", fallback, 0, 0, suspended); retryErr == nil {
				_ = primary.Close()
				primary = fallback
				info = fallbackInfo
				createErr = nil
			} else {
				writeProcessStartDiagnostics("agent", expectedSID, session.ID, token, fallback, retryErr)
				_ = fallback.Close()
				createErr = retryErr
			}
		}
	}
	if errors.Is(createErr, windows.ERROR_ACCESS_DENIED) {
		if retryInfo, retryErr := create("breakaway", primary, createBreakawayFromJob, 0, suspended); retryErr == nil {
			info = retryInfo
			createErr = nil
		}
	}
	if errors.Is(createErr, windows.ERROR_ACCESS_DENIED) {
		if parent, parentErr := interactivePowerShellProcess(expectedSID, session.ID); parentErr == nil {
			launchParent := func(attempt string, flags uint32) bool {
				retryInfo, retryErr := create(attempt, primary, flags, parent, false)
				if retryErr != nil {
					return false
				}
				info = retryInfo
				createErr = nil
				suspended = false
				return true
			}
			if forceParentBreakaway {
				if !launchParent("powershell_parent_breakaway", createBreakawayFromJob) {
					_ = launchParent("powershell_parent", 0)
				}
			} else {
				if !launchParent("powershell_parent", 0) {
					_ = launchParent("powershell_parent_breakaway", createBreakawayFromJob)
				}
			}
			_ = windows.CloseHandle(parent)
		}
	}
	if createErr != nil {
		_ = windows.CloseHandle(job)
		if errors.Is(createErr, errProcessStartEnvironment) {
			return nil, createErr
		}
		writeProcessStartDiagnostics("agent", expectedSID, session.ID, token, primary, createErr)
		return nil, processStartFailureWithWin32Error(errors.Join(errProcessStartCreateProcess, errProcessStartAgentCreate), createErr)
	}
	if err := windows.AssignProcessToJobObject(job, info.Process); err != nil {
		_ = windows.TerminateProcess(info.Process, 1)
		_ = windows.CloseHandle(info.Thread)
		_ = windows.CloseHandle(info.Process)
		_ = windows.CloseHandle(job)
		return nil, processStartFailure(errProcessStartJobAssign)
	}
	if !suspended {
		return &Process{process: info.Process, thread: info.Thread, job: job}, nil
	}
	resumeCount, err := windows.ResumeThread(info.Thread)
	if err != nil || resumeCount == ^uint32(0) {
		_ = windows.TerminateJobObject(job, 1)
		_ = windows.CloseHandle(info.Thread)
		_ = windows.CloseHandle(info.Process)
		_ = windows.CloseHandle(job)
		return nil, processStartFailure(errProcessStartResume)
	}
	// A process created through a validated interactive parent can carry an
	// additional suspend count from the parent/job boundary. Drain only the
	// bounded counts needed to make the primary thread runnable; never loop on
	// an untrusted or changing value.
	for attempt := 0; resumeCount > 1 && attempt < 3; attempt++ {
		resumeCount, err = windows.ResumeThread(info.Thread)
		if err != nil || resumeCount == ^uint32(0) {
			_ = windows.TerminateJobObject(job, 1)
			_ = windows.CloseHandle(info.Thread)
			_ = windows.CloseHandle(info.Process)
			_ = windows.CloseHandle(job)
			return nil, processStartFailure(errProcessStartResume)
		}
	}
	if resumeCount > 1 {
		_ = windows.TerminateJobObject(job, 1)
		_ = windows.CloseHandle(info.Thread)
		_ = windows.CloseHandle(info.Process)
		_ = windows.CloseHandle(job)
		return nil, processStartFailure(errProcessStartResume)
	}
	return &Process{process: info.Process, thread: info.Thread, job: job}, nil
}

// createProcessAsUserWithParent keeps the ordinary CreateProcessAsUser path
// unchanged. The extended startup path is used only for the explicitly
// validated PowerShell parent fallback, so a SYSTEM task job cannot force the
// managed process to inherit session-0 job or desktop state.
func createProcessAsUserWithParent(token windows.Token, application, command, environment, working *uint16, startup *windows.StartupInfo, flags uint32, parent windows.Handle, processInfo *windows.ProcessInformation) error {
	if parent == 0 {
		return windows.CreateProcessAsUser(token, application, command, nil, nil, false, flags, environment, working, startup, processInfo)
	}
	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return err
	}
	defer attributes.Delete()
	parentValue := uintptr(parent)
	if err := attributes.Update(windows.PROC_THREAD_ATTRIBUTE_PARENT_PROCESS, unsafe.Pointer(&parentValue), unsafe.Sizeof(parentValue)); err != nil {
		return err
	}
	startupEx := windows.StartupInfoEx{
		StartupInfo:             *startup,
		ProcThreadAttributeList: attributes.List(),
	}
	startupEx.StartupInfo.Cb = uint32(unsafe.Sizeof(startupEx))
	return windows.CreateProcessAsUser(token, application, command, nil, nil, false, flags|windows.EXTENDED_STARTUPINFO_PRESENT, environment, working, &startupEx.StartupInfo, processInfo)
}

func createProcessWithToken(token windows.Token, application, command, environment, working *uint16, startup *windows.StartupInfo, flags uint32, processInfo *windows.ProcessInformation) error {
	create := windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateProcessWithTokenW")
	result, _, callErr := create.Call(
		uintptr(token),
		createProcessWithTokenLogonFlags,
		uintptr(unsafe.Pointer(application)),
		uintptr(unsafe.Pointer(command)),
		uintptr(flags),
		uintptr(unsafe.Pointer(environment)),
		uintptr(unsafe.Pointer(working)),
		uintptr(unsafe.Pointer(startup)),
		uintptr(unsafe.Pointer(processInfo)),
	)
	if result != 0 {
		return nil
	}
	if callErr != windows.ERROR_SUCCESS {
		return callErr
	}
	return windows.GetLastError()
}

// CreateProcessAsUser requires the caller to hold SeAssignPrimaryTokenPrivilege
// and SeIncreaseQuotaPrivilege. Service tokens commonly contain these
// privileges in a disabled state, so enable only those two privileges at the
// process boundary immediately before creating the managed user process.
func enableCreateProcessPrivileges() error {
	var token windows.Token
	err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &token)
	if err != nil {
		return err
	}
	defer token.Close()
	for _, name := range []string{"SeAssignPrimaryTokenPrivilege", "SeIncreaseQuotaPrivilege"} {
		nameUTF16, err := windows.UTF16PtrFromString(name)
		if err != nil {
			return err
		}
		var luid windows.LUID
		if err := windows.LookupPrivilegeValue(nil, nameUTF16, &luid); err != nil {
			return err
		}
		state := windows.Tokenprivileges{PrivilegeCount: 1}
		state.Privileges[0] = windows.LUIDAndAttributes{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED}
		if err := windows.AdjustTokenPrivileges(token, false, &state, uint32(unsafe.Sizeof(state)), nil, nil); err != nil {
			return err
		}
		if err := windows.GetLastError(); errors.Is(err, windows.ERROR_NOT_ALL_ASSIGNED) {
			return err
		}
	}
	return nil
}

// writeProcessStartDiagnostics is enabled only by the disposable Windows
// native smoke. It records token metadata and the native failure code without
// writing paths, usernames, credentials, or raw error messages.
func writeProcessStartDiagnostics(stage, expectedSID string, expectedSessionID uint32, token, primary windows.Token, createErr error) {
	if os.Getenv("CHUZI_RUN_WINDOWS_JOB_POOL_SMOKE") != "1" {
		return
	}
	path := nativeSmokeDiagnosticsPath()
	if path == "" {
		return
	}
	processStartDiagnosticsMu.Lock()
	defer processStartDiagnosticsMu.Unlock()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer file.Close()

	write := func(format string, args ...any) {
		_, _ = fmt.Fprintf(file, format+"\n", args...)
	}
	write("DIAGNOSTIC_VERSION=1")
	write("STAGE=%s", stage)
	write("WIN32_ERROR_CLASS=%s", processStartDiagnosticCategory(createErr))
	writeTokenDiagnostics(write, "CURRENT", windows.GetCurrentProcessToken(), expectedSID, expectedSessionID)
	writeTokenDiagnostics(write, "TARGET", token, expectedSID, expectedSessionID)
	writeTokenDiagnostics(write, "PRIMARY", primary, expectedSID, expectedSessionID)
	write("CURRENT_SE_ASSIGN_PRIMARY_TOKEN=%s", tokenPrivilegeState(windows.GetCurrentProcessToken(), "SeAssignPrimaryTokenPrivilege"))
	write("CURRENT_SE_INCREASE_QUOTA=%s", tokenPrivilegeState(windows.GetCurrentProcessToken(), "SeIncreaseQuotaPrivilege"))
	write("CURRENT_SE_IMPERSONATE=%s", tokenPrivilegeState(windows.GetCurrentProcessToken(), "SeImpersonatePrivilege"))
	write("PRIMARY_TOKEN_ACCESS_INFORMATION=%s", tokenInfoState(primary, windows.TokenAccessInformation))
	write("END=1")
}

func nativeSmokeDiagnosticsPath() string {
	path := strings.TrimSpace(os.Getenv("CHUZI_WINDOWS_JOB_POOL_SMOKE_NATIVE_TOKEN_DIAGNOSTICS"))
	if path == "" {
		root := strings.TrimSpace(os.Getenv("CHUZI_WINDOWS_JOB_POOL_SMOKE_ROOT"))
		if root == "" || !filepath.IsAbs(root) || strings.ContainsAny(root, "\x00\r\n") {
			return ""
		}
		path = filepath.Join(root, "native-token-diagnostics.log")
	}
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
		return ""
	}
	return path
}

func writeProcessLaunchAttempt(stage, attempt string, suspended bool, parent windows.Handle, info windows.ProcessInformation, createErr error) {
	if os.Getenv("CHUZI_RUN_WINDOWS_JOB_POOL_SMOKE") != "1" || stage == "" || attempt == "" {
		return
	}
	path := nativeSmokeDiagnosticsPath()
	if path == "" {
		return
	}
	processStartDiagnosticsMu.Lock()
	defer processStartDiagnosticsMu.Unlock()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	write := func(format string, args ...any) { _, _ = fmt.Fprintf(file, format+"\n", args...) }
	write("LAUNCH_STAGE=%s", stage)
	write("LAUNCH_ATTEMPT=%s", attempt)
	write("LAUNCH_CREATE_SUSPENDED=%t", suspended)
	if parent == 0 {
		write("LAUNCH_PARENT_PROCESS_ID=none")
	} else if processID, processErr := windows.GetProcessId(parent); processErr == nil {
		write("LAUNCH_PARENT_PROCESS_ID=%d", processID)
	} else {
		write("LAUNCH_PARENT_PROCESS_ID=unavailable")
	}
	if createErr != nil {
		write("LAUNCH_RESULT=%s", processStartDiagnosticCategory(createErr))
	} else {
		write("LAUNCH_RESULT=success")
		write("LAUNCH_PROCESS_ID=%d", info.ProcessId)
		write("LAUNCH_THREAD_ID=%d", info.ThreadId)
		if sessionID, sessionErr := processSessionID(info.ProcessId); sessionErr == nil {
			write("LAUNCH_SESSION_ID=%d", sessionID)
		} else {
			write("LAUNCH_SESSION_ID=unavailable")
		}
		write("LAUNCH_IMAGE_CLASS=%s", processImageClass(info.Process))
	}
	write("LAUNCH_END=1")
}

func processSessionID(processID uint32) (uint32, error) {
	if processID == 0 {
		return 0, windows.ERROR_INVALID_PARAMETER
	}
	var sessionID uint32
	if err := windows.ProcessIdToSessionId(processID, &sessionID); err != nil {
		return 0, err
	}
	return sessionID, nil
}

func processParentID(processID uint32) (uint32, error) {
	if processID == 0 {
		return 0, windows.ERROR_INVALID_PARAMETER
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err := windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if entry.ProcessID == processID {
			return entry.ParentProcessID, nil
		}
	}
	return 0, windows.ERROR_FILE_NOT_FOUND
}

func processImageClass(process windows.Handle) string {
	if process == 0 {
		return "unavailable"
	}
	image, err := queryProcessImage(process)
	if err != nil {
		return "unavailable"
	}
	if strings.EqualFold(filepath.Base(image), "chuzi-user-agent.exe") {
		return "chuzi_user_agent"
	}
	return "other"
}

func writeTokenDiagnostics(write func(string, ...any), label string, token windows.Token, expectedSID string, expectedSessionID uint32) {
	if token == 0 {
		write("%s_TOKEN_PRESENT=false", label)
		return
	}
	write("%s_TOKEN_PRESENT=true", label)
	if value, err := tokenInfoUint32(token, windows.TokenType); err == nil {
		write("%s_TOKEN_TYPE=%d", label, value)
	} else {
		write("%s_TOKEN_TYPE=unavailable", label)
	}
	if value, err := tokenInfoUint32(token, windows.TokenSessionId); err == nil {
		write("%s_TOKEN_SESSION_ID=%d", label, value)
		write("%s_TOKEN_SESSION_MATCH=%t", label, value == expectedSessionID)
	} else {
		write("%s_TOKEN_SESSION_ID=unavailable", label)
		write("%s_TOKEN_SESSION_MATCH=unavailable", label)
	}
	user, err := token.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		write("%s_TOKEN_SID_MATCH=unavailable", label)
	} else {
		write("%s_TOKEN_SID_MATCH=%t", label, user.User.Sid.String() == expectedSID)
	}
}

func tokenInfoUint32(token windows.Token, class uint32) (uint32, error) {
	var value uint32
	var returned uint32
	if err := windows.GetTokenInformation(token, class, (*byte)(unsafe.Pointer(&value)), uint32(unsafe.Sizeof(value)), &returned); err != nil {
		return 0, err
	}
	return value, nil
}

func tokenInfoState(token windows.Token, class uint32) string {
	if token == 0 {
		return "unavailable"
	}
	var size uint32
	if err := windows.GetTokenInformation(token, class, nil, 0, &size); err != windows.ERROR_INSUFFICIENT_BUFFER || size == 0 {
		return "unavailable"
	}
	buffer := make([]byte, size)
	if err := windows.GetTokenInformation(token, class, &buffer[0], uint32(len(buffer)), &size); err != nil {
		return "unavailable"
	}
	return "available"
}

func tokenPrivilegeState(token windows.Token, name string) string {
	if token == 0 {
		return "unavailable"
	}
	nameUTF16, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return "unavailable"
	}
	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, nameUTF16, &luid); err != nil {
		return "unavailable"
	}
	var size uint32
	if err := windows.GetTokenInformation(token, windows.TokenPrivileges, nil, 0, &size); err != windows.ERROR_INSUFFICIENT_BUFFER || size == 0 {
		return "unavailable"
	}
	buffer := make([]byte, size)
	if err := windows.GetTokenInformation(token, windows.TokenPrivileges, &buffer[0], uint32(len(buffer)), &size); err != nil {
		return "unavailable"
	}
	privileges := (*windows.Tokenprivileges)(unsafe.Pointer(&buffer[0]))
	for _, privilege := range privileges.AllPrivileges() {
		if privilege.Luid == luid {
			if privilege.Attributes&windows.SE_PRIVILEGE_ENABLED != 0 {
				return "enabled"
			}
			if privilege.Attributes&windows.SE_PRIVILEGE_ENABLED_BY_DEFAULT != 0 {
				return "enabled_by_default"
			}
			return "disabled"
		}
	}
	return "absent"
}

func processStartDiagnosticCategory(err error) string {
	switch classifyProcessStartWin32Error(err) {
	case errProcessStartWin32AccessDenied:
		return "access_denied"
	case errProcessStartWin32Privilege:
		return "privilege_not_held"
	case errProcessStartWin32FileMissing:
		return "file_missing"
	case errProcessStartWin32PathMissing:
		return "path_missing"
	case errProcessStartWin32InvalidArg:
		return "invalid_parameter"
	case errProcessStartWin32BadExecutable:
		return "bad_executable"
	case errProcessStartWin32Environment:
		return "environment_missing"
	case errProcessStartWin32Token:
		return "token_invalid"
	default:
		return "unknown"
	}
}

// StartProcessAsSlotUserForSlot retains the older ordinal-only helper for
// internal compatibility. New service code must use the slot-ID helper below.
func StartProcessAsSlotUserForSlot(expectedSID string, sessionID uint32, ordinal int, executable string, args []string, environment map[string]string, workingDirectory string) (*Process, error) {
	if ordinal < 1 || ordinal > 9999 {
		return nil, processStartFailure(errProcessStartDesktopValidation)
	}
	desktop, err := managedDesktopName(fmt.Sprintf("ordinal-%d", ordinal))
	if err != nil {
		return nil, processStartFailure(errProcessStartDesktopValidation)
	}
	return StartProcessAsSlotUser(expectedSID, sessionID, executable, args, environment, workingDirectory, "winsta0\\"+desktop)
}

// StartProcessAsSlotUserForSlotID derives the desktop from the service-owned
// logical slot ID; callers cannot supply a desktop name.
func StartProcessAsSlotUserForSlotID(expectedSID string, sessionID uint32, slotID string, executable string, args []string, environment map[string]string, workingDirectory string) (*Process, error) {
	return startProcessAsSlotUserForSlotID(expectedSID, sessionID, slotID, executable, args, environment, workingDirectory, false, false, false, false)
}

// startProcessAsSlotUserForSlotIDBreakaway retries the validated parent
// fallback outside any inherited parent Job Object. It is used only after a
// created agent exits during Windows loader initialization.
func startProcessAsSlotUserForSlotIDBreakaway(expectedSID string, sessionID uint32, slotID string, executable string, args []string, environment map[string]string, workingDirectory string) (*Process, error) {
	return startProcessAsSlotUserForSlotID(expectedSID, sessionID, slotID, executable, args, environment, workingDirectory, true, false, false, false)
}

func startProcessAsSlotUserForSlotIDCommandInterpreter(expectedSID string, sessionID uint32, slotID string, executable string, args []string, environment map[string]string, workingDirectory string) (*Process, error) {
	return startProcessAsSlotUserForSlotID(expectedSID, sessionID, slotID, executable, args, environment, workingDirectory, true, true, false, false)
}

func startProcessAsSlotUserForSlotIDDesktopless(expectedSID string, sessionID uint32, slotID string, executable string, args []string, environment map[string]string, workingDirectory string) (*Process, error) {
	return startProcessAsSlotUserForSlotID(expectedSID, sessionID, slotID, executable, args, environment, workingDirectory, true, false, false, true)
}

func startProcessAsSlotUserForSlotIDToken(expectedSID string, sessionID uint32, slotID string, executable string, args []string, environment map[string]string, workingDirectory string) (*Process, error) {
	return startProcessAsSlotUserForSlotID(expectedSID, sessionID, slotID, executable, args, environment, workingDirectory, true, false, true, false)
}

func startProcessAsSlotUserForSlotID(expectedSID string, sessionID uint32, slotID string, executable string, args []string, environment map[string]string, workingDirectory string, forceParentBreakaway bool, commandInterpreter bool, useTokenProcess bool, desktopless bool) (*Process, error) {
	desktop, err := managedDesktopName(slotID)
	if err != nil {
		return nil, processStartFailure(errProcessStartDesktopValidation)
	}
	return startProcessAsSlotUser(expectedSID, sessionID, executable, args, environment, workingDirectory, "winsta0\\"+desktop, forceParentBreakaway, commandInterpreter, useTokenProcess, desktopless)
}

// verifySessionDesktop validates the session fence and managed desktop name.
// Desktop existence is established by the target-session bootstrap and the
// successful CreateProcessAsUser call; a service in another session cannot
// OpenDesktop on the user's WinSta0 directly.
func verifySessionDesktop(expectedSID string, sessionID uint32, desktop string) error {
	if !managedDesktopPattern.MatchString("winsta0\\" + desktop) {
		return ErrProcessStart
	}
	_, err := VerifySession(expectedSID, sessionID)
	return err
}

func (p *Process) Wait(timeout time.Duration) (uint32, error) {
	if p == nil || timeout < 0 {
		return 0, ErrProcessStart
	}
	p.waitMu.Lock()
	defer p.waitMu.Unlock()
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return 0, ErrProcessStart
	}
	process := p.process
	p.mu.Unlock()
	milliseconds := uint32(timeout / time.Millisecond)
	if milliseconds == 0 && timeout > 0 {
		milliseconds = 1
	}
	result, err := windows.WaitForSingleObject(process, milliseconds)
	if err != nil {
		return 0, ErrProcessStart
	}
	if result == uint32(windows.WAIT_TIMEOUT) {
		return 0, context.DeadlineExceeded
	}
	var code uint32
	if err := windows.GetExitCodeProcess(process, &code); err != nil {
		return 0, ErrProcessStart
	}
	return code, nil
}

func (p *Process) Terminate() error {
	if p == nil {
		return nil
	}
	p.waitMu.Lock()
	defer p.waitMu.Unlock()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	if result, err := windows.WaitForSingleObject(p.process, 0); err == nil && result == uint32(windows.WAIT_OBJECT_0) {
		return nil
	}
	if err := windows.TerminateJobObject(p.job, 1); err != nil {
		// Termination races with natural process exit are idempotent. Confirm
		// the process state before surfacing a real cleanup failure.
		if result, waitErr := windows.WaitForSingleObject(p.process, 0); waitErr == nil && result == uint32(windows.WAIT_OBJECT_0) {
			return nil
		}
		return ErrProcessTerminate
	}
	result, waitErr := windows.WaitForSingleObject(p.process, uint32((2*time.Second)/time.Millisecond))
	if waitErr != nil || result != uint32(windows.WAIT_OBJECT_0) {
		return ErrProcessTerminate
	}
	return nil
}

func (p *Process) Close() error {
	if p == nil {
		return nil
	}
	p.waitMu.Lock()
	defer p.waitMu.Unlock()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	_ = windows.CloseHandle(p.thread)
	_ = windows.CloseHandle(p.process)
	if err := windows.CloseHandle(p.job); err != nil {
		return ErrProcessTerminate
	}
	return nil
}

func tokenEnvironment(token windows.Token, overrides map[string]string) (*uint16, func(), error) {
	values := make(map[string]string)
	var block *uint16
	if err := windows.CreateEnvironmentBlock(&block, token, false); err == nil && block != nil {
		readTokenEnvironment(block, values)
		_ = windows.DestroyEnvironmentBlock(block)
	}
	// A freshly-created disposable account can have a valid interactive token
	// before userenv.dll has finished materializing its profile. In that small
	// window CreateEnvironmentBlock fails. Launching with no environment would
	// break Windows process startup, so fill only the non-sensitive system
	// variables needed by the fixed agent/runtime boundary.
	fillSystemEnvironment(values)
	if profile, err := tokenProfileDirectory(token); err == nil && profile != "" {
		values["USERPROFILE"] = "USERPROFILE=" + profile
		values["APPDATA"] = "APPDATA=" + filepath.Join(profile, "AppData", "Roaming")
		values["LOCALAPPDATA"] = "LOCALAPPDATA=" + filepath.Join(profile, "AppData", "Local")
		values["TEMP"] = "TEMP=" + filepath.Join(profile, "AppData", "Local", "Temp")
		values["TMP"] = "TMP=" + filepath.Join(profile, "AppData", "Local", "Temp")
		if drive := filepath.VolumeName(profile); drive != "" {
			values["HOMEDRIVE"] = "HOMEDRIVE=" + drive
			values["HOMEPATH"] = "HOMEPATH=" + strings.TrimPrefix(profile, drive)
		}
	}
	for key, value := range overrides {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, '\x00') {
			return nil, func() {}, ErrProcessStart
		}
		values[strings.ToUpper(key)] = key + "=" + value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	entries := make([]string, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, values[key])
	}
	encoded, err := encodeEnvironmentBlock(entries)
	if err != nil {
		return nil, func() {}, fmt.Errorf("%w", ErrProcessStart)
	}
	return &encoded[0], func() {}, nil
}

func encodeEnvironmentBlock(entries []string) ([]uint16, error) {
	if len(entries) == 0 {
		return nil, ErrProcessStart
	}
	encoded := make([]uint16, 0, len(entries)*2+1)
	for _, entry := range entries {
		if entry == "" || strings.ContainsRune(entry, '\x00') {
			return nil, ErrProcessStart
		}
		encoded = append(encoded, utf16.Encode([]rune(entry))...)
		encoded = append(encoded, 0)
	}
	// CreateProcessAsUser requires a Unicode environment block terminated by
	// two NUL code units. UTF16FromString cannot be used because it rejects
	// the embedded separators that are required by this API.
	encoded = append(encoded, 0)
	return encoded, nil
}

func readTokenEnvironment(block *uint16, values map[string]string) {
	for offset, limit := uintptr(0), uintptr(32767); offset < limit; {
		start := (*uint16)(unsafe.Add(unsafe.Pointer(block), offset*2))
		length := uintptr(0)
		for offset+length < limit && *(*uint16)(unsafe.Add(unsafe.Pointer(block), (offset+length)*2)) != 0 {
			length++
		}
		if length == 0 {
			return
		}
		entry := windows.UTF16ToString(unsafe.Slice(start, length))
		if index := strings.IndexByte(entry, '='); index > 0 {
			key := strings.ToUpper(entry[:index])
			if _, ok := tokenEnvironmentAllowed[key]; ok {
				values[key] = key + "=" + entry[index+1:]
			}
		}
		offset += length + 1
	}
}

func fillSystemEnvironment(values map[string]string) {
	for key := range tokenEnvironmentAllowed {
		if _, ok := values[key]; ok {
			continue
		}
		if value, ok := os.LookupEnv(key); ok && value != "" {
			values[key] = key + "=" + value
		}
	}
	if _, ok := values["SYSTEMROOT"]; !ok {
		if value, err := windows.GetWindowsDirectory(); err == nil && value != "" {
			values["SYSTEMROOT"] = "SYSTEMROOT=" + value
			values["WINDIR"] = "WINDIR=" + value
		}
	}
	if _, ok := values["PATH"]; !ok {
		if value, err := windows.GetSystemDirectory(); err == nil && value != "" {
			values["PATH"] = "PATH=" + value
		}
	}
}

func tokenProfileDirectory(token windows.Token) (string, error) {
	var length uint32
	if err := windows.GetUserProfileDirectory(token, nil, &length); err != windows.ERROR_INSUFFICIENT_BUFFER || length == 0 {
		return "", err
	}
	buffer := make([]uint16, length)
	if err := windows.GetUserProfileDirectory(token, &buffer[0], &length); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buffer), nil
}
