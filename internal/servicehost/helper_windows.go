//go:build windows

package servicehost

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"github.com/Semcosm/chuzi/internal/coretransport"
	"golang.org/x/sys/windows"
)

var (
	helperWTSAPI          = windows.NewLazySystemDLL("wtsapi32.dll")
	helperWTSQuerySession = helperWTSAPI.NewProc("WTSQuerySessionInformationW")
	helperWTSFreeMemory   = helperWTSAPI.NewProc("WTSFreeMemory")
)

const (
	helperWTSConnectState uint32 = 8
	helperWTSActive       uint32 = 0
)

const helperBrokerPipe = "\\\\.\\pipe\\chuzi-session-bootstrap-v1"

// WindowsHelperStarter is the concrete service-side starter for the fixed
// interactive broker. The requested SID/session identify the managed slot and
// are used only to exclude that disposable account. The broker is launched
// under the single authenticated operator session discovered from WTS; a slot
// token is never used as the helper identity.
type WindowsHelperStarter struct {
	mu     sync.Mutex
	next   uint64
	active map[uint32]*windowsHelperProcess
}

// NewWindowsHelperStarter returns a starter with no mutable process state
// shared with callers. The service should retain this value for its lifetime.
func NewWindowsHelperStarter() *WindowsHelperStarter {
	return &WindowsHelperStarter{active: make(map[uint32]*windowsHelperProcess)}
}

func (s *WindowsHelperStarter) Start(ctx context.Context, runtime TrustedRuntime, sid string, session uint32) (HelperProcess, error) {
	if s == nil || ctx == nil || ctx.Err() != nil || runtime.Validate() != nil || !validSIDText(sid) || session == 0 {
		return nil, ErrHelperUnavailable
	}
	s.mu.Lock()
	if s.active == nil {
		s.active = make(map[uint32]*windowsHelperProcess)
	}
	s.mu.Unlock()
	if filepath.Clean(runtime.Broker) != runtime.Broker || strings.ContainsAny(runtime.Broker, "\"\r\n") {
		return nil, ErrHelperMismatch
	}
	brokerPath, err := windows.UTF16PtrFromString(runtime.Broker)
	if err != nil {
		return nil, ErrHelperMismatch
	}
	if info, err := windows.GetFileAttributes(brokerPath); err != nil || info&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return nil, ErrHelperUnavailable
	}
	operatorSID, operatorSession, err := findInteractiveOperator(sid, session)
	if err != nil {
		return nil, ErrHelperUnavailable
	}
	s.mu.Lock()
	for _, active := range s.active {
		if active != nil && active.sid == operatorSID && active.session == operatorSession {
			s.mu.Unlock()
			return nil, ErrHelperUnavailable
		}
	}
	s.mu.Unlock()
	var userToken windows.Token
	err = windows.WTSQueryUserToken(operatorSession, &userToken)
	if err != nil {
		return nil, ErrHelperUnavailable
	}
	defer userToken.Close()
	user, err := userToken.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil || user.User.Sid.String() != operatorSID {
		return nil, ErrHelperMismatch
	}
	if tokenSession, err := helperTokenUint32(userToken, windows.TokenSessionId); err != nil || tokenSession != operatorSession {
		return nil, ErrHelperMismatch
	}
	if active, err := helperSessionActive(operatorSession); err != nil || !active {
		return nil, ErrHelperUnavailable
	}
	primary, err := duplicateHelperPrimaryToken(userToken)
	if err != nil {
		return nil, ErrHelperUnavailable
	}
	defer primary.Close()
	commandLine, err := windows.UTF16FromString("\"" + runtime.Broker + "\"")
	if err != nil {
		return nil, ErrHelperMismatch
	}
	workingDirectory, err := windows.UTF16PtrFromString(runtime.Root)
	if err != nil {
		return nil, ErrHelperMismatch
	}
	desktop, err := windows.UTF16PtrFromString("winsta0\\default")
	if err != nil {
		return nil, ErrHelperMismatch
	}
	startup := windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{})), Desktop: desktop, ShowWindow: windows.SW_HIDE}
	var processInfo windows.ProcessInformation
	flags := uint32(windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_NEW_PROCESS_GROUP)
	if err := windows.CreateProcessAsUser(primary, nil, &commandLine[0], nil, nil, false, flags, nil, workingDirectory, &startup, &processInfo); err != nil {
		return nil, ErrHelperUnavailable
	}
	_ = windows.CloseHandle(processInfo.Thread)
	process := &windowsHelperProcess{starter: s, handle: processInfo.Process, pid: processInfo.ProcessId, sid: operatorSID, session: operatorSession, executable: filepath.Clean(runtime.Broker), generation: s.nextGeneration()}
	s.mu.Lock()
	s.active[process.pid] = process
	s.mu.Unlock()
	return process, nil
}

