package environment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRuntimeHandoffResolvesClosedEntrypointsAndPreservesPackageGeneration(t *testing.T) {
	root := t.TempDir()
	files := map[string][]byte{
		"worker.mjs":   []byte("worker"),
		"headless.mjs": []byte("headless"),
		"adapter.mjs":  []byte("adapter"),
	}
	resources := make([]Resource, 0, len(files))
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(content)
		resources = append(resources, Resource{Path: name, SHA256: hex.EncodeToString(digest[:]), Size: int64(len(content))})
	}
	manifest := Manifest{API: API, EnvironmentID: "env-windows", Version: "1.0.0", Targets: []string{"windows-amd64"}, Capabilities: []string{"browser"}, Resources: resources, Entrypoints: []Entrypoint{{Name: WorkerEntrypointName, Path: "worker.mjs", Runtime: "browser-worker"}, {Name: HeadlessEntrypointName, Path: "headless.mjs", Runtime: "browser-worker"}, {Name: AdapterBridgeEntrypointName, Path: "adapter.mjs", Runtime: "adapter-bridge"}}, Signer: "test-signer"}
	digest, err := manifest.ComputeDigest()
	if err != nil {
		t.Fatal(err)
	}
	manifest.ManifestDigest = digest
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ManifestName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	pkg := Package{Root: root, Manifest: manifest, Record: Record{EnvironmentID: manifest.EnvironmentID, Version: manifest.Version, ManifestDigest: digest, Signer: manifest.Signer, Installed: true, Verified: true, Trusted: true, Enabled: true, Healthy: true, Ready: true, Generation: 9, UpdatedAt: time.Now().UTC()}}
	handoff, err := NewRuntimeHandoff(pkg, HeadlessEntrypointName)
	if err != nil {
		t.Fatal(err)
	}
	if !handoff.Valid() || handoff.WorkerName() != HeadlessEntrypointName || handoff.AdapterBridgeName() != AdapterBridgeEntrypointName || handoff.PackageGeneration() != 9 || handoff.EnvironmentID() != manifest.EnvironmentID || handoff.WorkerPath() != filepath.Join(root, "headless.mjs") || handoff.AdapterBridgePath() != filepath.Join(root, "adapter.mjs") {
		t.Fatalf("unexpected handoff: %#v", handoff)
	}
	if !handoff.Matches(manifest.EnvironmentID, manifest.Version, manifest.ManifestDigest, manifest.Signer) {
		t.Fatal("handoff should match its signed package metadata")
	}
	if handoff.Matches(manifest.EnvironmentID, manifest.Version, strings.Repeat("0", len(manifest.ManifestDigest)), manifest.Signer) {
		t.Fatal("handoff matched a different manifest digest")
	}
	if handoff.Matches(manifest.EnvironmentID, manifest.Version, manifest.ManifestDigest, "other-signer") {
		t.Fatal("handoff matched a different signer")
	}
}

func TestRuntimeHandoffRejectsUntrustedOrArbitraryEntrypoints(t *testing.T) {
	base := Package{Root: filepath.Join(t.TempDir(), "missing"), Manifest: Manifest{EnvironmentID: "env", Version: "1", Signer: "signer"}, Record: Record{Generation: 2}}
	if _, err := NewRuntimeHandoff(base, "arbitrary"); !errors.Is(err, ErrInvalidHandoff) {
		t.Fatalf("arbitrary worker name error = %v", err)
	}
	if _, err := NewRuntimeHandoff(base, WorkerEntrypointName); !errors.Is(err, ErrInvalidHandoff) {
		t.Fatalf("unready package error = %v", err)
	}
}
