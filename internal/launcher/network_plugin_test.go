package launcher

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pluginArchive(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	entry, err := writer.Create("adapter/main.js")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("adapter")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestNetworkPluginManagerDownloadsAndKeepsPluginUntrusted(t *testing.T) {
	payload := pluginArchive(t)
	digest := sha256.Sum256(payload)
	commit := strings.Repeat("e", 40)
	manifest := ReleaseManifest{
		Format: ManifestFormat, Channel: ChannelNightly, Version: "nightly-4", Commit: commit, Target: "linux-amd64",
		Plugins: []PluginDescriptor{{ID: "demo", Version: "1", API: PluginAPIV1, Archive: "demo.zip", SHA256: hex.EncodeToString(digest[:]), SignedBy: "test-key", Installable: true}},
	}
	artifact := ReleaseArtifact{Component: "demo", Target: manifest.Target, Version: manifest.Version, Path: "demo.zip", Size: int64(len(payload)), SHA256: hex.EncodeToString(digest[:])}
	index := ReleaseIndex{Format: ReleaseIndexFormat, Channel: manifest.Channel, Version: manifest.Version, Commit: commit, Target: manifest.Target, Manifest: manifest, Artifacts: []ReleaseArtifact{artifact}}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/demo.zip" {
			http.NotFound(writer, request)
			return
		}
		_, _ = writer.Write(payload)
	}))
	defer server.Close()

	install := t.TempDir()
	manager, err := NewNetworkPluginManager(ManagerOptions{InstallRoot: install, Trust: PluginTrustPolicy{AllowedSigners: []string{"test-key"}}}, index, server.URL+"/index.json", filepath.Join(install, "downloads"), ArtifactDownloader{AllowHTTPForLoopback: true, RetryDelay: 0})
	if err != nil {
		t.Fatal(err)
	}
	state, err := manager.Install(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if !state.Installed || state.Trusted || state.Enabled || state.Health != HealthUntrusted {
		t.Fatalf("unexpected post-install state: %#v", state)
	}
	if _, err := os.Stat(filepath.Join(install, "plugins", "demo", "adapter", "main.js")); err != nil {
		t.Fatalf("installed plugin payload missing: %v", err)
	}
	if _, err := manager.SetEnabled(context.Background(), "demo", true); err == nil {
		t.Fatal("untrusted plugin was enabled")
	}
	if _, err := manager.SetTrusted(context.Background(), "demo", true); err != nil {
		t.Fatal(err)
	}
	state, err = manager.SetEnabled(context.Background(), "demo", true)
	if err != nil || !state.Trusted || !state.Enabled || state.Health != HealthHealthy {
		t.Fatalf("unexpected trusted enabled state: %#v err=%v", state, err)
	}
}
