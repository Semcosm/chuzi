//go:build windows

package servicehost

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestSCMAccessMasksKeepDesktopLifecycleLeastPrivilege(t *testing.T) {
	if scmQueryAccess != windows.SC_MANAGER_CONNECT {
		t.Fatalf("SCM query access = %#x, want SC_MANAGER_CONNECT", scmQueryAccess)
	}
	if serviceQueryAccess&(windows.SERVICE_START|windows.SERVICE_STOP|windows.DELETE) != 0 {
		t.Fatalf("service query access includes mutation rights: %#x", serviceQueryAccess)
	}
	if serviceStartAccess != serviceQueryAccess|windows.SERVICE_START {
		t.Fatalf("service start access = %#x, want query|start", serviceStartAccess)
	}
	if serviceStopAccess != serviceQueryAccess|windows.SERVICE_STOP {
		t.Fatalf("service stop access = %#x, want query|stop", serviceStopAccess)
	}
}