// findInteractiveOperator selects the authenticated operator session for the
// helper. More than one candidate is ambiguous and therefore rejected; the
// service must never guess which desktop may drive a login.
func findInteractiveOperator(excludedSID string, excludedSession uint32) (string, uint32, error) {
	var raw *windows.WTS_SESSION_INFO
	var count uint32
	if err := windows.WTSEnumerateSessions(0, 0, 1, &raw, &count); err != nil || raw == nil {
		return "", 0, ErrHelperUnavailable
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(raw)))
	var candidateSID string
	var candidateSession uint32
	for _, entry := range unsafe.Slice(raw, count) {
		if entry.SessionID == 0 || entry.SessionID == excludedSession || entry.State != helperWTSActive {
			continue
		}
		var token windows.Token
		if err := windows.WTSQueryUserToken(entry.SessionID, &token); err != nil {
			continue
		}
		user, err := token.GetTokenUser()
		_ = token.Close()
		if err != nil || user == nil || user.User.Sid == nil {
			continue
		}
		sid := user.User.Sid.String()
		if sid == excludedSID || sid == "S-1-5-18" || strings.HasPrefix(sid, "S-1-5-80-") {
			continue
		}
		if candidateSID != "" {
			return "", 0, ErrHelperUnavailable
		}
		candidateSID, candidateSession = sid, entry.SessionID
	}
	if candidateSID == "" || candidateSession == 0 {
		return "", 0, ErrHelperUnavailable
	}
	return candidateSID, candidateSession, nil
}

func (s *WindowsHelperStarter) nextGeneration() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	if s.next == 0 {
		s.next++
	}
	return s.next
}

type windowsHelperProcess struct {
	starter    *WindowsHelperStarter
	handle     windows.Handle
	pid        uint32
	sid        string
	session    uint32
	executable string
	generation uint64
	mu         sync.Mutex
	stopped    bool
}

func (p *windowsHelperProcess) Registration(ctx context.Context) (HelperRegistration, error) {
	if p == nil || ctx == nil || ctx.Err() != nil {
		return HelperRegistration{}, ErrHelperUnavailable
	}
	p.mu.Lock()
	if p.stopped || p.handle == 0 {
		p.mu.Unlock()
		return HelperRegistration{}, ErrHelperStopped
	}
	handle, pid, sid, session, executable, generation := p.handle, p.pid, p.sid, p.session, p.executable, p.generation
	p.mu.Unlock()
	if event, _ := windows.WaitForSingleObject(handle, 0); event != uint32(windows.WAIT_TIMEOUT) {
		return HelperRegistration{}, ErrHelperUnavailable
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return HelperRegistration{}, ErrHelperUnavailable
	}
	defer windows.CloseHandle(process)
	var token windows.Token
	err = windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token)
	if err != nil {
		return HelperRegistration{}, ErrHelperUnavailable
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil || user.User.Sid.String() != sid {
		return HelperRegistration{}, ErrHelperMismatch
	}
	tokenSession, err := helperTokenUint32(token, windows.TokenSessionId)
	if err != nil || tokenSession != session {
		return HelperRegistration{}, ErrHelperMismatch
	}
	if active, err := helperSessionActive(session); err != nil || !active {
		return HelperRegistration{}, ErrHelperUnavailable
	}
	image, err := helperProcessImage(process)
	if err != nil || !helperPathsEqual(image, executable) {
		return HelperRegistration{}, ErrHelperMismatch
	}
	if err := waitHelperBrokerPipe(ctx, sid, session, executable); err != nil {
		return HelperRegistration{}, err
	}
	return HelperRegistration{SID: sid, SessionID: session, Generation: generation, Executable: executable, State: HelperReady, Heartbeat: time.Now().UTC()}, nil
}

