//go:build windows

package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/environment"
	"github.com/Semcosm/chuzi/internal/slot"
	"github.com/Semcosm/chuzi/internal/slotlifecycle"
	"github.com/Semcosm/chuzi/internal/slotwindows"
	"github.com/Semcosm/chuzi/internal/store"
)

type serviceSessionBootstrapper struct{}

func (serviceSessionBootstrapper) Start(context.Context, slotwindows.ManagedIdentity) (slotwindows.BootstrapSession, error) {
	return slotwindows.BootstrapSession{ID: 17, State: "active"}, nil
}

func (serviceSessionBootstrapper) Stop(context.Context, slotwindows.ManagedIdentity, slotwindows.BootstrapSession) error {
	return nil
}

func TestResolveSessionBootstrapperUsesInjectedProvider(t *testing.T) {
	fake := serviceSessionBootstrapper{}
	resolved := resolveSessionBootstrapper(serviceOptions{sessionBootstrapper: fake})
	if _, ok := resolved.(serviceSessionBootstrapper); !ok {
		t.Fatalf("resolved provider = %T, want injected fake", resolved)
	}
}

func TestResolveSessionBootstrapperDefaultsToFixedBrokerClient(t *testing.T) {
	resolved := resolveSessionBootstrapper(serviceOptions{})
	if resolved == nil {
		t.Fatal("default session bootstrapper is nil")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := resolved.Start(ctx, slotwindows.ManagedIdentity{})
	if !errors.Is(err, slotwindows.ErrSessionUnavailable) {
		t.Fatalf("cancelled broker client error = %v", err)
	}
}

type dynamicPoolProvisioner struct {
	mu    sync.Mutex
	calls map[string]int
}

func (p *dynamicPoolProvisioner) Provision(_ context.Context, request slot.ProvisionRequest) (slot.ProvisionResult, error) {
	p.mu.Lock()
	p.calls[request.PoolID]++
	p.mu.Unlock()
	summary := slot.EnvironmentSummary{
		EnvironmentID:  request.Requirement.EnvironmentID,
		Version:        request.Requirement.Version,
		Generation:     request.EnvironmentGeneration,
		Capabilities:   append([]string(nil), request.Requirement.Capabilities...),
		ManifestDigest: request.Requirement.ManifestDigest,
		Signer:         request.Requirement.Signer,
		Trusted:        true,
		AgentVersion:   "test-agent",
		SessionState:   "ready",
		DesktopReady:   true,
		AgentHandle:    "opaque",
		UpdatedAt:      time.Now().UTC(),
	}
	return slot.ProvisionResult{AgentHandle: "opaque", Summary: summary}, nil
}

func (p *dynamicPoolProvisioner) Inspect(_ context.Context, request slot.ProvisionRequest) (slot.EnvironmentSummary, error) {
	result, err := p.Provision(context.Background(), request)
	return result.Summary, err
}

func (*dynamicPoolProvisioner) Retire(context.Context, slot.ProvisionRequest) error { return nil }

func TestWindowsAllPoolReconcilerDiscoversEveryStoredPool(t *testing.T) {
	dataDir := t.TempDir()
	cfg, err := config.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	now := time.Date(2026, time.October, 10, 0, 0, 0, 0, time.UTC)
	digest := strings.Repeat("a", 64)
	for _, poolID := range []string{"pool-a", "pool-b"} {
		if err := database.PutEnvironmentRecord(environment.Record{
			EnvironmentID: "env/" + poolID, Version: "1.0.0", Capabilities: []string{"desktop"},
			ManifestDigest: digest, Signer: "test-signer", Installed: true, Verified: true, Trusted: true,
			Enabled: true, Healthy: true, Ready: true, Generation: 1, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		if err := database.ReconcileJobPool(slot.PoolConfig{
			PoolID: poolID, EnvironmentID: "env/" + poolID, EnvironmentVersion: "1.0.0",
			DesiredSlots: 1, Capabilities: []string{"desktop"}, ManifestDigest: digest, Signer: "test-signer", RequireTrusted: true,
		}, now); err != nil {
			t.Fatal(err)
		}
	}

	provisioner := &dynamicPoolProvisioner{calls: make(map[string]int)}
	reconciler := &windowsAllPoolReconciler{
		database: database, provisioner: provisioner, now: func() time.Time { return now },
		provisionTimeout: time.Second, cleanupTimeout: time.Second, owner: "service", items: make(map[string]*slotlifecycle.Reconciler),
	}
	if err := reconciler.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(reconciler.items) != 2 {
		t.Fatalf("cached reconcilers = %d, want 2", len(reconciler.items))
	}
	provisioner.mu.Lock()
	defer provisioner.mu.Unlock()
	for _, poolID := range []string{"pool-a", "pool-b"} {
		if provisioner.calls[poolID] == 0 {
			t.Fatalf("pool %q was not provisioned", poolID)
		}
	}
}
