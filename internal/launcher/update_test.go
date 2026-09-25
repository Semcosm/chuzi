package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestUpdateInfoJSONUsesUIFieldNames(t *testing.T) {
	data, err := json.Marshal(UpdateInfo{Available: true, Manifest: &ReleaseManifest{Format: ManifestFormat, Channel: ChannelNightly, Version: "nightly-2", Target: "linux-amd64"}, Reason: "update_available"})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); !strings.Contains(got, `"available":true`) || !strings.Contains(got, `"manifest"`) || !strings.Contains(got, `"reason":"update_available"`) {
		t.Fatalf("update JSON = %s", data)
	}
}

func TestManifestUpdateCheckerReportsOnlyValidatedCandidate(t *testing.T) {
	manifest := ReleaseManifest{Format: ManifestFormat, Channel: ChannelNightly, Version: "nightly-2", Target: "linux-amd64"}
	checker := ManifestUpdateChecker{Source: StaticManifestSource{Manifest: manifest}}
	info, err := checker.Check(context.Background(), UpdateRequest{CurrentVersion: "nightly-1", Target: "linux-amd64", Channel: ChannelNightly})
	if err != nil || !info.Available || info.Manifest == nil || info.Reason != "update_available" {
		t.Fatalf("update result = %#v, err=%v", info, err)
	}
	info, err = checker.Check(context.Background(), UpdateRequest{CurrentVersion: "nightly-2", Target: "linux-amd64", Channel: ChannelNightly})
	if err != nil || info.Available || info.Manifest != nil || info.Reason != "up_to_date" {
		t.Fatalf("up-to-date result = %#v, err=%v", info, err)
	}
	if _, err := checker.Check(context.Background(), UpdateRequest{CurrentVersion: "nightly-1", Target: "windows-amd64", Channel: ChannelNightly}); err == nil {
		t.Fatal("target mismatch was accepted")
	}
	if _, err := checker.Check(context.Background(), UpdateRequest{CurrentVersion: "nightly-1", Target: "linux-amd64", Channel: ChannelStable}); err == nil {
		t.Fatal("channel mismatch was accepted")
	}
	older := checker
	older.Source = StaticManifestSource{Manifest: ReleaseManifest{Format: ManifestFormat, Channel: ChannelNightly, Version: "nightly-1", Target: "linux-amd64"}}
	info, err = older.Check(context.Background(), UpdateRequest{CurrentVersion: "nightly-2", Target: "linux-amd64", Channel: ChannelNightly})
	if err != nil || info.Available || info.Reason != "up_to_date" {
		t.Fatalf("older candidate result = %#v, err=%v", info, err)
	}
	if _, err := (ManifestUpdateChecker{}).Check(context.Background(), UpdateRequest{CurrentVersion: "1", Target: "linux-amd64", Channel: ChannelNightly}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("missing source error = %v", err)
	}
}

func TestManifestValidationRejectsDependencyAndPluginAmbiguity(t *testing.T) {
	manifest := ReleaseManifest{Format: ManifestFormat, Channel: ChannelNightly, Version: "1", Target: "linux-amd64", Components: []Component{{ID: "service", Version: "1", Dependencies: []string{"missing"}}}}
	if err := manifest.Validate(); err == nil {
		t.Fatal("unknown component dependency was accepted")
	}
	manifest.Components[0].Dependencies = nil
	manifest.Plugins = []PluginDescriptor{{ID: "demo", Version: "1", API: PluginAPIV1, Installable: true, Archive: "demo.zip"}}
	if err := manifest.Validate(); err == nil {
		t.Fatal("installable plugin without digest was accepted")
	}
}

func TestReleaseIndexRequiresArtifactMetadataToMatchManifest(t *testing.T) {
	commit := strings.Repeat("c", 40)
	manifest := ReleaseManifest{Format: ManifestFormat, Channel: ChannelNightly, Version: "nightly-2", Commit: commit, Target: "linux-amd64", Components: []Component{{ID: "service", Version: "nightly-2", Artifact: "service.tar.gz"}}}
	index := ReleaseIndex{Format: ReleaseIndexFormat, Channel: ChannelNightly, Version: "nightly-2", Commit: commit, Target: "linux-amd64", Manifest: manifest, Artifacts: []ReleaseArtifact{{Component: "service", Target: "linux-amd64", Version: "nightly-2", Path: "other.tar.gz", Size: 1, SHA256: strings.Repeat("a", 64)}}}
	if err := index.Validate(); err == nil {
		t.Fatal("artifact path mismatch was accepted")
	}
	index.Artifacts[0].Path = "service.tar.gz"
	index.Artifacts = append(index.Artifacts, ReleaseArtifact{Component: "other", Target: "linux-amd64", Version: "nightly-2", Path: "service.tar.gz", Size: 1, SHA256: strings.Repeat("b", 64)})
	if err := index.Validate(); err == nil {
		t.Fatal("duplicate artifact path was accepted")
	}
}

func TestReleaseIndexRequiresDeclaredPluginArtifact(t *testing.T) {
	commit := strings.Repeat("d", 40)
	manifest := ReleaseManifest{
		Format: ManifestFormat, Channel: ChannelNightly, Version: "nightly-3", Commit: commit, Target: "linux-amd64",
		Plugins: []PluginDescriptor{{ID: "demo", Version: "1", API: PluginAPIV1, Installable: true, Archive: "demo.zip", SHA256: strings.Repeat("a", 64)}},
	}
	index := ReleaseIndex{Format: ReleaseIndexFormat, Channel: manifest.Channel, Version: manifest.Version, Commit: commit, Target: manifest.Target, Manifest: manifest}
	if err := index.Validate(); err == nil {
		t.Fatal("installable plugin without release artifact was accepted")
	}
	index.Artifacts = []ReleaseArtifact{{Component: "demo", Target: manifest.Target, Version: manifest.Version, Path: "demo.zip", Size: 1, SHA256: strings.Repeat("a", 64)}}
	if err := index.Validate(); err != nil {
		t.Fatalf("declared plugin artifact rejected: %v", err)
	}
	index.Artifacts = append(index.Artifacts, ReleaseArtifact{Component: "unknown", Target: manifest.Target, Version: manifest.Version, Path: "unknown.zip", Size: 1, SHA256: strings.Repeat("b", 64)})
	if err := index.Validate(); err == nil {
		t.Fatal("unknown release artifact was accepted")
	}
}
