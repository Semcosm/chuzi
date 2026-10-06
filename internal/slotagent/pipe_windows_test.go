//go:build windows

package slotagent

import (
	"context"
	"errors"
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
	handler := &RuntimeHandler{Leases: leases, Launcher: resetTestLauncher{job: resetTestJob{}}, jobs: make(map[agentJobKey]Job)}
	prepare := Request{Command: PrepareSlot, RequestID: "request-1", Owner: "service", AccountID: "account-1", SlotID: "pool-001", LeaseID: "slot-lease", EnvironmentGeneration: 2, Auth: "token"}
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

func TestRuntimeHandlerAllowsWorkerAndAdapterForOneRequest(t *testing.T) {
	leases := &LeaseState{}
	if err := leases.Update("pool-001", 2, "provision-pool-001", "maintenance-pool-001", "maintenance-pool-001", "token"); err != nil {
		t.Fatal(err)
	}
	handler := &RuntimeHandler{Leases: leases, Launcher: resetTestLauncher{job: resetTestJob{}}, jobs: make(map[agentJobKey]Job)}
	prepare := Request{Command: PrepareSlot, RequestID: "request-1", Owner: "service", AccountID: "account-1", SlotID: "pool-001", LeaseID: "slot-lease", EnvironmentGeneration: 2, Auth: "token"}
	if _, err := handler.HandleAgentCommand(context.Background(), prepare); err != nil {
		t.Fatal(err)
	}
	worker := prepare
	worker.Command, worker.CommandID, worker.JobKind = StartJob, "start-worker", BrowserWorker
	if _, err := handler.HandleAgentCommand(context.Background(), worker); err != nil {
		t.Fatal(err)
	}
	adapter := worker
	adapter.Command, adapter.CommandID, adapter.JobKind = StartJob, "start-adapter", Adapter
	if _, err := handler.HandleAgentCommand(context.Background(), adapter); err != nil {
		t.Fatal(err)
	}
	if len(handler.jobs) != 2 {
		t.Fatalf("jobs=%d, want worker and adapter", len(handler.jobs))
	}
	duplicate := worker
	duplicate.CommandID = "start-worker-duplicate"
	if _, err := handler.HandleAgentCommand(context.Background(), duplicate); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate worker error=%v, want ErrDuplicate", err)
	}
	stop := prepare
	stop.Command, stop.CommandID, stop.AccountID = StopJob, "stop-1", ""
	if _, err := handler.HandleAgentCommand(context.Background(), stop); err != nil {
		t.Fatal(err)
	}
	if len(handler.jobs) != 0 || handler.active {
		t.Fatalf("jobs after stop=%d active=%v", len(handler.jobs), handler.active)
	}
}
