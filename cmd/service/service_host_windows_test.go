//go:build windows

package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
)

func TestWindowsServiceHostConnectsBeforePreparationAndHandlesStop(t *testing.T) {
	prepareStarted := make(chan struct{})
	allowPrepare := make(chan struct{})
	host := windowsServiceHost{
		options: serviceOptions{shutdownTimeout: time.Second},
		prepare: func(_ context.Context, options serviceOptions) (serviceOptions, error) {
			close(prepareStarted)
			<-allowPrepare
			return options, nil
		},
		run: func(ctx context.Context, _ serviceOptions) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	requests := make(chan svc.ChangeRequest, 2)
	statuses := make(chan svc.Status, 8)
	result := make(chan struct {
		serviceSpecific bool
		exitCode        uint32
	}, 1)
	go func() {
		serviceSpecific, exitCode := host.Execute(nil, requests, statuses)
		result <- struct {
			serviceSpecific bool
			exitCode        uint32
		}{serviceSpecific: serviceSpecific, exitCode: exitCode}
	}()
	if status := receiveServiceStatus(t, statuses); status.State != svc.StartPending {
		t.Fatalf("first status = %v, want StartPending", status.State)
	}
	select {
	case <-prepareStarted:
	case <-time.After(time.Second):
		t.Fatal("startup preparation did not begin")
	}
	requests <- svc.ChangeRequest{Cmd: svc.Interrogate}
	if status := receiveServiceStatus(t, statuses); status.State != svc.StartPending {
		t.Fatalf("interrogate status = %v, want StartPending", status.State)
	}
	close(allowPrepare)
	if status := receiveServiceStatus(t, statuses); status.State != svc.Running {
		t.Fatalf("prepared status = %v, want Running", status.State)
	}
	requests <- svc.ChangeRequest{Cmd: svc.Stop}
	if status := receiveServiceStatus(t, statuses); status.State != svc.StopPending {
		t.Fatalf("stop status = %v, want StopPending", status.State)
	}
	if status := receiveServiceStatus(t, statuses); status.State != svc.Stopped {
		t.Fatalf("final status = %v, want Stopped", status.State)
	}
	select {
	case got := <-result:
		if got.serviceSpecific || got.exitCode != 0 {
			t.Fatalf("Execute result = %#v, want clean stop", got)
		}
	case <-time.After(time.Second):
		t.Fatal("service host did not return after Stop")
	}
}

func TestWindowsServiceHostStopCancelsPendingPreparation(t *testing.T) {
	prepareStarted := make(chan struct{})
	runnerCalled := make(chan struct{}, 1)
	host := windowsServiceHost{
		options: serviceOptions{shutdownTimeout: time.Second},
		prepare: func(ctx context.Context, options serviceOptions) (serviceOptions, error) {
			close(prepareStarted)
			<-ctx.Done()
			return serviceOptions{}, ctx.Err()
		},
		run: func(context.Context, serviceOptions) error {
			runnerCalled <- struct{}{}
			return nil
		},
	}
	requests := make(chan svc.ChangeRequest, 1)
	statuses := make(chan svc.Status, 6)
	result := make(chan struct {
		serviceSpecific bool
		exitCode        uint32
	}, 1)
	go func() {
		serviceSpecific, exitCode := host.Execute(nil, requests, statuses)
		result <- struct {
			serviceSpecific bool
			exitCode        uint32
		}{serviceSpecific: serviceSpecific, exitCode: exitCode}
	}()
	if status := receiveServiceStatus(t, statuses); status.State != svc.StartPending {
		t.Fatalf("first status = %v, want StartPending", status.State)
	}
	select {
	case <-prepareStarted:
	case <-time.After(time.Second):
		t.Fatal("startup preparation did not begin")
	}
	requests <- svc.ChangeRequest{Cmd: svc.Stop}
	if status := receiveServiceStatus(t, statuses); status.State != svc.StopPending {
		t.Fatalf("stop status = %v, want StopPending", status.State)
	}
	if status := receiveServiceStatus(t, statuses); status.State != svc.Stopped {
		t.Fatalf("final status = %v, want Stopped", status.State)
	}
	select {
	case got := <-result:
		if got.serviceSpecific || got.exitCode != 0 {
			t.Fatalf("Execute result = %#v, want clean stop", got)
		}
	case <-time.After(time.Second):
		t.Fatal("service host did not return after Stop")
	}
	select {
	case <-runnerCalled:
		t.Fatal("runner started after preparation was cancelled")
	default:
	}
}

func TestWindowsServiceHostPreparationFailureStopsWithError(t *testing.T) {
	runnerCalled := make(chan struct{}, 1)
	host := windowsServiceHost{
		prepare: func(context.Context, serviceOptions) (serviceOptions, error) {
			return serviceOptions{}, errors.New("preflight")
		},
		run: func(context.Context, serviceOptions) error {
			runnerCalled <- struct{}{}
			return nil
		},
	}
	statuses := make(chan svc.Status, 4)
	serviceSpecific, exitCode := host.Execute(nil, nil, statuses)
	if status := receiveServiceStatus(t, statuses); status.State != svc.StartPending {
		t.Fatalf("first status = %v, want StartPending", status.State)
	}
	if status := receiveServiceStatus(t, statuses); status.State != svc.Stopped || status.Win32ExitCode != 1 {
		t.Fatalf("failure status = %#v, want Stopped with exit code 1", status)
	}
	if !serviceSpecific || exitCode != 1 {
		t.Fatalf("Execute result = serviceSpecific %v, exit %d; want failure", serviceSpecific, exitCode)
	}
	select {
	case <-runnerCalled:
		t.Fatal("runner started after preparation failed")
	default:
	}
}

func TestWindowsServiceHostRecordsSanitizedRuntimeFailure(t *testing.T) {
	diagnostics := make(chan serviceHostDiagnosticEvent, 8)
	host := windowsServiceHost{
		prepare: func(_ context.Context, options serviceOptions) (serviceOptions, error) {
			return options, nil
		},
		run: func(context.Context, serviceOptions) error {
			return withServiceStartupStage("runtime_assembly", errors.New("password=secret C:\\private\\profile"))
		},
		diagnose: func(event serviceHostDiagnosticEvent) { diagnostics <- event },
	}
	statuses := make(chan svc.Status, 4)
	serviceSpecific, exitCode := host.Execute(nil, nil, statuses)
	if status := receiveServiceStatus(t, statuses); status.State != svc.StartPending {
		t.Fatalf("first status = %v, want StartPending", status.State)
	}
	if status := receiveServiceStatus(t, statuses); status.State != svc.Running {
		t.Fatalf("second status = %v, want Running", status.State)
	}
	if status := receiveServiceStatus(t, statuses); status.State != svc.Stopped || status.Win32ExitCode != 1 {
		t.Fatalf("failure status = %#v, want Stopped with exit code 1", status)
	}
	if !serviceSpecific || exitCode != 1 {
		t.Fatalf("Execute result = serviceSpecific %v, exit %d; want failure", serviceSpecific, exitCode)
	}
	close(diagnostics)
	found := false
	for event := range diagnostics {
		if event.Stage == "runtime_assembly" && event.Outcome == "failed" {
			found = true
			if event.ErrorClass != "startup_failed" {
				t.Fatalf("error class = %q, want startup_failed", event.ErrorClass)
			}
		}
		if strings.Contains(event.Stage+event.Outcome+event.ErrorClass+event.Control, "secret") || strings.Contains(event.Stage+event.Outcome+event.ErrorClass+event.Control, "private") {
			t.Fatalf("diagnostic contains raw error data: %#v", event)
		}
	}
	if !found {
		t.Fatal("runtime startup failure diagnostic was not recorded")
	}
}

func receiveServiceStatus(t *testing.T, statuses <-chan svc.Status) svc.Status {
	t.Helper()
	select {
	case status := <-statuses:
		return status
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for service status")
		return svc.Status{}
	}
}
