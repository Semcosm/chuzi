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
