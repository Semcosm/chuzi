//go:build windows

package servicehost

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func TestControllerStatusUsesQueryOnlyServiceAccess(t *testing.T) {
	managerHandle, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		t.Skipf("SCM query unavailable: %v", err)
	}
	defer windows.CloseServiceHandle(managerHandle)
	serviceHandle, err := windows.OpenService(managerHandle, windows.StringToUTF16Ptr(ServiceName), windows.SERVICE_QUERY_CONFIG|windows.SERVICE_QUERY_STATUS)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		t.Skip("ChuziCore is not installed")
	}
	if err != nil {
		t.Fatalf("query-only OpenService: %v", err)
	}
	defer windows.CloseServiceHandle(serviceHandle)
	service := &mgr.Service{Name: ServiceName, Handle: serviceHandle}
	config, err := service.Config()
	if err != nil {
		t.Fatalf("query-only service Config: %v", err)
	}
	argv, ok := parseWindowsCommandLine(config.BinaryPathName)
	if !ok || len(argv) == 0 {
		t.Fatalf("invalid installed service command line %q", config.BinaryPathName)
	}
	runtime, err := NewTrustedRuntime(filepath.Dir(filepath.Clean(argv[0])))
	if err != nil {
		t.Fatalf("installed service runtime: %v", err)
	}
	dataRoot, err := FixedDataRoot()
	if err != nil {
		t.Fatal(err)
	}
	controller, err := NewControllerWithDataRoot(runtime, dataRoot, filepath.Join(dataRoot, "core-config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Status(context.Background()); err != nil {
		t.Fatalf("controller.Status() with query-only access: %v", err)
	}
}

func TestWaitSCMStateFailsWhenServiceStopsDuringStart(t *testing.T) {
	query := sequenceServiceQuery(
		svc.Status{State: svc.StartPending, ProcessId: 41},
		svc.Status{State: svc.Stopped},
	)
	started := time.Now()
	if _, err := waitSCMStateWithQuery(context.Background(), query, true, time.Second); !errors.Is(err, ErrHostUnavailable) {
		t.Fatalf("start wait error = %v, want ErrHostUnavailable", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("failed start took %s, want prompt failure", elapsed)
	}
}

func TestWaitSCMStateAcceptsRunningAfterStartPending(t *testing.T) {
	query := sequenceServiceQuery(
		svc.Status{State: svc.StartPending, ProcessId: 42},
		svc.Status{State: svc.Running, ProcessId: 42},
	)
	got, err := waitSCMStateWithQuery(context.Background(), query, true, time.Second)
	if err != nil {
		t.Fatalf("wait for running service: %v", err)
	}
	if got.State != StateRunning || got.PID != 42 {
		t.Fatalf("status = %#v, want running PID 42", got)
	}
}

func TestWaitSCMStateBoundsPendingStart(t *testing.T) {
	query := sequenceServiceQuery(svc.Status{State: svc.StartPending, ProcessId: 43})
	started := time.Now()
	if _, err := waitSCMStateWithQuery(context.Background(), query, true, 20*time.Millisecond); !errors.Is(err, ErrHostUnavailable) {
		t.Fatalf("stuck start error = %v, want ErrHostUnavailable", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("stuck start took %s, want bounded return", elapsed)
	}
}

func TestWaitSCMStateWaitsForStoppedService(t *testing.T) {
	query := sequenceServiceQuery(
		svc.Status{State: svc.StopPending, ProcessId: 44},
		svc.Status{State: svc.Stopped},
	)
	got, err := waitSCMStateWithQuery(context.Background(), query, false, time.Second)
	if err != nil {
		t.Fatalf("wait for stopped service: %v", err)
	}
	if got.State != StateStopped {
		t.Fatalf("status = %#v, want stopped", got)
	}
}

func sequenceServiceQuery(statuses ...svc.Status) func() (svc.Status, error) {
	index := 0
	return func() (svc.Status, error) {
		status := statuses[index]
		if index < len(statuses)-1 {
			index++
		}
		return status, nil
	}
}
