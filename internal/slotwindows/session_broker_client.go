package slotwindows

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"time"
)

const sessionBrokerCallTimeout = 30 * time.Second

// exchangeSessionBroker shares the bounded wire exchange between the fixed
// Windows pipe client and deterministic transport tests. The caller owns conn.
func exchangeSessionBroker(ctx context.Context, conn net.Conn, request SessionBrokerRequest) (BootstrapSession, error) {
	return exchangeSessionBrokerWithWTS(ctx, conn, request, nil)
}

func exchangeSessionBrokerWithWTS(ctx context.Context, conn net.Conn, request SessionBrokerRequest, handler brokerWTSHandler) (BootstrapSession, error) {
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
	reader := bufio.NewReader(io.LimitReader(conn, 1024*1024))
	for {
		data, err := reader.ReadBytes('\n')
		if err != nil || len(data) > 8192 {
			return BootstrapSession{}, ErrSessionUnavailable
		}
		var envelope struct{ Kind string }
		if json.Unmarshal(data, &envelope) != nil {
			return BootstrapSession{}, ErrSessionUnavailable
		}
		if envelope.Kind != "" {
			var frame brokerWTSFrame
			if decodeJSONLine(data, &frame) != nil || answerBrokerWTS(callCtx, conn, request, frame, handler) != nil {
				return BootstrapSession{}, ErrSessionQuery
			}
			continue
		}
		response, err := DecodeSessionBrokerResponse(data)
		if err != nil || response.Code != SessionBrokerOK || response.Operation != request.Operation || callCtx.Err() != nil {
			return BootstrapSession{}, ErrSessionUnavailable
		}
		return BootstrapSession{ID: response.SessionID, State: response.State}, nil
	}
}
