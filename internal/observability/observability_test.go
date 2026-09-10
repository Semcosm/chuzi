package observability

import (
	"strings"
	"testing"
)

func TestRedactIdentifierIsStableAndDoesNotContainInput(t *testing.T) {
	secret := "!private-room:example.org"
	first := RedactIdentifier(secret)
	second := RedactIdentifier(secret)
	if first == "" || first != second || first == secret {
		t.Fatalf("redacted identifier = %q/%q", first, second)
	}
	if strings.Contains(first, secret) {
		t.Fatalf("redacted identifier contains input: %q", first)
	}
}

func TestFuncSinkReceivesClassifiedEvent(t *testing.T) {
	called := false
	sink := FuncSink(func(event Event) {
		called = true
		if event.ErrorClass != "send_failed" || event.Resource != "id_room" {
			t.Fatalf("event = %#v", event)
		}
	})
	sink.Record(Event{ErrorClass: "send_failed", Resource: "id_room"})
	if !called {
		t.Fatal("sink was not called")
	}
}
