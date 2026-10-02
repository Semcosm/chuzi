//go:build windows

package slotagent

import (
	"context"
	"testing"

	"github.com/Semcosm/chuzi/internal/protocol"
)

type resetTestJob struct{}

func (resetTestJob) RoundTrip(context.Context, protocol.Envelope) (protocol.Envelope, error) {
	return protocol.Envelope{}, nil
}

func (resetTestJob) Stop() error { return nil }

type resetTestLauncher struct{ job Job }

func (l resetTestLauncher) Start(context.Context, Request) (Job, error) { return l.job, nil }

func TestRuntimeHandlerRestoresBootstrapLeaseAfterStop(t *testing.T) {
	leases := &LeaseState{}
	if err := leases.Update("pool-001", 2, "provision-pool-001", "maintenance-pool-001", "maintenance-pool-001", "token"); err != nil {
		t.Fatal(err)
	}
	handler := &RuntimeHandler{Leases: leases, Launcher: resetTestLauncher{job: resetTestJob{}}, jobs: make(map[string]Job)}
	prepare := Request{Command: PrepareSlot, RequestID: "request-1", AccountID: "account-1", SlotID: "pool-001", LeaseID: "slot-lease", EnvironmentGeneration: 2, Auth: "token"}
	if _, err := handler.HandleAgentCommand(context.Background(), prepare); err != nil {
		t.Fatal(err)
	}
	start := prepare
	start.Command, start.CommandID = StartJob, "start-1"
	start.JobKind = BrowserWorker
	if _, err := handler.HandleAgentCommand(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	stop := start
	stop.Command, stop.CommandID = StopJob, "stop-1"
	stop.JobKind, stop.AccountID = "", ""
	if _, err := handler.HandleAgentCommand(context.Background(), stop); err != nil {
		t.Fatal(err)
	}
	if leases.LeaseID != "provision-pool-001" || leases.RequestID != "maintenance-pool-001" || leases.AccountID != "maintenance-pool-001" {
		t.Fatalf("lease after stop = %#v, want bootstrap lease", leases)
	}
}
