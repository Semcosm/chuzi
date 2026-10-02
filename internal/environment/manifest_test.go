package environment

import (
	"context"
	"crypto/ed25519"
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

func testManifest(t *testing.T, content []byte) (Manifest, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = pub
	digest := sha256Digest(content)
	m := Manifest{API: API, EnvironmentID: "env-demo", Version: "1.0.0", Targets: []string{"linux-amd64"}, Capabilities: []string{"browser"}, Permissions: []string{"browser.loopback"}, Resources: []Resource{{Path: "runtime.dat", SHA256: digest, Size: int64(len(content))}}, Dependencies: []string{}, InstallPolicy: InstallPolicy{Atomic: true}, CleanupPolicy: CleanupPolicy{RemoveResources: true}, HealthProbes: []HealthProbe{{Name: "agent", Kind: "protocol"}}, Entrypoints: []Entrypoint{{Name: "worker", Path: "runtime.dat", Runtime: "browser-worker"}}, Signer: "test-signer"}
	sealed, err := m.Seal(priv)
	if err != nil {
		t.Fatal(err)
	}
	return sealed, priv
}
func sha256Digest(v []byte) string { h := sha256.Sum256(v); return hex.EncodeToString(h[:]) }

func TestManifestSealCanonicalAndPackageValidation(t *testing.T) {
	content := []byte("runtime data")
	m, priv := testManifest(t, content)
	if err := m.VerifySignature(priv.Public().(ed25519.PublicKey)); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "runtime.dat"), content, 0600); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(root, ManifestName), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidatePackage(root, "linux-amd64", TrustStore{"test-signer": priv.Public().(ed25519.PublicKey)}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "extra.dat"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidatePackage(root, "linux-amd64", TrustStore{"test-signer": priv.Public().(ed25519.PublicKey)}); !errors.Is(err, ErrUndeclaredResource) {
		t.Fatalf("extra resource error = %v", err)
	}
}

func TestPackageRejectsSymlinkedParent(t *testing.T) {
	content := []byte("runtime data")
	m, priv := testManifest(t, content)
	actual := t.TempDir()
	if err := os.WriteFile(filepath.Join(actual, "runtime.dat"), content, 0600); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(actual, ManifestName), data, 0600); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	linkedParent := filepath.Join(parent, "linked")
	if err := os.Symlink(filepath.Dir(actual), linkedParent); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	root := filepath.Join(linkedParent, filepath.Base(actual))
	if _, err := ValidatePackage(root, "linux-amd64", TrustStore{"test-signer": priv.Public().(ed25519.PublicKey)}); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("symlinked package parent error = %v", err)
	}
}

func TestManifestRejectsShellAndTraversal(t *testing.T) {
	m := Manifest{API: API, EnvironmentID: "env", Version: "1", Targets: []string{"linux"}, Capabilities: []string{"x"}, Signer: "s", Resources: []Resource{{Path: "run.sh", SHA256: "0000000000000000000000000000000000000000000000000000000000000000", Size: 1}}, Entrypoints: []Entrypoint{{Name: "x", Path: "run.sh", Runtime: "agent"}}}
	if !errors.Is(m.Validate(), ErrInvalidManifest) {
		t.Fatal("shell resource accepted")
	}
	m.Resources[0].Path = "../run.dat"
	m.Entrypoints[0].Path = "../run.dat"
	if !errors.Is(m.Validate(), ErrInvalidManifest) {
		t.Fatal("traversal accepted")
	}
}

func TestManifestRejectsPathCollapsingVersion(t *testing.T) {
	m := Manifest{API: API, EnvironmentID: "env", Version: "..", Targets: []string{"linux"}, Capabilities: []string{"x"}, Signer: "s", Resources: []Resource{{Path: "run.dat", SHA256: strings.Repeat("0", 64), Size: 1}}, Entrypoints: []Entrypoint{{Name: "x", Path: "run.dat", Runtime: "agent"}}}
	if !errors.Is(m.Validate(), ErrInvalidManifest) {
		t.Fatal("path-collapsing environment version was accepted")
	}
}

