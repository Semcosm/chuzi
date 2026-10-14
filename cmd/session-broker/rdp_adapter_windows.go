//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/Semcosm/chuzi/internal/slotwindows"
	"golang.org/x/sys/windows"
)

const (
	rdpCredentialType    = 2
	rdpCredentialPersist = 1
	rdpLoginTimeout      = 2 * time.Minute
)

var (
	credAdvapi32 = windows.NewLazySystemDLL("advapi32.dll")
	credWrite    = credAdvapi32.NewProc("CredWriteW")
	credRead     = credAdvapi32.NewProc("CredReadW")
	credFree     = credAdvapi32.NewProc("CredFree")
	credDelete   = credAdvapi32.NewProc("CredDeleteW")
	wtsAdapter   = windows.NewLazySystemDLL("wtsapi32.dll").NewProc("WTSLogoffSession")
)

type nativeCredential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        int64
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

type rdpSessionAdapter struct {
	root    string
	secrets *slotwindows.SessionBrokerSecretStore
	mu      sync.Mutex
	active  map[string]*rdpOwnedSession
}

type rdpOwnedSession struct {
	ownership slotwindows.SessionBrokerOwnership
	process   *os.Process
	target    string
	username  string
}

func newRDPSessionAdapter(root string, secrets *slotwindows.SessionBrokerSecretStore) (*rdpSessionAdapter, error) {
	if root == "" || filepath.Clean(root) != root || !filepath.IsAbs(root) || secrets == nil {
		return nil, slotwindows.ErrSessionBrokerInvalidRequest
	}
	return &rdpSessionAdapter{root: root, secrets: secrets, active: make(map[string]*rdpOwnedSession)}, nil
}

