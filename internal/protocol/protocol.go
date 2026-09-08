package protocol

const Version = "v1"

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
