// Package environment defines the signed, service-owned runtime environment
// package boundary. It is intentionally separate from adapters and workers.
package environment

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	API                       = "chuzi-environment/v1"
	ManifestName              = "environment-manifest.json"
	SignatureAlgorithmEd25519 = "ed25519"
)

var (
	ErrInvalidManifest    = errors.New("environment: invalid manifest")
	ErrInvalidPath        = errors.New("environment: invalid package path")
	ErrInvalidSignature   = errors.New("environment: invalid signature")
	ErrUntrustedSigner    = errors.New("environment: signer is not trusted")
	ErrUndeclaredResource = errors.New("environment: undeclared resource")
)

type Resource struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type Entrypoint struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Runtime string `json:"runtime"`
}
type InstallPolicy struct {
	Atomic          bool `json:"atomic"`
	RequiresRestart bool `json:"requires_restart,omitempty"`
}
type CleanupPolicy struct {
	RemoveResources bool `json:"remove_resources"`
	RetainRollback  bool `json:"retain_rollback,omitempty"`
}
type HealthProbe struct {
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	Target         string `json:"target,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

// RuntimeContract describes the fixed service-owned pieces of a base slot
// environment. It contains identifiers only; executable and filesystem paths
// remain installation-owned and cannot be selected by a package or caller.
type RuntimeContract struct {
	Kind         string   `json:"kind"`
	ServiceOwned []string `json:"service_owned"`
	Entrypoints  []string `json:"entrypoints"`
	Isolation    []string `json:"isolation"`
}

// Manifest is the only metadata accepted as an environment package contract.
// ManifestDigest is the SHA-256 of CanonicalBytes with digest/signature fields
// omitted; this prevents a self-referential digest.
type Manifest struct {
	API                string           `json:"api"`
	EnvironmentID      string           `json:"environment_id"`
	Version            string           `json:"version"`
	Targets            []string         `json:"targets"`
	Capabilities       []string         `json:"capabilities"`
	Permissions        []string         `json:"permissions"`
	Resources          []Resource       `json:"resources"`
	Dependencies       []string         `json:"dependencies"`
	InstallPolicy      InstallPolicy    `json:"install_policy"`
	CleanupPolicy      CleanupPolicy    `json:"cleanup_policy"`
	HealthProbes       []HealthProbe    `json:"health_probes"`
	Entrypoints        []Entrypoint     `json:"entrypoints"`
	Signer             string           `json:"signer"`
	Signature          string           `json:"signature,omitempty"`
	SignatureAlgorithm string           `json:"signature_algorithm,omitempty"`
	ManifestDigest     string           `json:"manifest_digest,omitempty"`
	RuntimeContract    *RuntimeContract `json:"runtime_contract,omitempty"`
}

func validEnvironmentVersion(v string) bool {
	return validToken(v, 128) && v != "." && v != ".." && !strings.ContainsAny(v, "/\\:@")
}

func (m Manifest) Validate() error {
	if m.API != API || !validEnvironmentID(m.EnvironmentID) || !validEnvironmentVersion(m.Version) || !validToken(m.Signer, 256) {
		return fmt.Errorf("%w: api, identity, version, and signer are required", ErrInvalidManifest)
	}
	if len(m.Targets) == 0 || len(m.Capabilities) == 0 {
		return fmt.Errorf("%w: targets and capabilities are required", ErrInvalidManifest)
	}
	if m.RuntimeContract != nil {
		if err := m.RuntimeContract.Validate(); err != nil {
			return fmt.Errorf("%w: runtime contract: %v", ErrInvalidManifest, err)
		}
	}
	if err := uniqueTokens(m.Targets, 128); err != nil {
		return fmt.Errorf("%w: targets: %v", ErrInvalidManifest, err)
	}
	if err := uniqueTokens(m.Capabilities, 128); err != nil {
		return fmt.Errorf("%w: capabilities: %v", ErrInvalidManifest, err)
	}
	if err := uniqueTokens(m.Permissions, 128); err != nil {
		return fmt.Errorf("%w: permissions: %v", ErrInvalidManifest, err)
	}
	if err := uniqueTokens(m.Dependencies, 256); err != nil {
		return fmt.Errorf("%w: dependencies: %v", ErrInvalidManifest, err)
	}
	seen := make(map[string]struct{}, len(m.Resources))
	for _, r := range m.Resources {
		if err := validateResource(r); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidManifest, err)
		}
		if _, ok := seen[r.Path]; ok {
			return fmt.Errorf("%w: duplicate resource %q", ErrInvalidManifest, r.Path)
		}
		seen[r.Path] = struct{}{}
	}
	if len(m.Entrypoints) == 0 {
		return fmt.Errorf("%w: at least one entrypoint is required", ErrInvalidManifest)
	}
	entryNames := make(map[string]struct{}, len(m.Entrypoints))
	for _, e := range m.Entrypoints {
		if !validToken(e.Name, 128) || validateRelative(e.Path) != nil || !validRuntime(e.Runtime) {
			return fmt.Errorf("%w: invalid entrypoint", ErrInvalidManifest)
		}
		if _, ok := entryNames[e.Name]; ok {
			return fmt.Errorf("%w: duplicate entrypoint", ErrInvalidManifest)
		}
		entryNames[e.Name] = struct{}{}
		if _, ok := seen[e.Path]; !ok {
			return fmt.Errorf("%w: entrypoint %q is not a resource", ErrInvalidManifest, e.Name)
		}
	}
	for _, p := range m.HealthProbes {
		if !validToken(p.Name, 128) || !validToken(p.Kind, 64) || p.TimeoutSeconds < 0 || p.TimeoutSeconds > 3600 || strings.ContainsAny(p.Target, "\x00\r\n") {
			return fmt.Errorf("%w: invalid health probe", ErrInvalidManifest)
		}
	}
	if m.Signature != "" && m.SignatureAlgorithm != SignatureAlgorithmEd25519 {
		return fmt.Errorf("%w: unsupported signature algorithm", ErrInvalidSignature)
	}
	if m.ManifestDigest != "" && !validSHA256(m.ManifestDigest) {
		return fmt.Errorf("%w: invalid manifest digest", ErrInvalidManifest)
	}
	return nil
}

func (c RuntimeContract) Validate() error {
	if c.Kind != "base-slot" {
		return errors.New("kind must be base-slot")
	}
	if err := uniqueTokens(c.ServiceOwned, 128); err != nil {
		return fmt.Errorf("service_owned: %w", err)
	}
	if err := uniqueTokens(c.Entrypoints, 128); err != nil {
		return fmt.Errorf("entrypoints: %w", err)
	}
	if err := uniqueTokens(c.Isolation, 128); err != nil {
		return fmt.Errorf("isolation: %w", err)
	}
	for _, value := range c.Entrypoints {
		switch value {
		case "worker", "headless":
		default:
			return fmt.Errorf("entrypoint %q is not base-owned", value)
		}
	}
	return nil
}
func validRuntime(v string) bool {
	switch v {
	case "browser-worker", "agent", "adapter-bridge":
		return true
	default:
		return false
	}
}
func validToken(v string, max int) bool {
	return strings.TrimSpace(v) == v && v != "" && len(v) <= max && !strings.ContainsAny(v, "\x00\r\n\t")
}

func validEnvironmentID(v string) bool {
	if !validToken(v, 256) || filepath.IsAbs(v) || filepath.VolumeName(v) != "" || strings.ContainsAny(v, "\\:@") {
		return false
	}
	for _, part := range strings.Split(v, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}
func uniqueTokens(values []string, max int) error {
	seen := map[string]struct{}{}
	for _, v := range values {
		if !validToken(v, max) {
			return errors.New("empty or invalid value")
		}
		if _, ok := seen[v]; ok {
			return fmt.Errorf("duplicate %q", v)
		}
		seen[v] = struct{}{}
	}
	return nil
}
func validSHA256(v string) bool {
	if len(v) != 64 {
		return false
	}
	_, err := hex.DecodeString(v)
	return err == nil
}
func validateResource(r Resource) error {
	if err := validateRelative(r.Path); err != nil {
		return fmt.Errorf("resource path: %w", err)
	}
	ext := strings.ToLower(filepath.Ext(r.Path))
	if ext == ".sh" || ext == ".bash" || ext == ".ps1" || ext == ".bat" || ext == ".cmd" || ext == ".com" || ext == ".exe" || ext == ".dll" {
		return errors.New("shell or executable resources are not allowed")
	}
	if r.Path == ManifestName || r.Size <= 0 || !validSHA256(r.SHA256) {
		return errors.New("invalid resource metadata")
	}
	return nil
}
func validateRelative(v string) error {
	if strings.TrimSpace(v) == "" || filepath.IsAbs(v) || filepath.VolumeName(v) != "" || filepath.Clean(v) != v || strings.ContainsAny(v, ":\\\x00") {
		return ErrInvalidPath
	}
	for _, part := range strings.Split(filepath.ToSlash(v), "/") {
		if part == "" || part == "." || part == ".." {
			return ErrInvalidPath
		}
	}
	return nil
}

func (m Manifest) canonical() (Manifest, error) {
	m.ManifestDigest, m.Signature, m.SignatureAlgorithm = "", "", ""
	m.Targets = append([]string(nil), m.Targets...)
	m.Capabilities = append([]string(nil), m.Capabilities...)
	m.Permissions = append([]string(nil), m.Permissions...)
	m.Dependencies = append([]string(nil), m.Dependencies...)
	m.Resources = append([]Resource(nil), m.Resources...)
	m.Entrypoints = append([]Entrypoint(nil), m.Entrypoints...)
	m.HealthProbes = append([]HealthProbe(nil), m.HealthProbes...)
	sort.Strings(m.Targets)
	sort.Strings(m.Capabilities)
	sort.Strings(m.Permissions)
	sort.Strings(m.Dependencies)
	sort.Slice(m.Resources, func(i, j int) bool { return m.Resources[i].Path < m.Resources[j].Path })
	sort.Slice(m.Entrypoints, func(i, j int) bool { return m.Entrypoints[i].Name < m.Entrypoints[j].Name })
	sort.Slice(m.HealthProbes, func(i, j int) bool { return m.HealthProbes[i].Name < m.HealthProbes[j].Name })
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}
func (m Manifest) CanonicalBytes() ([]byte, error) {
	c, err := m.canonical()
	if err != nil {
		return nil, err
	}
	return json.Marshal(c)
}
func (m Manifest) ComputeDigest() (string, error) {
	b, err := m.CanonicalBytes()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Seal computes the canonical digest and signs the same canonical bytes. The
// private key is accepted only at package-build time and is never retained.
func (m Manifest) Seal(private ed25519.PrivateKey) (Manifest, error) {
	if len(private) != ed25519.PrivateKeySize {
		return Manifest{}, ErrInvalidSignature
	}
	m.Signature, m.SignatureAlgorithm = "", ""
	digest, err := m.ComputeDigest()
	if err != nil {
		return Manifest{}, err
	}
	m.ManifestDigest = digest
	canonical, err := m.CanonicalBytes()
	if err != nil {
		return Manifest{}, err
	}
	m.SignatureAlgorithm = SignatureAlgorithmEd25519
	m.Signature = hex.EncodeToString(ed25519.Sign(private, canonical))
	return m, nil
}

// CanonicalDigest is a descriptive alias used by package tooling.
func (m Manifest) CanonicalDigest() (string, error) { return m.ComputeDigest() }
func (m Manifest) VerifyDigest() error {
	d, err := m.ComputeDigest()
	if err != nil {
		return err
	}
	if !strings.EqualFold(d, m.ManifestDigest) {
		return fmt.Errorf("%w: digest mismatch", ErrInvalidManifest)
	}
	return nil
}
func (m Manifest) VerifySignature(key ed25519.PublicKey) error {
	if len(key) != ed25519.PublicKeySize || m.SignatureAlgorithm != SignatureAlgorithmEd25519 || m.Signature == "" {
		return ErrInvalidSignature
	}
	b, err := m.CanonicalBytes()
	if err != nil {
		return err
	}
	sig, err := hex.DecodeString(m.Signature)
	if err != nil || !ed25519.Verify(key, b, sig) {
		return ErrInvalidSignature
	}
	return nil
}

type TrustStore map[string]ed25519.PublicKey

func (t TrustStore) Verify(m Manifest) error {
	key, ok := t[m.Signer]
	if !ok {
		return ErrUntrustedSigner
	}
	if err := m.VerifyDigest(); err != nil {
		return err
	}
	return m.VerifySignature(key)
}

func ReadManifest(root string) (Manifest, error) {
	data, err := os.ReadFile(filepath.Join(root, ManifestName))
	if err != nil {
		return Manifest{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return Manifest{}, fmt.Errorf("%w: trailing JSON", ErrInvalidManifest)
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}
func pathInPackage(root, rel string) (string, error) {
	if err := validateRelative(rel); err != nil {
		return "", fmt.Errorf("%w: resource-relative-path", err)
	}
	joined := filepath.Join(root, filepath.FromSlash(rel))
	base, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	target, err := filepath.Abs(joined)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(base, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: resource-containment", ErrInvalidPath)
	}
	cur := base
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil {
			return "", err
		}
		reparse, reparseErr := isReparsePoint(cur)
		if reparseErr != nil || reparse || info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeIrregular != 0 {
			return "", fmt.Errorf("%w: resource-reparse-check", ErrInvalidPath)
		}
	}
	return target, nil
}
func fileDigest(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// executableHeader rejects native binaries and script launchers even when a
// package author hides them behind a data-file extension. JavaScript and other
// runtime source files remain valid resources when their manifest runtime
// explicitly selects the corresponding closed entrypoint.
func executableHeader(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	var header [8]byte
	n, err := io.ReadFull(f, header[:])
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return false, err
	}
	b := header[:n]
	if len(b) >= 2 && (b[0] == '#' && b[1] == '!') {
		return true, nil
	}
	if len(b) >= 2 && b[0] == 'M' && b[1] == 'Z' {
		return true, nil
	}
	if len(b) >= 4 && string(b[:4]) == "\x7fELF" {
		return true, nil
	}
	if len(b) >= 4 {
		switch string(b[:4]) {
		case "\xfe\xed\xfa\xce", "\xce\xfa\xed\xfe", "\xfe\xed\xfa\xcf", "\xcf\xfa\xed\xfe":
			return true, nil
		}
	}
	return false, nil
}

// ValidatePackage checks every package entry without executing it. The only
// unlisted file permitted is the manifest itself.
func ValidatePackage(root, target string, trust TrustStore) (Manifest, error) {
	if strings.TrimSpace(root) == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return Manifest{}, fmt.Errorf("%w: package-root-form", ErrInvalidPath)
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Manifest{}, fmt.Errorf("%w: package-root-type", ErrInvalidPath)
	}
	if reparse, reparseErr := isReparsePath(root); reparseErr != nil || reparse {
		return Manifest{}, fmt.Errorf("%w: package-root-reparse-check", ErrInvalidPath)
	}
	// A package root can itself be ordinary while a parent directory is a
	// symlink/junction. Resolve the complete chain before reading any resource.
	resolved, resolveErr := filepath.EvalSymlinks(root)
	if resolveErr != nil || !sameResolvedPath(filepath.Clean(resolved), filepath.Clean(root)) {
		return Manifest{}, fmt.Errorf("%w: package-root-resolution", ErrInvalidPath)
	}
	m, err := ReadManifest(root)
	if err != nil {
		return Manifest{}, err
	}
	if target != "" && !contains(m.Targets, target) {
		return Manifest{}, fmt.Errorf("%w: target unsupported", ErrInvalidManifest)
	}
	if err := m.VerifyDigest(); err != nil {
		return Manifest{}, err
	}
	if trust != nil {
		if err := trust.Verify(m); err != nil {
			return Manifest{}, err
		}
	}
	declared := map[string]struct{}{}
	for _, r := range m.Resources {
		declared[r.Path] = struct{}{}
		path, err := pathInPackage(root, r.Path)
		if err != nil {
			return Manifest{}, fmt.Errorf("package resource path: %w", err)
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModePerm&0o111 != 0 {
			return Manifest{}, fmt.Errorf("%w: resource %s", ErrInvalidManifest, r.Path)
		}
		digest, size, err := fileDigest(path)
		if err != nil || size != r.Size || !strings.EqualFold(digest, r.SHA256) {
			return Manifest{}, fmt.Errorf("%w: resource %s digest", ErrInvalidManifest, r.Path)
		}
		executable, err := executableHeader(path)
		if err != nil {
			return Manifest{}, fmt.Errorf("%w: resource %s", ErrInvalidManifest, r.Path)
		}
		if executable {
			return Manifest{}, fmt.Errorf("%w: executable resource %s", ErrInvalidManifest, r.Path)
		}
	}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		reparse, reparseErr := isReparsePoint(path)
		if reparseErr != nil || reparse || entry.Type()&os.ModeSymlink != 0 || entry.Type()&os.ModeIrregular != 0 {
			return fmt.Errorf("%w: package-entry-reparse-check", ErrInvalidPath)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == ManifestName {
			return nil
		}
		if _, ok := declared[rel]; !ok {
			return fmt.Errorf("%w: %s", ErrUndeclaredResource, rel)
		}
		return nil
	})
	if err != nil {
		return Manifest{}, err
	}
	return m, nil
}
func contains(values []string, wanted string) bool {
	for _, v := range values {
		if v == wanted {
			return true
		}
	}
	return false
}
