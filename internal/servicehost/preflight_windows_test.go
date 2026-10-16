//go:build windows

package servicehost

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeriveServiceSID(t *testing.T) {
	sid, err := deriveServiceSID(ServiceName)
	if err != nil {
		t.Fatal(err)
	}
	const want = "S-1-5-80-1936457352-483135088-2315033984-64399253-3431197935"
	if sid.String() != want {
		t.Fatalf("service SID = %s, want %s", sid, want)
	}
}

func TestSupportedWindowsEdition(t *testing.T) {
	tests := []struct {
		name             string
		product          string
		build            string
		displayVersion   string
		installationType string
		want             bool
	}{
		{name: "windows 11 pro 23h2", product: "Windows 11 Pro", build: "22621", displayVersion: "23H2", want: true},
		{name: "stale windows 10 product on windows 11 25h2", product: "Windows 10 Pro for Workstations", build: "26200", displayVersion: "25H2", installationType: "Client", want: true},
		{name: "windows 11 home", product: "Windows 11 Home", build: "22631", displayVersion: "23H2", want: false},
		{name: "windows 11 old release", product: "Windows 11 Enterprise", build: "22000", displayVersion: "21H2", want: false},
		{name: "windows 10 pro", product: "Windows 10 Pro", build: "19045", displayVersion: "22H2", want: false},
		{name: "server desktop experience", product: "Windows Server 2022 Standard", build: "20348", installationType: "Server", want: true},
		{name: "server core", product: "Windows Server 2025 Standard", build: "26100", installationType: "Server Core", want: false},
		{name: "unknown product", product: "Windows 10 Enterprise", build: "19045", displayVersion: "22H2", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := supportedWindowsEdition(test.product, test.build, test.displayVersion, test.installationType); got != test.want {
				t.Fatalf("supportedWindowsEdition() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestFixedConfigPathRejectsUncleanInput(t *testing.T) {
	root := "C:\\ProgramData\\chuzi\\data"
	if !fixedConfigPath(root, root+"\\core-config.json") {
		t.Fatal("fixed config path rejected")
	}
	if fixedConfigPath(root, root+"\\sub\\..\\core-config.json") {
		t.Fatal("unclean fixed config path accepted")
	}
}

func TestVerifyRuntimeSignatureUsesExplicitBuildClassification(t *testing.T) {
	root := t.TempDir()
	runtime, err := NewTrustedRuntime(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{runtime.Core, runtime.Broker, runtime.Agent, runtime.Node, runtime.Shell} {
		if err := os.WriteFile(path, []byte("validation fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manifest := filepath.Join(root, "build-manifest.json")
	if err := os.WriteFile(manifest, []byte(`{"version":"test-1-0123456789ab"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyRuntimeSignature(runtime, false); err != nil {
		t.Fatalf("explicit test payload rejected: %v", err)
	}
	if err := os.WriteFile(manifest, []byte(`{"version":"dev"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyRuntimeSignature(runtime, false); err == nil {
		t.Fatal("development payload accepted without Authenticode")
	}
	if err := VerifyRuntimeSignature(runtime, true); err == nil {
		t.Fatal("production payload without signature policy was accepted")
	}
	if err := os.Remove(runtime.Broker); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte(`{"version":"test-1-0123456789ab"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyRuntimeSignature(runtime, false); err == nil {
		t.Fatal("validation payload with a missing fixed runtime file was accepted")
	}
}
