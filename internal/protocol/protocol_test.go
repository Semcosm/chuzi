package protocol

import "testing"

func TestRequestUsesCurrentProtocol(t *testing.T) {
	request := Request("req-1", "hello", map[string]string{"service": "chuzi"})

	if request.Protocol != Version {
		t.Fatalf("protocol = %q, want %q", request.Protocol, Version)
	}
	if request.ID != "req-1" || request.Type != "hello" {
		t.Fatalf("unexpected request metadata: %#v", request)
	}
}

func TestLifecycleMessageTypesAreVersionedConstants(t *testing.T) {
	for name, value := range map[string]string{
		"hello":            Hello,
		"hello ack":        HelloAck,
		"session start":    SessionStart,
		"session started":  SessionStarted,
		"session success":  SessionSuccess,
		"session failure":  SessionFailure,
		"session cancel":   SessionCancel,
		"session cancelled": SessionCancelled,
		"shutdown":         Shutdown,
		"shutdown ack":     ShutdownAck,
	} {
		if value == "" {
			t.Errorf("%s message type is empty", name)
		}
	}
}
