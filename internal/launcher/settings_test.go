package launcher

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestFileSettingsStoreDefaultsRoundTripAndAtomicPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".chuzi", "settings.json")
	defaults := DefaultBehaviorSettings()
	store, err := NewFileSettingsStore(path, defaults)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != defaults {
		t.Fatalf("missing settings = %#v, want %#v", got, defaults)
	}
	want := BehaviorSettings{
		AutoCheckUpdates: true,
		AutoRepair:       true,
		UpdateChannel:    ChannelStable,
		LaunchOnLogin:    true,
		CloseToTray:      true,
		CheckInterval:    15 * time.Minute,
	}
	if err := store.Save(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err = store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("round trip settings = %#v, want %#v", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("settings permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestFileSettingsStoreRejectsTrailingAndUnknownData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	store, err := NewFileSettingsStore(path, DefaultBehaviorSettings())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"update_channel":"nightly","unexpected":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background()); err == nil {
		t.Fatal("unknown settings field was accepted")
	}
	if err := os.WriteFile(path, []byte(`{"update_channel":"nightly"} {}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background()); err == nil {
		t.Fatal("trailing settings JSON was accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Load(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled load = %v, want context.Canceled", err)
	}
}
