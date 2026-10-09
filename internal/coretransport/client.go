package coretransport

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"sync"
	"sync/atomic"

	"github.com/Semcosm/chuzi/internal/coreapi"
)

type Client struct {
	conn          net.Conn
	reader        *bufio.Reader
	maxFrameBytes int
	sequence      atomic.Uint64
	writeMu       sync.Mutex
	pendingMu     sync.Mutex
	pending       map[string]chan Envelope
	closed        chan struct{}
	closeOnce     sync.Once
}

// Connect dials the service endpoint and completes the protocol handshake.
// The returned Client is ready for Core API calls.
func Connect(ctx context.Context, path string, config Config) (*Client, error) {
	conn, err := Dial(ctx, path)
	if err != nil {
		return nil, err
	}
	client, err := NewClient(conn, config)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if _, err := client.Hello(ctx); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

func NewClient(conn net.Conn, config Config) (*Client, error) {
	if conn == nil {
		return nil, ErrInvalidTransport
	}
	max := config.MaxFrameBytes
	if max <= 0 {
		max = DefaultMaxFrame
	}
	if max < 1024 || max > 16<<20 {
		return nil, ErrInvalidTransport
	}
	client := &Client{
		conn:          conn,
		reader:        bufio.NewReaderSize(conn, minInt(max+1, 64<<10)),
		maxFrameBytes: max,
		pending:       make(map[string]chan Envelope),
		closed:        make(chan struct{}),
	}
	go client.readLoop()
	return client, nil
}

func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	c.closeOnce.Do(func() {
		close(c.closed)
		_ = c.conn.Close()
		c.pendingMu.Lock()
		for id, channel := range c.pending {
			delete(c.pending, id)
			_ = channel
		}
		c.pendingMu.Unlock()
	})
	return nil
}

func (c *Client) readLoop() {
	for {
		line, err := readFrame(c.reader, c.maxFrameBytes)
		if err != nil {
			_ = c.Close()
			return
		}
		var envelope Envelope
		if err := json.Unmarshal(line, &envelope); err != nil {
			_ = c.Close()
			return
		}
		c.pendingMu.Lock()
		channel := c.pending[envelope.ID]
		c.pendingMu.Unlock()
		if channel == nil {
			continue
		}
		select {
		case channel <- envelope:
		default:
		}
	}
}

func (c *Client) Call(ctx context.Context, method string, params any, result any) error {
	if c == nil || c.conn == nil {
		return ErrClientClosed
	}
	if err := classifyContext(ctx); err != nil {
		return err
	}
	select {
	case <-c.closed:
		return ErrClientClosed
	default:
	}
	id := "c-" + formatUint(c.sequence.Add(1))
	raw, err := marshalRequest(id, method, params)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if len(raw) > c.maxFrameBytes {
		return ErrFrameTooLarge
	}
	responseChannel := make(chan Envelope, 1)
	c.pendingMu.Lock()
	c.pending[id] = responseChannel
	c.pendingMu.Unlock()
	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()
	c.writeMu.Lock()
	_, writeErr := c.conn.Write(raw)
	c.writeMu.Unlock()
	if writeErr != nil {
		return ErrClientClosed
	}
	select {
	case envelope, ok := <-responseChannel:
		if !ok {
			return ErrClientClosed
		}
		if envelope.Protocol != ProtocolVersion || envelope.ID != id {
			return ErrInvalidTransport
		}
		if envelope.Type == "error" {
			return errorFromPayload(envelope.Error)
		}
		if envelope.Type != "result" {
			return ErrInvalidTransport
		}
		if result == nil {
			return nil
		}
		if err := json.Unmarshal(envelope.Result, result); err != nil {
			return ErrInvalidTransport
		}
		return nil
	case <-ctx.Done():
		_ = c.sendCancel(id)
		return classifyContext(ctx)
	case <-c.closed:
		return ErrClientClosed
	}
}

func (c *Client) sendCancel(id string) error {
	raw, err := marshalRequest("cancel-"+id, methodCancel, CancelParams{ID: id})
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.conn.Write(raw)
	return err
}

