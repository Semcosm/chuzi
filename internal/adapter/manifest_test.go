package adapter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func testManifest(content []byte) Manifest {
	digest := sha256.Sum256(content)
	return Manifest{
		Format: ManifestFormat, ID: "demo-adapter", API: AdapterAPI, Version: "1.2.3",
		Entry: "adapter.mjs", Capabilities: []string{"demo@1"},
		Permissions: []string{"browser.cdp.loopback"}, Targets: []string{"linux-amd64"},
		SignedBy: "test-signer", Resources: []Resource{{Path: "adapter.mjs", SHA256: hex.EncodeToString(digest[:]), Size: int64(len(content))}},
	}
}

func writeTestPackage(t *testing.T, root string, manifest Manifest, content []byte) string {
	t.Helper()
	packageRoot := filepath.Join(root, manifest.ID)
	if err := os.MkdirAll(packageRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageRoot, manifest.Entry), content, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageRoot, ManifestName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return packageRoot
}

func TestManifestValidateRequiresTrustedAdapterMetadata(t *testing.T) {
	content := []byte("export default {};")
	valid := testManifest(content)
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	for name, mutate := range map[string]func(*Manifest){
		"api":      func(m *Manifest) { m.API = "chuzi.plugin/v1" },
		"signer":   func(m *Manifest) { m.SignedBy = "" },
		"entry":    func(m *Manifest) { m.Entry = "../adapter.mjs" },
		"resource": func(m *Manifest) { m.Resources[0].Path = "..\\adapter.mjs" },
		"checksum": func(m *Manifest) { m.Resources[0].SHA256 = "bad" },
		"size":     func(m *Manifest) { m.Resources[0].Size = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if err := candidate.Validate(); !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("Validate() = %v, want ErrInvalidManifest", err)
			}
		})
	}
}

func TestValidatePackageRejectsChecksumSymlinkAndUndeclaredFiles(t *testing.T) {
	content := []byte("export default {};")
	root := t.TempDir()
	manifest := testManifest(content)
	packageRoot := writeTestPackage(t, root, manifest, content)
	if _, err := ValidatePackage(packageRoot, "linux-amd64"); err != nil {
		t.Fatalf("valid package rejected: %v", err)
	}
	if err := os.WriteFile(filepath.Join(packageRoot, "extra.mjs"), []byte("extra"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidatePackage(packageRoot, "linux-amd64"); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("undeclared resource error = %v, want ErrInvalidManifest", err)
	}
	if err := os.Remove(filepath.Join(packageRoot, "extra.mjs")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageRoot, manifest.Entry), []byte("tampered"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidatePackage(packageRoot, "linux-amd64"); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("checksum error = %v, want ErrInvalidManifest", err)
	}
	if err := os.WriteFile(filepath.Join(packageRoot, manifest.Entry), content, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(packageRoot, manifest.Entry), filepath.Join(packageRoot, "link.mjs")); err == nil {
		defer os.Remove(filepath.Join(packageRoot, "link.mjs"))
		if _, err := ValidatePackage(packageRoot, "linux-amd64"); !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("symlink error = %v, want ErrInvalidPath", err)
		}
	}
}

func TestRegistryRequiresInstalledVerifiedTrustedEnabled(t *testing.T) {
	content := []byte("export default {};")
	root := t.TempDir()
	manifest := testManifest(content)
	writeTestPackage(t, root, manifest, content)
	for _, test := range []struct {
		name  string
		state State
		want  error
	}{
		{"not-installed", State{}, ErrNotInstalled},
		{"not-verified", State{Installed: true}, ErrNotVerified},
		{"not-trusted", State{Installed: true, Verified: true}, ErrNotTrusted},
		{"disabled", State{Installed: true, Verified: true, Trusted: true}, ErrDisabled},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry, err := NewRegistry(root, "linux-amd64", map[string]State{manifest.ID: test.state})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := registry.Resolve(manifest.ID); !errors.Is(err, test.want) {
				t.Fatalf("Resolve() = %v, want %v", err, test.want)
			}
		})
	}
	registry, err := NewRegistry(root, "linux-amd64", map[string]State{manifest.ID: {Installed: true, Verified: true, Trusted: true, Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := registry.Resolve(manifest.ID)
	if err != nil || resolved.Entry != filepath.Join(root, manifest.ID, manifest.Entry) {
		t.Fatalf("Resolve() = %#v, %v", resolved, err)
	}
}

func TestRegistryReportsInstalledButCorruptPackageAsUnverified(t *testing.T) {
	content := []byte("export default {};")
	root := t.TempDir()
	manifest := testManifest(content)
	packageRoot := writeTestPackage(t, root, manifest, content)
	if err := os.WriteFile(filepath.Join(packageRoot, manifest.Entry), []byte("corrupt"), 0o700); err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(root, "linux-amd64", map[string]State{manifest.ID: {Installed: true, Verified: true, Trusted: true, Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Resolve(manifest.ID); !errors.Is(err, ErrNotVerified) {
		t.Fatalf("Resolve() = %v, want ErrNotVerified", err)
	}
}

func TestRegistryRejectsManifestMutationAfterInstall(t *testing.T) {
	content := []byte("export default {};")
	root := t.TempDir()
	manifest := testManifest(content)
	packageRoot := writeTestPackage(t, root, manifest, content)
	digest, err := ManifestDigest(packageRoot)
	if err != nil {
		t.Fatal(err)
	}
	state := State{
		Installed: true, Verified: true, Trusted: true, Enabled: true,
		Version: manifest.Version, Entry: manifest.Entry, SignedBy: manifest.SignedBy,
		ManifestSHA256: digest,
	}
	registry, err := NewRegistry(root, "linux-amd64", map[string]State{manifest.ID: state})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Resolve(manifest.ID); err != nil {
		t.Fatalf("Resolve() rejected the installed package: %v", err)
	}
	manifest.SignedBy = "replacement-signer"
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageRoot, ManifestName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err = NewRegistry(root, "linux-amd64", map[string]State{manifest.ID: state})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Resolve(manifest.ID); !errors.Is(err, ErrNotVerified) {
		t.Fatalf("Resolve() after manifest mutation = %v, want ErrNotVerified", err)
	}
}
