//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
	"unsafe"

	"github.com/Semcosm/chuzi/internal/sessionruntime"
	"golang.org/x/sys/windows"
)

const wtsActive = 0

type probeInput struct {
	Schema         string `json:"schema"`
	RunID          string `json:"run_id"`
	LoginMethod    string `json:"login_method"`
	SID            string `json:"sid"`
	SessionID      uint32 `json:"session_id"`
	OldSessionGone bool   `json:"old_session_gone"`
	ShellReady     bool   `json:"shell_ready"`
	AgentReady     bool   `json:"agent_ready"`
	WorkerReady    bool   `json:"worker_ready"`
	IdentityCheck  string `json:"identity_check"`
	LoginCheck     string `json:"login_check"`
	HiveCheck      string `json:"hive_check"`
	NativePending  bool   `json:"native_pending"`
}

type wtsSessionInfo struct {
	SessionID      uint32
	WinStationName *uint16
	State          uint32
}

var (
	wtsDLL                   = windows.NewLazySystemDLL("wtsapi32.dll")
	procWTSOpenServer        = wtsDLL.NewProc("WTSOpenServerW")
	procWTSCloseServer       = wtsDLL.NewProc("WTSCloseServer")
	procWTSEnumerateSessions = wtsDLL.NewProc("WTSEnumerateSessionsW")
	procWTSFreeMemory        = wtsDLL.NewProc("WTSFreeMemory")
	procWTSQueryUserToken    = wtsDLL.NewProc("WTSQueryUserToken")
	probeRunIDPattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	probeSIDPattern          = regexp.MustCompile(`^S-1-[0-9]+(?:-[0-9]+)+$`)
)

const (
	probeLoginMethod   = "registered-interactive-helper-loopback-rdp"
	probeIdentityFirst = "helper_sid_and_session_verified"
	probeIdentityRetry = "same_owned_sid_after_relogin"
	probeLoginFirst    = "automatic_credential_manager_login"
	probeLoginRetry    = "automatic_credential_manager_relogin"
	probeHiveFirst     = "sid_derived_profile_hive_ready"
	probeHiveRetry     = "sid_derived_profile_hive_reused"
)

func runProbe() {
	manifestPath := flag.String("manifest", "", "service-owned probe manifest")
	outputPath := flag.String("output", "", "service-owned redacted report")
	flag.Parse()
	if strings.TrimSpace(*manifestPath) == "" || strings.TrimSpace(*outputPath) == "" {
		fmt.Fprintln(os.Stderr, "manifest and output are required")
		os.Exit(2)
	}
	input, err := readInput(*manifestPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "probe manifest rejected")
		os.Exit(2)
	}
	now := time.Now().UTC()
	report := sessionruntime.ProbeReport{Schema: "chuzi.session-probe/v1", RunID: input.RunID, LoginMethod: input.LoginMethod, PrivilegeClass: "unknown", SessionID: input.SessionID, OldSessionGone: input.OldSessionGone, ShellReady: input.ShellReady, AgentReady: input.AgentReady, WorkerReady: input.WorkerReady, NativePending: input.NativePending}
	report.Checkpoints = []sessionruntime.Checkpoint{{Name: "manifest", State: input.IdentityCheck, ObservedAt: now}, {Name: "login", State: input.LoginCheck, ObservedAt: now}, {Name: "profile_hive", State: input.HiveCheck, ObservedAt: now}, {Name: "shell", State: boolState(input.ShellReady), ObservedAt: now}, {Name: "agent", State: boolState(input.AgentReady), ObservedAt: now}, {Name: "worker", State: boolState(input.WorkerReady), ObservedAt: now}}
	current, tokenErr := windows.GetCurrentProcessToken().GetTokenUser()
	if tokenErr == nil && current.User.Sid != nil && current.User.Sid.String() == "S-1-5-18" {
		report.PrivilegeClass = "system_tcb_verified"
	}
	if input.SID != "" {
		digest := sha256.Sum256([]byte(input.SID))
		report.SIDHash = hex.EncodeToString(digest[:])[:16]
	}
	active, sessionErr := activeSessionMatches(input.SID, input.SessionID)
	if report.PrivilegeClass != "system_tcb_verified" || sessionErr != nil || !active {
		report.Checkpoints = append(report.Checkpoints, sessionruntime.Checkpoint{Name: "wts_sid_match", State: "failed", ObservedAt: time.Now().UTC()})
		if !writeReport(*outputPath, report) {
			fmt.Fprintln(os.Stderr, "probe report unavailable")
		}
		os.Exit(1)
	}
	report.Checkpoints = append(report.Checkpoints, sessionruntime.Checkpoint{Name: "wts_sid_match", State: "ready", ObservedAt: time.Now().UTC()})
	if !writeReport(*outputPath, report) {
		fmt.Fprintln(os.Stderr, "probe report unavailable")
		os.Exit(1)
	}
}