func (c *Client) Hello(ctx context.Context) (HelloResult, error) {
	var result HelloResult
	err := c.Call(ctx, methodHello, HelloParams{Version: ProtocolVersion}, &result)
	if err == nil && result.Version != ProtocolVersion {
		return HelloResult{}, ErrUnsupportedVersion
	}
	return result, err
}

func (c *Client) SubmitRequest(ctx context.Context, input coreapi.SubmitRequest) (coreapi.Request, bool, error) {
	var result SubmitResult
	if err := c.Call(ctx, methodSubmitRequest, input, &result); err != nil {
		return coreapi.Request{}, false, err
	}
	return result.Request, result.Idempotent, nil
}

func (c *Client) GetRequest(ctx context.Context, id string) (coreapi.Request, error) {
	var result coreapi.Request
	err := c.Call(ctx, methodGetRequest, struct {
		RequestID string `json:"request_id"`
	}{id}, &result)
	return result, err
}

func (c *Client) ListRequests(ctx context.Context, query coreapi.RequestQuery) ([]coreapi.Request, error) {
	var result RequestsResult
	err := c.Call(ctx, methodListRequests, query, &result)
	return result.Requests, err
}

func (c *Client) GetAccount(ctx context.Context, id string) (coreapi.Account, error) {
	var result coreapi.Account
	err := c.Call(ctx, methodGetAccount, struct {
		AccountID string `json:"account_id"`
	}{id}, &result)
	return result, err
}

func (c *Client) CancelRequest(ctx context.Context, input coreapi.CancelRequest) (coreapi.Request, error) {
	var result coreapi.Request
	err := c.Call(ctx, methodCancelRequest, input, &result)
	return result, err
}

func (c *Client) GetResult(ctx context.Context, id string) (coreapi.Result, error) {
	var result coreapi.Result
	err := c.Call(ctx, methodGetResult, struct {
		RequestID string `json:"request_id"`
	}{id}, &result)
	return result, err
}

func (c *Client) ListEvents(ctx context.Context, query coreapi.EventQuery) ([]coreapi.Event, error) {
	var result EventsResult
	err := c.Call(ctx, methodListEvents, query, &result)
	return result.Events, err
}

func (c *Client) ListNotifications(ctx context.Context, query coreapi.NotificationQuery) ([]coreapi.Notification, error) {
	var result NotificationsResult
	err := c.Call(ctx, methodListNotifications, query, &result)
	return result.Notifications, err
}

func (c *Client) GetBrowserView(ctx context.Context, input coreapi.BrowserViewRequest) (coreapi.BrowserView, error) {
	var result coreapi.BrowserView
	err := c.Call(ctx, methodGetBrowserView, input, &result)
	return result, err
}

func (c *Client) IssueRDPCapability(ctx context.Context, input coreapi.RDPCapabilityRequest) (coreapi.RDPCapability, error) {
	var result coreapi.RDPCapability
	err := c.Call(ctx, methodIssueRDPCapability, input, &result)
	return result, err
}

func (c *Client) SubmitDiagnosticReport(ctx context.Context, input coreapi.DiagnosticReport) (coreapi.DiagnosticStatus, error) {
	var result coreapi.DiagnosticStatus
	err := c.Call(ctx, methodSubmitDiagnostic, input, &result)
	return result, err
}

func (c *Client) GetDiagnosticSnapshot(ctx context.Context, input coreapi.DiagnosticSnapshotRequest) (coreapi.DiagnosticSnapshot, error) {
	var result coreapi.DiagnosticSnapshot
	err := c.Call(ctx, methodGetDiagnosticSnapshot, input, &result)
	return result, err
}

func (c *Client) GetJobPoolStatus(ctx context.Context, poolID string) (coreapi.JobPoolStatus, error) {
	var result JobPoolStatusResult
	err := c.Call(ctx, methodGetJobPoolStatus, struct {
		PoolID string `json:"pool_id"`
	}{poolID}, &result)
	return result.Status, err
}

