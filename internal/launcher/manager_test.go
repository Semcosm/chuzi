package launcher

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func resourceFor(t *testing.T, root, name, contents string) Resource {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte(contents)
	if err := os.WriteFile(filepath.Join(root, name), data, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	return Resource{Path: name, SHA256: hex.EncodeToString(digest[:]), Size: int64(len(data))}
}

func TestFileRepairerStagesAllResourcesBeforeCommit(t *testing.T) {
	source, install := t.TempDir(), t.TempDir()
	first := resourceFor(t, source, "bin/one", "one")
	second := resourceFor(t, source, "bin/two", "two")
	second.SHA256 = strings.Repeat("0", 64)
	manifest := ReleaseManifest{Format: ManifestFormat, Channel: ChannelNightly, Version: "1", Target: "linux-amd64", Components: []Component{{ID: "service", Version: "1", Resources: []Resource{first, second}}}}
	if _, err := (FileRepairer{SourceRoot: source}).Repair(context.Background(), RepairRequest{InstallRoot: install, Manifest: manifest}); err == nil {
		t.Fatal("invalid source was accepted")
	}
	if _, err := os.Stat(filepath.Join(install, "bin", "one")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial repair was committed: %v", err)
	}
}

func TestFilesystemComponentManagerDependenciesAndPersistence(t *testing.T) {
	source, install := t.TempDir(), t.TempDir()
	dep := resourceFor(t, source, "runtime/helper", "helper")
	main := resourceFor(t, source, "service/main", "service")
	manifest := ReleaseManifest{Format: ManifestFormat, Channel: ChannelNightly, Version: "1", Target: "linux-amd64", Components: []Component{
		{ID: "runtime", Version: "1", Required: true, Resources: []Resource{dep}},
		{ID: "service", Version: "1", Required: false, Dependencies: []string{"runtime"}, Resources: []Resource{main}},
	}}
	manager, err := NewFilesystemComponentManager(ManagerOptions{InstallRoot: install, SourceRoot: source, Manifest: manifest})
	if err != nil {
		t.Fatal(err)
	}
	state, err := manager.Install(context.Background(), "service")
	if err != nil || !state.Installed || state.Health != HealthHealthy {
		t.Fatalf("install state = %#v, err=%v", state, err)
	}
	if _, err := os.Stat(filepath.Join(install, "runtime/helper")); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetEnabled(context.Background(), "service", false); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewFilesystemComponentManager(ManagerOptions{InstallRoot: install, SourceRoot: source, Manifest: manifest})
	if err != nil {
		t.Fatal(err)
	}
	states, err := reloaded.List(context.Background())
	if err != nil || len(states) != 2 || states[1].Enabled {
		t.Fatalf("reloaded states = %#v, err=%v", states, err)
	}
	if err := reloaded.Remove(context.Background(), "runtime"); !errors.Is(err, ErrRequired) {
		t.Fatalf("required component removal error = %v", err)
	}
}

func TestFilesystemComponentInstallRollsBackFilesWhenStateCommitFails(t *testing.T) {
	source, install := t.TempDir(), t.TempDir()
	resource := resourceFor(t, source, "service/main", "new")
	manifest := ReleaseManifest{Format: ManifestFormat, Channel: ChannelNightly, Version: "1", Target: "linux-amd64", Components: []Component{{ID: "service", Version: "1", Resources: []Resource{resource}}}}
	manager, err := NewFilesystemComponentManager(ManagerOptions{InstallRoot: install, SourceRoot: source, Manifest: manifest})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(install, "state-block"), 0o700); err != nil {
		t.Fatal(err)
	}
	manager.state = filepath.Join(install, "state-block")
	if _, err := manager.Install(context.Background(), "service"); !errors.Is(err, ErrTransaction) {
		t.Fatalf("state commit error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(install, "service/main")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("resource survived failed transaction: %v", err)
	}
}

func TestFilesystemPluginManagerTrustAndArchiveSafety(t *testing.T) {
	source, install := t.TempDir(), t.TempDir()
	archive := filepath.Join(source, "plugin.zip")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create("plugin/main.js")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = entry.Write([]byte("plugin"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	digest, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(digest)
	manifest := ReleaseManifest{Format: ManifestFormat, Channel: ChannelNightly, Version: "1", Target: "linux-amd64", Plugins: []PluginDescriptor{{ID: "demo", Version: "1", API: PluginAPIV1, Archive: "plugin.zip", SHA256: hex.EncodeToString(hash[:]), SignedBy: "test-key", Installable: true}}}
	manager, err := NewFilesystemPluginManager(ManagerOptions{InstallRoot: install, SourceRoot: source, Manifest: manifest, Trust: PluginTrustPolicy{AllowedSigners: []string{"test-key"}}})
	if err != nil {
		t.Fatal(err)
	}
	state, err := manager.Install(context.Background(), "demo")
	if err != nil || state.Trusted || state.Enabled || state.Health != HealthUntrusted {
		t.Fatalf("install state = %#v, err=%v", state, err)
	}
	if _, err := manager.SetEnabled(context.Background(), "demo", true); !errors.Is(err, ErrNotTrusted) {
		t.Fatalf("untrusted enable error = %v", err)
	}
	if _, err := manager.SetTrusted(context.Background(), "demo", true); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetEnabled(context.Background(), "demo", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(install, "plugins/demo/plugin/main.js")); err != nil {
		t.Fatal(err)
	}
}
