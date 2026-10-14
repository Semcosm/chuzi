//go:build windows

package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"github.com/Semcosm/chuzi/internal/servicehost"
	"github.com/Semcosm/chuzi/internal/slotwindows"
	"golang.org/x/sys/windows"
)

// runSessionBroker is the only production entrypoint for the fixed broker
// listener. It accepts no arguments: the executable, pipe, service SID, and
// operator identity all come from the signed installation and current token.
func runSessionBroker() error {
	executable, err := os.Executable()
	if err != nil {
		return servicehost.ErrInvalidConfig
	}
	runtime, err := servicehost.NewTrustedRuntime(filepath.Dir(filepath.Clean(executable)))
	if err != nil || !strings.EqualFold(filepath.Clean(executable), runtime.Broker) {
		return servicehost.ErrInvalidConfig
	}
	serviceSID, err := servicehost.ServiceSIDString(servicehost.ServiceName)
	if err != nil {
		return err
	}
	brokerSID, sessionID, err := currentInteractiveIdentity()
	if err != nil || sessionID == 0 || brokerSID == "S-1-5-18" || brokerSID == serviceSID {
		return servicehost.ErrHostUnavailable
	}
	// The signed payload is read/execute-only for the interactive operator.
	// Keep broker-owned staging and its restart fence in the authenticated
	// operator's fixed temporary state root instead of attempting to write
	// beside the installed binaries. The root is derived only from the OS
	// temp directory and the authenticated broker SID.
	stateRoot := filepath.Join(os.TempDir(), "chuzi-session-broker", brokerSID)
	if stateRoot == "" || filepath.Clean(stateRoot) != stateRoot || !filepath.IsAbs(stateRoot) {
		return servicehost.ErrHostUnavailable
	}
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		return servicehost.ErrHostUnavailable
	}
	if err := slotwindows.ProtectSessionBrokerStateRoot(stateRoot, brokerSID); err != nil {
		return servicehost.ErrHostUnavailable
	}
	journal, err := slotwindows.NewFileSessionBrokerJournal(filepath.Join(stateRoot, "session-broker-ownership.json"))
	if err != nil {
		return err
	}
	secrets := slotwindows.NewSessionBrokerSecretStore()
	adapter, err := newRDPSessionAdapter(stateRoot, secrets)
	if err != nil {
		return err
	}
	broker, err := slotwindows.NewSessionBrokerWithJournal(adapter, journal)
	if err != nil {
		return err
	}
	secretListener, err := slotwindows.NewSessionBrokerSecretListener(serviceSID, brokerSID, runtime.Core, runtime.Broker, secrets)
	if err != nil {
		return err
	}
	listener, err := slotwindows.NewSessionBrokerListenerWithExecutables(serviceSID, brokerSID, runtime.Core, runtime.Broker, broker)
	if err != nil {
		_ = secretListener.Close()
		return err
	}
	defer listener.Close()
	defer secretListener.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errs := make(chan error, 2)
	go func() { errs <- secretListener.Serve(ctx) }()
	go func() { errs <- listener.Serve(ctx) }()
	select {
	case <-ctx.Done():
		return nil
	case serveErr := <-errs:
		if serveErr != nil {
			stop()
		}
		return serveErr
	}
}

func currentInteractiveIdentity() (string, uint32, error) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return "", 0, errors.New("token unavailable")
	}
	var session uint32
	var returned uint32
	if err := windows.GetTokenInformation(token, windows.TokenSessionId, (*byte)(unsafe.Pointer(&session)), uint32(unsafe.Sizeof(session)), &returned); err != nil || returned < uint32(unsafe.Sizeof(session)) {
		return "", 0, errors.New("session unavailable")
	}
	return user.User.Sid.String(), session, nil
}
