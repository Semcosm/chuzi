package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Semcosm/chuzi/internal/launcher"
)

func TestLauncherSettingsAndLockHelpers(t *testing.T) {
	root := t.TempDir()
	store, err := newSettingsStore(root, "")
	if err != nil {
		t.Fatal(err)
	}
	settings, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings != launcher.DefaultBehaviorSettings() {
		t.Fatalf("default settings = %#v", settings)
	}
	lock, err := acquireMutationLock(context.Background(), root, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireMutationLock(context.Background(), root, ""); err != launcher.ErrLockHeld {
		t.Fatalf("second launcher lock = %v, want ErrLockHeld", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	if got, want := filepath.Base(store.Path), "launcher-settings.json"; got != want {
		t.Fatalf("default settings filename = %q, want %q", got, want)
	}
}

func TestLauncherProgressReporterWritesOnlyToConfiguredWriter(t *testing.T) {
	var output bytes.Buffer
	reporter := stderrProgress{writer: &output}
	reporter.Report(launcher.ProgressEvent{Operation: "repair", Stage: "verify", Item: "service", Completed: 1, Total: 2})
	if got := output.String(); got == "" || !bytes.Contains([]byte(got), []byte("operation=repair")) {
		t.Fatalf("progress output = %q", got)
	}
	if managerCommandNeedsItem("component-list") {
		t.Fatal("component-list unexpectedly requires an item")
	}
	if !managerCommandNeedsItem("plugin-install") {
		t.Fatal("plugin-install did not require an item")
	}
}

func TestReadSettingsInputUsesFileOrStdinMarker(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "settings.json")
	if err := os.WriteFile(path, []byte("{\"update_channel\":\"nightly\"}"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := readSettingsInput(path)
	if err != nil || string(data) != "{\"update_channel\":\"nightly\"}" {
		t.Fatalf("file settings = %q, err=%v", data, err)
	}
	if _, err := readSettingsInput(filepath.Join(root, "missing")); err == nil {
		t.Fatal("missing settings file unexpectedly succeeded")
	}

	previous := os.Stdin
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	os.Stdin = reader
	defer func() { os.Stdin = previous }()
	if _, err := writer.WriteString("{\"update_channel\":\"stable\"}"); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	data, err = readSettingsInput("-")
	if err != nil || string(data) != "{\"update_channel\":\"stable\"}" {
		t.Fatalf("stdin settings = %q, err=%v", data, err)
	}
}

func TestControlCommandRejectsPackagePathsBeforeCoreCall(t *testing.T) {
	root := t.TempDir()
	handled, err := runControlCommand(context.Background(), "environment-install", root, "", "", -1, 0, "idem", "operator", "", "env/v1", "1.0.0", "../package")
	if !handled || err == nil || !bytes.Contains([]byte(err.Error()), []byte("controlled reference")) {
		t.Fatalf("package path result = handled=%v err=%v", handled, err)
	}
}

func TestControlCommandsDoNotRequireReleaseManifest(t *testing.T) {
	for _, command := range []string{
		"core-status", "core-start", "core-stop", "core-call",
		"job-pool-list", "job-pool-apply", "job-pool-delete", "environment-list", "environment-install",
	} {
		if launcherCommandNeedsManifest(command, false) {
			t.Fatalf("%s unexpectedly requires a release manifest", command)
		}
	}
	if !launcherCommandNeedsManifest("show", false) || !launcherCommandNeedsManifest("verify", true) {
		t.Fatal("manifest-backed commands unexpectedly bypass manifest loading")
	}
}
