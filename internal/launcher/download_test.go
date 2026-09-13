package launcher

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func releaseIndexForTest(t *testing.T, version string, artifacts []ReleaseArtifact) ReleaseIndex {
	t.Helper()
	manifest := ReleaseManifest{
		Format: ManifestFormat, Channel: ChannelNightly, Version: version,
		Commit: strings.Repeat("a", 40), Target: "linux-amd64",
		Components: []Component{{ID: "service", Version: version, Artifact: "service.tar.gz"}},
	}
	return ReleaseIndex{Format: ReleaseIndexFormat, Channel: ChannelNightly, Version: version,
		Commit: manifest.Commit, Target: manifest.Target, Manifest: manifest, Artifacts: artifacts}
}

func artifactForBytes(path, component string, data []byte) ReleaseArtifact {
	digest := sha256.Sum256(data)
	return ReleaseArtifact{Component: component, Target: "linux-amd64", Version: "nightly-2", Path: path, Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:])}
}

func TestHTTPReleaseIndexSourceRetriesServerErrorsAndRejectsTrailingJSON(t *testing.T) {
	index := releaseIndexForTest(t, "nightly-2", []ReleaseArtifact{{Component: "service", Target: "linux-amd64", Version: "nightly-2", Path: "service.tar.gz", Size: 1, SHA256: strings.Repeat("b", 64)}})
	data, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if calls.Add(1) == 1 {
			writer.WriteHeader(http.StatusBadGateway)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(data)
	}))
	defer server.Close()
	source := HTTPReleaseIndexSource{URL: server.URL + "/index.json", AllowHTTPForLoopback: true, MaxAttempts: 2, RetryDelay: 0}
	got, err := source.FetchIndex(context.Background())
	if err != nil || got.Version != index.Version || calls.Load() != 2 {
		t.Fatalf("index = %#v err=%v calls=%d", got, err, calls.Load())
	}
	trailing := server.URL + "/trailing"
	server.Config.Handler = http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write(append(data, []byte("{}")...))
	})
	source.URL = trailing
	if _, err := source.FetchIndex(context.Background()); err == nil {
		t.Fatal("trailing release index JSON was accepted")
	}
}

func TestHTTPReleaseIndexSourceRequiresHTTPSExceptExplicitLoopback(t *testing.T) {
	source := HTTPReleaseIndexSource{URL: "http://example.invalid/index.json"}
	if _, err := source.FetchIndex(context.Background()); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("insecure non-loopback error = %v", err)
	}
	if _, err := (HTTPReleaseIndexSource{URL: "https://user:secret@example.invalid/index.json"}).FetchIndex(context.Background()); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("URL userinfo error = %v", err)
	}
}

func TestDownloadSourcesRejectRedirectsAcrossOriginsAndSchemes(t *testing.T) {
	other := httptest.NewServer(http.NotFoundHandler())
	defer other.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, other.URL+"/redirected", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	if _, err := (HTTPReleaseIndexSource{
		URL:                  redirect.URL + "/index.json",
		AllowHTTPForLoopback: true,
		RetryDelay:           0,
	}).FetchIndex(context.Background()); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("cross-origin index redirect error = %v", err)
	}

	payload := []byte("redirected artifact")
	artifact := artifactForBytes("service.tar.gz", "service", payload)
	if _, err := (ArtifactDownloader{AllowHTTPForLoopback: true, RetryDelay: 0}).Download(
		context.Background(), redirect.URL+"/index.json", artifact, t.TempDir(),
	); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("cross-origin artifact redirect error = %v", err)
	}

	secure := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, "http://127.0.0.1:1/redirected", http.StatusTemporaryRedirect)
	}))
	defer secure.Close()
	secure.Client().CheckRedirect = nil
	secureSource := HTTPReleaseIndexSource{
		URL:                  secure.URL + "/index.json",
		Client:               secure.Client(),
		AllowHTTPForLoopback: true,
		RetryDelay:           0,
	}
	if _, err := secureSource.FetchIndex(context.Background()); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("scheme downgrade redirect error = %v", err)
	}
}

func TestArtifactDownloaderVerifiesSizeDigestAndCleansPartialFiles(t *testing.T) {
	payload := []byte("component archive bytes")
	digest := sha256.Sum256(payload)
	artifact := ReleaseArtifact{Component: "service", Target: "linux-amd64", Version: "nightly-2", Path: "service.tar.gz", Size: int64(len(payload)), SHA256: hex.EncodeToString(digest[:])}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write(payload)
	}))
	defer server.Close()
	destination := t.TempDir()
	downloader := ArtifactDownloader{AllowHTTPForLoopback: true, RetryDelay: 0}
	path, err := downloader.Download(context.Background(), server.URL+"/index.json", artifact, destination)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != string(payload) {
		t.Fatalf("downloaded data = %q err=%v", data, err)
	}
	bad := artifact
	bad.SHA256 = strings.Repeat("0", 64)
	if _, err := downloader.Download(context.Background(), server.URL+"/index.json", bad, destination); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("digest mismatch error = %v", err)
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".chuzi-artifact-") {
			t.Fatalf("partial artifact survived: %s", entry.Name())
		}
	}
	limited := downloader
	limited.MaxBytes = int64(len(payload) - 1)
	if _, err := limited.Download(context.Background(), server.URL+"/index.json", artifact, t.TempDir()); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("size limit error = %v", err)
	}
}

