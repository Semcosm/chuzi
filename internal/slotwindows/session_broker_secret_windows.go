//go:build windows

package slotwindows

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"io"
	"net"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/Semcosm/chuzi/internal/coretransport"
	"golang.org/x/sys/windows"
)

const sessionBrokerSecretMaxWire = sessionBrokerSecretMaxFrame + 4

// SessionBrokerSecretListener accepts only the installed Core service image.
// Its DACL also names the broker SID so the broker can own the endpoint, but
// the authenticated caller validator rejects broker-originated handoffs.
type SessionBrokerSecretListener struct {
	listener net.Listener
	store    *SessionBrokerSecretStore
	caller   sessionBrokerCallerValidator
}

func NewSessionBrokerSecretListener(serviceSID, brokerSID, serviceExecutable, brokerExecutable string, store *SessionBrokerSecretStore) (*SessionBrokerSecretListener, error) {
	if store == nil {
		return nil, ErrSessionBrokerSecretInvalid
	}
	acl := SessionBrokerACL{ServiceSID: serviceSID, BrokerSID: brokerSID}
	if acl.Validate() != nil {
		return nil, ErrSessionBrokerSecretInvalid
	}
	service, err := windowsStringSID(serviceSID)
	if err != nil {
		return nil, ErrSessionBrokerSecretInvalid
	}
	broker, err := windowsStringSID(brokerSID)
	if err != nil {
		return nil, ErrSessionBrokerSecretInvalid
	}
	listener, err := winio.ListenPipe(SessionBrokerSecretPipeName, &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;" + service + ")(A;;GA;;;" + broker + ")",
		InputBufferSize:    sessionBrokerSecretMaxWire,
		OutputBufferSize:   64,
	})
	if err != nil {
		return nil, ErrSessionBrokerSecretInvalid
	}
	return &SessionBrokerSecretListener{listener: listener, store: store, caller: newSessionBrokerServiceCallerValidator(SessionBrokerACL{ServiceSID: serviceSID, BrokerSID: brokerSID}, serviceExecutable, brokerExecutable)}, nil
}

func windowsStringSID(value string) (string, error) {
	// Keep the conversion in this file so the SDDL is always generated from a
	// parsed SID rather than concatenating caller text into a security ACL.
	sid, err := windows.StringToSid(value)
	if err != nil {
		return "", err
	}
	return sid.String(), nil
}

func (l *SessionBrokerSecretListener) Serve(ctx context.Context) error {
	if l == nil || l.listener == nil || l.store == nil || ctx == nil {
		return ErrSessionBrokerSecretInvalid
	}
	go func() { <-ctx.Done(); _ = l.Close() }()
	for {
		conn, err := l.listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return ErrSessionBrokerSecretInvalid
		}
		go l.serveConn(ctx, conn)
	}
}

func (l *SessionBrokerSecretListener) serveConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	if l.caller == nil || l.caller.Validate(ctx, conn) != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var length [4]byte
	if _, err := io.ReadFull(io.LimitReader(conn, 4), length[:]); err != nil {
		return
	}
	frameLength := binary.LittleEndian.Uint32(length[:])
	if frameLength == 0 || frameLength > sessionBrokerSecretMaxFrame {
		return
	}
	frame := make([]byte, frameLength)
	defer clear(frame)
	if _, err := io.ReadFull(bufio.NewReader(io.LimitReader(conn, int64(frameLength))), frame); err != nil {
		return
	}
	if len(frame) >= len(sessionBrokerSecretDropMagic) && string(frame[:len(sessionBrokerSecretDropMagic)]) == sessionBrokerSecretDropMagic {
		drop, err := decodeSessionBrokerSecretDrop(frame)
		if err != nil || l.store.DropOwnership(drop.ownership(), drop.Nonce) != nil {
			return
		}
		_, _ = conn.Write([]byte{1})
		return
	}
	secret, err := decodeSessionBrokerSecret(frame)
	if err != nil {
		return
	}
	defer clear(secret.Password)
	if err := l.store.Put(secret); err != nil {
		return
	}
	_, _ = conn.Write([]byte{1})
}

