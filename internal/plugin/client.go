package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Semcosm/chuzi/internal/automation"
)

var (
	ErrProtocol      = errors.New("plugin: protocol failure")
	ErrProcessExited = errors.New("plugin: process exited")
)

// Client speaks the versioned JSONL adapter protocol over an isolated plugin
// process. It is deliberately independent of the process launch mode.
type Client struct {
	process   *Process
	writeMu   sync.Mutex
	stateMu   sync.Mutex
	closeOnce sync.Once
	closeDone chan struct{}
	closeErr  error
	next      uint64
	pending   map[string]chan automation.Envelope
	readDone  chan struct{}
	readErr   error
	descriptor automation.Descriptor
}

func StartAdapter(ctx context.Context, config Command) (*Client, error) {
	process, err := start(ctx, config, true)
	if err != nil {
		return nil, err
	}
	client := &Client{
		process:  process,
		pending:  make(map[string]chan automation.Envelope),
		readDone: make(chan struct{}),
		closeDone: make(chan struct{}),
	}
	go client.readLoop()
	if err := client.handshake(ctx); err != nil {
		_ = client.Close(context.Background())
		return nil, err
	}
	return client, nil
}

func (c *Client) nextID(prefix string) string {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.next++
	return prefix + "-" + strconv.FormatUint(c.next, 10)
}

func (c *Client) readLoop() {
	decoder := json.NewDecoder(bufio.NewReader(c.process.stdout))
	for {
		var message automation.Envelope
		if err := decoder.Decode(&message); err != nil {
			c.stateMu.Lock()
			if !errors.Is(err, io.EOF) {
				c.readErr = fmt.Errorf("%w: decode: %v", ErrProtocol, err)
			}
			close(c.readDone)
			c.stateMu.Unlock()
			return
		}
		if err := message.Validate(); err != nil {
			c.stateMu.Lock()
			c.readErr = err
			close(c.readDone)
			c.stateMu.Unlock()
			return
		}
		c.stateMu.Lock()
		pending := c.pending[message.ID]
		c.stateMu.Unlock()
		if pending != nil {
			pending <- message
		}
	}
}

func (c *Client) handshake(ctx context.Context) error {
	response, err := c.await(ctx, automation.Request(c.nextID("hello"), automation.Hello, nil), automation.HelloAck)
	if err != nil {
		return err
	}
	descriptor := automation.Descriptor{
		ID:      response.Payload["adapter_id"],
		Version: response.Payload["version"],
		API:     response.Payload["api"],
	}
	capabilities, err := parseCapabilities(response.Payload["capabilities"])
	if err != nil {
		return err
	}
	for _, item := range capabilities {
		parts := strings.SplitN(item, "@", 2)
		descriptor.Capabilities = append(descriptor.Capabilities, automation.Capability{ID: parts[0], Version: parts[1]})
	}
	if err := descriptor.Validate(); err != nil {
		return err
	}
	c.stateMu.Lock()
	c.descriptor = descriptor
	c.stateMu.Unlock()
	return nil
}

func parseCapabilities(value string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	items := strings.Split(value, ",")
	result := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			return nil, fmt.Errorf("%w: empty capability declaration", ErrProtocol)
		}
		parts := strings.SplitN(item, "@", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			return nil, fmt.Errorf("%w: invalid capability declaration %q", ErrProtocol, item)
		}
		result = append(result, strings.TrimSpace(parts[0])+"@"+strings.TrimSpace(parts[1]))
	}
	return result, nil
}

func (c *Client) send(message automation.Envelope) error {
	if err := message.Validate(); err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.process == nil || c.process.stdin == nil {
		return ErrProcessExited
	}
	if err := json.NewEncoder(c.process.stdin).Encode(message); err != nil {
		return fmt.Errorf("%w: encode: %v", ErrProtocol, err)
	}
	return nil
}

