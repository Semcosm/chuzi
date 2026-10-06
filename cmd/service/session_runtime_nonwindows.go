//go:build !windows

package main

import (
	"context"
	"errors"

	"github.com/Semcosm/chuzi/internal/session"
	"github.com/Semcosm/chuzi/internal/store"
)

var errSessionAgentUnavailable = errors.New("session agent unavailable")

type serviceSessionRuntime struct{}

func newServiceSessionRuntime(*store.Store, slotAgentResolver, string) session.Runtime {
	return serviceSessionRuntime{}
}
func (serviceSessionRuntime) Provision(context.Context, session.Binding) error { return nil }
func (serviceSessionRuntime) AgentReady(context.Context, session.Binding) error {
	return errSessionAgentUnavailable
}
func (serviceSessionRuntime) StartWorker(context.Context, session.Binding) error {
	return errSessionAgentUnavailable
}
func (serviceSessionRuntime) StartAdapter(context.Context, session.Binding) error {
	return errSessionAgentUnavailable
}
func (serviceSessionRuntime) Stop(context.Context, session.Binding) error { return nil }
