//go:build windows

package slotwindows

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
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
	caller   sessionBrokerCallerValidator
	close    sync.Once
}

type sessionBrokerCallerValidator interface {
	Validate(context.Context, net.Conn) error
}

type sessionBrokerCallerValidatorFunc func(context.Context, net.Conn) error

func (f sessionBrokerCallerValidatorFunc) Validate(ctx context.Context, conn net.Conn) error {
	if f == nil {
		return ErrSessionBrokerInvalidRequest
	}
	return f(ctx, conn)
}

// NewSessionBrokerListener creates the fixed session-bootstrap pipe with a
// protected DACL granting only the service and deployment broker SIDs. The
// broker process must provide a real SessionBrokerAdapter; this constructor
// does not create or simulate a Windows session.
func NewSessionBrokerListener(serviceSID, brokerSID string, broker *SessionBroker) (*SessionBrokerListener, error) {
	return newSessionBrokerListener(serviceSID, brokerSID, "", "", broker)
}

// NewSessionBrokerListenerWithExecutable additionally binds the broker pipe
// to the signed broker image. Callers in the production service should use
// this constructor with TrustedRuntime.Broker; the legacy constructor keeps
// the same narrow SID ACL while leaving the image policy to its caller.
func NewSessionBrokerListenerWithExecutable(serviceSID, brokerSID, expectedExecutable string, broker *SessionBroker) (*SessionBrokerListener, error) {
	return newSessionBrokerListener(serviceSID, brokerSID, "", expectedExecutable, broker)
}

// NewSessionBrokerListenerWithExecutables binds both authenticated callers:
// the service must be the installed Core image, while the interactive caller
// must be the installed broker image.
func NewSessionBrokerListenerWithExecutables(serviceSID, brokerSID, serviceExecutable, brokerExecutable string, broker *SessionBroker) (*SessionBrokerListener, error) {
	return newSessionBrokerListener(serviceSID, brokerSID, serviceExecutable, brokerExecutable, broker)
}

func newSessionBrokerListener(serviceSID, brokerSID, serviceExecutable, expectedExecutable string, broker *SessionBroker) (*SessionBrokerListener, error) {
	acl := SessionBrokerACL{ServiceSID: serviceSID, BrokerSID: brokerSID}
	if broker == nil || acl.Validate() != nil {
		return nil, ErrSessionBrokerInvalidRequest
	}
	if strings.TrimSpace(expectedExecutable) != "" && (filepath.Clean(expectedExecutable) != expectedExecutable || !filepath.IsAbs(expectedExecutable)) {
		return nil, ErrSessionBrokerInvalidRequest
	}
	if strings.TrimSpace(serviceExecutable) != "" && (filepath.Clean(serviceExecutable) != serviceExecutable || !filepath.IsAbs(serviceExecutable)) {
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
	return &SessionBrokerListener{listener: listener, broker: broker, caller: newSessionBrokerCallerValidator(acl, serviceExecutable, expectedExecutable)}, nil
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
	if l.caller == nil || l.caller.Validate(ctx, conn) != nil {
		_ = writeSessionBrokerResponseContext(ctx, conn, SessionBrokerResponse{Version: SessionBrokerProtocolVersion, Operation: SessionBrokerStart, Code: SessionBrokerUnauthorized})
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	reader := bufio.NewReader(io.LimitReader(conn, 1024*1024))
	data, err := reader.ReadBytes('\n')
	if err != nil || len(data) > sessionBrokerMaxFrame {
		_ = writeSessionBrokerResponseContext(ctx, conn, SessionBrokerResponse{Version: SessionBrokerProtocolVersion, Operation: SessionBrokerStart, Code: SessionBrokerInvalidRequest})
		return
	}
	request, err := DecodeSessionBrokerRequest(data[:len(data)-1])
	if err != nil {
		_ = writeSessionBrokerResponseContext(ctx, conn, SessionBrokerResponse{Version: SessionBrokerProtocolVersion, Operation: SessionBrokerStart, Code: sessionBrokerCode(err)})
		return
	}
	response := l.broker.Handle(withBrokerWTSAuthority(ctx, conn, reader), request)
	_ = writeSessionBrokerResponseContext(ctx, conn, response)
}

func writeSessionBrokerResponse(conn net.Conn, response SessionBrokerResponse) error {
	return writeSessionBrokerResponseContext(context.Background(), conn, response)
}

func writeSessionBrokerResponseContext(ctx context.Context, conn net.Conn, response SessionBrokerResponse) error {
	if ctx == nil {
		ctx = context.Background()
	}
	payload, err := EncodeSessionBrokerResponse(response)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	deadline := time.Now().Add(2 * time.Second)
	if requested, ok := ctx.Deadline(); ok && requested.Before(deadline) {
		deadline = requested
	}
	_ = conn.SetWriteDeadline(deadline)
	defer func() { _ = conn.SetWriteDeadline(time.Time{}) }()
	stop := context.AfterFunc(ctx, func() { _ = conn.SetWriteDeadline(time.Now()) })
	defer stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	var written int
	written, err = conn.Write(payload)
	if err == nil && written != len(payload) {
		err = io.ErrShortWrite
	}
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
