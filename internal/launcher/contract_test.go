package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
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
