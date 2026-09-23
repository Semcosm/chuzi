// Package observability defines the redacted event boundary shared by
// adapters and delivery workers. It intentionally has no logger or network
// dependency so callers can choose a deployment-specific sink.
package observability

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// Event contains only classified, non-secret operational metadata. Callers
// must not add raw errors, credentials, message bodies, or account IDs.
type Event struct {
	At         time.Time     `json:"at,omitempty"`
	Component  string        `json:"component,omitempty"`
	Operation  string        `json:"operation,omitempty"`
	Outcome    string        `json:"outcome,omitempty"`
	RequestID  string        `json:"request_id,omitempty"`
	Resource   string        `json:"resource,omitempty"`
	ErrorClass string        `json:"error_class,omitempty"`
	Duration   time.Duration `json:"-"`
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

// SanitizeEvent returns the only representation permitted to leave the
// in-process observability boundary. Identifiers are hashed and free-form
// fields are restricted to bounded classification tokens.
func SanitizeEvent(event Event, fallback time.Time) Event {
	if event.At.IsZero() {
		event.At = fallback
	}
	if event.At.IsZero() {
		event.At = time.Unix(0, 0).UTC()
	}
	event.At = event.At.UTC()
	event.Component = safeField(event.Component)
	event.Operation = safeField(event.Operation)
	event.Outcome = safeField(event.Outcome)
	if event.RequestID != "" {
		event.RequestID = RedactIdentifier(event.RequestID)
	}
	if event.Resource != "" {
		event.Resource = RedactIdentifier(event.Resource)
	}
	event.ErrorClass = safeField(event.ErrorClass)
	if event.Duration < 0 {
		event.Duration = 0
	}
	if event.Duration > 24*time.Hour {
		event.Duration = 24 * time.Hour
	}
	return event
}

// EventBuffer keeps a bounded, redacted window for explicit user-consented
// diagnostics. It never stores raw log lines, stack traces, credentials, or
// page content.
type EventBuffer struct {
	mu     sync.Mutex
	limit  int
	events []Event
}

func NewEventBuffer(limit int) *EventBuffer {
	if limit < 1 {
		limit = 128
	}
	if limit > 2048 {
		limit = 2048
	}
	return &EventBuffer{limit: limit}
}

func (b *EventBuffer) Record(event Event) {
	if b == nil {
		return
	}
	event = SanitizeEvent(event, time.Now().UTC())
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, event)
	if excess := len(b.events) - b.limit; excess > 0 {
		copy(b.events, b.events[excess:])
		b.events = b.events[:b.limit]
	}
}

func (b *EventBuffer) Snapshot(limit int) []Event {
	if b == nil {
		return nil
	}
	if limit <= 0 || limit > b.limit {
		limit = b.limit
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	start := len(b.events) - limit
	if start < 0 {
		start = 0
	}
	result := make([]Event, len(b.events)-start)
	copy(result, b.events[start:])
	return result
}

// MultiSink fans one redacted event out to a set of sinks. A nil sink is
// ignored, which makes optional log/metric wiring safe in tests and in a
// minimal deployment.
type MultiSink []Sink

func (s MultiSink) Record(event Event) {
	for _, sink := range s {
		if sink != nil {
			sink.Record(event)
		}
	}
}

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
