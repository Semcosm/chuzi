package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/launcher"
)

// TestLauncherConsumerFlow exercises the shipped CLI against a local release
// catalog. It deliberately uses synthetic archives and never starts a browser.
func TestLauncherConsumerFlow(t *testing.T) {
	target := consumerTarget()
	if target == "" {
		t.Skipf("unsupported test host %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	binary := buildConsumerLauncher(t)
	root := filepath.Join(t.TempDir(), "install")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	operationTemp := filepath.Join(t.TempDir(), "operation-temp")
	if err := os.MkdirAll(operationTemp, 0o700); err != nil {
		t.Fatal(err)
	}

	marker := []byte("standalone-launcher")
	if err := os.WriteFile(filepath.Join(root, "launcher.marker"), marker, 0o700); err != nil {
		t.Fatal(err)
	}
	old := makeConsumerRelease(t, target, "nightly-101-111111111111", strings.Repeat("1", 40), marker, []byte("worker-old"), []byte("service-old"))
	new := makeConsumerRelease(t, target, "nightly-102-222222222222", strings.Repeat("2", 40), marker, []byte("worker-new"), []byte("service-new"))
	bad := makeConsumerRelease(t, target, "nightly-103-333333333333", strings.Repeat("3", 40), marker, []byte("worker-bad"), []byte("service-bad"))
	badServiceArtifact, ok := bad.index.Artifact("service")
	if !ok {
		t.Fatal("bad fixture is missing service artifact")
	}
	badArchive := make([]byte, int(badServiceArtifact.Size))
	for index := range badArchive {
		badArchive[index] = byte((index % 251) + 1)
	}
	bad.artifacts["/"+badServiceArtifact.Path] = badArchive

	var serverMu sync.Mutex
	current := &old
	requests := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		serverMu.Lock()
		release := current
		requests[request.URL.Path]++
		serverMu.Unlock()
		if request.URL.Path == "/index.json" {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write(release.indexBytes)
			return
		}
		if data, ok := release.artifacts[request.URL.Path]; ok {
			writer.Header().Set("Content-Type", "application/octet-stream")
			_, _ = writer.Write(data)
			return
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()

	manifestPath := filepath.Join(root, "release-manifest.json")
	writeConsumerJSON(t, manifestPath, old.manifest)
	indexURL := server.URL + "/index.json"

	var stdout []byte
	var stderr string
	var err error
	stdout, stderr, err = runConsumerLauncher(t, binary, root, operationTemp, "-manifest", manifestPath, "-release-index", indexURL, "-allow-http-loopback", "-command", "initialize")
	if err != nil {
		t.Fatalf("initialize failed: %v", err)
	}
	var initialization launcher.InitializationStatus
	decodeConsumerJSON(t, stdout, &initialization)
	if !initialization.FirstRun || initialization.NextAction != "select_components" || !containsString(initialization.Required, "launcher") {
		t.Fatalf("initialization = %#v", initialization)
	}
	if !containsString(initialization.Optional, "service") || !containsString(initialization.Optional, "browser-worker") {
		t.Fatalf("initialization optional components = %#v", initialization.Optional)
	}

	stdout, stderr, err = runConsumerLauncher(t, binary, root, operationTemp, "-manifest", manifestPath, "-release-index", indexURL, "-allow-http-loopback", "-command", "component-install", "-item", "service")
	if err != nil {
		t.Fatalf("initial service install failed: %v", err)
	}
	var serviceState launcher.ComponentState
	decodeConsumerJSON(t, stdout, &serviceState)
	if serviceState.ID != "service" || !serviceState.Installed || serviceState.Version != old.manifest.Version || serviceState.Health != launcher.HealthHealthy {
		t.Fatalf("initial service state = %#v", serviceState)
	}
	assertConsumerFile(t, filepath.Join(root, "browser-worker", "worker.mjs"), []byte("worker-old"))
	assertConsumerFile(t, filepath.Join(root, "service", "service.bin"), []byte("service-old"))

	stdout, stderr, err = runConsumerLauncher(t, binary, root, operationTemp, "-manifest", manifestPath, "-release-index", indexURL, "-allow-http-loopback", "-command", "component-list")
	if err != nil {
		t.Fatalf("component list after install failed: %v", err)
	}
	var states []launcher.ComponentState
	decodeConsumerJSON(t, stdout, &states)
	assertConsumerComponent(t, states, "launcher", true, old.manifest.Version, launcher.HealthHealthy)
	assertConsumerComponent(t, states, "browser-worker", true, old.manifest.Version, launcher.HealthHealthy)
	assertConsumerComponent(t, states, "service", true, old.manifest.Version, launcher.HealthHealthy)

	serverMu.Lock()
	firstBrowserRequests := requests["/"+consumerArtifactPath(t, old.index, "browser-worker")]
	firstServiceRequests := requests["/"+consumerArtifactPath(t, old.index, "service")]
	serverMu.Unlock()
	stdout, stderr, err = runConsumerLauncher(t, binary, root, operationTemp, "-manifest", manifestPath, "-release-index", indexURL, "-allow-http-loopback", "-command", "component-install", "-item", "service")
	if err != nil {
		t.Fatalf("repeat service install failed: %v", err)
	}
	serverMu.Lock()
	if requests["/"+consumerArtifactPath(t, old.index, "browser-worker")] != firstBrowserRequests || requests["/"+consumerArtifactPath(t, old.index, "service")] != firstServiceRequests {
		serverMu.Unlock()
		t.Fatalf("repeat install bypassed archive cache: requests=%v", requests)
	}
	serverMu.Unlock()

	if err := os.WriteFile(filepath.Join(root, "service", "service.bin"), []byte("corrupt"), 0o700); err != nil {
		t.Fatal(err)
	}
	sourceRoot := filepath.Join(t.TempDir(), "repair-source")
	writeConsumerResourceTree(t, sourceRoot, old)
	stdout, stderr, err = runConsumerLauncher(t, binary, root, operationTemp, "-manifest", manifestPath, "-source-root", sourceRoot, "-command", "repair")
	if err != nil {
		t.Fatalf("repair failed: %v", err)
	}
	var repair launcher.RepairResult
	decodeConsumerJSON(t, stdout, &repair)
	if !containsString(repair.Repaired, "service/service.bin") {
		t.Fatalf("repair result = %#v", repair)
	}
	assertConsumerFile(t, filepath.Join(root, "service", "service.bin"), []byte("service-old"))

	serverMu.Lock()
	current = &new
	serverMu.Unlock()
	stdout, stderr, err = runConsumerLauncher(t, binary, root, operationTemp, "-manifest", manifestPath, "-release-index", indexURL, "-allow-http-loopback", "-current-version", old.manifest.Version, "-command", "check-update")
	if err != nil {
		t.Fatalf("check-update failed: %v", err)
	}
	var update launcher.UpdateInfo
	decodeConsumerJSON(t, stdout, &update)
	if !update.Available || update.Reason != "update_available" || update.Manifest == nil || update.Manifest.Version != new.manifest.Version {
		t.Fatalf("update result = %#v", update)
	}

	stdout, stderr, err = runConsumerLauncher(t, binary, root, operationTemp, "-manifest", manifestPath, "-release-index", indexURL, "-allow-http-loopback", "-command", "component-install", "-item", "service")
	if err != nil {
		t.Fatalf("upgrade install failed with old local manifest: %v", err)
	}
	decodeConsumerJSON(t, stdout, &serviceState)
	if serviceState.Version != new.manifest.Version || serviceState.Health != launcher.HealthHealthy {
		t.Fatalf("upgraded service state = %#v", serviceState)
	}
	assertConsumerFile(t, filepath.Join(root, "browser-worker", "worker.mjs"), []byte("worker-new"))
	assertConsumerFile(t, filepath.Join(root, "service", "service.bin"), []byte("service-new"))

	serverMu.Lock()
	newBrowserRequests := requests["/"+consumerArtifactPath(t, new.index, "browser-worker")]
	newServiceRequests := requests["/"+consumerArtifactPath(t, new.index, "service")]
	serverMu.Unlock()
	stdout, stderr, err = runConsumerLauncher(t, binary, root, operationTemp, "-manifest", manifestPath, "-release-index", indexURL, "-allow-http-loopback", "-command", "component-install", "-item", "service")
	if err != nil {
		t.Fatalf("repeat upgrade install failed: %v", err)
	}
	serverMu.Lock()
	if requests["/"+consumerArtifactPath(t, new.index, "browser-worker")] != newBrowserRequests || requests["/"+consumerArtifactPath(t, new.index, "service")] != newServiceRequests {
		serverMu.Unlock()
		t.Fatalf("repeat upgrade bypassed archive cache: requests=%v", requests)
	}
	serverMu.Unlock()

	if err := os.WriteFile(filepath.Join(root, "release-manifest-new.json"), mustConsumerJSON(new.manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	serverMu.Lock()
	current = &bad
	serverMu.Unlock()
	if _, stderr, err = runConsumerLauncher(t, binary, root, operationTemp, "-manifest", manifestPath, "-release-index", indexURL, "-allow-http-loopback", "-command", "component-install", "-item", "service"); err == nil {
		t.Fatalf("corrupt archive unexpectedly succeeded")
	} else if !strings.Contains(stderr, "sha256 mismatch") && !strings.Contains(stderr, "content length") {
		t.Fatalf("corrupt archive error = %q", stderr)
	}
	assertConsumerFile(t, filepath.Join(root, "service", "service.bin"), []byte("service-new"))
	stdout, stderr, err = runConsumerLauncher(t, binary, root, operationTemp, "-manifest", filepath.Join(root, "release-manifest-new.json"), "-command", "component-list")
	if err != nil {
		t.Fatalf("state inspection after failed upgrade failed: %v", err)
	}
	decodeConsumerJSON(t, stdout, &states)
	assertConsumerComponent(t, states, "service", true, new.manifest.Version, launcher.HealthHealthy)
	assertNoConsumerTempFiles(t, filepath.Join(operationTemp, ".chuzi-component-source-*"))
	assertNoConsumerTempFiles(t, filepath.Join(root, ".chuzi", "downloads", ".chuzi-artifact-*"))

	stdout, stderr, err = runConsumerLauncher(t, binary, root, operationTemp, "-manifest", manifestPath, "-release-index", indexURL, "-allow-http-loopback", "-command", "initialize-complete")
	if err != nil {
		t.Fatalf("initialize-complete failed: %v", err)
	}
	var completed map[string]bool
	decodeConsumerJSON(t, stdout, &completed)
	if !completed["initialized"] {
		t.Fatalf("completion result = %#v", completed)
	}
}

type consumerRelease struct {
	manifest   launcher.ReleaseManifest
	index      launcher.ReleaseIndex
	indexBytes []byte
	artifacts  map[string][]byte
	resources  map[string][]byte
}

func makeConsumerRelease(t *testing.T, target, version, commit string, marker, worker, service []byte) consumerRelease {
	t.Helper()
	extension := ".tar.gz"
	if target == "windows-amd64" {
		extension = ".zip"
	}
	components := []struct {
		id       string
		relative string
		data     []byte
		required bool
		deps     []string
	}{
		{id: "launcher", relative: "launcher.marker", data: marker, required: true},
		{id: "browser-worker", relative: "browser-worker/worker.mjs", data: worker},
		{id: "service", relative: "service/service.bin", data: service, deps: []string{"browser-worker"}},
	}
	manifest := launcher.ReleaseManifest{Format: launcher.ManifestFormat, Channel: launcher.ChannelNightly, Version: version, Commit: commit, Target: target, GeneratedAt: time.Unix(1, 0).UTC()}
	artifacts := make(map[string][]byte)
	resources := make(map[string][]byte)
	for _, item := range components {
		digest := sha256.Sum256(item.data)
		artifactPath := filepath.ToSlash(filepath.Join("artifacts", item.id+extension))
		archive := consumerArchive(t, target, map[string][]byte{item.relative: item.data})
		artifacts["/"+artifactPath] = archive
		resources[item.relative] = append([]byte(nil), item.data...)
		manifest.Components = append(manifest.Components, launcher.Component{ID: item.id, Version: version, Required: item.required, Dependencies: item.deps, Artifact: artifactPath, Resources: []launcher.Resource{{Path: item.relative, SHA256: hex.EncodeToString(digest[:]), Size: int64(len(item.data))}}})
	}
	index := launcher.ReleaseIndex{Format: launcher.ReleaseIndexFormat, Channel: manifest.Channel, Version: version, Commit: commit, Target: target, GeneratedAt: manifest.GeneratedAt, Manifest: manifest}
	for _, component := range manifest.Components {
		archivePath := component.Artifact
		archive := artifacts["/"+archivePath]
		digest := sha256.Sum256(archive)
		index.Artifacts = append(index.Artifacts, launcher.ReleaseArtifact{Component: component.ID, Target: target, Version: version, Path: archivePath, Size: int64(len(archive)), SHA256: hex.EncodeToString(digest[:])})
	}
	indexBytes, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	return consumerRelease{manifest: manifest, index: index, indexBytes: indexBytes, artifacts: artifacts, resources: resources}
}

func consumerArchive(t *testing.T, target string, files map[string][]byte) []byte {
	t.Helper()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var output bytes.Buffer
	if target == "windows-amd64" {
		writer := zip.NewWriter(&output)
		for _, name := range names {
			entry, err := writer.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := entry.Write(files[name]); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		return output.Bytes()
	}
	compressed := gzip.NewWriter(&output)
	writer := tar.NewWriter(compressed)
	for _, name := range names {
		data := files[name]
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o700, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func buildConsumerLauncher(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	binary := filepath.Join(t.TempDir(), "chuzi-launcher")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	command := exec.Command("go", "build", "-o", binary, "./cmd/launcher")
	command.Dir = repoRoot
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build launcher: %v\n%s", err, output)
	}
	return binary
}

func runConsumerLauncher(t *testing.T, binary, workingDir, tempDir string, args ...string) ([]byte, string, error) {
	t.Helper()
	command := exec.Command(binary, args...)
	command.Dir = workingDir
	command.Env = append(os.Environ(), "TMPDIR="+tempDir, "TMP="+tempDir, "TEMP="+tempDir)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	return stdout.Bytes(), stderr.String(), err
}

func writeConsumerJSON(t *testing.T, filename string, value any) {
	t.Helper()
	if err := os.WriteFile(filename, mustConsumerJSON(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustConsumerJSON(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("marshal consumer fixture: %v", err))
	}
	return data
}

func decodeConsumerJSON(t *testing.T, data []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatalf("decode launcher JSON %q: %v", data, err)
	}
}

func writeConsumerResourceTree(t *testing.T, root string, release consumerRelease) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, component := range release.manifest.Components {
		for _, resource := range component.Resources {
			data, ok := release.resources[resource.Path]
			if !ok {
				t.Fatalf("resource %s missing from fixture", resource.Path)
			}
			filename := filepath.Join(root, filepath.FromSlash(resource.Path))
			if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filename, data, 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func assertConsumerFile(t *testing.T, filename string, want []byte) {
	t.Helper()
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, want) {
		t.Fatalf("%s = %q, want %q", filename, data, want)
	}
}

func assertConsumerComponent(t *testing.T, states []launcher.ComponentState, id string, installed bool, version, health string) {
	t.Helper()
	for _, state := range states {
		if state.ID == id {
			if state.Installed != installed || state.Version != version || state.Health != health {
				t.Fatalf("component %s = %#v", id, state)
			}
			return
		}
	}
	t.Fatalf("component %s missing from %#v", id, states)
}

func assertNoConsumerTempFiles(t *testing.T, pattern string) {
	t.Helper()
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files survived: %v", matches)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func consumerTarget() string {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "windows/amd64":
		return "windows-amd64"
	case "linux/amd64":
		return "linux-amd64"
	case "linux/arm64":
		return "linux-arm64"
	case "darwin/arm64":
		return "darwin-arm64"
	default:
		return ""
	}
}

func consumerArtifactPath(t *testing.T, index launcher.ReleaseIndex, component string) string {
	t.Helper()
	artifact, ok := index.Artifact(component)
	if !ok {
		t.Fatalf("artifact %s missing from %#v", component, index.Artifacts)
	}
	return artifact.Path
}
