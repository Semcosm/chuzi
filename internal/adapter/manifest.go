// Package adapter defines the installed adapter package boundary. It only
// accepts packages below a service-derived root and never resolves a caller
// supplied executable path.
package adapter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	ManifestFormat = "chuzi-adapter/v1"
	AdapterAPI     = "chuzi.adapter/v1"
	ManifestName   = "adapter-manifest.json"
)

var (
	ErrInvalidManifest = errors.New("adapter: invalid manifest")
	ErrInvalidPath     = errors.New("adapter: invalid package path")
	ErrNotInstalled    = errors.New("adapter: package is not installed")
	ErrNotVerified     = errors.New("adapter: package is not verified")
	ErrNotTrusted      = errors.New("adapter: package is not trusted")
	ErrDisabled        = errors.New("adapter: package is disabled")
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

type Resource struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Manifest is deliberately self-contained so a package can be inspected
// before its entrypoint is ever started.
type Manifest struct {
	Format       string     `json:"format"`
	ID           string     `json:"id"`
	API          string     `json:"api"`
	Version      string     `json:"version"`
	Entry        string     `json:"entry"`
	Capabilities []string   `json:"capabilities"`
	Permissions  []string   `json:"permissions"`
	Targets      []string   `json:"targets,omitempty"`
	SignedBy     string     `json:"signed_by,omitempty"`
	Resources    []Resource `json:"resources"`
}

func (m Manifest) Validate() error {
	if m.Format != ManifestFormat || !identifierPattern.MatchString(m.ID) ||
		m.API != AdapterAPI || strings.TrimSpace(m.Version) == "" || strings.TrimSpace(m.SignedBy) == "" {
		return fmt.Errorf("%w: format, id, api, and version are required", ErrInvalidManifest)
	}
	if err := validateRelative(m.Entry); err != nil {
		return fmt.Errorf("%w: entry: %v", ErrInvalidManifest, err)
	}
	if len(m.Capabilities) == 0 {
		return fmt.Errorf("%w: capabilities are required", ErrInvalidManifest)
	}
	if err := uniqueStrings(m.Capabilities, "capability"); err != nil {
		return err
	}
	if err := uniqueStrings(m.Permissions, "permission"); err != nil {
		return err
	}
	if err := uniqueStrings(m.Targets, "target"); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(m.Resources))
	for _, resource := range m.Resources {
		if err := validateResource(resource); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidManifest, err)
		}
		if _, ok := seen[resource.Path]; ok {
			return fmt.Errorf("%w: duplicate resource %q", ErrInvalidManifest, resource.Path)
		}
		seen[resource.Path] = struct{}{}
	}
	if _, ok := seen[m.Entry]; !ok {
		return fmt.Errorf("%w: entry resource is missing", ErrInvalidManifest)
	}
	return nil
}

