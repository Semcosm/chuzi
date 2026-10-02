package slotagent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Semcosm/chuzi/internal/protocol"
)

type leaseCheck struct{ want string }

func (l leaseCheck) ValidateAgentLease(_ context.Context, r Request) error {
	if r.LeaseID != l.want {
		return ErrStaleLease
	}
	return nil
}

type handlerFunc func(context.Context, Request) (Response, error)

func (h handlerFunc) HandleAgentCommand(ctx context.Context, r Request) (Response, error) {
	return h(ctx, r)
}

type staleWorkerHandler struct{ disconnected bool }

func (h *staleWorkerHandler) HandleAgentCommand(context.Context, Request) (Response, error) {
	return Response{OK: true}, nil
}

func (h *staleWorkerHandler) HandleWorkerFrame(context.Context, Frame) (Frame, error) {
	return Frame{}, ErrStaleLease
}

func (h *staleWorkerHandler) DisconnectJobs(context.Context) { h.disconnected = true }

type workerLeaseCheck struct {
	want  string
	calls int
}

func (l *workerLeaseCheck) ValidateAgentLease(context.Context, Request) error {
	return nil
}

func (l *workerLeaseCheck) ValidateAgentWorkerLease(_ context.Context, frame Frame) error {
	l.calls++
	if frame.LeaseID != l.want {
		return ErrStaleLease
	}
	return nil
}

type acceptingWorkerHandler struct {
	forwarded    bool
	disconnected bool
}

func (h *acceptingWorkerHandler) HandleAgentCommand(context.Context, Request) (Response, error) {
	return Response{OK: true}, nil
}

func (h *acceptingWorkerHandler) HandleWorkerFrame(_ context.Context, frame Frame) (Frame, error) {
	h.forwarded = true
	return Frame{Kind: "worker_event", SlotID: frame.SlotID, RequestID: frame.RequestID, Owner: frame.Owner, LeaseID: frame.LeaseID, EnvironmentGeneration: frame.EnvironmentGeneration, Worker: frame.Worker}, nil
}

func (h *acceptingWorkerHandler) DisconnectJobs(context.Context) { h.disconnected = true }

func TestAgentRequestHasClosedCommandAndJobEnums(t *testing.T) {
	base := Request{CommandID: "command-1", RequestID: "request-1", Owner: "service", AccountID: "account-1", SlotID: "pool-001", LeaseID: "lease-1", EnvironmentGeneration: 2, Command: StartJob, JobKind: BrowserWorker}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	base.Owner = ""
	if !errors.Is(base.Validate(), ErrInvalidMessage) {
		t.Fatal("missing owner accepted")
	}
	base.Owner = "service"
	base.Command = Command("run-powershell")
	if !errors.Is(base.Validate(), ErrUnsupported) {
		t.Fatal("unknown command accepted")
	}
	base.Command, base.JobKind = StartJob, JobKind("shell")
	if !errors.Is(base.Validate(), ErrInvalidMessage) {
		t.Fatal("unknown job kind accepted")
	}
	base.Command, base.JobKind, base.AccountID = PrepareSlot, "", ""
	if !errors.Is(base.Validate(), ErrInvalidMessage) {
		t.Fatal("prepare without account accepted")
	}
	base.AccountID = "account-1"
	base.SlotID = "pool\\\\001"
	if !errors.Is(base.Validate(), ErrInvalidMessage) {
		t.Fatal("path-shaped slot ID accepted")
	}
}

func TestAgentServerChecksLeaseSlotGenerationAndDuplicateRequest(t *testing.T) {
	server, err := NewServer("pool-001", 2, leaseCheck{want: "lease-1"}, handlerFunc(func(_ context.Context, r Request) (Response, error) { return Response{OK: true}, nil }))
	if err != nil {
		t.Fatal(err)
	}
	req := Request{CommandID: "command-1", RequestID: "request-1", Owner: "service", SlotID: "pool-001", LeaseID: "lease-1", EnvironmentGeneration: 2, Command: Health}
	response, err := server.Handle(context.Background(), req)
	if err != nil || !response.OK || response.RequestID != req.RequestID || response.LeaseID != req.LeaseID {
		t.Fatalf("Handle = %#v, %v", response, err)
	}
	if _, err := server.Handle(context.Background(), req); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate = %v", err)
	}
	req.CommandID, req.LeaseID = "command-2", "stale"
	if _, err := server.Handle(context.Background(), req); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("stale lease = %v", err)
	}
	req.CommandID, req.LeaseID, req.EnvironmentGeneration = "command-3", "lease-1", 3
	if _, err := server.Handle(context.Background(), req); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("generation mismatch = %v", err)
	}
}

