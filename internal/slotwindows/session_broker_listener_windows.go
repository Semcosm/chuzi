//go:build windows

package slotwindows

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

const sessionBrokerMaxFrame = 8 * 1024

var (
	errSessionBrokerListenerClosed = errors.New("slotwindows: session broker listener closed")
)

// SessionBrokerListener is the deployment-side fixed broker endpoint. The
// named-pipe DACL is the authorization boundary: unauthorised callers cannot
// obtain a connection and therefore cannot reach JSON decoding or the broker
// state machine. The path is intentionally not configurable.
type SessionBrokerListener struct {
	listener net.Listener
	broker   *SessionBroker
	close    sync.Once
}

// NewSessionBrokerListener creates the fixed session-bootstrap pipe with a
// protected DACL granting only the service and deployment broker SIDs. The
// broker process must provide a real SessionBrokerAdapter; this constructor
// does not create or simulate a Windows session.
func NewSessionBrokerListener(serviceSID, brokerSID string, broker *SessionBroker) (*SessionBrokerListener, error) {
	acl := SessionBrokerACL{ServiceSID: serviceSID, BrokerSID: brokerSID}
	if broker == nil || acl.Validate() != nil {
		return nil, ErrSessionBrokerInvalidRequest
	}
	service, err := windows.StringToSid(serviceSID)
	if err != nil {
		return nil, ErrSessionBrokerInvalidRequest
	}
	brokerIdentity, err := windows.StringToSid(brokerSID)
	if err != nil {
		return nil, ErrSessionBrokerInvalidRequest
	}
	securityDescriptor := "D:P(A;;GA;;;" + service.String() + ")(A;;GA;;;" + brokerIdentity.String() + ")"
	listener, err := winio.ListenPipe(SessionBrokerPipeName, &winio.PipeConfig{
		SecurityDescriptor: securityDescriptor,
		InputBufferSize:    sessionBrokerMaxFrame,
		OutputBufferSize:   sessionBrokerMaxFrame,
	})
	if err != nil {
		return nil, errSessionBrokerListenerClosed
	}
	return &SessionBrokerListener{listener: listener, broker: broker}, nil
}

// Serve accepts one JSONL request per connection until ctx is cancelled.
// Access control is enforced by the pipe DACL before this method reads any
// bytes from a client. Protocol and provider failures are returned as stable
// response codes; raw OS/provider details never cross the pipe.
func (l *SessionBrokerListener) Serve(ctx context.Context) error {
	if l == nil || l.listener == nil || l.broker == nil || ctx == nil {
		return ErrSessionBrokerInvalidRequest
	}
	go func() {
		<-ctx.Done()
		_ = l.Close()
	}()
	for {
		conn, err := l.listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return errSessionBrokerListenerClosed
		}
		go l.serveConn(ctx, conn)
	}
}

func (l *SessionBrokerListener) serveConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	reader := bufio.NewReader(io.LimitReader(conn, sessionBrokerMaxFrame))
	data, err := reader.ReadBytes('\n')
	if err != nil || len(data) > sessionBrokerMaxFrame {
		_ = writeSessionBrokerResponse(conn, SessionBrokerResponse{Version: SessionBrokerProtocolVersion, Operation: SessionBrokerStart, Code: SessionBrokerInvalidRequest})
		return
	}
	request, err := DecodeSessionBrokerRequest(data[:len(data)-1])
	if err != nil {
		_ = writeSessionBrokerResponse(conn, SessionBrokerResponse{Version: SessionBrokerProtocolVersion, Operation: SessionBrokerStart, Code: sessionBrokerCode(err)})
		return
	}
	response := l.broker.Handle(ctx, request)
	_ = writeSessionBrokerResponse(conn, response)
}

func writeSessionBrokerResponse(conn net.Conn, response SessionBrokerResponse) error {
	payload, err := EncodeSessionBrokerResponse(response)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	_, err = conn.Write(payload)
	clear(payload)
	return err
}

// Close stops accepting new connections. Existing requests are bounded by
// their read deadline and the caller's context.
func (l *SessionBrokerListener) Close() error {
	if l == nil || l.listener == nil {
		return nil
	}
	var err error
	l.close.Do(func() { err = l.listener.Close() })
	return err
}