func (p *windowsHelperProcess) Stop(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return nil
	}
	p.stopped = true
	handle, pid := p.handle, p.pid
	p.handle = 0
	p.mu.Unlock()
	if p.starter != nil {
		p.starter.mu.Lock()
		delete(p.starter.active, pid)
		p.starter.mu.Unlock()
	}
	if handle == 0 {
		return nil
	}
	defer windows.CloseHandle(handle)
	if event, _ := windows.WaitForSingleObject(handle, 0); event == uint32(windows.WAIT_TIMEOUT) {
		if err := windows.TerminateProcess(handle, 1); err != nil && !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return ErrHelperUnavailable
		}
	}
	for {
		if event, _ := windows.WaitForSingleObject(handle, 0); event != uint32(windows.WAIT_TIMEOUT) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ErrHelperUnavailable
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func duplicateHelperPrimaryToken(token windows.Token) (windows.Token, error) {
	var primary windows.Token
	access := uint32(windows.TOKEN_ASSIGN_PRIMARY | windows.TOKEN_DUPLICATE | windows.TOKEN_QUERY | windows.TOKEN_ADJUST_DEFAULT | windows.TOKEN_ADJUST_SESSIONID)
	if err := windows.DuplicateTokenEx(token, access, nil, windows.SecurityImpersonation, windows.TokenPrimary, &primary); err != nil {
		return 0, err
	}
	return primary, nil
}

func helperTokenUint32(token windows.Token, class uint32) (uint32, error) {
	var value uint32
	var returned uint32
	if err := windows.GetTokenInformation(token, class, (*byte)(unsafe.Pointer(&value)), uint32(unsafe.Sizeof(value)), &returned); err != nil || returned < uint32(unsafe.Sizeof(value)) {
		return 0, windows.ERROR_INVALID_DATA
	}
	return value, nil
}

func helperProcessImage(process windows.Handle) (string, error) {
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &size); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buffer[:size]), nil
}

func helperPathsEqual(actual, expected string) bool {
	normalize := func(value string) string {
		value = strings.TrimSpace(strings.ReplaceAll(value, "/", "\\"))
		if trimmed := strings.TrimPrefix(value, "\\\\?\\UNC\\"); trimmed != value {
			value = "\\\\" + trimmed
		} else if trimmed := strings.TrimPrefix(value, "\\\\?\\"); trimmed != value {
			value = trimmed
		}
		return strings.ToLower(strings.TrimRight(filepath.Clean(value), "\\"))
	}
	return filepath.IsAbs(expected) && normalize(actual) == normalize(expected)
}

func waitHelperBrokerPipe(ctx context.Context, sid string, session uint32, executable string) error {
	if ctx == nil {
		return ErrHelperUnavailable
	}
	probeCtx := ctx
	cancel := func() {}
	if _, ok := probeCtx.Deadline(); !ok {
		probeCtx, cancel = context.WithTimeout(probeCtx, 5*time.Second)
	}
	defer cancel()
	for {
		conn, err := winio.DialPipeContext(probeCtx, helperBrokerPipe)
		if err == nil {
			identityErr := coretransport.ValidateWindowsPipePeer(conn, sid, session, executable)
			_ = conn.Close()
			if identityErr != nil {
				return ErrHelperMismatch
			}
			return nil
		}
		select {
		case <-probeCtx.Done():
			return ErrHelperUnavailable
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func helperSessionActive(session uint32) (bool, error) {
	var buffer *uint32
	var bytes uint32
	r1, _, callErr := helperWTSQuerySession.Call(0, uintptr(session), uintptr(helperWTSConnectState), uintptr(unsafe.Pointer(&buffer)), uintptr(unsafe.Pointer(&bytes)))
	if r1 == 0 || buffer == nil || bytes < 4 {
		if callErr != nil {
			return false, callErr
		}
		return false, windows.ERROR_INVALID_DATA
	}
	defer helperWTSFreeMemory.Call(uintptr(unsafe.Pointer(buffer)))
	state := *buffer
	return state == helperWTSActive, nil
}
