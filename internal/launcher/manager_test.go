package launcher

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	adapterpkg "github.com/Semcosm/chuzi/internal/adapter"
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

func TestFileRepairerReportsDeterministicProgress(t *testing.T) {
	source, install := t.TempDir(), t.TempDir()
	resource := resourceFor(t, source, "service/main", "service")
	manifest := ReleaseManifest{Format: ManifestFormat, Channel: ChannelNightly, Version: "1", Target: "linux-amd64", Components: []Component{{ID: "service", Version: "1", Resources: []Resource{resource}}}}
	var events []ProgressEvent
	reporter := ProgressFunc(func(event ProgressEvent) { events = append(events, event) })
	if _, err := (FileRepairer{SourceRoot: source, Progress: reporter}).Repair(context.Background(), RepairRequest{InstallRoot: install, Manifest: manifest}); err != nil {
		t.Fatal(err)
	}
	if len(events) < 3 {
		t.Fatalf("progress events = %#v, want verify/stage/commit", events)
	}
	if events[0].Operation != "repair" || events[0].Stage != "verify" || events[0].Item != resource.Path {
		t.Fatalf("first progress event = %#v", events[0])
	}
	if events[len(events)-1].Stage != "commit" || events[len(events)-1].Completed != 1 {
		t.Fatalf("last progress event = %#v", events[len(events)-1])
	}
}