func (c *Client) await(ctx context.Context, request automation.Envelope, expected ...string) (automation.Envelope, error) {
	if ctx == nil {
		return automation.Envelope{}, ErrProtocol
	}
	responses := make(chan automation.Envelope, 4)
	c.stateMu.Lock()
	c.pending[request.ID] = responses
	c.stateMu.Unlock()
	defer func() {
		c.stateMu.Lock()
		delete(c.pending, request.ID)
		c.stateMu.Unlock()
	}()
	if err := c.send(request); err != nil {
		return automation.Envelope{}, err
	}
	for {
		select {
		case response, ok := <-responses:
			if !ok {
				c.stateMu.Lock()
				err := c.readErr
				c.stateMu.Unlock()
				if err != nil {
					return automation.Envelope{}, err
				}
				return automation.Envelope{}, ErrProcessExited
			}
			for _, messageType := range expected {
				if response.Type == messageType {
					return response, nil
				}
			}
			if response.Type == automation.Error {
				return automation.Envelope{}, fmt.Errorf("%w: %s", ErrProtocol, response.Error)
			}
		case <-ctx.Done():
			return automation.Envelope{}, ctx.Err()
		case <-c.readDone:
			c.stateMu.Lock()
			err := c.readErr
			c.stateMu.Unlock()
			if err != nil {
				return automation.Envelope{}, err
			}
			return automation.Envelope{}, ErrProcessExited
		}
	}
}

func (c *Client) Describe(context.Context) (automation.Descriptor, error) {
	if c == nil {
		return automation.Descriptor{}, ErrProcessExited
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.descriptor, nil
}

func (c *Client) Execute(ctx context.Context, session automation.Session, operation automation.Operation) (automation.Result, error) {
	if err := session.Validate(); err != nil {
		return automation.Result{}, err
	}
	if err := operation.Validate(); err != nil {
		return automation.Result{}, err
	}
	parameters, err := json.Marshal(operation.Parameters)
	if err != nil {
		return automation.Result{}, fmt.Errorf("%w: parameters: %v", ErrProtocol, err)
	}
	payload := map[string]string{
		"session_id":     session.SessionID,
		"account_id":     session.AccountID,
		"request_id":     session.RequestID,
		"profile_dir":    session.ProfileDir,
		"runtime":        session.Runtime,
		"session_handle": session.Handle,
		"operation_id":   operation.ID,
		"operation":      operation.Name,
		"parameters":     string(parameters),
	}
	if !operation.Deadline.IsZero() {
		payload["deadline"] = operation.Deadline.UTC().Format(time.RFC3339Nano)
	}
	request := automation.Request(c.nextID("execute"), automation.Execute, payload)
	response, err := c.await(ctx, request, automation.OperationSucceeded, automation.OperationFailed)
	if err != nil {
		return automation.Result{}, err
	}
	result := automation.Result{Succeeded: response.Type == automation.OperationSucceeded}
	if facts := response.Payload["facts"]; facts != "" {
		if err := json.Unmarshal([]byte(facts), &result.Facts); err != nil {
			return automation.Result{}, fmt.Errorf("%w: facts: %v", ErrProtocol, err)
		}
	}
	if !result.Succeeded {
		retryable, parseErr := strconv.ParseBool(response.Payload["retryable"])
		if parseErr != nil {
			return automation.Result{}, fmt.Errorf("%w: retryable flag", ErrProtocol)
		}
		failure := &automation.Failure{Class: automation.FailureClass(response.Payload["failure_class"]), Code: response.Payload["failure_code"], Retryable: retryable}
		result.Failure = failure
	}
	if err := result.Validate(); err != nil {
		return automation.Result{}, err
	}
	return result, nil
}

func (c *Client) Cancel(ctx context.Context, operationID string) error {
	if strings.TrimSpace(operationID) == "" {
		return fmt.Errorf("%w: operation id is required", automation.ErrInvalidContract)
	}
	_, err := c.await(ctx, automation.Request(c.nextID("cancel"), automation.Cancel, map[string]string{"operation_id": operationID}), automation.OperationCancelled)
	return err
}

func (c *Client) Close(ctx context.Context) error {
	if c == nil || c.process == nil {
		return nil
	}
	closeCtx := ctx
	if closeCtx == nil {
		closeCtx = context.Background()
	}
	c.closeOnce.Do(func() {
		defer close(c.closeDone)
		_, _ = c.await(closeCtx, automation.Request(c.nextID("shutdown"), automation.Shutdown, nil), automation.ShutdownAck)
		c.closeErr = c.process.Close(closeCtx)
	})
	<-c.closeDone
	return c.closeErr
}
