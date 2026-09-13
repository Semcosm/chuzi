package main

import (
	"bytes"
	"context"
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
