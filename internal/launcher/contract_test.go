package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testManifest(t *testing.T, root string) ReleaseManifest {
	t.Helper()
	data := []byte("chuzi")
	if err := os.WriteFile(filepath.Join(root, "chuzi"), data, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	return ReleaseManifest{
		Format: ManifestFormat, Channel: ChannelNightly, Version: "nightly-1", Commit: "abc", Target: "linux-amd64",
		Components: []Component{{ID: "service", Version: "nightly-1", Required: true, Resources: []Resource{{Path: "chuzi", SHA256: hex.EncodeToString(digest[:]), Size: int64(len(data))}}}},
	}
}

func TestManifestValidationAndFileVerification(t *testing.T) {
	root := t.TempDir()
	manifest := testManifest(t, root)
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}
	result, err := (FileVerifier{}).Verify(context.Background(), root, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid || len(result.Issues) != 0 {
		t.Fatalf("unexpected verification result: %#v", result)
	}
	if err := os.WriteFile(filepath.Join(root, "chuzi"), []byte("changed"), 0o700); err != nil {
		t.Fatal(err)
	}
	result, err = (FileVerifier{}).Verify(context.Background(), root, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if result.Valid || len(result.Issues) != 1 || result.Issues[0].Kind != "size_mismatch" {
		t.Fatalf("expected deterministic mismatch: %#v", result)
	}
}

func TestManifestRejectsUnsafePathsAndDuplicateEntries(t *testing.T) {
	base := ReleaseManifest{Format: ManifestFormat, Channel: ChannelNightly, Version: "nightly-1", Target: "linux-amd64"}
	base.Components = []Component{{ID: "service", Version: "nightly-1", Resources: []Resource{{Path: "../secret", SHA256: "0000000000000000000000000000000000000000000000000000000000000000"}}}}
	if err := base.Validate(); err == nil {
		t.Fatal("unsafe resource path was accepted")
	}
	base.Components[0].Resources[0].Path = "chuzi"
	base.Components = append(base.Components, Component{ID: "service", Version: "nightly-1"})
	if err := base.Validate(); err == nil {
		t.Fatal("duplicate component was accepted")
	}
}

func TestBehaviorSettingsValidation(t *testing.T) {
	if err := (BehaviorSettings{UpdateChannel: ChannelNightly}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (BehaviorSettings{UpdateChannel: "preview"}).Validate(); err == nil {
		t.Fatal("unknown channel was accepted")
	}
}

func TestManifestValidatesPluginCapabilities(t *testing.T) {
	manifest := ReleaseManifest{
		Format: ManifestFormat, Channel: ChannelNightly, Version: "nightly-1", Target: "windows-amd64",
		Components: []Component{{ID: "browser-worker", Version: "nightly-1"}},
		Plugins: []PluginDescriptor{{
			ID: "bettergi", Version: "0.1.0", API: PluginAPIV1,
			Capabilities: []string{"bettergi.session.v1"}, Installable: false, SourceComponent: "browser-worker",
		}},
	}
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}
	manifest.Plugins[0].SourceComponent = ""
	if err := manifest.Validate(); err == nil {
		t.Fatal("builtin adapter without a source component was accepted")
	}
	manifest.Plugins[0].SourceComponent = "browser-worker"
	manifest.Plugins[0].Capabilities = []string{"bettergi.session.v1", "bettergi.session.v1"}
	if err := manifest.Validate(); err == nil {
		t.Fatal("duplicate plugin capability was accepted")
	}
}

func TestManifestValidatesAdapterDistribution(t *testing.T) {
	manifest := ReleaseManifest{
		Format: ManifestFormat, Channel: ChannelNightly, Version: "nightly-1", Target: "linux-amd64",
		Components: []Component{{ID: "browser-worker", Version: "nightly-1"}},
		Plugins: []PluginDescriptor{{
			ID: "genshin", Version: "nightly-1", API: AdapterAPIV1,
			Distribution: PluginDistributionBuiltin, SourceComponent: "browser-worker",
			Capabilities: []string{"genshin-cloudgame@1"},
		}},
	}
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}
	manifest.Plugins[0].Archive = "genshin.tar.gz"
	if err := manifest.Validate(); err == nil {
		t.Fatal("builtin adapter archive was accepted")
	}
	manifest.Plugins[0].Distribution = PluginDistributionPackage
	manifest.Plugins[0].Installable = true
	manifest.Plugins[0].Entry = "adapter.mjs"
	manifest.Plugins[0].SHA256 = strings.Repeat("a", 64)
	manifest.Plugins[0].SignedBy = "release-key"
	if err := manifest.Validate(); err != nil {
		t.Fatalf("package adapter validation = %v", err)
	}
	manifest.Plugins[0].SignedBy = ""
	if err := manifest.Validate(); err == nil {
		t.Fatal("package adapter without signer was accepted")
	}
}

func TestReleaseIndexExcludesBuiltinAdapterArtifacts(t *testing.T) {
	commit := strings.Repeat("e", 40)
	manifest := ReleaseManifest{
		Format: ManifestFormat, Channel: ChannelNightly, Version: "nightly-4", Commit: commit, Target: "linux-amd64",
		Components: []Component{{ID: "browser-worker", Version: "nightly-4", Artifact: "browser-worker.tar.gz"}},
		Plugins: []PluginDescriptor{{
			ID: "genshin", Version: "nightly-4", API: AdapterAPIV1,
			Distribution: PluginDistributionBuiltin, SourceComponent: "browser-worker",
		}},
	}
	index := ReleaseIndex{
		Format: ReleaseIndexFormat, Channel: manifest.Channel, Version: manifest.Version,
		Commit: commit, Target: manifest.Target, Manifest: manifest,
		Artifacts: []ReleaseArtifact{{
			Component: "browser-worker", Target: manifest.Target, Version: manifest.Version,
			Path: "browser-worker.tar.gz", Size: 1, SHA256: strings.Repeat("a", 64),
		}},
	}
	if err := index.Validate(); err != nil {
		t.Fatalf("builtin adapter index validation = %v", err)
	}
	index.Artifacts = append(index.Artifacts, ReleaseArtifact{
		Component: "genshin", Target: manifest.Target, Version: manifest.Version,
		Path: "genshin.tar.gz", Size: 1, SHA256: strings.Repeat("b", 64),
	})
	if err := index.Validate(); err == nil {
		t.Fatal("builtin adapter artifact was accepted")
	}
}