func (l *SessionBrokerSecretListener) Close() error {
	if l == nil || l.listener == nil {
		return nil
	}
	return l.listener.Close()
}

// SendSessionBrokerSecret transmits a single in-memory credential under the
// service-to-broker pipe ACL and authenticates the installed broker image.
func SendSessionBrokerSecret(ctx context.Context, secret SessionBrokerSecret, expectedExecutable string) error {
	if ctx == nil || secret.validate() != nil || expectedExecutable == "" {
		return ErrSessionBrokerSecretInvalid
	}
	if secret.Nonce == [16]byte{} {
		if _, err := rand.Read(secret.Nonce[:]); err != nil {
			return ErrSessionBrokerSecretInvalid
		}
	}
	frame, err := encodeSessionBrokerSecret(secret)
	if err != nil {
		return err
	}
	defer clear(frame)
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := winio.DialPipeContext(callCtx, SessionBrokerSecretPipeName)
	if err != nil {
		return ErrSessionBrokerSecretMissing
	}
	defer conn.Close()
	if coretransport.ValidateWindowsPipeExecutable(conn, expectedExecutable) != nil {
		return ErrSessionBrokerSecretInvalid
	}
	deadline, _ := callCtx.Deadline()
	_ = conn.SetDeadline(deadline)
	var length [4]byte
	binary.LittleEndian.PutUint32(length[:], uint32(len(frame)))
	wire := make([]byte, 4+len(frame))
	copy(wire, length[:])
	copy(wire[4:], frame)
	defer clear(wire)
	if _, err := conn.Write(wire); err != nil {
		return ErrSessionBrokerSecretMissing
	}
	var ack [1]byte
	if _, err := io.ReadFull(conn, ack[:]); err != nil || ack[0] != 1 || callCtx.Err() != nil {
		return ErrSessionBrokerSecretBusy
	}
	return nil
}

// DropSessionBrokerSecret removes a pending handoff after the public start
// request was cancelled before the broker could consume it. It uses the same
// authenticated service-to-broker pipe and an exact ownership fence; no
// credential bytes are sent.
func DropSessionBrokerSecret(ctx context.Context, ownership SessionBrokerOwnership, nonce [16]byte, expectedExecutable string) error {
	if ctx == nil || validateJournalOwnership(ownership, false) != nil || expectedExecutable == "" {
		return ErrSessionBrokerSecretInvalid
	}
	frame, err := encodeSessionBrokerSecretDrop(sessionBrokerSecretDrop{SlotID: ownership.SlotID, Ordinal: ownership.Ordinal, Generation: ownership.Generation, SID: ownership.SID, Owner: ownership.Owner, Nonce: nonce})
	if err != nil {
		return err
	}
	defer clear(frame)
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	conn, err := winio.DialPipeContext(callCtx, SessionBrokerSecretPipeName)
	if err != nil {
		return ErrSessionBrokerSecretMissing
	}
	defer conn.Close()
	if coretransport.ValidateWindowsPipeExecutable(conn, expectedExecutable) != nil {
		return ErrSessionBrokerSecretInvalid
	}
	deadline, _ := callCtx.Deadline()
	_ = conn.SetDeadline(deadline)
	var length [4]byte
	binary.LittleEndian.PutUint32(length[:], uint32(len(frame)))
	wire := make([]byte, 4+len(frame))
	copy(wire, length[:])
	copy(wire[4:], frame)
	defer clear(wire)
	if _, err := conn.Write(wire); err != nil {
		return ErrSessionBrokerSecretMissing
	}
	var ack [1]byte
	if _, err := io.ReadFull(conn, ack[:]); err != nil || ack[0] != 1 || callCtx.Err() != nil {
		return ErrSessionBrokerSecretBusy
	}
	return nil
}
