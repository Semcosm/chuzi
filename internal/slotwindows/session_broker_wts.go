package slotwindows

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"
)

// WTS callbacks reuse the service-authenticated request connection. They carry
// no SID: the service binds every query/logoff to its original managed request.
// The operator never obtains a user token or an arbitrary logoff capability.
type brokerWTSFrame struct {
	Version   int    `json:"version"`
	Kind      string `json:"kind"`
	SessionID uint32 `json:"session_id,omitempty"`
	State     string `json:"state,omitempty"`
	Code      string `json:"code,omitempty"`
}

type brokerWTSAuthority struct {
	conn   net.Conn
	reader *bufio.Reader
	mu     sync.Mutex
}
type brokerWTSKey struct{}

func withBrokerWTSAuthority(ctx context.Context, conn net.Conn, reader *bufio.Reader) context.Context {
	return context.WithValue(ctx, brokerWTSKey{}, &brokerWTSAuthority{conn: conn, reader: reader})
}

func brokerWTSCall(ctx context.Context, kind string, id uint32) (BootstrapSession, error) {
	if ctx == nil || ctx.Err() != nil {
		return BootstrapSession{}, ErrSessionQuery
	}
	authority, ok := ctx.Value(brokerWTSKey{}).(*brokerWTSAuthority)
	if !ok || authority == nil {
		return BootstrapSession{}, ErrSessionQuery
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	if requested, ok := ctx.Deadline(); ok && requested.Before(deadline) {
		deadline = requested
	}
	if err := authority.conn.SetDeadline(deadline); err != nil {
		return BootstrapSession{}, ErrSessionQuery
	}
	defer authority.conn.SetDeadline(time.Time{})
	stop := context.AfterFunc(ctx, func() { _ = authority.conn.SetDeadline(time.Now()) })
	defer stop()
	frame := brokerWTSFrame{Version: SessionBrokerProtocolVersion, Kind: kind, SessionID: id}
	if err := writeBrokerWTSFrame(authority.conn, frame); err != nil {
		return BootstrapSession{}, ErrSessionQuery
	}
	data, err := authority.reader.ReadBytes('\n')
	if err != nil || len(data) > 8192 {
		return BootstrapSession{}, ErrSessionQuery
	}
	var reply brokerWTSFrame
	if decodeJSONLine(data, &reply) != nil || reply.Version != frame.Version || reply.Kind != kind {
		return BootstrapSession{}, ErrSessionQuery
	}
	session := BootstrapSession{ID: reply.SessionID, State: reply.State}
	switch reply.Code {
	case "ok":
		if kind == "wts_logoff" || (session.ID != 0 && session.State == "active") {
			return session, nil
		}
	case "absent":
		if session.ID == 0 && session.State == "logged_off" {
			return session, ErrSessionUnavailable
		}
	case "disconnected":
		if session.ID != 0 && session.State == "disconnected" {
			return session, ErrSessionDisconnected
		}
	case "changed":
		return session, ErrSessionChanged
	}
	return BootstrapSession{}, ErrSessionQuery
}

// BrokerFindSession requires the authenticated service callback, rather than
// WTSQueryUserToken in the interactive operator process.
func BrokerFindSession(ctx context.Context) (BootstrapSession, error) {
	return brokerWTSCall(ctx, "wts_query", 0)
}
func BrokerLogoffSession(ctx context.Context, id uint32) error {
	if id == 0 {
		return ErrSessionChanged
	}
	_, err := brokerWTSCall(ctx, "wts_logoff", id)
	return err
}

func writeBrokerWTSFrame(conn net.Conn, frame brokerWTSFrame) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	n, err := conn.Write(data)
	if err == nil && n != len(data) {
		return ErrSessionQuery
	}
	return err
}

type brokerWTSHandler func(context.Context, SessionBrokerRequest, string, uint32) (BootstrapSession, error)

func answerBrokerWTS(ctx context.Context, conn net.Conn, request SessionBrokerRequest, frame brokerWTSFrame, handler brokerWTSHandler) error {
	if handler == nil || frame.Version != SessionBrokerProtocolVersion || (frame.Kind != "wts_query" && frame.Kind != "wts_logoff") || frame.Code != "" || frame.State != "" || (frame.Kind == "wts_query" && frame.SessionID != 0) || (frame.Kind == "wts_logoff" && frame.SessionID == 0) {
		return ErrSessionQuery
	}
	// Stop cannot change the original exact session fence.
	if frame.Kind == "wts_logoff" && request.Operation == SessionBrokerStop && frame.SessionID != request.SessionID {
		return ErrSessionChanged
	}
	session, err := handler(ctx, request, frame.Kind, frame.SessionID)
	reply := brokerWTSFrame{Version: frame.Version, Kind: frame.Kind, SessionID: session.ID, State: session.State, Code: "query_failed"}
	switch {
	case err == nil:
		reply.Code = "ok"
	case sessionConfirmedAbsent(session, err):
		reply.Code = "absent"
	case errors.Is(err, ErrSessionDisconnected):
		reply.Code = "disconnected"
	case errors.Is(err, ErrSessionChanged):
		reply.Code = "changed"
	}
	return writeBrokerWTSFrame(conn, reply)
}