func TestManifestRejectsEnvironmentKeyDelimiter(t *testing.T) {
	m := Manifest{API: API, EnvironmentID: "env@demo", Version: "1", Targets: []string{"linux"}, Capabilities: []string{"x"}, Signer: "s", Resources: []Resource{{Path: "run.dat", SHA256: strings.Repeat("0", 64), Size: 1}}, Entrypoints: []Entrypoint{{Name: "x", Path: "run.dat", Runtime: "agent"}}}
	if !errors.Is(m.Validate(), ErrInvalidManifest) {
		t.Fatal("environment key delimiter was accepted")
	}
}

func TestPackageRejectsNativeExecutableHiddenByDataExtension(t *testing.T) {
	content := []byte("MZ\x90\x00native")
	m, priv := testManifest(t, content)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "runtime.dat"), content, 0600); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(root, ManifestName), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidatePackage(root, "linux-amd64", TrustStore{"test-signer": priv.Public().(ed25519.PublicKey)}); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("native executable package error = %v", err)
	}
}

func TestManagerLifecycleGatesAndRestart(t *testing.T) {
	content := []byte("runtime data")
	m, priv := testManifest(t, content)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "runtime.dat"), content, 0600); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(source, ManifestName), data, 0600); err != nil {
		t.Fatal(err)
	}
	install := t.TempDir()
	options := Options{InstallRoot: install, Target: "linux-amd64", Trust: TrustStore{"test-signer": priv.Public().(ed25519.PublicKey)}}
	manager, err := NewManager(options)
	if err != nil {
		t.Fatal(err)
	}
	record, err := manager.Install(nil, source)
	if err == nil || !errors.Is(err, context.Canceled) {
		_ = record
	}
	record, err = manager.Install(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if record.Ready || !record.Verified || record.Trusted || record.Enabled {
		t.Fatalf("install gates = %#v", record)
	}
	if _, err := manager.Resolve("env-demo", "1.0.0", "worker"); !errors.Is(err, ErrNotTrusted) {
		t.Fatalf("resolve before trust = %v", err)
	}
	if _, err := manager.SetTrusted("env-demo", "1.0.0", true); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetEnabled("env-demo", "1.0.0", true); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.HealthCheck(context.Background(), "env-demo", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	manager2, err := NewManager(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager2.HealthCheck(context.Background(), "env-demo", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager2.Resolve("env-demo", "1.0.0", "worker"); err != nil {
		t.Fatal(err)
	}
	withoutTrust, err := NewManager(Options{InstallRoot: install, Target: "linux-amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := withoutTrust.HealthCheck(context.Background(), "env-demo", "1.0.0"); !errors.Is(err, ErrNotVerified) && !errors.Is(err, ErrUntrustedSigner) {
		t.Fatalf("health check without signer trust = %v", err)
	}
}

func TestManagerResolvesClosedRuntimeEntrypoint(t *testing.T) {
	content := []byte("runtime data")
	m, priv := testManifest(t, content)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "runtime.dat"), content, 0600); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(source, ManifestName), data, 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(Options{InstallRoot: t.TempDir(), Target: "linux-amd64", Trust: TrustStore{"test-signer": priv.Public().(ed25519.PublicKey)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetTrusted(m.EnvironmentID, m.Version, true); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetEnabled(m.EnvironmentID, m.Version, true); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.HealthCheck(context.Background(), m.EnvironmentID, m.Version); err != nil {
		t.Fatal(err)
	}
	packageValue, entry, err := manager.ResolveEntrypoint(m.EnvironmentID, m.Version, "worker", "browser-worker")
	if err != nil || entry.Path != "runtime.dat" || packageValue.Root == "" {
		t.Fatalf("resolved entrypoint = %#v/%#v, %v", packageValue, entry, err)
	}
	if _, _, err := manager.ResolveEntrypoint(m.EnvironmentID, m.Version, "worker", "agent"); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("runtime mismatch error = %v", err)
	}
}

type recordSinkFake struct {
	records []Record
}

func (s *recordSinkFake) PutEnvironmentRecord(record Record) error {
	s.records = append(s.records, record)
	return nil
}

func TestManagerPromotesOnlyReadyVerifiedRecords(t *testing.T) {
	content := []byte("runtime data")
	m, priv := testManifest(t, content)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "runtime.dat"), content, 0600); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(source, ManifestName), data, 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(Options{InstallRoot: t.TempDir(), Target: "linux-amd64", Trust: TrustStore{"test-signer": priv.Public().(ed25519.PublicKey)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	sink := &recordSinkFake{}
	if err := manager.PromoteReady(sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.records) != 0 {
		t.Fatalf("untrusted install was promoted: %#v", sink.records)
	}
	if _, err := manager.SetTrusted(m.EnvironmentID, m.Version, true); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetEnabled(m.EnvironmentID, m.Version, true); err != nil {
		t.Fatal(err)
	}
	// The default health probe is successful; the record becomes ready when
	// the manager performs the health transition with all gates enabled.
	if _, err := manager.HealthCheck(context.Background(), m.EnvironmentID, m.Version); err != nil {
		t.Fatal(err)
	}
	if err := manager.PromoteReady(sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.records) != 1 || !sink.records[0].IsReady() {
		t.Fatalf("promoted records = %#v", sink.records)
	}
}

func TestManagerInstallIsIdempotentAndRefusesExternalModification(t *testing.T) {
	content := []byte("runtime data")
	m, priv := testManifest(t, content)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "runtime.dat"), content, 0600); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(source, ManifestName), data, 0600); err != nil {
		t.Fatal(err)
	}
	install := t.TempDir()
	options := Options{InstallRoot: install, Target: "linux-amd64", Trust: TrustStore{"test-signer": priv.Public().(ed25519.PublicKey)}}
	manager, err := NewManager(options)
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.Install(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Install(context.Background(), source)
	if err != nil || second.Generation != first.Generation {
		t.Fatalf("repeat install = %#v, %v; first=%#v", second, err, first)
	}
	root := filepath.Join(install, "environments", m.EnvironmentID, m.Version)
	if err := os.WriteFile(filepath.Join(root, "runtime.dat"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(context.Background(), source); !errors.Is(err, ErrExternalModification) {
		t.Fatalf("tampered reinstall = %v", err)
	}
	if err := manager.Remove(m.EnvironmentID, m.Version); !errors.Is(err, ErrExternalModification) {
		t.Fatalf("tampered remove = %v", err)
	}
}

func TestManagerUpgradeKeepsPreviousVersionOnFailureAndIsIdempotent(t *testing.T) {
	content := []byte("runtime data")
	firstManifest, private := testManifest(t, content)
	firstSource := t.TempDir()
	if err := os.WriteFile(filepath.Join(firstSource, "runtime.dat"), content, 0600); err != nil {
		t.Fatal(err)
	}
	firstJSON, _ := json.Marshal(firstManifest)
	if err := os.WriteFile(filepath.Join(firstSource, ManifestName), firstJSON, 0600); err != nil {
		t.Fatal(err)
	}
	secondManifest := firstManifest
	secondManifest.Version = "2.0.0"
	secondManifest, err := secondManifest.Seal(private)
	if err != nil {
		t.Fatal(err)
	}
	secondSource := t.TempDir()
	if err := os.WriteFile(filepath.Join(secondSource, "runtime.dat"), content, 0600); err != nil {
		t.Fatal(err)
	}
	secondJSON, _ := json.Marshal(secondManifest)
	if err := os.WriteFile(filepath.Join(secondSource, ManifestName), secondJSON, 0600); err != nil {
		t.Fatal(err)
	}
	install := t.TempDir()
	manager, err := NewManager(Options{InstallRoot: install, Target: "linux-amd64", Trust: TrustStore{"test-signer": private.Public().(ed25519.PublicKey)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(context.Background(), firstSource); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Upgrade(context.Background(), secondSource); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Upgrade(context.Background(), secondSource); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Get(firstManifest.EnvironmentID, firstManifest.Version); err != nil {
		t.Fatalf("previous version was not retained: %v", err)
	}
	if _, err := manager.Get(secondManifest.EnvironmentID, secondManifest.Version); err != nil {
		t.Fatalf("upgraded version was not retained: %v", err)
	}
	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, ManifestName), secondJSON, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Upgrade(context.Background(), broken); err == nil {
		t.Fatal("incomplete upgrade unexpectedly succeeded")
	}
	if _, err := manager.Get(firstManifest.EnvironmentID, firstManifest.Version); err != nil {
		t.Fatalf("failed upgrade removed previous version: %v", err)
	}
}

func TestManagerRollbackValidatesBothTreesAndFencesGeneration(t *testing.T) {
	oldContent := []byte("old runtime")
	oldManifest, oldPrivate := testManifest(t, oldContent)
	oldSource := t.TempDir()
	if err := os.WriteFile(filepath.Join(oldSource, "runtime.dat"), oldContent, 0600); err != nil {
		t.Fatal(err)
	}
	oldJSON, _ := json.Marshal(oldManifest)
	if err := os.WriteFile(filepath.Join(oldSource, ManifestName), oldJSON, 0600); err != nil {
		t.Fatal(err)
	}
	newContent := []byte("new runtime")
	newManifest, newPrivate := testManifest(t, newContent)
	newSource := t.TempDir()
	if err := os.WriteFile(filepath.Join(newSource, "runtime.dat"), newContent, 0600); err != nil {
		t.Fatal(err)
	}
	newJSON, _ := json.Marshal(newManifest)
	if err := os.WriteFile(filepath.Join(newSource, ManifestName), newJSON, 0600); err != nil {
		t.Fatal(err)
	}
	install := t.TempDir()
	trust := TrustStore{"test-signer": oldPrivate.Public().(ed25519.PublicKey)}
	manager, err := NewManager(Options{InstallRoot: install, Target: "linux-amd64", Trust: trust})
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.Install(context.Background(), oldSource)
	if err != nil {
		t.Fatal(err)
	}
	trust["test-signer-new"] = newPrivate.Public().(ed25519.PublicKey)
	newManifest.Signer = "test-signer-new"
	newManifest, err = newManifest.Seal(newPrivate)
	if err != nil {
		t.Fatal(err)
	}
	newJSON, _ = json.Marshal(newManifest)
	if err := os.WriteFile(filepath.Join(newSource, ManifestName), newJSON, 0600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(install, "environments", oldManifest.EnvironmentID, oldManifest.Version)
	if err := os.Rename(root, root+".rollback"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := copyTree(newSource, root); err != nil {
		t.Fatal(err)
	}
	if err := manager.Rollback(oldManifest.EnvironmentID, oldManifest.Version); err != nil {
		t.Fatal(err)
	}
	record, err := manager.Get(oldManifest.EnvironmentID, oldManifest.Version)
	if err != nil {
		t.Fatal(err)
	}
	if record.Generation <= first.Generation || !record.Verified || record.Trusted || record.Ready {
		t.Fatalf("rollback record = %#v", record)
	}
	if _, err := ValidatePackage(root, "linux-amd64", trust); err != nil {
		t.Fatalf("restored package = %v", err)
	}
}

func TestManagerDetectsValidReplacementAndUnknownPackageRoot(t *testing.T) {
	content := []byte("runtime data")
	m, priv := testManifest(t, content)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "runtime.dat"), content, 0600); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(source, ManifestName), data, 0600); err != nil {
		t.Fatal(err)
	}
	install := t.TempDir()
	manager, err := NewManager(Options{InstallRoot: install, Target: "linux-amd64", Trust: TrustStore{"test-signer": priv.Public().(ed25519.PublicKey)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(install, "environments", m.EnvironmentID, m.Version)
	if err := os.WriteFile(filepath.Join(root, "runtime.dat"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.HealthCheck(context.Background(), m.EnvironmentID, m.Version); !errors.Is(err, ErrNotVerified) {
		t.Fatalf("tampered health check = %v", err)
	}
	if err := manager.Remove(m.EnvironmentID, m.Version); !errors.Is(err, ErrExternalModification) {
		t.Fatalf("tampered remove after health check = %v", err)
	}
	unknownRoot := filepath.Join(install, "environments", "unknown", "1")
	if err := os.MkdirAll(unknownRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := manager.Remove("unknown", "1"); !errors.Is(err, ErrExternalModification) {
		t.Fatalf("unknown root removal = %v", err)
	}
}

func TestManagerRejectsTrailingStateJSON(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(state, []byte("{\\\"records\\\":{}} {}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(Options{InstallRoot: t.TempDir(), StatePath: state}); err == nil {
		t.Fatal("trailing state JSON was accepted")
	}
}

func TestRecordRejectsInconsistentLifecycleGates(t *testing.T) {
	record := Record{EnvironmentID: "env", Version: "1", ManifestDigest: strings.Repeat("a", 64), Signer: "signer", Installed: true, Trusted: true, Generation: 1, UpdatedAt: time.Unix(1, 0)}
	if !errors.Is(record.Validate(), ErrNotTrusted) {
		t.Fatalf("trusted without verified = %v", record.Validate())
	}
}
