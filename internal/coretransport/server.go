package coretransport

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"

	"github.com/Semcosm/chuzi/internal/coreapi"
)

type Config struct {
	MaxFrameBytes int
}

type Server struct {
	api           coreapi.API
	listener      net.Listener
	maxFrameBytes int
	closed        chan struct{}
	closeOnce     sync.Once
	mu            sync.Mutex
	connections   map[net.Conn]struct{}
	wg            sync.WaitGroup
}

func NewServer(api coreapi.API, listener net.Listener, config Config) (*Server, error) {
	if api == nil || listener == nil {
		return nil, ErrInvalidTransport
	}
	maxFrame := config.MaxFrameBytes
	if maxFrame <= 0 {
		maxFrame = DefaultMaxFrame
	}
	if maxFrame < 1024 || maxFrame > 16<<20 {
		return nil, ErrInvalidTransport
	}
	return &Server{api: api, listener: listener, maxFrameBytes: maxFrame, closed: make(chan struct{}), connections: make(map[net.Conn]struct{})}, nil
}

func (s *Server) Serve() error {
	if s == nil || s.listener == nil {
		return ErrInvalidTransport
	}
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.closed:
				return nil
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		s.mu.Lock()
		s.connections[conn] = struct{}{}
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() { s.mu.Lock(); delete(s.connections, conn); s.mu.Unlock(); _ = conn.Close() }()
			s.serveConnection(conn)
		}()
	}
}

func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		close(s.closed)
		_ = s.listener.Close()
		s.mu.Lock()
		for conn := range s.connections {
			_ = conn.Close()
		}
		s.mu.Unlock()
	})
	s.wg.Wait()
	return nil
}

func (s *Server) serveConnection(conn net.Conn) {
	connectionContext, cancelConnection := context.WithCancel(context.Background())
	var requests sync.WaitGroup
	defer func() {
		cancelConnection()
		requests.Wait()
	}()
	reader := bufio.NewReaderSize(conn, minInt(s.maxFrameBytes+1, 64<<10))
	var writeMu sync.Mutex
	cancels := make(map[string]context.CancelFunc)
	var cancelMu sync.Mutex
	ready := false
	for {
		line, err := readFrame(reader, s.maxFrameBytes)
		if err != nil {
			if err == io.EOF {
				return
			}
			if errors.Is(err, ErrFrameTooLarge) {
				sendEnvelope(&writeMu, conn, errorResponse("", err), s.maxFrameBytes)
			} else if len(line) > 0 {
				sendEnvelope(&writeMu, conn, errorResponse("", ErrInvalidTransport), s.maxFrameBytes)
			}
			return
		}
		var envelope Envelope
		if err := json.Unmarshal(line, &envelope); err != nil {
			sendEnvelope(&writeMu, conn, errorResponse("", ErrInvalidTransport), s.maxFrameBytes)
			continue
		}
		if err := validateEnvelope(envelope); err != nil {
			sendEnvelope(&writeMu, conn, errorResponse(envelope.ID, err), s.maxFrameBytes)
			continue
		}
		if envelope.Protocol != ProtocolVersion {
			sendEnvelope(&writeMu, conn, errorResponse(envelope.ID, ErrUnsupportedVersion), s.maxFrameBytes)
			continue
		}
		if envelope.Method == methodHello {
			var params HelloParams
			if err := decodeParams(envelope.Params, &params); err != nil || params.Version != ProtocolVersion {
				sendEnvelope(&writeMu, conn, errorResponse(envelope.ID, ErrUnsupportedVersion), s.maxFrameBytes)
				continue
			}
			ready = true
			sendEnvelope(&writeMu, conn, response(envelope.ID, HelloResult{Version: ProtocolVersion, Methods: methodList()}), s.maxFrameBytes)
			continue
		}
		if !ready {
			sendEnvelope(&writeMu, conn, errorResponse(envelope.ID, ErrNotReady), s.maxFrameBytes)
			continue
		}
		if envelope.Method == methodCancel {
			var params CancelParams
			if err := decodeParams(envelope.Params, &params); err != nil {
				sendEnvelope(&writeMu, conn, errorResponse(envelope.ID, err), s.maxFrameBytes)
				continue
			}
			cancelMu.Lock()
			cancel, ok := cancels[params.ID]
			if ok {
				cancel()
			}
			cancelMu.Unlock()
			sendEnvelope(&writeMu, conn, response(envelope.ID, CancelResult{Cancelled: ok}), s.maxFrameBytes)
			continue
		}
		ctx, cancel := context.WithCancel(connectionContext)
		cancelMu.Lock()
		cancels[envelope.ID] = cancel
		cancelMu.Unlock()
		requests.Add(1)
		go func(envelope Envelope, ctx context.Context) {
			defer requests.Done()
			defer cancel()
			defer func() { cancelMu.Lock(); delete(cancels, envelope.ID); cancelMu.Unlock() }()
			result, err := s.dispatch(ctx, envelope.Method, envelope.Params)
			if err != nil {
				sendEnvelope(&writeMu, conn, errorResponse(envelope.ID, err), s.maxFrameBytes)
				return
			}
			sendEnvelope(&writeMu, conn, response(envelope.ID, result), s.maxFrameBytes)
		}(envelope, ctx)
	}
}

