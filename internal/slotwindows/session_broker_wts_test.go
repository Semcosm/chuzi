package slotwindows

import (
	"bufio"
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestBrokerWTSUsesOriginalServiceIdentityAndExactStopFence(t *testing.T) {
	for _, state := range []string{"active", "disconnected", "logged_off", "query_failed"} {
		t.Run(state, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			request := validBrokerRequest()
			request.Operation = SessionBrokerStop
			request.SessionID = 44
			serverResult := make(chan error, 1)
			go func() {
				reader := bufio.NewReader(server)
				_, err := reader.ReadBytes('\n')
				if err != nil {
					serverResult <- err
					return
				}
				brokerCtx := withBrokerWTSAuthority(ctx, server, reader)
				session, err := BrokerFindSession(brokerCtx)
				switch state {
				case "active":
					if err != nil || session.ID != 44 {
						serverResult <- ErrSessionQuery
						return
					}
				case "disconnected":
					if !errors.Is(err, ErrSessionDisconnected) || session.ID != 44 {
						serverResult <- ErrSessionQuery
						return
					}
				case "logged_off":
					if !sessionConfirmedAbsent(session, err) {
						serverResult <- ErrSessionQuery
						return
					}
				case "query_failed":
					if !errors.Is(err, ErrSessionQuery) || session.State == "logged_off" {
						serverResult <- ErrSessionQuery
						return
					}
				}
				serverResult <- writeSessionBrokerResponseForTest(server, SessionBrokerResponse{Version: SessionBrokerProtocolVersion, Operation: SessionBrokerStop, Code: SessionBrokerOK, State: "stopped"})
			}()
			calls := 0
			_, err := exchangeSessionBrokerWithWTS(ctx, client, request, func(_ context.Context, got SessionBrokerRequest, kind string, id uint32) (BootstrapSession, error) {
				calls++
				if got != request || kind != "wts_query" || id != 0 {
					return BootstrapSession{}, ErrSessionIdentity
				}
				switch state {
				case "active":
					return BootstrapSession{ID: 44, State: state}, nil
				case "disconnected":
					return BootstrapSession{ID: 44, State: state}, ErrSessionDisconnected
				case "logged_off":
					return BootstrapSession{State: state}, ErrSessionUnavailable
				default:
					return BootstrapSession{}, ErrSessionQuery
				}
			})
			if err != nil || calls != 1 {
				t.Fatalf("exchange err=%v calls=%d", err, calls)
			}
			if err := <-serverResult; err != nil {
				t.Fatal(err)
			}
		})
	}
	request := validBrokerRequest()
	request.Operation = SessionBrokerStop
	request.SessionID = 44
	called := false
	err := answerBrokerWTS(context.Background(), nil, request, brokerWTSFrame{Version: SessionBrokerProtocolVersion, Kind: "wts_logoff", SessionID: 45}, func(context.Context, SessionBrokerRequest, string, uint32) (BootstrapSession, error) {
		called = true
		return BootstrapSession{}, nil
	})
	if !errors.Is(err, ErrSessionChanged) || called {
		t.Fatalf("wrong stop fence reached WTS: %v %v", err, called)
	}
	if _, err := BrokerFindSession(context.Background()); !errors.Is(err, ErrSessionQuery) {
		t.Fatal("unauthenticated WTS query accepted")
	}
}

func writeSessionBrokerResponseForTest(conn net.Conn, response SessionBrokerResponse) error {
	data, err := EncodeSessionBrokerResponse(response)
	if err != nil {
		return err
	}
	_, err = conn.Write(append(data, '\n'))
	return err
}