func readInput(path string) (probeInput, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return probeInput{}, err
	}
	if containsForbiddenField(raw) {
		return probeInput{}, errors.New("sensitive field")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var input probeInput
	if err := decoder.Decode(&input); err != nil || input.Schema != "chuzi.session-probe-input/v1" || !probeRunIDPattern.MatchString(input.RunID) || input.LoginMethod != probeLoginMethod || !probeSIDPattern.MatchString(input.SID) || input.SessionID == 0 || !validInputValues(input) {
		return probeInput{}, errors.New("invalid manifest")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return probeInput{}, errors.New("trailing manifest data")
	} else if !errors.Is(err, io.EOF) {
		return probeInput{}, errors.New("invalid manifest tail")
	}
	return input, nil
}

func containsForbiddenField(raw []byte) bool {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err == nil && object != nil {
		for key, child := range object {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "password") || strings.Contains(lower, "secret") || strings.Contains(lower, "credential") || strings.Contains(lower, "endpoint") || strings.Contains(lower, "profile") {
				return true
			}
			if containsForbiddenField(child) {
				return true
			}
		}
		return false
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err == nil {
		for _, child := range list {
			if containsForbiddenField(child) {
				return true
			}
		}
	}
	return false
}

func validInputValues(input probeInput) bool {
	first := input.IdentityCheck == probeIdentityFirst && input.LoginCheck == probeLoginFirst && input.HiveCheck == probeHiveFirst
	retry := input.IdentityCheck == probeIdentityRetry && input.LoginCheck == probeLoginRetry && input.HiveCheck == probeHiveRetry
	return first || retry
}

func activeSessionMatches(expectedSID string, expectedID uint32) (bool, error) {
	server, _, _ := procWTSOpenServer.Call(0)
	if server == 0 {
		return false, errors.New("wts unavailable")
	}
	defer procWTSCloseServer.Call(server)
	var sessions *wtsSessionInfo
	var count uint32
	ok, _, _ := procWTSEnumerateSessions.Call(server, 0, 1, uintptr(unsafe.Pointer(&sessions)), uintptr(unsafe.Pointer(&count)))
	if ok == 0 {
		return false, errors.New("wts enumerate failed")
	}
	defer procWTSFreeMemory.Call(uintptr(unsafe.Pointer(sessions)))
	items := unsafe.Slice(sessions, count)
	for _, item := range items {
		if item.State != wtsActive || item.SessionID != expectedID {
			continue
		}
		var token windows.Token
		ok, _, _ := procWTSQueryUserToken.Call(uintptr(item.SessionID), uintptr(unsafe.Pointer(&token)))
		if ok == 0 {
			continue
		}
		user, err := token.GetTokenUser()
		_ = token.Close()
		if err == nil && user.User.Sid != nil && user.User.Sid.String() == expectedSID {
			return true, nil
		}
	}
	return false, nil
}

func boolState(value bool) string {
	if value {
		return "ready"
	}
	return "not_attempted"
}

func writeReport(path string, report sessionruntime.ProbeReport) bool {
	if err := report.Validate(); err != nil {
		return false
	}
	data, err := json.Marshal(report)
	if err != nil {
		return false
	}
	return os.WriteFile(path, data, 0o600) == nil
}
