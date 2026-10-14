//go:build windows

package main

import (
	"context"
	"errors"
	"github.com/Semcosm/chuzi/internal/sessionruntime"
	"net"
	"os"
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
	root      string
	secrets   *slotwindows.SessionBrokerSecretStore
	mu        sync.Mutex
	active    map[string]*rdpOwnedSession
	journal   *slotwindows.FileBrokerTargetJournal
	allocator *sessionruntime.LoopbackAllocator
}

type rdpOwnedSession struct {
	ownership slotwindows.SessionBrokerOwnership
	process   *rdpProcess
	target    string
	username  string
}

func newRDPSessionAdapter(root string, secrets *slotwindows.SessionBrokerSecretStore) (*rdpSessionAdapter, error) {
	if root == "" || filepath.Clean(root) != root || !filepath.IsAbs(root) || secrets == nil {
		return nil, slotwindows.ErrSessionBrokerInvalidRequest
	}
	journal, err := slotwindows.NewFileBrokerTargetJournal(filepath.Join(root, "session-broker-targets.json"))
	if err != nil {
		return nil, err
	}
	allocator, err := newBrokerAllocator(journal)
	if err != nil {
		return nil, err
	}
	if _, err := journal.List(context.Background()); err != nil {
		return nil, err
	}
	return &rdpSessionAdapter{root: root, secrets: secrets, journal: journal, allocator: allocator, active: make(map[string]*rdpOwnedSession)}, nil
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
	// A reservation without a committed broker session remains quarantined after
	// crash. Absence at one instant cannot prove that pending Winlogon finished.
	if _, exists, err := findBrokerTarget(ctx, a.journal, a.allocator, ownership); err != nil || exists {
		return slotwindows.BootstrapSession{}, slotwindows.ErrSessionBrokerUnknownOwnership
	}
	reservation, err := a.allocator.Allocate(ctx, brokerTargetIdentity(ownership))
	if err != nil {
		return slotwindows.BootstrapSession{}, slotwindows.ErrSessionUnavailable
	}
	launched := false
	credentialClean := false
	targetHost, _, err := net.SplitHostPort(reservation.Endpoint())
	if err != nil {
		return slotwindows.BootstrapSession{}, slotwindows.ErrSessionUnavailable
	}
	defer func() {
		if !launched && credentialClean {
			_ = a.allocator.Release(context.WithoutCancel(ctx), reservation)
		}
	}()
	target := "TERMSRV/" + targetHost
	username := localRDPUsername(secret.Username)
	if err := writeRDPCredential(target, username, secret.Password); err != nil {
		return slotwindows.BootstrapSession{}, slotwindows.ErrSessionUnavailable
	}
	credentialOwned := true
	defer func() {
		if credentialOwned {
			credentialClean = deleteOwnedRDPCredential(target, username) == nil
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
	process, err := startRDPProcess(mstsc, rdpPath)
	if err != nil {
		return slotwindows.BootstrapSession{}, err
	}
	launched = true
	done := process.done
	keepProcess := false
	defer func() {
		if !keepProcess {
			_ = process.Close()
		}
	}()
	deadline := time.Now().Add(rdpLoginTimeout)
	if requested, ok := ctx.Deadline(); ok && requested.Before(deadline) {
		deadline = requested
	}
	for {
		if ctx.Err() != nil || time.Now().After(deadline) {
			return slotwindows.BootstrapSession{}, slotwindows.ErrSessionUnavailable
		}
		current, findErr := slotwindows.BrokerFindSession(ctx)
		if findErr == nil && current.State == "active" && current.ID != 0 {
			a.mu.Lock()
			a.active[ownership.SlotID] = &rdpOwnedSession{ownership: ownership, process: process, target: target, username: username}
			a.mu.Unlock()
			credentialOwned = false
			keepProcess = true
			return slotwindows.BootstrapSession{ID: current.ID, State: "active"}, nil
		}
		if findErr != nil && !errors.Is(findErr, slotwindows.ErrSessionUnavailable) {
			return slotwindows.BootstrapSession{}, findErr
		}
		select {
		case <-ctx.Done():
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
	current, err := slotwindows.BrokerFindSession(ctx)
	if err != nil {
		return slotwindows.BootstrapSession{ID: current.ID, State: current.State}, err
	}
	return slotwindows.BootstrapSession{ID: current.ID, State: current.State}, nil
}

func (a *rdpSessionAdapter) Stop(ctx context.Context, ownership slotwindows.SessionBrokerOwnership) error {
	if a == nil || ctx == nil {
		return slotwindows.ErrSessionUnavailable
	}
	current, err := slotwindows.BrokerFindSession(ctx)
	absent := errors.Is(err, slotwindows.ErrSessionUnavailable) && current.ID == 0 && current.State == "logged_off"
	if !absent {
		if err != nil && !errors.Is(err, slotwindows.ErrSessionDisconnected) {
			return err
		}
		if current.ID != ownership.SessionID {
			return slotwindows.ErrSessionChanged
		}
		if err := slotwindows.BrokerLogoffSession(ctx, current.ID); err != nil {
			return err
		}
	}
	a.mu.Lock()
	owned := a.active[ownership.SlotID]
	if owned != nil && (owned.ownership.PoolID != ownership.PoolID || owned.ownership.SID != ownership.SID || owned.ownership.Generation != ownership.Generation || owned.ownership.Owner != ownership.Owner) {
		a.mu.Unlock()
		return slotwindows.ErrSessionChanged
	}
	a.mu.Unlock()
	if owned != nil && owned.process != nil {
		if err := owned.process.Close(); err != nil {
			return slotwindows.ErrSessionUnavailable
		}
		a.mu.Lock()
		owned.process = nil
		a.mu.Unlock()
	}
	deadline := time.Now().Add(10 * time.Second)
	for !absent {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return slotwindows.ErrSessionUnavailable
		}
		observed, err := slotwindows.BrokerFindSession(ctx)
		absent = errors.Is(err, slotwindows.ErrSessionUnavailable) && observed.ID == 0 && observed.State == "logged_off"
		if !absent && err != nil && !errors.Is(err, slotwindows.ErrSessionDisconnected) {
			return err
		}
		if !absent && observed.ID != ownership.SessionID {
			return slotwindows.ErrSessionChanged
		}
		if !absent {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	target, exists, err := findBrokerTarget(ctx, a.journal, a.allocator, ownership)
	if err != nil {
		return slotwindows.ErrSessionBrokerUnknownOwnership
	}
	if exists {
		host, _, err := net.SplitHostPort(target.Endpoint())
		if err != nil {
			return slotwindows.ErrSessionBrokerUnknownOwnership
		}
		username := localRDPUsername(accountNameForSID(ownership.SID))
		if err := deleteOwnedRDPCredential("TERMSRV/"+host, username); err != nil {
			return err
		}
		if err := a.allocator.Release(ctx, target); err != nil {
			return slotwindows.ErrSessionUnavailable
		}
	}
	// A missing reservation is idempotent only after authoritative WTS absence:
	// a previous Stop may have released it before persisting its tombstone.
	a.mu.Lock()
	delete(a.active, ownership.SlotID)
	a.mu.Unlock()
	return nil
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
	exists, err := credentialExists(target)
	if err != nil {
		return err
	}
	if exists {
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

func credentialExists(target string) (bool, error) {
	targetPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return false, slotwindows.ErrSessionUnavailable
	}
	for _, credentialType := range []uint32{rdpCredentialType, 1} {
		var value *nativeCredential
		if result, _, callErr := credRead.Call(uintptr(unsafe.Pointer(targetPtr)), uintptr(credentialType), 0, uintptr(unsafe.Pointer(&value))); result != 0 && value != nil {
			credFree.Call(uintptr(unsafe.Pointer(value)))
			return true, nil
		} else if result != 0 || !errors.Is(callErr, windows.ERROR_NOT_FOUND) {
			return false, slotwindows.ErrSessionUnavailable
		}
	}
	return false, nil
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
		if result, _, callErr := credRead.Call(uintptr(unsafe.Pointer(targetPtr)), uintptr(credentialType), 0, uintptr(unsafe.Pointer(&value))); result == 0 || value == nil {
			if result == 0 && errors.Is(callErr, windows.ERROR_NOT_FOUND) {
				continue
			}
			return slotwindows.ErrSessionUnavailable
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
