package automation

import (
	"fmt"
	"strings"
)

// Message types define the wire contract used when an Adapter is hosted in a
// separate plugin process. Payload values remain strings so the protocol is
// deterministic and compatible with the existing JSONL worker boundary.
const (
	Hello              = "hello"
	HelloAck           = "hello_ack"
	Execute            = "execute"
	OperationStarted   = "operation_started"
	OperationSucceeded = "operation_succeeded"
	OperationFailed    = "operation_failed"
	Cancel             = "cancel"
	OperationCancelled = "operation_cancelled"
	Shutdown           = "shutdown"
	ShutdownAck        = "shutdown_ack"
	Error              = "error"
)

type Envelope struct {
	Protocol string            `json:"protocol"`
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	Payload  map[string]string `json:"payload,omitempty"`
	Error    string            `json:"error,omitempty"`
}

func Request(id, messageType string, payload map[string]string) Envelope {
	return Envelope{Protocol: APIVersion, ID: id, Type: messageType, Payload: payload}
}

func (e Envelope) Validate() error {
	if e.Protocol != APIVersion {
		return fmt.Errorf("%w: protocol %q", ErrInvalidContract, e.Protocol)
	}
	if strings.TrimSpace(e.ID) == "" || strings.TrimSpace(e.Type) == "" {
		return fmt.Errorf("%w: message id and type are required", ErrInvalidContract)
	}
	switch e.Type {
	case Hello, HelloAck, Execute, OperationStarted, OperationSucceeded,
		OperationFailed, Cancel, OperationCancelled, Shutdown, ShutdownAck, Error:
	default:
		return fmt.Errorf("%w: unknown message type %q", ErrInvalidContract, e.Type)
	}
	if e.Type == Error && strings.TrimSpace(e.Error) == "" {
		return fmt.Errorf("%w: error message requires a stable code", ErrInvalidContract)
	}
	return nil
}
