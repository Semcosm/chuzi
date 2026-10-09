package main

import (
	"context"
	"errors"
	"sync"
)

type slotReconciler interface {
	Reconcile(context.Context) error
}

// allPoolReconciler is implemented by the logical platform-neutral runtime.
// Unlike the Windows native reconciler, it can discover pools created through
// Core after startup and must keep reconciling even after the startup pool was
// deleted.
type allPoolReconciler interface {
	ReconcileAll(context.Context) error
}

type slotProfileAccess interface {
	GrantProfile(context.Context, string, string) error
	RevokeProfile(context.Context, string, string) error
}

type slotAgentResolver interface {
	AgentEndpoint(string, string) (string, string, error)
}

type slotCapabilityRevoker interface {
	RevokeSlot(context.Context, string) error
}

var errSlotLifecycleUnhealthy = errors.New("slot lifecycle is unhealthy")

// slotLifecycleHealth is the small shared state behind the readiness probe.
// Reconcile errors are classified at the service boundary; raw OS errors never
// reach the health response or observability sinks.
type slotLifecycleHealth struct {
	mu      sync.RWMutex
	failing bool
}

func newSlotLifecycleHealth() *slotLifecycleHealth {
	return &slotLifecycleHealth{}
}

func (h *slotLifecycleHealth) set(err error) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.failing = err != nil
	h.mu.Unlock()
}

func (h *slotLifecycleHealth) probe(context.Context) error {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	failing := h.failing
	h.mu.RUnlock()
	if failing {
		return errSlotLifecycleUnhealthy
	}
	return nil
}
