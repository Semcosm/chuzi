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
	// The deployment adapter is intentionally fail-closed until a reviewed
	// RDP/Winlogon implementation is installed. The listener and protocol are
	// still real; no fabricated successful session can cross the pipe.
	broker := slotwindows.NewSessionBroker(unavailableSessionAdapter{})
	listener, err := slotwindows.NewSessionBrokerListenerWithExecutables(serviceSID, brokerSID, runtime.Core, runtime.Broker, broker)
	if err != nil {
		return err
	}
	defer listener.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return listener.Serve(ctx)
}

type unavailableSessionAdapter struct{}

func (unavailableSessionAdapter) Start(context.Context, slotwindows.SessionBrokerOwnership) (slotwindows.BootstrapSession, error) {
	return slotwindows.BootstrapSession{}, slotwindows.ErrSessionUnavailable
}

func (unavailableSessionAdapter) Stop(context.Context, slotwindows.SessionBrokerOwnership) error {
	return slotwindows.ErrSessionUnavailable
}

func (unavailableSessionAdapter) Find(context.Context, string) (slotwindows.BootstrapSession, error) {
	return slotwindows.BootstrapSession{}, slotwindows.ErrSessionUnavailable
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