func (c *Client) ListJobPools(ctx context.Context) ([]coreapi.JobPool, error) {
	var result JobPoolsResult
	err := c.Call(ctx, methodListJobPools, struct{}{}, &result)
	return result.JobPools, err
}

func (c *Client) GetJobPool(ctx context.Context, poolID string) (coreapi.JobPool, error) {
	var result JobPoolResult
	err := c.Call(ctx, methodGetJobPool, struct {
		PoolID string `json:"pool_id"`
	}{poolID}, &result)
	return result.JobPool, err
}

func (c *Client) ApplyJobPool(ctx context.Context, input coreapi.JobPoolApplyRequest) (coreapi.JobPoolOperation, error) {
	var result JobPoolOperationResult
	err := c.Call(ctx, methodApplyJobPool, input, &result)
	return result.Operation, err
}

func (c *Client) ScaleJobPool(ctx context.Context, input coreapi.JobPoolScaleRequest) (coreapi.JobPoolOperation, error) {
	var result JobPoolOperationResult
	err := c.Call(ctx, methodScaleJobPool, input, &result)
	return result.Operation, err
}

func (c *Client) DrainJobPool(ctx context.Context, input coreapi.JobPoolActionRequest) (coreapi.JobPoolOperation, error) {
	var result JobPoolOperationResult
	err := c.Call(ctx, methodDrainJobPool, input, &result)
	return result.Operation, err
}

func (c *Client) ResumeJobPool(ctx context.Context, input coreapi.JobPoolActionRequest) (coreapi.JobPoolOperation, error) {
	var result JobPoolOperationResult
	err := c.Call(ctx, methodResumeJobPool, input, &result)
	return result.Operation, err
}

func (c *Client) DeleteJobPool(ctx context.Context, input coreapi.JobPoolDeleteRequest) (coreapi.JobPoolOperation, error) {
	var result JobPoolOperationResult
	err := c.Call(ctx, methodDeleteJobPool, input, &result)
	return result.Operation, err
}

func (c *Client) GetJobPoolOperation(ctx context.Context, operationID string) (coreapi.JobPoolOperation, error) {
	var result JobPoolOperationResult
	err := c.Call(ctx, methodGetJobPoolOperation, struct {
		OperationID string `json:"operation_id"`
	}{operationID}, &result)
	return result.Operation, err
}

func (c *Client) StartSlotSession(ctx context.Context, input coreapi.StartSlotSessionRequest) (coreapi.SlotSessionOperation, error) {
	var result SlotSessionOperationResult
	err := c.Call(ctx, methodStartSlotSession, input, &result)
	return result.Operation, err
}

func (c *Client) GetSlotSessionOperation(ctx context.Context, operationID string) (coreapi.SlotSessionOperation, error) {
	var result SlotSessionOperationResult
	err := c.Call(ctx, methodGetSlotSessionOperation, struct {
		OperationID string `json:"operation_id"`
	}{operationID}, &result)
	return result.Operation, err
}

func (c *Client) ListEnvironments(ctx context.Context) ([]coreapi.Environment, error) {
	var result EnvironmentsResult
	err := c.Call(ctx, methodListEnvironments, struct{}{}, &result)
	return result.Environments, err
}

func (c *Client) EnvironmentOperation(ctx context.Context, input coreapi.EnvironmentOperationRequest) (coreapi.EnvironmentOperation, error) {
	var result EnvironmentOperationResult
	err := c.Call(ctx, methodEnvironmentOperation, input, &result)
	return result.Operation, err
}

func (c *Client) GetEnvironmentOperation(ctx context.Context, operationID string) (coreapi.EnvironmentOperation, error) {
	var result EnvironmentOperationResult
	err := c.Call(ctx, methodGetEnvironmentOperation, struct {
		OperationID string `json:"operation_id"`
	}{operationID}, &result)
	return result.Operation, err
}

func formatUint(value uint64) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	var buf [20]byte
	at := len(buf)
	for value > 0 {
		at--
		buf[at] = digits[value%10]
		value /= 10
	}
	return string(buf[at:])
}

var _ coreapi.API = (*Client)(nil)