func TestArtifactDownloaderRejectsCrossOriginAndHonorsCancellation(t *testing.T) {
	payload := []byte("payload")
	artifact := artifactForBytes("service.tar.gz", "service", payload)
	artifact.Version = "nightly-2"
	other := httptest.NewServer(http.NotFoundHandler())
	defer other.Close()
	artifact.URL = other.URL + "/service.tar.gz"
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	if _, err := (ArtifactDownloader{AllowHTTPForLoopback: true}).Download(context.Background(), server.URL+"/index.json", artifact, t.TempDir()); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("cross-origin error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (ArtifactDownloader{AllowHTTPForLoopback: true}).Download(ctx, server.URL+"/index.json", artifact, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled download error = %v", err)
	}
}

func TestNetworkComponentManagerDownloadsDependenciesOnce(t *testing.T) {
	runtimeArchive := tarGzBytes(t, map[string]string{"runtime/helper": "helper"})
	serviceArchive := tarGzBytes(t, map[string]string{"service/main": "service"})
	runtimeArtifact := artifactForBytes("runtime.tar.gz", "runtime", runtimeArchive)
	serviceArtifact := artifactForBytes("service.tar.gz", "service", serviceArchive)
	runtimeArtifact.Version, serviceArtifact.Version = "nightly-2", "nightly-2"
	runtimeData := []byte("helper")
	serviceData := []byte("service")
	runtimeHash := sha256.Sum256(runtimeData)
	serviceHash := sha256.Sum256(serviceData)
	manifest := ReleaseManifest{Format: ManifestFormat, Channel: ChannelNightly, Version: "nightly-2", Commit: strings.Repeat("c", 40), Target: "linux-amd64", Components: []Component{
		{ID: "runtime", Version: "nightly-2", Required: true, Artifact: "runtime.tar.gz", Resources: []Resource{{Path: "runtime/helper", SHA256: hex.EncodeToString(runtimeHash[:]), Size: int64(len(runtimeData))}}},
		{ID: "service", Version: "nightly-2", Artifact: "service.tar.gz", Dependencies: []string{"runtime"}, Resources: []Resource{{Path: "service/main", SHA256: hex.EncodeToString(serviceHash[:]), Size: int64(len(serviceData))}}},
	}}
	index := ReleaseIndex{Format: ReleaseIndexFormat, Channel: manifest.Channel, Version: manifest.Version, Commit: manifest.Commit, Target: manifest.Target, Manifest: manifest, Artifacts: []ReleaseArtifact{runtimeArtifact, serviceArtifact}}
	if err := index.Validate(); err != nil {
		t.Fatal(err)
	}
	var counts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/runtime.tar.gz":
			counts.Add(1)
			_, _ = writer.Write(runtimeArchive)
		case "/service.tar.gz":
			counts.Add(1)
			_, _ = writer.Write(serviceArchive)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	install := t.TempDir()
	manager, err := NewNetworkComponentManager(ManagerOptions{InstallRoot: install, Manifest: manifest}, index, server.URL+"/index.json", filepath.Join(install, "downloads"), ArtifactDownloader{AllowHTTPForLoopback: true, RetryDelay: 0})
	if err != nil {
		t.Fatal(err)
	}
	state, err := manager.Install(context.Background(), "service")
	if err != nil || !state.Installed || state.Health != HealthHealthy {
		t.Fatalf("network install state = %#v err=%v", state, err)
	}
	if counts.Load() != 2 {
		t.Fatalf("archive requests = %d, want 2", counts.Load())
	}
	states, err := manager.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	foundService := false
	for _, component := range states {
		if component.ID == "service" {
			foundService = true
			if !component.Installed || component.Health != HealthHealthy {
				t.Fatalf("service list state = %#v", component)
			}
		}
	}
	if !foundService {
		t.Fatal("service was absent from list state")
	}
	if _, err := os.Stat(filepath.Join(install, "runtime/helper")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(install, "service/main")); err != nil {
		t.Fatal(err)
	}
}

func TestNetworkComponentManagerRejectsManifestContractMismatch(t *testing.T) {
	manifest := ReleaseManifest{
		Format: ManifestFormat, Channel: ChannelNightly, Version: "nightly-2",
		Commit: strings.Repeat("d", 40), Target: "linux-amd64",
		Components: []Component{{ID: "service", Version: "nightly-2", Artifact: "service.tar.gz"}},
	}
	artifactData := []byte("archive")
	digest := sha256.Sum256(artifactData)
	index := ReleaseIndex{
		Format: ReleaseIndexFormat, Channel: manifest.Channel, Version: manifest.Version,
		Commit: manifest.Commit, Target: manifest.Target, Manifest: manifest,
		Artifacts: []ReleaseArtifact{{Component: "service", Target: manifest.Target, Version: manifest.Version, Path: "service.tar.gz", Size: int64(len(artifactData)), SHA256: hex.EncodeToString(digest[:])}},
	}
	local := manifest
	local.Components = []Component{{ID: "service", Version: "nightly-other", Artifact: "service.tar.gz"}}
	if _, err := NewNetworkComponentManager(ManagerOptions{InstallRoot: t.TempDir(), Manifest: local}, index, "https://example.invalid/index.json", "", ArtifactDownloader{}); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("manifest mismatch error = %v", err)
	}
}

func tarGzBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var output strings.Builder
	gzipWriter := gzip.NewWriter(&stringWriter{builder: &output})
	tarWriter := tar.NewWriter(gzipWriter)
	for name, contents := range files {
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o700, Size: int64(len(contents))}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tarWriter, contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return []byte(output.String())
}

type stringWriter struct{ builder *strings.Builder }

func (w *stringWriter) Write(data []byte) (int, error) { return w.builder.Write(data) }
