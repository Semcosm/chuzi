package slotwindows

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestSessionBrokerExchangeRejectsInvalidAndMismatchedResponses(t *testing.T) {
	for _, test := range []struct {
		name      string
		operation SessionBrokerOperation
		response  string
		want      BootstrapSession
		ok        bool
	}{
		{name: "start", operation: SessionBrokerStart, response: `{"version":1,"operation":"start","code":"ok","session_id":44,"state":"active"}` + "\n", want: BootstrapSession{ID: 44, State: "active"}, ok: true},
		{name: "stop", operation: SessionBrokerStop, response: `{"version":1,"operation":"stop","code":"ok","state":"stopped"}` + "\n", want: BootstrapSession{State: "stopped"}, ok: true},
		{name: "stop missing state", operation: SessionBrokerStop, response: `{"version":1,"operation":"stop","code":"ok"}` + "\n"},
		{name: "stop still active", operation: SessionBrokerStop, response: `{"version":1,"operation":"stop","code":"ok","state":"active"}` + "\n"},
		{name: "wrong operation", operation: SessionBrokerStop, response: `{"version":1,"operation":"start","code":"ok","session_id":44,"state":"active"}` + "\n"},
		{name: "inactive", operation: SessionBrokerStart, response: `{"version":1,"operation":"start","code":"ok","session_id":44,"state":"disconnected"}` + "\n"},
		{name: "missing provider", operation: SessionBrokerStart, response: `{"version":1,"operation":"start","code":"session_unavailable"}` + "\n"},
		{name: "unknown field", operation: SessionBrokerStart, response: `{"version":1,"operation":"start","code":"session_unavailable","detail":"private provider text"}` + "\n"},
		{name: "truncated", operation: SessionBrokerStart, response: `{"version":1`},
		{name: "oversized", operation: SessionBrokerStart, response: strings.Repeat("x", 8192) + "\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer server.Close()
				_, _ = bufio.NewReader(server).ReadBytes('\n')
				_, _ = io.WriteString(server, test.response)
			}()
			request := SessionBrokerRequest{Version: 1, Operation: test.operation, SlotID: "slot-001", Ordinal: 1, Generation: 1, SID: "S-1-5-21-1", Owner: "owner-1"}
			if request.Operation == SessionBrokerStop {
				request.SessionID = 44
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			got, err := exchangeSessionBroker(ctx, client, request)
			_ = client.Close()
			<-done
			if test.ok {
				if err != nil || got != test.want {
					t.Fatalf("session = %#v, error = %v", got, err)
				}
			} else if got != (BootstrapSession{}) || !errors.Is(err, ErrSessionUnavailable) {
				t.Fatalf("failure was not closed and redacted: session = %#v, error = %v", got, err)
			}
		})
	}
}

func TestSessionBrokerExchangeCancellationUnblocksReadAndWrite(t *testing.T) {
	for _, stage := range []string{"read", "write"} {
		t.Run(stage, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			enteredWrite := make(chan struct{})
			result := make(chan error, 1)
			go func() {
				_, err := exchangeSessionBroker(ctx, brokerObservedWrite{Conn: client, entered: enteredWrite}, SessionBrokerRequest{Version: 1, Operation: SessionBrokerStart, SlotID: "slot-001", Ordinal: 1, Generation: 1, SID: "S-1-5-21-1", Owner: "owner-1"})
				result <- err
			}()
			<-enteredWrite
			if stage == "read" {
				if _, err := bufio.NewReader(server).ReadBytes('\n'); err != nil {
					t.Fatal(err)
				}
			}
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, ErrSessionUnavailable) {
					t.Fatalf("cancellation error = %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancellation left broker I/O blocked")
			}
		})
	}
}

type brokerObservedWrite struct {
	net.Conn
	entered chan struct{}
}

func (c brokerObservedWrite) Write(data []byte) (int, error) {
	close(c.entered)
	return c.Conn.Write(data)
}
