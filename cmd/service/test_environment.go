package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/environment"
)

const (
	testEnvironmentPackageRef   = "chuzi-windows-test-v1"
	testEnvironmentID           = "chuzi/windows-test"
	testEnvironmentVersion      = "1.0.0"
	testEnvironmentSignerPrefix = "chuzi-local-test-v1-"
)

var testEnvironmentMu sync.Mutex

// prepareTestEnvironmentPackage is reached only through the reserved Core
// catalog key. Resource names and their installed source directory are fixed.
func prepareTestEnvironmentPackage(cfg config.Config, runtimeRoot, target string, manager *environment.Manager) error {
	testEnvironmentMu.Lock()
	defer testEnvironmentMu.Unlock()
	if manager == nil || !filepath.IsAbs(runtimeRoot) {
		return environment.ErrPackageReference
	}
	trustPath := environmentTrustStorePath(cfg)
	catalog := filepath.Join(cfg.DataDir, ".chuzi", "environment-catalog")
	entry := filepath.Join(catalog, testEnvironmentPackageRef)
	trust, err := environment.LoadTrustStore(trustPath)
	if errors.Is(err, os.ErrNotExist) {
		trust = environment.TrustStore{}
	} else if err != nil {
		return err
	}
	if _, err := os.Lstat(entry); err == nil {
		manifest, err := environment.ValidatePackage(entry, target, trust)
		if err != nil || manifest.EnvironmentID != testEnvironmentID || manifest.Version != testEnvironmentVersion {
			return environment.ErrPackageReference
		}
		public, ok := trust[manifest.Signer]
		if !ok {
			return environment.ErrPackageReference
		}
		return manager.AddTrustedSigner(manifest.Signer, public)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(catalog, 0700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(catalog, ".test-environment-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	manifest := environment.Manifest{
		API: environment.API, EnvironmentID: testEnvironmentID, Version: testEnvironmentVersion,
		Targets: []string{target}, Capabilities: []string{"browser"},
		Permissions: []string{"browser.loopback"}, InstallPolicy: environment.InstallPolicy{Atomic: true},
		CleanupPolicy: environment.CleanupPolicy{RemoveResources: true},
	}
	for _, item := range []struct{ name, entry, runtime string }{
		{"worker.mjs", "worker", "browser-worker"},
		{"headless.mjs", "headless", "browser-worker"},
		{"headless-adapter.mjs", "adapter-bridge", "adapter-bridge"},
		{"cdp-runtime.mjs", "", ""},
		{"local-test-page.html", "", ""},
	} {
		source, err := environment.ResolveRegularResource(filepath.Join(runtimeRoot, "browser-worker", "src"), item.name)
		if err != nil {
			return environment.ErrPackageReference
		}
		info, err := os.Lstat(source)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 8<<20 {
			return environment.ErrPackageReference
		}
		data, err := os.ReadFile(source)
		if err != nil || int64(len(data)) != info.Size() {
			return environment.ErrPackageReference
		}
		if err := os.WriteFile(filepath.Join(stage, item.name), data, 0600); err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		manifest.Resources = append(manifest.Resources, environment.Resource{Path: item.name, SHA256: hex.EncodeToString(digest[:]), Size: int64(len(data))})
		if item.entry != "" {
			manifest.Entrypoints = append(manifest.Entrypoints, environment.Entrypoint{Name: item.entry, Path: item.name, Runtime: item.runtime})
		}
	}
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		return err
	}
	defer clear(private)
	keyDigest := sha256.Sum256(public)
	manifest.Signer = testEnvironmentSignerPrefix + hex.EncodeToString(keyDigest[:8])
	if _, exists := trust[manifest.Signer]; exists {
		return environment.ErrPackageReference
	}
	manifest, err = manifest.Seal(private)
	if err != nil {
		return err
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stage, environment.ManifestName), data, 0600); err != nil {
		return err
	}
	trust[manifest.Signer] = public
	if _, err := environment.ValidatePackage(stage, target, trust); err != nil {
		return err
	}
	if err := saveTestTrustStore(trustPath, trust); err != nil {
		return err
	}
	if err := os.Rename(stage, entry); err != nil {
		return err
	}
	return manager.AddTrustedSigner(manifest.Signer, public)
}

func saveTestTrustStore(path string, trust environment.TrustStore) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	encoded := make(map[string]string, len(trust))
	for signer, public := range trust {
		encoded[signer] = base64.StdEncoding.EncodeToString(public)
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".environment-trust-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return err
	}
	if err := json.NewEncoder(file).Encode(encoded); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
