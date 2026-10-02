//go:build windows

package browser

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/internal/protocol"
	"github.com/Semcosm/chuzi/internal/slotagent"
)

type AgentConnection struct {
	Path  string
	Token string
}

type AgentResolver interface {
	ResolveAgent(string, string) (AgentConnection, error)
}

type AgentProcessFactory struct {
	resolver AgentResolver
	kind     slotagent.JobKind
}

// leaseCommandID makes control command retries idempotent for one slot lease
// while allowing a later retry of the same request to use a fresh lease.
func leaseCommandID(prefix, requestID, leaseID string) string {
	digest := sha256.Sum256([]byte(prefix + "\x00" + requestID + "\x00" + leaseID))
	return prefix + "-" + hex.EncodeToString(digest[:])
}

func NewAgentProcessFactory(resolver AgentResolver, kind slotagent.JobKind) (WorkerFactory, error) {
	if resolver == nil || (kind != slotagent.BrowserWorker && kind != slotagent.Adapter) {
		return nil, ErrInvalidProcessConfig
	}
	return &AgentProcessFactory{resolver: resolver, kind: kind}, nil
}

func (f *AgentProcessFactory) Start(ctx context.Context, spec WorkerSpec) (Worker, error) {
	if f == nil || ctx == nil || spec.validate() != nil || spec.SlotID == "" || spec.EnvironmentGeneration == 0 || spec.SlotLeaseID == "" || spec.AgentHandle == "" {
		return nil, ErrInvalidWork
	}
	endpoint, err := f.resolver.ResolveAgent(spec.SlotID, spec.AgentHandle)
	if err != nil || endpoint.Path == "" || endpoint.Token == "" {
		return nil, ErrWorkerCrashed
	}
	client, err := slotagent.Dial(ctx, endpoint.Path)
	if err != nil {
		return nil, err
	}
	worker := &agentWorker{client: client, spec: spec, token: endpoint.Token, kind: f.kind}
	prepare := slotagent.Request{CommandID: leaseCommandID("prepare", spec.RequestID, spec.SlotLeaseID), RequestID: spec.RequestID, AccountID: spec.AccountID, SlotID: spec.SlotID, LeaseID: spec.SlotLeaseID, EnvironmentGeneration: spec.EnvironmentGeneration, Auth: endpoint.Token, Command: slotagent.PrepareSlot}
	if _, err := client.Call(ctx, prepare); err != nil {
		_ = client.Close()
		return nil, err
	}
	start := slotagent.Request{CommandID: leaseCommandID("start", spec.RequestID, spec.SlotLeaseID), RequestID: spec.RequestID, AccountID: spec.AccountID, SlotID: spec.SlotID, LeaseID: spec.SlotLeaseID, EnvironmentGeneration: spec.EnvironmentGeneration, Auth: endpoint.Token, Command: slotagent.StartJob, JobKind: f.kind}
	if _, err := client.Call(ctx, start); err != nil {
		_ = client.Close()
		return nil, err
	}
	return worker, nil
}

type agentWorker struct {
	client  *slotagent.Client
	spec    WorkerSpec
	token   string
	kind    slotagent.JobKind
	mu      sync.Mutex
	closed  bool
	stopped bool
	next    uint64
}

func (w *agentWorker) nextID(prefix string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.next++
	return fmt.Sprintf("%s-%d", prefix, w.next)
}

func (w *agentWorker) call(ctx context.Context, message protocol.Envelope) (protocol.Envelope, error) {
	frame := slotagent.Frame{Kind: "worker_request", SlotID: w.spec.SlotID, RequestID: w.spec.RequestID, LeaseID: w.spec.SlotLeaseID, EnvironmentGeneration: w.spec.EnvironmentGeneration, Worker: &message}
	response, err := w.client.WorkerCall(ctx, frame)
	if err != nil {
		return protocol.Envelope{}, err
	}
	return *response.Worker, nil
}

