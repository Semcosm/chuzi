package protocol

const Version = "v1"

// Message types are kept as constants so the Go control plane and the Node
// worker cannot silently drift on lifecycle spelling.
const (
	Hello          = "hello"
	HelloAck       = "hello_ack"
	Ping           = "ping"
	Pong           = "pong"
	Shutdown       = "shutdown"
	ShutdownAck    = "shutdown_ack"
	SessionStart   = "session_start"
	SessionStarted = "session_started"
	SessionSuccess = "session_succeeded"
	SessionFailure = "session_failed"
	SessionCancel  = "session_cancel"
	SessionCancelled = "session_cancelled"
	Error          = "error"
)

type Envelope struct {
	Protocol string            `json:"protocol"`
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	Payload  map[string]string `json:"payload,omitempty"`
	Error    string            `json:"error,omitempty"`
}

func Request(id, messageType string, payload map[string]string) Envelope {
	return Envelope{
		Protocol: Version,
		ID:       id,
		Type:     messageType,
		Payload:  payload,
	}
}
