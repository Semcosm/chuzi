//go:build windows

package main

import (
	"context"
	"errors"
	"sync"

	"github.com/Semcosm/chuzi/internal/session"
	"github.com/Semcosm/chuzi/internal/slot"
	"github.com/Semcosm/chuzi/internal/slotagent"
	"github.com/Semcosm/chuzi/internal/store"
)

type serviceSessionRuntime struct {
	store    *store.Store
	resolver slotAgentResolver
	owner    string
	mu       sync.Mutex
	clients  map[string]sessionAgentClient
}

type sessionAgentClient struct {
	client *slotagent.Client
	auth   string
}

func newServiceSessionRuntime(database *store.Store, resolver slotAgentResolver, owner string) session.Runtime {
	return &serviceSessionRuntime{store: database, resolver: resolver, owner: owner, clients: make(map[string]sessionAgentClient)}
}

func (r *serviceSessionRuntime) Provision(_ context.Context, b session.Binding) error {
	if r == nil || r.store == nil {
		return errors.New("session provisioning unavailable")
	}
	value, err := r.store.GetSlot(b.SlotID)
	if err != nil || value.EnvironmentGeneration != b.EnvironmentGeneration || value.Status != slot.Leased {
		return errors.New("session slot lease unavailable")
	}
	return nil
}

func (r *serviceSessionRuntime) client(ctx context.Context, b session.Binding) (*slotagent.Client, string, error) {
	if r == nil || r.resolver == nil {
		return nil, "", errors.New("session agent unavailable")
	}
	r.mu.Lock()
	if existing, ok := r.clients[b.SessionID]; ok && existing.client != nil {
		r.mu.Unlock()
		return existing.client, existing.auth, nil
	}
	r.mu.Unlock()
	endpoint, token, err := r.resolver.AgentEndpoint(b.SlotID, b.AgentHandle)
	if err != nil {
		return nil, "", errors.New("session agent unavailable")
	}
	client, err := slotagent.Dial(ctx, endpoint)
	if err != nil {
		return nil, "", errors.New("session agent unavailable")
	}
	r.mu.Lock()
	r.clients[b.SessionID] = sessionAgentClient{client: client, auth: token}
	r.mu.Unlock()
	return client, token, nil
}

func (r *serviceSessionRuntime) call(ctx context.Context, b session.Binding, command slotagent.Command, kind slotagent.JobKind) error {
	client, auth, err := r.client(ctx, b)
	if err != nil {
		return err
	}
	accountID := ""
	if command == slotagent.StartJob {
		accountID = b.AccountID
	}
	request := slotagent.Request{CommandID: b.SessionID + "-" + string(command), RequestID: b.RequestID, Owner: r.owner, AccountID: accountID, SlotID: b.SlotID, LeaseID: b.SlotLeaseID, EnvironmentGeneration: b.EnvironmentGeneration, Auth: auth, Command: command, JobKind: kind}
	response, err := client.Call(ctx, request)
	if err != nil || !response.OK {
		r.mu.Lock()
		delete(r.clients, b.SessionID)
		r.mu.Unlock()
		_ = client.Close()
		return errors.New("session agent command failed")
	}
	return nil
}

func (r *serviceSessionRuntime) AgentReady(ctx context.Context, b session.Binding) error {
	return r.call(ctx, b, slotagent.Health, "")
}
func (r *serviceSessionRuntime) StartWorker(ctx context.Context, b session.Binding) error {
	return r.call(ctx, b, slotagent.StartJob, slotagent.BrowserWorker)
}
func (r *serviceSessionRuntime) StartAdapter(ctx context.Context, b session.Binding) error {
	return r.call(ctx, b, slotagent.StartJob, slotagent.Adapter)
}

func (r *serviceSessionRuntime) Stop(ctx context.Context, b session.Binding) error {
	r.mu.Lock()
	entry := r.clients[b.SessionID]
	delete(r.clients, b.SessionID)
	r.mu.Unlock()
	if entry.client == nil {
		return nil
	}
	request := slotagent.Request{CommandID: b.SessionID + "-stop", RequestID: b.RequestID, Owner: r.owner, SlotID: b.SlotID, LeaseID: b.SlotLeaseID, EnvironmentGeneration: b.EnvironmentGeneration, Auth: entry.auth, Command: slotagent.StopJob}
	_, err := entry.client.Call(ctx, request)
	_ = entry.client.Close()
	return err
}