func (w *agentWorker) Run(ctx context.Context) (WorkerResult, error) {
	if w == nil || w.client == nil {
		return WorkerResult{}, ErrWorkerNotRunning
	}
	hello := protocol.Request(w.nextID("hello"), protocol.Hello, map[string]string{"service": "chuzi-session-runner", "version": protocol.Version})
	if _, err := w.call(ctx, hello); err != nil {
		return WorkerResult{}, err
	}
	request := protocol.Request(w.nextID("session"), protocol.SessionStart, map[string]string{"session_id": w.spec.SessionID, "account_id": w.spec.AccountID, "request_id": w.spec.RequestID})
	if w.spec.Mode != "" {
		request.Payload["mode"] = w.spec.Mode
	}
	started, err := w.call(ctx, request)
	if err != nil {
		return WorkerResult{}, err
	}
	if started.Error != "" {
		return WorkerResult{}, ErrWorkerProtocol
	}
	if started.Type == protocol.SessionFailure {
		return WorkerResult{Failure: failureClass(started.Payload["failure"])}, nil
	}
	if started.Type == protocol.SessionCancelled {
		return WorkerResult{Failure: account.TransientFailure}, context.Canceled
	}
	if started.Type == protocol.SessionStarted && w.spec.Mode == "adapter" {
		handle, valid := sessionHandle(started.Payload)
		if !valid {
			return WorkerResult{}, fmt.Errorf("%w: adapter session handle", ErrWorkerProtocol)
		}
		return WorkerResult{Succeeded: true, Handle: handle}, nil
	}
	for {
		select {
		case <-ctx.Done():
			return WorkerResult{}, ctx.Err()
		default:
		}
		message, err := w.call(ctx, protocol.Request(w.nextID("ping"), protocol.Ping, nil))
		if err != nil {
			return WorkerResult{}, err
		}
		select {
		case <-ctx.Done():
			return WorkerResult{}, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
		// A worker terminal event is returned by the same request ID as the
		// corresponding lifecycle command. A ping only keeps the bridge alive;
		// the runtime emits terminal events through the session request and the
		// agent returns them on the next bridge call.
		switch message.Type {
		case protocol.SessionSuccess:
			return WorkerResult{Succeeded: true}, nil
		case protocol.SessionFailure:
			return WorkerResult{Failure: failureClass(message.Payload["failure"])}, nil
		case protocol.SessionCancelled:
			return WorkerResult{Failure: account.TransientFailure}, context.Canceled
		}
	}
}

func (w *agentWorker) Cancel(ctx context.Context) error {
	if w == nil || w.client == nil {
		return ErrWorkerNotRunning
	}
	_, err := w.call(ctx, protocol.Request(w.nextID("cancel"), protocol.SessionCancel, map[string]string{"session_id": w.spec.SessionID}))
	command := slotagent.Request{CommandID: w.nextID("cancel-job"), RequestID: w.spec.RequestID, SlotID: w.spec.SlotID, LeaseID: w.spec.SlotLeaseID, EnvironmentGeneration: w.spec.EnvironmentGeneration, Auth: w.token, Command: slotagent.CancelJob}
	_, callErr := w.client.Call(ctx, command)
	if callErr == nil {
		w.mu.Lock()
		w.stopped = true
		w.mu.Unlock()
	}
	return errors.Join(err, callErr)
}

func (w *agentWorker) Close(ctx context.Context) error {
	if w == nil || w.client == nil {
		return nil
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	stopped := w.stopped
	w.mu.Unlock()
	var stopErr error
	if !stopped {
		command := slotagent.Request{CommandID: w.nextID("stop-job"), RequestID: w.spec.RequestID, SlotID: w.spec.SlotID, LeaseID: w.spec.SlotLeaseID, EnvironmentGeneration: w.spec.EnvironmentGeneration, Auth: w.token, Command: slotagent.StopJob}
		_, stopErr = w.client.Call(ctx, command)
	}
	return errors.Join(stopErr, w.client.Close())
}

var _ WorkerFactory = (*AgentProcessFactory)(nil)
var _ Worker = (*agentWorker)(nil)