func (a *rdpSessionAdapter) Start(ctx context.Context, ownership slotwindows.SessionBrokerOwnership) (slotwindows.BootstrapSession, error) {
	if a == nil || ctx == nil || ctx.Err() != nil {
		return slotwindows.BootstrapSession{}, slotwindows.ErrSessionUnavailable
	}
	secret, err := a.secrets.Take(ownership)
	if err != nil {
		return slotwindows.BootstrapSession{}, slotwindows.ErrSessionUnavailable
	}
	defer clear(secret.Password)
	targetHost := rdpTargetHost(ownership.Ordinal)
	target := "TERMSRV/" + targetHost
	username := localRDPUsername(secret.Username)
	if err := writeRDPCredential(target, username, secret.Password); err != nil {
		return slotwindows.BootstrapSession{}, slotwindows.ErrSessionUnavailable
	}
	credentialOwned := true
	defer func() {
		if credentialOwned {
			_ = deleteOwnedRDPCredential(target, username)
		}
	}()
	rdpPath, err := writeRDPProfile(a.root, targetHost, username, ownership)
	if err != nil {
		return slotwindows.BootstrapSession{}, slotwindows.ErrSessionUnavailable
	}
	defer os.Remove(rdpPath)
	systemDir, err := windows.GetSystemDirectory()
	if err != nil || systemDir == "" {
		return slotwindows.BootstrapSession{}, slotwindows.ErrSessionUnavailable
	}
	mstsc := filepath.Join(systemDir, "mstsc.exe")
	command := exec.Command(mstsc, rdpPath)
	if err := command.Start(); err != nil {
		return slotwindows.BootstrapSession{}, slotwindows.ErrSessionUnavailable
	}
	process := command.Process
	done := make(chan struct{})
	go func() { _ = command.Wait(); close(done) }()
	deadline := time.Now().Add(rdpLoginTimeout)
	if requested, ok := ctx.Deadline(); ok && requested.Before(deadline) {
		deadline = requested
	}
	for {
		if ctx.Err() != nil || time.Now().After(deadline) {
			_ = process.Kill()
			<-done
			return slotwindows.BootstrapSession{}, slotwindows.ErrSessionUnavailable
		}
		current, findErr := slotwindows.FindSession(ownership.SID)
		if findErr == nil && current.State == "active" && current.ID != 0 {
			a.mu.Lock()
			a.active[ownership.SlotID] = &rdpOwnedSession{ownership: ownership, process: process, target: target, username: username}
			a.mu.Unlock()
			credentialOwned = false
			return slotwindows.BootstrapSession{ID: current.ID, State: "active"}, nil
		}
		select {
		case <-ctx.Done():
			_ = process.Kill()
			<-done
			return slotwindows.BootstrapSession{}, slotwindows.ErrSessionUnavailable
		case <-done:
			return slotwindows.BootstrapSession{}, slotwindows.ErrSessionUnavailable
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (a *rdpSessionAdapter) Find(ctx context.Context, sid string) (slotwindows.BootstrapSession, error) {
	if a == nil || ctx == nil || sid == "" {
		return slotwindows.BootstrapSession{}, slotwindows.ErrSessionUnavailable
	}
	if err := ctx.Err(); err != nil {
		return slotwindows.BootstrapSession{}, err
	}
	current, err := slotwindows.FindSession(sid)
	if err != nil {
		return slotwindows.BootstrapSession{ID: current.ID, State: current.State}, err
	}
	return slotwindows.BootstrapSession{ID: current.ID, State: current.State}, nil
}

func (a *rdpSessionAdapter) Stop(ctx context.Context, ownership slotwindows.SessionBrokerOwnership) error {
	if a == nil || ctx == nil {
		return slotwindows.ErrSessionUnavailable
	}
	current, err := slotwindows.FindSession(ownership.SID)
	if err != nil {
		if errors.Is(err, slotwindows.ErrSessionUnavailable) && current.State == "logged_off" {
			if cleanupErr := deleteOwnedRDPCredential("TERMSRV/"+rdpTargetHost(ownership.Ordinal), localRDPUsername(accountNameForSID(ownership.SID))); cleanupErr != nil {
				return slotwindows.ErrSessionUnavailable
			}
			return nil
		}
		return slotwindows.ErrSessionUnavailable
	}
	if current.ID != ownership.SessionID || current.State != "active" {
		return slotwindows.ErrSessionChanged
	}
	if result, _, callErr := wtsAdapter.Call(0, uintptr(current.ID), 0); result == 0 {
		_ = callErr
		return slotwindows.ErrSessionUnavailable
	}
	a.mu.Lock()
	owned := a.active[ownership.SlotID]
	delete(a.active, ownership.SlotID)
	a.mu.Unlock()
	if owned != nil && owned.process != nil {
		_ = owned.process.Kill()
	}
	deadline := time.Now().Add(10 * time.Second)
	if requested, ok := ctx.Deadline(); ok && requested.Before(deadline) {
		deadline = requested
	}
	for {
		if time.Now().After(deadline) {
			return slotwindows.ErrSessionUnavailable
		}
		observed, findErr := slotwindows.FindSession(ownership.SID)
		if errors.Is(findErr, slotwindows.ErrSessionUnavailable) && observed.State == "logged_off" {
			username := ""
			if owned != nil {
				username = owned.username
			}
			if username == "" {
				username = localRDPUsername(accountNameForSID(ownership.SID))
			}
			if cleanupErr := deleteOwnedRDPCredential("TERMSRV/"+rdpTargetHost(ownership.Ordinal), username); cleanupErr != nil {
				return slotwindows.ErrSessionUnavailable
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return slotwindows.ErrSessionUnavailable
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func rdpTargetHost(ordinal int) string {
	if ordinal < 1 {
		ordinal = 1
	}
	index := ordinal - 1
	return fmt.Sprintf("127.0.%d.%d", 1+index/253, 2+index%253)
}

func writeRDPProfile(root, host, username string, ownership slotwindows.SessionBrokerOwnership) (string, error) {
	if root == "" || host == "" || username == "" || ownership.SlotID == "" {
		return "", slotwindows.ErrSessionBrokerInvalidRequest
	}
	file, err := os.CreateTemp(root, ".chuzi-session-*.rdp")
	if err != nil {
		return "", err
	}
	path := file.Name()
	contents := strings.Join([]string{
		"full address:s:" + host + ":3389",
		"username:s:" + username,
		"prompt for credentials:i:0",
		"administrative session:i:0",
		"screen mode id:i:2",
		"desktopwidth:i:1024",
		"desktopheight:i:768",
		"redirectclipboard:i:0",
		"redirectdrives:i:0",
		"redirectprinters:i:0",
		"audiomode:i:2",
		"autoreconnection enabled:i:0",
		"authentication level:i:2",
		"negotiate security layer:i:1",
	}, "\r\n") + "\r\n"
	if _, err := file.WriteString(contents); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func writeRDPCredential(target, username string, password []uint16) error {
	if target == "" || username == "" || len(password) == 0 {
		return slotwindows.ErrSessionBrokerSecretInvalid
	}
	if credentialExists(target) {
		return slotwindows.ErrSessionBrokerSecretBusy
	}
	targetPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	userPtr, err := windows.UTF16PtrFromString(username)
	if err != nil {
		return err
	}
	bytes := make([]byte, len(password)*2)
	for i, value := range password {
		bytes[i*2] = byte(value)
		bytes[i*2+1] = byte(value >> 8)
	}
	defer clear(bytes)
	credential := nativeCredential{Type: rdpCredentialType, TargetName: targetPtr, CredentialBlobSize: uint32(len(bytes)), CredentialBlob: &bytes[0], Persist: rdpCredentialPersist, UserName: userPtr}
	if result, _, callErr := credWrite.Call(uintptr(unsafe.Pointer(&credential)), 0); result == 0 {
		_ = callErr
		return slotwindows.ErrSessionUnavailable
	}
	return nil
}

func credentialExists(target string) bool {
	targetPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return false
	}
	for _, credentialType := range []uint32{rdpCredentialType, 1} {
		var value *nativeCredential
		if result, _, _ := credRead.Call(uintptr(unsafe.Pointer(targetPtr)), uintptr(credentialType), 0, uintptr(unsafe.Pointer(&value))); result != 0 && value != nil {
			credFree.Call(uintptr(unsafe.Pointer(value)))
			return true
		}
	}
	return false
}

func deleteOwnedRDPCredential(target, expectedUsername string) error {
	if err := validateRDPCredentialCleanup(target, expectedUsername); err != nil {
		return err
	}
	targetPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return slotwindows.ErrSessionUnavailable
	}
	for _, credentialType := range []uint32{rdpCredentialType, 1} {
		var value *nativeCredential
		if result, _, _ := credRead.Call(uintptr(unsafe.Pointer(targetPtr)), uintptr(credentialType), 0, uintptr(unsafe.Pointer(&value))); result == 0 || value == nil {
			continue
		}
		actual := ""
		if value.UserName != nil {
			actual = windows.UTF16PtrToString(value.UserName)
		}
		credFree.Call(uintptr(unsafe.Pointer(value)))
		if !strings.EqualFold(actual, expectedUsername) {
			return slotwindows.ErrSessionBrokerSecretInvalid
		}
		if result, _, _ := credDelete.Call(uintptr(unsafe.Pointer(targetPtr)), uintptr(credentialType), 0); rdpCredentialDeleteResult(result) != nil {
			return slotwindows.ErrSessionUnavailable
		}
	}
	return nil
}

func accountNameForSID(value string) string {
	sid, err := windows.StringToSid(value)
	if err != nil {
		return ""
	}
	name := make([]uint16, 256)
	domain := make([]uint16, 256)
	nameLen := uint32(len(name))
	domainLen := uint32(len(domain))
	var use uint32
	if err := windows.LookupAccountSid(nil, sid, &name[0], &nameLen, &domain[0], &domainLen, &use); err != nil {
		return ""
	}
	return windows.UTF16ToString(name[:nameLen])
}