func readFrame(reader *bufio.Reader, max int) ([]byte, error) {
	var frame []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		frame = append(frame, chunk...)
		if len(frame) > max {
			return frame, ErrFrameTooLarge
		}
		if err != bufio.ErrBufferFull {
			return frame, err
		}
	}
}

func sendEnvelope(mu *sync.Mutex, writer io.Writer, envelope Envelope, maxFrameBytes int) {
	raw, err := json.Marshal(envelope)
	if err != nil {
		raw, _ = json.Marshal(errorResponse(envelope.ID, ErrInvalidTransport))
	}
	if len(raw)+1 > maxFrameBytes {
		raw, _ = json.Marshal(errorResponse(envelope.ID, ErrFrameTooLarge))
	}
	if len(raw)+1 > maxFrameBytes {
		return
	}
	raw = append(raw, '\n')
	mu.Lock()
	defer mu.Unlock()
	_, _ = writer.Write(raw)
}

func (s *Server) dispatch(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	if err := classifyContext(ctx); err != nil {
		return nil, err
	}
	switch method {
	case methodSubmitRequest:
		var params coreapi.SubmitRequest
		if err := decodeParams(raw, &params); err != nil {
			return nil, err
		}
		request, idempotent, err := s.api.SubmitRequest(ctx, params)
		if err != nil {
			return nil, err
		}
		return SubmitResult{Request: request, Idempotent: idempotent}, nil
	case methodGetRequest:
		var params struct {
			RequestID string `json:"request_id"`
		}
		if err := decodeParams(raw, &params); err != nil {
			return nil, err
		}
		return s.api.GetRequest(ctx, params.RequestID)
	case methodGetAccount:
		var params struct {
			AccountID string `json:"account_id"`
		}
		if err := decodeParams(raw, &params); err != nil {
			return nil, err
		}
		return s.api.GetAccount(ctx, params.AccountID)
	case methodCancelRequest:
		var params coreapi.CancelRequest
		if err := decodeParams(raw, &params); err != nil {
			return nil, err
		}
		return s.api.CancelRequest(ctx, params)
	case methodGetResult:
		var params struct {
			RequestID string `json:"request_id"`
		}
		if err := decodeParams(raw, &params); err != nil {
			return nil, err
		}
		return s.api.GetResult(ctx, params.RequestID)
	case methodListEvents:
		var params coreapi.EventQuery
		if err := decodeParams(raw, &params); err != nil {
			return nil, err
		}
		items, err := s.api.ListEvents(ctx, params)
		return EventsResult{Events: items}, err
	case methodListNotifications:
		var params coreapi.NotificationQuery
		if err := decodeParams(raw, &params); err != nil {
			return nil, err
		}
		items, err := s.api.ListNotifications(ctx, params)
		return NotificationsResult{Notifications: items}, err
	case methodGetBrowserView:
		viewAPI, ok := s.api.(coreapi.BrowserViewAPI)
		if !ok {
			return nil, coreapi.NewError(coreapi.CodeUnavailable, "browser view is unavailable")
		}
		var params coreapi.BrowserViewRequest
		if err := decodeParams(raw, &params); err != nil {
			return nil, err
		}
		return viewAPI.GetBrowserView(ctx, params)
	case methodSubmitDiagnostic:
		diagnosticsAPI, ok := s.api.(coreapi.DiagnosticsAPI)
		if !ok {
			return nil, coreapi.NewError(coreapi.CodeUnavailable, "diagnostics are unavailable")
		}
		var params coreapi.DiagnosticReport
		if err := decodeParams(raw, &params); err != nil {
			return nil, err
		}
		return diagnosticsAPI.SubmitDiagnosticReport(ctx, params)
	default:
		return nil, invalidMethodError(method)
	}
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