func uniqueStrings(values []string, label string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return fmt.Errorf("%w: empty %s", ErrInvalidManifest, label)
		}
		if _, ok := seen[value]; ok {
			return fmt.Errorf("%w: duplicate %s %q", ErrInvalidManifest, label, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateResource(resource Resource) error {
	if err := validateRelative(resource.Path); err != nil {
		return fmt.Errorf("resource path: %v", err)
	}
	if resource.Size <= 0 {
		return fmt.Errorf("resource %s has invalid size", resource.Path)
	}
	if len(resource.SHA256) != sha256.Size*2 {
		return fmt.Errorf("resource %s has invalid sha256", resource.Path)
	}
	if _, err := hex.DecodeString(resource.SHA256); err != nil {
		return fmt.Errorf("resource %s has invalid sha256", resource.Path)
	}
	return nil
}

func validateRelative(value string) error {
	if strings.TrimSpace(value) == "" || filepath.IsAbs(value) || filepath.Clean(value) != value ||
		strings.ContainsRune(value, '\\') || strings.ContainsRune(value, 0) {
		return ErrInvalidPath
	}
	for _, part := range strings.Split(filepath.ToSlash(value), "/") {
		if part == "" || part == "." || part == ".." {
			return ErrInvalidPath
		}
	}
	return nil
}

func readManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("%w: decode: %v", ErrInvalidManifest, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Manifest{}, fmt.Errorf("%w: trailing JSON", ErrInvalidManifest)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// ValidatePackage validates a manifest and every declared resource without
// executing any package code. Symlinks are rejected at every path component.
func ValidatePackage(packageRoot string, target string) (Manifest, error) {
	root, err := cleanAbsolute(packageRoot)
	if err != nil {
		return Manifest{}, err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: %v", ErrNotInstalled, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Manifest{}, fmt.Errorf("%w: package root is not a directory", ErrInvalidPath)
	}
	manifest, err := readManifest(filepath.Join(root, ManifestName))
	if err != nil {
		return Manifest{}, err
	}
	if target != "" && len(manifest.Targets) > 0 && !contains(manifest.Targets, target) {
		return Manifest{}, fmt.Errorf("%w: target %q is not supported", ErrInvalidManifest, target)
	}
	for _, resource := range manifest.Resources {
		path, err := packagePath(root, resource.Path)
		if err != nil {
			return Manifest{}, err
		}
		stat, err := os.Stat(path)
		if err != nil {
			return Manifest{}, fmt.Errorf("%w: resource %s: %v", ErrInvalidManifest, resource.Path, err)
		}
		if !stat.Mode().IsRegular() || stat.Size() != resource.Size {
			return Manifest{}, fmt.Errorf("%w: resource %s metadata mismatch", ErrInvalidManifest, resource.Path)
		}
		digest, err := fileDigest(path)
		if err != nil || !strings.EqualFold(digest, resource.SHA256) {
			return Manifest{}, fmt.Errorf("%w: resource %s checksum mismatch", ErrInvalidManifest, resource.Path)
		}
	}
	declared := make(map[string]struct{}, len(manifest.Resources))
	for _, resource := range manifest.Resources {
		declared[resource.Path] = struct{}{}
	}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: symlink package entry", ErrInvalidPath)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		relative = filepath.ToSlash(relative)
		if relative == ManifestName {
			return nil
		}
		if _, ok := declared[relative]; !ok {
			return fmt.Errorf("%w: undeclared resource %s", ErrInvalidManifest, relative)
		}
		return nil
	})
	if err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func cleanAbsolute(value string) (string, error) {
	if strings.TrimSpace(value) == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return "", fmt.Errorf("%w: root must be an absolute clean path", ErrInvalidPath)
	}
	return value, nil
}

func packagePath(root, relative string) (string, error) {
	if err := validateRelative(relative); err != nil {
		return "", fmt.Errorf("%w: %s", ErrInvalidPath, relative)
	}
	joined := filepath.Join(root, filepath.FromSlash(relative))
	relativeToRoot, err := filepath.Rel(root, joined)
	if err != nil || relativeToRoot == ".." || strings.HasPrefix(relativeToRoot, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: resource escapes package root", ErrInvalidPath)
	}
	current := root
	for _, part := range strings.Split(relativeToRoot, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%w: symlink resource", ErrInvalidPath)
		}
	}
	return joined, nil
}

func fileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// ManifestDigest returns the digest of the package manifest itself. The
// launcher records this at install time so later edits to otherwise valid
// resources cannot change the entrypoint or permissions behind its back.
func ManifestDigest(packageRoot string) (string, error) {
	root, err := cleanAbsolute(packageRoot)
	if err != nil {
		return "", err
	}
	manifestPath, err := packagePath(root, ManifestName)
	if err != nil {
		return "", err
	}
	return fileDigest(manifestPath)
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// Package is the verified, service-owned entrypoint returned by Registry.
type Package struct {
	Root     string
	Manifest Manifest
	Entry    string
}

type State struct {
	Installed      bool   `json:"installed"`
	Verified       bool   `json:"verified"`
	Trusted        bool   `json:"trusted"`
	Enabled        bool   `json:"enabled"`
	Version        string `json:"version,omitempty"`
	Entry          string `json:"entry,omitempty"`
	SignedBy       string `json:"signed_by,omitempty"`
	ManifestSHA256 string `json:"manifest_sha256,omitempty"`
}

// LoadStates reads the launcher metadata without depending on launcher
// implementation types. A missing state file means no package is runnable.
func LoadStates(path string) (map[string]State, error) {
	result := make(map[string]State)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	var raw struct {
		Initialized bool                       `json:"initialized"`
		Components  map[string]json.RawMessage `json:"components"`
		Plugins     map[string]State           `json:"plugins"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("%w: state decode: %v", ErrInvalidManifest, err)
	}
	for id, state := range raw.Plugins {
		result[id] = state
	}
	return result, nil
}

type Registry struct {
	root   string
	target string
	items  map[string]Package
	state  map[string]State
}

// NewRegistry scans only direct adapter ID directories below root. A package
// is present in the catalog even when disabled or untrusted, but Resolve will
// refuse to return it until all lifecycle gates are satisfied.
func NewRegistry(root, target string, states map[string]State) (*Registry, error) {
	cleanRoot, err := cleanAbsolute(root)
	if err != nil {
		return nil, err
	}
	registry := &Registry{root: cleanRoot, target: target, items: make(map[string]Package), state: make(map[string]State)}
	for id, state := range states {
		registry.state[id] = state
	}
	entries, err := os.ReadDir(cleanRoot)
	if os.IsNotExist(err) {
		return registry, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !identifierPattern.MatchString(entry.Name()) {
			continue
		}
		packageRoot := filepath.Join(cleanRoot, entry.Name())
		manifest, validateErr := ValidatePackage(packageRoot, target)
		if validateErr != nil {
			continue
		}
		if manifest.ID != entry.Name() {
			continue
		}
		entryPath, pathErr := packagePath(packageRoot, manifest.Entry)
		if pathErr != nil {
			continue
		}
		registry.items[manifest.ID] = Package{Root: packageRoot, Manifest: manifest, Entry: entryPath}
	}
	return registry, nil
}

func (r *Registry) List() []Package {
	if r == nil {
		return nil
	}
	items := make([]Package, 0, len(r.items))
	for _, item := range r.items {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Manifest.ID < items[j].Manifest.ID })
	return items
}

func (r *Registry) Resolve(id string) (Package, error) {
	if r == nil || !identifierPattern.MatchString(id) {
		return Package{}, ErrInvalidManifest
	}
	state, hasState := r.state[id]
	item, ok := r.items[id]
	if !ok {
		if hasState && state.Installed {
			return Package{}, ErrNotVerified
		}
		return Package{}, ErrNotInstalled
	}
	if !state.Installed {
		return Package{}, ErrNotInstalled
	}
	manifest, err := ValidatePackage(item.Root, r.target)
	if err != nil || manifest.ID != item.Manifest.ID || manifest.Version != item.Manifest.Version {
		return Package{}, ErrNotVerified
	}
	if state.Version != "" && state.Version != manifest.Version {
		return Package{}, ErrNotVerified
	}
	if state.Entry != "" && state.Entry != manifest.Entry {
		return Package{}, ErrNotVerified
	}
	if state.SignedBy != "" && state.SignedBy != manifest.SignedBy {
		return Package{}, ErrNotVerified
	}
	if state.ManifestSHA256 != "" {
		digest, digestErr := ManifestDigest(item.Root)
		if digestErr != nil || !strings.EqualFold(digest, state.ManifestSHA256) {
			return Package{}, ErrNotVerified
		}
	}
	if !state.Verified {
		return Package{}, ErrNotVerified
	}
	if !state.Trusted {
		return Package{}, ErrNotTrusted
	}
	if !state.Enabled {
		return Package{}, ErrDisabled
	}
	return item, nil
}