func TestAgentServerDisconnectsWorkerOnStaleLease(t *testing.T) {
	handler := &staleWorkerHandler{}
	server, err := NewServer("pool-001", 2, leaseCheck{want: "lease-1"}, handler)
	if err != nil {
		t.Fatal(err)
	}
	frame := Frame{Kind: "worker_request", SlotID: "pool-001", RequestID: "request-1", Owner: "service", LeaseID: "lease-1", EnvironmentGeneration: 2, Worker: &protocol.Envelope{Protocol: protocol.Version, ID: "ping-1", Type: protocol.Ping}}
	if _, err := server.HandleWorker(context.Background(), frame); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("stale worker lease = %v", err)
	}
	if !handler.disconnected {
		t.Fatal("stale worker did not disconnect active jobs")
	}
}

func TestAgentServerValidatesWorkerLeaseBeforeAlternateHandler(t *testing.T) {
	leases := &workerLeaseCheck{want: "lease-1"}
	handler := &acceptingWorkerHandler{}
	server, err := NewServer("pool-001", 2, leases, handler)
	if err != nil {
		t.Fatal(err)
	}
	frame := Frame{Kind: "worker_request", SlotID: "pool-001", RequestID: "request-1", Owner: "service", LeaseID: "stale", EnvironmentGeneration: 2, Worker: &protocol.Envelope{Protocol: protocol.Version, ID: "ping-1", Type: protocol.Ping}}
	if _, err := server.HandleWorker(context.Background(), frame); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("stale worker lease = %v", err)
	}
	if leases.calls != 1 || handler.forwarded || !handler.disconnected {
		t.Fatalf("worker fence bypassed: calls=%d forwarded=%t disconnected=%t", leases.calls, handler.forwarded, handler.disconnected)
	}
	frame.LeaseID = "lease-1"
	if _, err := server.HandleWorker(context.Background(), frame); err != nil {
		t.Fatalf("valid worker lease = %v", err)
	}
	if leases.calls != 2 || !handler.forwarded {
		t.Fatalf("worker lease validator not applied: calls=%d forwarded=%t", leases.calls, handler.forwarded)
	}
}

func TestAgentServerBoundsCommandDedupeWindow(t *testing.T) {
	server, err := NewServer("pool-001", 1, leaseCheck{want: "lease-1"}, handlerFunc(func(_ context.Context, _ Request) (Response, error) {
		return Response{OK: true}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	request := Request{RequestID: "request-1", Owner: "service", SlotID: "pool-001", LeaseID: "lease-1", EnvironmentGeneration: 1, Command: Health}
	for index := 0; index <= maxSeenCommands; index++ {
		request.CommandID = fmt.Sprintf("command-%d", index)
		if _, err := server.Handle(context.Background(), request); err != nil {
			t.Fatalf("command %d = %v", index, err)
		}
	}
	request.CommandID = "command-0"
	if _, err := server.Handle(context.Background(), request); err != nil {
		t.Fatalf("evicted command was not accepted: %v", err)
	}
}

func TestAgentRequestRejectsArbitraryShellFields(t *testing.T) {
	// Strict decoding at the transport rejects fields absent from Request; the
	// typed Go representation likewise has no executable/argv/script property.
	if _, err := NewServer("pool-001", 1, leaseCheck{want: "lease"}, handlerFunc(func(context.Context, Request) (Response, error) { return Response{}, nil })); err != nil {
		t.Fatal(err)
	}
}

func TestWireRejectsUnknownFieldsAndProfilePathWorkerPayload(t *testing.T) {
	input := "{\"kind\":\"command\",\"request\":{\"command_id\":\"c\",\"request_id\":\"r\",\"slot_id\":\"pool-001\",\"lease_id\":\"l\",\"environment_generation\":1,\"command\":\"health\",\"executable\":\"cmd.exe\"}}\n"
	if _, err := ReadFrame(strings.NewReader(input)); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("unknown field error = %v", err)
	}
	worker := protocol.Envelope{Protocol: protocol.Version, ID: "session-1", Type: protocol.SessionStart, Payload: map[string]string{"profile_dir": "C:\\private\\profile"}}
	if err := ValidateWorkerEnvelope(worker); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("profile path error = %v", err)
	}
	worker.Payload = map[string]string{"profile": "C:\\private\\profile"}
	if err := ValidateWorkerEnvelope(worker); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("profile field error = %v", err)
	}
	worker.Payload = map[string]string{"diagnostic": "private runtime detail"}
	if err := ValidateWorkerEnvelope(worker); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("unknown worker field error = %v", err)
	}
	worker.Type = protocol.SessionFailure
	worker.Payload = map[string]string{"failure": "transient", "reason": "C:\\private\\path"}
	if err := ValidateWorkerEnvelope(worker); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("path-shaped worker reason error = %v", err)
	}
	worker.Payload = map[string]string{"failure": "runtime", "reason": "browser_crashed"}
	if err := ValidateWorkerEnvelope(worker); err != nil {
		t.Fatalf("runtime worker failure rejected: %v", err)
	}
}
