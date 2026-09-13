package launcher

import (
	"context"
	"errors"
	"testing"
)

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