func TestFileRepairerDoesNotReplaceNonRegularTarget(t *testing.T) {
	source, install := t.TempDir(), t.TempDir()
	resource := resourceFor(t, source, "service/main", "service")
	if err := os.MkdirAll(filepath.Join(install, "service", "main"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := ReleaseManifest{Format: ManifestFormat, Channel: ChannelNightly, Version: "1", Target: "linux-amd64", Components: []Component{{ID: "service", Version: "1", Resources: []Resource{resource}}}}
	if _, err := (FileRepairer{SourceRoot: source}).Repair(context.Background(), RepairRequest{InstallRoot: install, Manifest: manifest}); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("non-regular target error = %v, want ErrInvalidPath", err)
	}
	if info, err := os.Stat(filepath.Join(install, "service", "main")); err != nil || !info.IsDir() {
		t.Fatalf("non-regular target changed: info=%v err=%v", info, err)
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

func TestFilesystemComponentManagerExplicitCoreRemovalAllowsRequiredService(t *testing.T) {
	source, install := t.TempDir(), t.TempDir()
	resource := resourceFor(t, source, "chuzi.exe", "core")
	manifest := ReleaseManifest{Format: ManifestFormat, Channel: ChannelTest, Version: "test-1-0123456789ab", Target: "windows-amd64", Components: []Component{{ID: "service", Version: "test-1-0123456789ab", Required: true, Resources: []Resource{resource}}}}
	manager, err := NewFilesystemComponentManager(ManagerOptions{InstallRoot: install, SourceRoot: source, Manifest: manifest})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(context.Background(), "service"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Remove(context.Background(), "service"); !errors.Is(err, ErrRequired) {
		t.Fatalf("normal required removal error = %v", err)
	}
	privileged, err := NewFilesystemComponentManager(ManagerOptions{
		InstallRoot: install, SourceRoot: source, Manifest: manifest, AllowRequiredRemoval: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := privileged.Remove(context.Background(), "service"); err != nil {
		t.Fatalf("explicit Core removal failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(install, "chuzi.exe")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Core executable remains after removal: %v", err)
	}
}

func TestFilesystemManagerInitializationIsExplicitAndDurable(t *testing.T) {
	root := t.TempDir()
	launcherResource := resourceFor(t, root, "chuzi-launcher", "launcher")
	manifest := ReleaseManifest{Format: ManifestFormat, Channel: ChannelNightly, Version: "1", Target: "linux-amd64", Components: []Component{
		{ID: "launcher", Version: "1", Required: true, Resources: []Resource{launcherResource}},
		{ID: "service", Version: "1"},
	}}
	manager, err := NewFilesystemComponentManager(ManagerOptions{InstallRoot: root, Manifest: manifest})
	if err != nil {
		t.Fatal(err)
	}
	status, err := manager.Initialize(context.Background())
	if err != nil || !status.FirstRun || status.NextAction != "select_components" {
		t.Fatalf("initialization status = %#v err=%v", status, err)
	}
	if len(status.Required) != 1 || status.Required[0] != "launcher" || len(status.Optional) != 1 || status.Optional[0] != "service" {
		t.Fatalf("component choices = %#v", status)
	}
	if !status.Components[0].Installed {
		t.Fatalf("existing launcher was not recognized: %#v", status.Components)
	}
	if err := manager.CompleteInitialization(context.Background()); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewFilesystemComponentManager(ManagerOptions{InstallRoot: root, Manifest: manifest})
	if err != nil {
		t.Fatal(err)
	}
	status, err = reloaded.Initialize(context.Background())
	if err != nil || status.FirstRun || status.NextAction != "manage_components" {
		t.Fatalf("completed initialization status = %#v err=%v", status, err)
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

func TestFilesystemManagerRejectsTrailingStateJSON(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, ".chuzi", "launcher-state.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte(`{"components":{},"plugins":{}} {}`), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := ReleaseManifest{Format: ManifestFormat, Channel: ChannelNightly, Version: "1", Target: "linux-amd64"}
	if _, err := NewFilesystemManager(ManagerOptions{InstallRoot: root, Manifest: manifest}); err == nil {
		t.Fatal("launcher state accepted trailing JSON")
	}
}

func TestCopyWithContextStopsBetweenReads(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader := &cancelAfterRead{cancel: cancel}
	var destination strings.Builder
	if _, err := copyWithContext(ctx, &destination, reader); !errors.Is(err, context.Canceled) {
		t.Fatalf("copy error = %v, want context.Canceled", err)
	}
}

type cancelAfterRead struct {
	cancel context.CancelFunc
	reads  int
}

func (r *cancelAfterRead) Read(buffer []byte) (int, error) {
	r.reads++
	if r.reads == 1 {
		buffer[0] = 'x'
		r.cancel()
		return 1, nil
	}
	return 0, io.EOF
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

func writeAdapterArchive(t *testing.T, path, id, version, source string) string {
	return writeAdapterArchiveWithSigner(t, path, id, version, source, "test-key")
}

func writeAdapterArchiveWithSigner(t *testing.T, path, id, version, source, signer string) string {
	t.Helper()
	content := []byte(source)
	digest := sha256.Sum256(content)
	manifest := adapterpkg.Manifest{
		Format: adapterpkg.ManifestFormat, ID: id, API: adapterpkg.AdapterAPI, Version: version,
		Entry: "adapter.mjs", Capabilities: []string{"genshin-cloudgame@1"},
		Permissions: []string{"browser.cdp.loopback"}, Targets: []string{"linux-amd64"}, SignedBy: signer,
		Resources: []adapterpkg.Resource{{Path: "adapter.mjs", SHA256: hex.EncodeToString(digest[:]), Size: int64(len(content))}},
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for name, data := range map[string][]byte{"adapter.mjs": content, "adapter-manifest.json": manifestData} {
		entry, createErr := writer.Create(name)
		if createErr != nil {
			_ = file.Close()
			t.Fatal(createErr)
		}
		if _, writeErr := entry.Write(data); writeErr != nil {
			_ = file.Close()
			t.Fatal(writeErr)
		}
	}
	if err := writer.Close(); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func TestAdapterPackageRejectsManifestSignerMismatch(t *testing.T) {
	source, install := t.TempDir(), t.TempDir()
	archive := filepath.Join(source, "genshin.zip")
	digest := writeAdapterArchiveWithSigner(t, archive, "genshin-cloudgame", "1.0.0", "package", "other-key")
	manager, err := NewFilesystemPluginManager(ManagerOptions{
		InstallRoot: install, SourceRoot: source, Manifest: adapterReleaseManifest("genshin.zip", digest, "1.0.0"),
		Trust: PluginTrustPolicy{AllowedSigners: []string{"test-key"}, RequireSigned: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(context.Background(), "genshin-cloudgame"); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("signer mismatch install = %v, want ErrInvalidManifest", err)
	}
	if _, err := os.Stat(filepath.Join(install, "plugins", "genshin-cloudgame")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("signer mismatch left an installed package: %v", err)
	}
}

func adapterReleaseManifest(archive, digest, version string) ReleaseManifest {
	return ReleaseManifest{
		Format: ManifestFormat, Channel: ChannelNightly, Version: version, Target: "linux-amd64",
		Plugins: []PluginDescriptor{{
			ID: "genshin-cloudgame", Version: version, API: AdapterAPIV1, Entry: "adapter.mjs",
			Distribution: PluginDistributionPackage, Archive: archive, SHA256: digest,
			Capabilities: []string{"genshin-cloudgame@1"}, Permissions: []string{"browser.cdp.loopback"},
			SignedBy: "test-key", Installable: true,
		}},
	}
}

func TestAdapterPackageLifecycleUpgradeClearsTrustAndPreservesFailedUpdate(t *testing.T) {
	source, install := t.TempDir(), t.TempDir()
	archive := filepath.Join(source, "genshin.zip")
	digest := writeAdapterArchive(t, archive, "genshin-cloudgame", "1.0.0", "version-one")
	manager, err := NewFilesystemPluginManager(ManagerOptions{
		InstallRoot: install, SourceRoot: source, Manifest: adapterReleaseManifest("genshin.zip", digest, "1.0.0"),
		Trust: PluginTrustPolicy{AllowedSigners: []string{"test-key"}, RequireSigned: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := manager.Install(context.Background(), "genshin-cloudgame")
	if err != nil || !state.Installed || !state.Verified || state.Trusted || state.Enabled || state.Running {
		t.Fatalf("initial adapter state = %#v, err=%v", state, err)
	}
	if _, err := manager.SetEnabled(context.Background(), "genshin-cloudgame", true); !errors.Is(err, ErrNotTrusted) {
		t.Fatalf("enable before trust = %v, want ErrNotTrusted", err)
	}
	if state, err = manager.SetTrusted(context.Background(), "genshin-cloudgame", true); err != nil || !state.Trusted || state.Enabled {
		t.Fatalf("trusted state = %#v, err=%v", state, err)
	}
	if state, err = manager.SetEnabled(context.Background(), "genshin-cloudgame", true); err != nil || !state.Enabled || !state.Running {
		t.Fatalf("enabled state = %#v, err=%v", state, err)
	}

	digest = writeAdapterArchive(t, archive, "genshin-cloudgame", "2.0.0", "version-two")
	updated, err := NewFilesystemPluginManager(ManagerOptions{
		InstallRoot: install, SourceRoot: source, Manifest: adapterReleaseManifest("genshin.zip", digest, "2.0.0"),
		Trust: PluginTrustPolicy{AllowedSigners: []string{"test-key"}, RequireSigned: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err = updated.Update(context.Background(), "genshin-cloudgame")
	if err != nil || !state.Verified || state.Trusted || state.Enabled || state.Running || state.Descriptor.Version != "2.0.0" {
		t.Fatalf("upgraded adapter state = %#v, err=%v", state, err)
	}
	installedData, err := os.ReadFile(filepath.Join(install, "plugins", "genshin-cloudgame", "adapter.mjs"))
	if err != nil || string(installedData) != "version-two" {
		t.Fatalf("installed upgraded package = %q, err=%v", installedData, err)
	}

	if _, err := updated.SetTrusted(context.Background(), "genshin-cloudgame", true); err != nil {
		t.Fatal(err)
	}
	if _, err := updated.SetEnabled(context.Background(), "genshin-cloudgame", true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archive, []byte("corrupt archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := updated.Update(context.Background(), "genshin-cloudgame"); err == nil {
		t.Fatal("corrupt adapter update was accepted")
	}
	installedData, err = os.ReadFile(filepath.Join(install, "plugins", "genshin-cloudgame", "adapter.mjs"))
	if err != nil || string(installedData) != "version-two" {
		t.Fatalf("failed update replaced old package = %q, err=%v", installedData, err)
	}
	states, err := updated.List(context.Background())
	if err != nil || len(states) != 1 || !states[0].Installed || states[0].Descriptor.Version != "2.0.0" {
		t.Fatalf("failed update state = %#v, err=%v", states, err)
	}
	if err := updated.Remove(context.Background(), "genshin-cloudgame"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(install, "plugins", "genshin-cloudgame")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed adapter directory remains: %v", err)
	}
}

func TestBuiltinAdapterFollowsSourceComponent(t *testing.T) {
	root := t.TempDir()
	manager, err := NewFilesystemManager(ManagerOptions{
		InstallRoot: root,
		Manifest: ReleaseManifest{
			Format: ManifestFormat, Channel: ChannelNightly, Version: "1", Target: "linux-amd64",
			Components: []Component{{ID: "browser-worker", Version: "1"}},
			Plugins:    []PluginDescriptor{{ID: "genshin", Version: "1", API: PluginAPIV1, Distribution: PluginDistributionBuiltin, SourceComponent: "browser-worker", Capabilities: []string{"genshin-cloudgame@1"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	states, err := manager.ListPlugins(context.Background())
	if err != nil || len(states) != 1 || states[0].Health != HealthUnavailable || states[0].Installed {
		t.Fatalf("missing source state = %#v, err=%v", states, err)
	}
	manager.data.Components["browser-worker"] = componentRecord{Installed: true, Enabled: true}
	states, err = manager.ListPlugins(context.Background())
	if err != nil || len(states) != 1 || states[0].Health != HealthIncluded || !states[0].Installed || !states[0].Trusted {
		t.Fatalf("included source state = %#v, err=%v", states, err)
	}
	if _, err := manager.InstallPlugin(context.Background(), "genshin"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("builtin install error = %v", err)
	}
	if err := manager.RemovePlugin(context.Background(), "genshin"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("builtin remove error = %v", err)
	}
}

func TestArchiveExtractionRejectsTraversalLinksDuplicatesAndOversize(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "unsafe.zip")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create("../escape")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = entry.Write([]byte("escape"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := extractArchive(context.Background(), archive, t.TempDir()); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("traversal archive error = %v", err)
	}
	large := filepath.Join(t.TempDir(), "large.zip")
	file, err = os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	writer = zip.NewWriter(file)
	entry, err = writer.Create("payload")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = entry.Write([]byte("0123456789"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := extractArchiveWithLimits(context.Background(), large, t.TempDir(), ArchiveLimits{MaxEntries: 2, MaxBytes: 4}); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("oversize archive error = %v", err)
	}
}
