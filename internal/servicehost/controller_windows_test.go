//go:build windows

package servicehost

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
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
