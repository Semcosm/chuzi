package slotwindows

import (
	"bufio"
	"context"
	"io"
	"net"
	"time"
)

const sessionBrokerCallTimeout = 30 * time.Second

// exchangeSessionBroker shares the bounded wire exchange between the fixed
// Windows pipe client and deterministic transport tests. The caller owns conn.
func exchangeSessionBroker(ctx context.Context, conn net.Conn, request SessionBrokerRequest) (BootstrapSession, error) {
	if ctx == nil || ctx.Err() != nil || conn == nil {
		return BootstrapSession{}, ErrSessionUnavailable
	}
	payload, err := EncodeSessionBrokerRequest(request)
	if err != nil {
		return BootstrapSession{}, ErrSessionUnavailable
	}
	payload = append(payload, '\n')
	defer clear(payload)
	callCtx, cancel := context.WithTimeout(ctx, sessionBrokerCallTimeout)
	defer cancel()
	deadline, _ := callCtx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return BootstrapSession{}, ErrSessionUnavailable
	}
	stop := context.AfterFunc(callCtx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	if written, err := conn.Write(payload); err != nil || written != len(payload) {
		return BootstrapSession{}, ErrSessionUnavailable
	}
	data, err := bufio.NewReader(io.LimitReader(conn, 8192)).ReadBytes('\n')
	if err != nil {
		return BootstrapSession{}, ErrSessionUnavailable
	}
	response, err := DecodeSessionBrokerResponse(data)
	if err != nil || response.Code != SessionBrokerOK || response.Operation != request.Operation || callCtx.Err() != nil {
		return BootstrapSession{}, ErrSessionUnavailable
	}
	return BootstrapSession{ID: response.SessionID, State: response.State}, nil
}
