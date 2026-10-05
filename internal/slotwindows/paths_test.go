package slotwindows

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDerivePathsIsSlotAndGenerationScoped(t *testing.T) {
	options := Options{DataDir: filepath.Join(t.TempDir(), "state"), UserPrefix: "ChuziJob", EnvironmentID: "env/v1", Version: "1.0.0"}
	first, err := options.DerivePaths("pool-001", 1, 4)
	if err != nil {
		t.Fatal(err)
	}
	second, err := options.DerivePaths("pool-002", 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if first.UserName != "ChuziJob0001" || first.Desktop == second.Desktop || first.Desktop == "ChuziSlot0001" || first.Work == second.Work || first.Work != filepath.Join(first.Generation, "work") {
		t.Fatalf("unexpected paths: %#v %#v", first, second)
	}
	nextGeneration, _ := options.DerivePaths("pool-001", 1, 5)
	if first.Generation == nextGeneration.Generation {
		t.Fatal("generation path did not change")
	}
}

func TestDerivePathsRejectsCallerPathShapedIDs(t *testing.T) {
	options := Options{DataDir: filepath.Join(t.TempDir(), "state"), UserPrefix: "ChuziJob", EnvironmentID: "env/v1", Version: "1.0.0"}
	for _, id := range []string{"../outside", `pool\001`, `C:\outside`, "..", "/tmp"} {
		if _, err := options.DerivePaths(id, 1, 1); err == nil {
			t.Errorf("accepted slot path %q", id)
		}
	}
	options.UserPrefix = "bad-prefix;"
	if _, err := options.DerivePaths("pool-001", 1, 1); err == nil || !strings.Contains(err.Error(), "slotwindows") {
		t.Fatal("accepted unsafe username prefix")
	}
}

func TestOptionsValidateBrowserRuntimeConfiguration(t *testing.T) {
	base := Options{DataDir: filepath.Join(t.TempDir(), "state"), UserPrefix: "ChuziJob", EnvironmentID: "env/v1", Version: "1.0.0"}
	for _, test := range []struct {
		name string
		mode string
		cmd  string
		want bool
	}{
		{name: "headless", mode: "headless", cmd: "chromium", want: true},
		{name: "headed", mode: "headed", cmd: "C:\\\\Program Files\\\\Chromium\\\\chrome.exe", want: true},
		{name: "unknown mode", mode: "windowed", cmd: "chromium"},
		{name: "missing command", mode: "headless"},
		{name: "command without mode", cmd: "chromium"},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := base
			options.BrowserMode, options.BrowserCommand = test.mode, test.cmd
			if got := options.Validate() == nil; got != test.want {
				t.Fatalf("Validate() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestOptionsValidateRejectsUnsafeDataDirectory(t *testing.T) {
	options := Options{DataDir: filepath.Join(t.TempDir(), "state\n"), UserPrefix: "ChuziJob", EnvironmentID: "env/v1", Version: "1.0.0"}
	if err := options.Validate(); err == nil {
		t.Fatal("unsafe data directory accepted")
	}
}
