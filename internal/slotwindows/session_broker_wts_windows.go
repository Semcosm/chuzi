//go:build windows

package slotwindows

import (
	"context"
	"errors"
	"golang.org/x/sys/windows"
)

var serviceWTSLogoff = windows.NewLazySystemDLL("wtsapi32.dll").NewProc("WTSLogoffSession")

func serviceBrokerWTS(ctx context.Context, request SessionBrokerRequest, kind string, id uint32) (BootstrapSession, error) {
	if ctx == nil || ctx.Err() != nil || request.validate() != nil {
		return BootstrapSession{}, ErrSessionQuery
	}
	current, err := FindSession(request.SID)
	session := BootstrapSession{ID: current.ID, State: current.State}
	if kind == "wts_query" {
		return session, err
	}
	if kind != "wts_logoff" || id == 0 {
		return BootstrapSession{}, ErrSessionQuery
	}
	if sessionConfirmedAbsent(session, err) {
		return BootstrapSession{}, nil
	}
	if err != nil && !errors.Is(err, ErrSessionDisconnected) {
		return session, err
	}
	if current.ID != id {
		return session, ErrSessionChanged
	}
	if result, _, _ := serviceWTSLogoff.Call(0, uintptr(id), 0); result == 0 {
		return BootstrapSession{}, ErrSessionQuery
	}
	return BootstrapSession{}, nil
}
