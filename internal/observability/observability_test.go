package observability

import (
	"strings"
	"testing"
	"time"
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

func TestEventBufferRetainsFailuresAndRedactsNativeDiagnostics(t *testing.T) {
	buffer := NewEventBuffer(4)
	buffer.Record(Event{
		Component: "windows_session", Operation: "provision", Outcome: "failed",
		Resource: "private-slot-id", ErrorClass: "provision_failed",
		FailureStage: "profile_create", NativeErrorKind: "hresult", NativeErrorCode: 0x80070005,
	})
	for index := 0; index < 12; index++ {
		buffer.Record(Event{Component: "queue", Operation: "claim", Outcome: "idle"})
	}

	events := buffer.Snapshot(4)
	if len(events) != 4 {
		t.Fatalf("event count = %d, want 4", len(events))
	}
	var failure *Event
	for index := range events {
		if events[index].ErrorClass == "provision_failed" {
			failure = &events[index]
			break
		}
	}
	if failure == nil {
		t.Fatalf("classified failure was evicted: %#v", events)
	}
	if failure.FailureStage != "profile_create" || failure.NativeErrorKind != "hresult" || failure.NativeErrorCode != 0x80070005 {
		t.Fatalf("native failure details = %#v", failure)
	}
	if failure.Resource == "private-slot-id" || strings.Contains(failure.Resource, "private-slot-id") {
		t.Fatalf("resource was not redacted: %q", failure.Resource)
	}
}

func TestSanitizeEventRejectsUnknownNativeErrorKind(t *testing.T) {
	event := SanitizeEvent(Event{FailureStage: "profile create", NativeErrorKind: "raw_os_error", NativeErrorCode: 5}, time.Time{})
	if event.FailureStage != "" || event.NativeErrorKind != "" || event.NativeErrorCode != 0 {
		t.Fatalf("unsafe diagnostic metadata survived sanitization: %#v", event)
	}
}
