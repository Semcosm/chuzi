// Package observability defines the redacted event boundary shared by
// adapters and delivery workers. It intentionally has no logger or network
// dependency so callers can choose a deployment-specific sink.
package observability

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// Event contains only classified, non-secret operational metadata. Callers
// must not add raw errors, credentials, message bodies, or account IDs.
type Event struct {
	At           time.Time
	Component    string
	Operation    string
	Outcome      string
	RequestID    string
	Resource     string
	ErrorClass   string
}

// Sink receives redacted operational events.
type Sink interface {
	Record(Event)
}

// FuncSink adapts a function to Sink for service wiring and tests.
type FuncSink func(Event)

func (f FuncSink) Record(event Event) {
	if f != nil {
		f(event)
	}
}

// NopSink discards events when observability is not configured.
type NopSink struct{}

func (NopSink) Record(Event) {}

// RedactIdentifier provides a stable short label for account and room IDs.
// The original value is never included in the result.
func RedactIdentifier(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(value))
	return "id_" + hex.EncodeToString(digest[:])[:12]
}
