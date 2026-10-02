package environment

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	ErrNotInstalled         = errors.New("environment: package is not installed")
	ErrNotVerified          = errors.New("environment: package is not verified")
	ErrNotTrusted           = errors.New("environment: package is not trusted")
	ErrDisabled             = errors.New("environment: package is disabled")
	ErrNotHealthy           = errors.New("environment: package is not healthy")
	ErrNotReady             = errors.New("environment: package is not ready")
	ErrRollbackUnavailable  = errors.New("environment: rollback is unavailable")
	ErrTransaction          = errors.New("environment: transaction rolled back")
	ErrExternalModification = errors.New("environment: package was modified externally")
)

// Record is the durable lifecycle projection. A successful install never
// implies trust, enablement, health, or readiness.
type Record struct {
	EnvironmentID  string    `json:"environment_id"`
	Version        string    `json:"version"`
	Capabilities   []string  `json:"capabilities,omitempty"`
	ManifestDigest string    `json:"manifest_digest"`
	Signer         string    `json:"signer"`
	Installed      bool      `json:"installed"`
	Verified       bool      `json:"verified"`
	Trusted        bool      `json:"trusted"`
	Enabled        bool      `json:"enabled"`
	Healthy        bool      `json:"healthy"`
	Ready          bool      `json:"ready"`
	Generation     uint64    `json:"generation"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (r Record) Validate() error {
	if !validEnvironmentID(r.EnvironmentID) || !validEnvironmentVersion(r.Version) || !validToken(r.Signer, 256) || r.Generation == 0 || r.UpdatedAt.IsZero() || !r.ManifestDigestValid() {
		return ErrInvalidManifest
	}
	if err := uniqueTokens(r.Capabilities, 128); err != nil {
		return ErrInvalidManifest
	}
	if r.Verified && !r.Installed {
		return ErrNotVerified
	}
	if r.Trusted && !r.Verified {
		return ErrNotTrusted
	}
	if r.Enabled && !r.Trusted {
		return ErrDisabled
	}
	if r.Healthy && !r.Verified {
		return ErrNotHealthy
	}
	if r.Ready && (!r.Installed || !r.Verified || !r.Trusted || !r.Enabled || !r.Healthy) {
		return ErrNotReady
	}
	return nil
}
func (r Record) ManifestDigestValid() bool {
	if len(r.ManifestDigest) != 64 {
		return false
	}
	_, err := hex.DecodeString(r.ManifestDigest)
	return err == nil
}
func (r Record) IsReady() bool {
	return r.Ready && r.Installed && r.Verified && r.Trusted && r.Enabled && r.Healthy
}

// Package is an opaque, verified runtime selection returned to the slot layer.
type Package struct {
	Root     string
	Manifest Manifest
	Record   Record
}

// RecordSink is the persistence boundary used to promote manager-verified
// lifecycle records into the control-service store. Keeping the interface
// here avoids a package dependency from the filesystem manager back into the
// bbolt implementation.
type RecordSink interface {
	PutEnvironmentRecord(Record) error
}
type HealthChecker func(context.Context, Manifest, string) error
type Options struct {
	InstallRoot string
	StatePath   string
	Target      string
	Trust       TrustStore
	Health      HealthChecker
	Clock       func() time.Time
}

type Manager struct {
	mu                      sync.Mutex
	root, statePath, target string
	trust                   TrustStore
	health                  HealthChecker
	clock                   func() time.Time
	records                 map[string]Record
	manifests               map[string]Manifest
}

type stateFile struct {
	Records map[string]Record `json:"records"`
}

func NewManager(options Options) (*Manager, error) {
	if options.InstallRoot == "" || !filepath.IsAbs(options.InstallRoot) {
		return nil, ErrInvalidPath
	}
	if options.StatePath == "" {
		options.StatePath = filepath.Join(options.InstallRoot, ".chuzi", "environment-state.json")
	}
	if !filepath.IsAbs(options.StatePath) {
		return nil, ErrInvalidPath
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	m := &Manager{root: filepath.Clean(options.InstallRoot), statePath: filepath.Clean(options.StatePath), target: options.Target, trust: options.Trust, health: options.Health, clock: options.Clock, records: map[string]Record{}, manifests: map[string]Manifest{}}
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
}
func key(id, version string) string { return id + "@" + version }
func (m *Manager) load() error {
	data, err := os.ReadFile(m.statePath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var s stateFile
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	if err := d.Decode(&s); err != nil {
		return err
	}
	var trailing any
	if err := d.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("environment: state contains trailing JSON")
		}
		return fmt.Errorf("environment: state trailing content: %v", err)
	}
	for k, r := range s.Records {
		if err := r.Validate(); err != nil {
			return fmt.Errorf("environment: invalid state %s: %w", k, err)
		}
		if k != key(r.EnvironmentID, r.Version) {
			return fmt.Errorf("environment: invalid state key %s", k)
		}
		m.records[k] = r
	}
	return nil
}
func (m *Manager) save() error {
	if err := os.MkdirAll(filepath.Dir(m.statePath), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(m.statePath), ".environment-state-")
	if err != nil {
		return err
	}
	name := f.Name()
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(name)
		}
	}()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(stateFile{Records: m.records}); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, m.statePath); err != nil {
		return err
	}
	ok = true
	return nil
}
func (m *Manager) packageRoot(id, version string) (string, error) {
	if !validEnvironmentID(id) || !validEnvironmentVersion(version) {
		return "", ErrInvalidPath
	}
	p := filepath.Join(m.root, "environments", id, version)
	abs, _ := filepath.Abs(p)
	base, _ := filepath.Abs(filepath.Join(m.root, "environments"))
	rel, err := filepath.Rel(base, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrInvalidPath
	}
	return p, nil
}

// Install validates and atomically stages a package. Existing versions remain
// untouched until the new directory and state projection are committed.
func (m *Manager) Install(ctx context.Context, source string) (Record, error) {
	if ctx == nil {
		return Record{}, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	manifest, err := ValidatePackage(source, m.target, m.trust)
	if err != nil {
		return Record{}, err
	}
	if _, trusted := m.trust[manifest.Signer]; !trusted {
		return Record{}, ErrUntrustedSigner
	}
	if err := manifest.VerifySignature(m.trust[manifest.Signer]); err != nil {
		return Record{}, err
	}
	if err := manifest.VerifyDigest(); err != nil {
		return Record{}, err
	}
	root, err := m.packageRoot(manifest.EnvironmentID, manifest.Version)
	if err != nil {
		return Record{}, err
	}
	if err := os.MkdirAll(filepath.Dir(root), 0700); err != nil {
		return Record{}, err
	}
	knownExisting := false
	if existing, ok := m.records[key(manifest.EnvironmentID, manifest.Version)]; ok {
		knownExisting = true
		if _, statErr := os.Stat(root); statErr != nil {
			if os.IsNotExist(statErr) {
				return Record{}, ErrExternalModification
			}
			return Record{}, statErr
		}
		if err := m.verifyExistingLocked(manifest.EnvironmentID, manifest.Version, root); err != nil {
			return Record{}, err
		}
		if strings.EqualFold(existing.ManifestDigest, manifest.ManifestDigest) && existing.Signer == manifest.Signer {
			return existing, nil
		}
	}
	if !knownExisting {
		if _, statErr := os.Stat(root); statErr == nil {
			return Record{}, ErrExternalModification
		} else if !os.IsNotExist(statErr) {
			return Record{}, statErr
		}
	}
	stage, err := os.MkdirTemp(filepath.Dir(root), ".environment-install-")
	if err != nil {
		return Record{}, err
	}
	defer os.RemoveAll(stage)
	if err := copyTreeContext(ctx, source, stage); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Record{}, ctxErr
		}
		return Record{}, fmt.Errorf("%w: stage package: %v", ErrTransaction, err)
	}
	stagedManifest, err := ValidatePackage(stage, m.target, m.trust)
	if err != nil || stagedManifest.ManifestDigest != manifest.ManifestDigest || stagedManifest.Signer != manifest.Signer {
		return Record{}, fmt.Errorf("%w: staged package validation", ErrTransaction)
	}
	old := root + ".rollback"
	if _, oldErr := os.Lstat(old); oldErr == nil {
		// A leftover rollback directory may contain files from an interrupted
		// update. Do not delete or adopt it without an operator recovery.
		return Record{}, ErrExternalModification
	} else if !os.IsNotExist(oldErr) {
		return Record{}, oldErr
	}
	hadOld := false
	if _, err := os.Stat(root); err == nil {
		if err := m.verifyExistingLocked(manifest.EnvironmentID, manifest.Version, root); err != nil {
			return Record{}, err
		}
		if err := os.Rename(root, old); err != nil {
			return Record{}, fmt.Errorf("%w: preserve previous version", ErrTransaction)
		}
		hadOld = true
	}
	if err := os.Rename(stage, root); err != nil {
		if hadOld {
			_ = os.Rename(old, root)
		}
		return Record{}, fmt.Errorf("%w: commit package", ErrTransaction)
	}
	now := m.clock().UTC()
	r := Record{EnvironmentID: manifest.EnvironmentID, Version: manifest.Version, Capabilities: append([]string(nil), manifest.Capabilities...), ManifestDigest: manifest.ManifestDigest, Signer: manifest.Signer, Installed: true, Verified: true, Generation: nextGeneration(m.records, manifest.EnvironmentID), UpdatedAt: now}
	packageKey := key(manifest.EnvironmentID, manifest.Version)
	previous, had := m.records[packageKey]
	previousManifest, hadManifest := m.manifests[packageKey]
	m.records[key(manifest.EnvironmentID, manifest.Version)] = r
	m.manifests[key(manifest.EnvironmentID, manifest.Version)] = manifest
	if err := m.save(); err != nil {
		m.records[packageKey] = previous
		if !had {
			delete(m.records, packageKey)
		}
		if hadManifest {
			m.manifests[packageKey] = previousManifest
		} else {
			delete(m.manifests, packageKey)
		}
		// Do not remove a tree that changed after the atomic rename. Leave it
		// for operator recovery rather than deleting unknown files.
		if checked, checkErr := ValidatePackage(root, m.target, m.trust); checkErr != nil || checked.ManifestDigest != manifest.ManifestDigest {
			return Record{}, fmt.Errorf("%w: package changed during rollback", ErrExternalModification)
		}
		_ = os.RemoveAll(root)
		if hadOld {
			_ = os.Rename(old, root)
		}
		return Record{}, fmt.Errorf("%w: state commit", ErrTransaction)
	}
	_ = os.RemoveAll(old)
	return r, nil
}

// Upgrade installs a new signed package while preserving the currently
// installed version. Versions are isolated under their own service-owned
// roots, so a failed upgrade cannot replace the active package. Repeating the
// same upgrade is idempotent through Install.
func (m *Manager) Upgrade(ctx context.Context, source string) (Record, error) {
	return m.Install(ctx, source)
}
func nextGeneration(records map[string]Record, id string) uint64 {
	var n uint64
	for _, r := range records {
		if r.EnvironmentID == id && r.Generation > n {
			n = r.Generation
		}
	}
	return n + 1
}
func copyTree(src, dst string) error {
	return copyTreeContext(context.Background(), src, dst)
}

func copyTreeContext(ctx context.Context, src, dst string) error {
	if ctx == nil {
		return context.Canceled
	}
	return filepath.WalkDir(src, func(path string, e os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if e.Type()&os.ModeSymlink != 0 || e.Type()&os.ModeIrregular != 0 {
			return ErrInvalidPath
		}
		target := filepath.Join(dst, rel)
		if e.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		if !e.Type().IsRegular() {
			return ErrInvalidPath
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, cpErr := io.Copy(out, in)
		closeErr := out.Close()
		if cpErr != nil {
			return cpErr
		}
		return closeErr
	})
}

func (m *Manager) Get(id, version string) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[key(id, version)]
	if !ok {
		return Record{}, ErrNotInstalled
	}
	return r, nil
}
func (m *Manager) List() []Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Record, 0, len(m.records))
	for _, r := range m.records {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].EnvironmentID != out[j].EnvironmentID {
			return out[i].EnvironmentID < out[j].EnvironmentID
		}
		return out[i].Version < out[j].Version
	})
	return out
}

// PromoteReady persists only records that are still backed by a valid signed
// package tree. A caller cannot promote a merely installed or manually edited
// state file into the scheduler's ready pool.
func (m *Manager) PromoteReady(sink RecordSink) error {
	if sink == nil {
		return ErrTransaction
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.records))
	for k := range m.records {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		record := m.records[k]
		if !record.IsReady() {
			continue
		}
		if _, err := m.currentManifestLocked(record.EnvironmentID, record.Version); err != nil {
			return ErrExternalModification
		}
		if err := sink.PutEnvironmentRecord(record); err != nil {
			return fmt.Errorf("%w: promote environment record", ErrTransaction)
		}
	}
	return nil
}
func (m *Manager) SetTrusted(id, version string, trusted bool) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := key(id, version)
	r, ok := m.records[k]
	if !ok {
		return Record{}, ErrNotInstalled
	}
	if !r.Verified {
		return Record{}, ErrNotVerified
	}
	if _, err := m.currentManifestLocked(id, version); err != nil {
		r.Verified = false
		r.Trusted, r.Enabled, r.Healthy, r.Ready = false, false, false, false
		if commitErr := m.commitRecord(k, r); commitErr != nil {
			return Record{}, commitErr
		}
		return Record{}, ErrNotVerified
	}
	if trusted {
		if _, ok := m.trust[r.Signer]; !ok {
			return Record{}, ErrUntrustedSigner
		}
	}
	r.Trusted = trusted
	if !trusted {
		r.Enabled = false
		r.Healthy = false
		r.Ready = false
	}
	r.UpdatedAt = m.clock().UTC()
	if err := m.commitRecord(k, r); err != nil {
		return Record{}, err
	}
	return r, nil
}
func (m *Manager) SetEnabled(id, version string, enabled bool) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := key(id, version)
	r, ok := m.records[k]
	if !ok {
		return Record{}, ErrNotInstalled
	}
	if enabled && !r.Trusted {
		return Record{}, ErrNotTrusted
	}
	if enabled {
		if _, err := m.currentManifestLocked(id, version); err != nil {
			r.Verified, r.Trusted, r.Enabled, r.Healthy, r.Ready = false, false, false, false, false
			if commitErr := m.commitRecord(k, r); commitErr != nil {
				return Record{}, commitErr
			}
			return Record{}, ErrNotVerified
		}
	}
	r.Enabled = enabled
	if !enabled {
		r.Ready = false
	}
	r.UpdatedAt = m.clock().UTC()
	if err := m.commitRecord(k, r); err != nil {
		return Record{}, err
	}
	return r, nil
}
func (m *Manager) HealthCheck(ctx context.Context, id, version string) (Record, error) {
	if ctx == nil {
		return Record{}, context.Canceled
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	k := key(id, version)
	r, ok := m.records[k]
	if !ok {
		return Record{}, ErrNotInstalled
	}
	if !r.Verified {
		return Record{}, ErrNotVerified
	}
	root, err := m.packageRoot(id, version)
	if err != nil {
		return Record{}, err
	}
	info, statErr := os.Lstat(root)
	if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		r.Installed, r.Verified, r.Trusted, r.Enabled, r.Healthy, r.Ready = false, false, false, false, false, false
		r.UpdatedAt = m.clock().UTC()
		if commitErr := m.commitRecord(k, r); commitErr != nil {
			return Record{}, commitErr
		}
		return r, ErrNotInstalled
	}
	manifest, err := m.currentManifestLocked(id, version)
	if err != nil {
		r.Verified, r.Trusted, r.Enabled, r.Healthy, r.Ready = false, false, false, false, false
		if commitErr := m.commitRecord(k, r); commitErr != nil {
			return Record{}, commitErr
		}
		return r, ErrNotVerified
	}
	if m.health != nil {
		if err := m.health(ctx, manifest, root); err != nil {
			r.Healthy = false
			r.Ready = false
			r.UpdatedAt = m.clock().UTC()
			if commitErr := m.commitRecord(k, r); commitErr != nil {
				return Record{}, commitErr
			}
			return r, ErrNotHealthy
		}
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	r.Healthy = true
	r.Ready = r.Installed && r.Verified && r.Trusted && r.Enabled
	r.UpdatedAt = m.clock().UTC()
	if err := m.commitRecord(k, r); err != nil {
		return Record{}, err
	}
	if !r.Ready {
		return r, ErrNotReady
	}
	return r, nil
}

func (m *Manager) currentManifestLocked(id, version string) (Manifest, error) {
	k := key(id, version)
	record, ok := m.records[k]
	if !ok {
		return Manifest{}, ErrNotInstalled
	}
	root, err := m.packageRoot(id, version)
	if err != nil {
		return Manifest{}, err
	}
	manifest, err := ValidatePackage(root, m.target, m.trust)
	if err != nil {
		return Manifest{}, err
	}
	if err := m.trust.Verify(manifest); err != nil {
		return Manifest{}, err
	}
	if manifest.ManifestDigest == "" || !strings.EqualFold(manifest.ManifestDigest, record.ManifestDigest) || manifest.Signer != record.Signer {
		return Manifest{}, ErrExternalModification
	}
	m.manifests[k] = manifest
	return manifest, nil
}
func (m *Manager) commitRecord(k string, r Record) error {
	old := m.records[k]
	m.records[k] = r
	if err := m.save(); err != nil {
		m.records[k] = old
		return fmt.Errorf("%w: state update", ErrTransaction)
	}
	return nil
}
func (m *Manager) Resolve(id, version, entry string) (Package, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := key(id, version)
	r, ok := m.records[k]
	if !ok {
		return Package{}, ErrNotInstalled
	}
	if !r.Verified {
		return Package{}, ErrNotVerified
	}
	if !r.Trusted {
		return Package{}, ErrNotTrusted
	}
	if !r.Enabled {
		return Package{}, ErrDisabled
	}
	if !r.Healthy {
		return Package{}, ErrNotHealthy
	}
	if !r.IsReady() {
		return Package{}, ErrNotReady
	}
	manifest, err := m.currentManifestLocked(id, version)
	if err != nil || !strings.EqualFold(manifest.ManifestDigest, r.ManifestDigest) || manifest.Signer != r.Signer {
		r.Verified, r.Trusted, r.Enabled, r.Healthy, r.Ready = false, false, false, false, false
		_ = m.commitRecord(k, r)
		return Package{}, ErrNotVerified
	}
	var selected Entrypoint
	for _, e := range manifest.Entrypoints {
		if e.Name == entry {
			selected = e
			break
		}
	}
	if selected.Path == "" {
		return Package{}, ErrInvalidManifest
	}
	root, err := m.packageRoot(id, version)
	if err != nil {
		return Package{}, err
	}
	return Package{Root: root, Manifest: manifest, Record: r}, nil
}

// ResolveRuntime selects exactly one manifest entrypoint for a closed runtime
// kind. Callers cannot supply a filesystem path or silently fall back to an
// unverified package entry.
func (m *Manager) ResolveRuntime(id, version, runtime string) (Package, Entrypoint, error) {
	if !validRuntime(runtime) {
		return Package{}, Entrypoint{}, ErrInvalidManifest
	}
	packageValue, err := m.Resolve(id, version, "__runtime_probe__")
	if err != nil && !errors.Is(err, ErrInvalidManifest) {
		return Package{}, Entrypoint{}, err
	}
	// Resolve performs all lifecycle and package integrity checks, but the
	// synthetic entry name above is intentionally not required to exist.
	// Re-run the shared checks while selecting the runtime entrypoint.
	m.mu.Lock()
	defer m.mu.Unlock()
	k := key(id, version)
	record, ok := m.records[k]
	if !ok {
		return Package{}, Entrypoint{}, ErrNotInstalled
	}
	if !record.IsReady() {
		return Package{}, Entrypoint{}, ErrNotReady
	}
	manifest, err := m.currentManifestLocked(id, version)
	if err != nil {
		return Package{}, Entrypoint{}, ErrNotVerified
	}
	var selected Entrypoint
	for _, entry := range manifest.Entrypoints {
		if entry.Runtime != runtime {
			continue
		}
		if selected.Path != "" {
			return Package{}, Entrypoint{}, fmt.Errorf("%w: multiple %s entrypoints", ErrInvalidManifest, runtime)
		}
		selected = entry
	}
	if selected.Path == "" {
		return Package{}, Entrypoint{}, fmt.Errorf("%w: runtime entrypoint unavailable", ErrInvalidManifest)
	}
	root, err := m.packageRoot(id, version)
	if err != nil {
		return Package{}, Entrypoint{}, err
	}
	packageValue = Package{Root: root, Manifest: manifest, Record: record}
	return packageValue, selected, nil
}

// ResolveEntrypoint selects a named, manifest-declared entrypoint and checks
// that its runtime kind matches the caller's closed protocol boundary.
func (m *Manager) ResolveEntrypoint(id, version, name, runtime string) (Package, Entrypoint, error) {
	if !validRuntime(runtime) || !validToken(name, 128) {
		return Package{}, Entrypoint{}, ErrInvalidManifest
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := key(id, version)
	record, ok := m.records[k]
	if !ok {
		return Package{}, Entrypoint{}, ErrNotInstalled
	}
	if !record.IsReady() {
		return Package{}, Entrypoint{}, ErrNotReady
	}
	manifest, err := m.currentManifestLocked(id, version)
	if err != nil {
		return Package{}, Entrypoint{}, ErrNotVerified
	}
	for _, entry := range manifest.Entrypoints {
		if entry.Name == name {
			if entry.Runtime != runtime {
				return Package{}, Entrypoint{}, ErrInvalidManifest
			}
			root, rootErr := m.packageRoot(id, version)
			if rootErr != nil {
				return Package{}, Entrypoint{}, rootErr
			}
			return Package{Root: root, Manifest: manifest, Record: record}, entry, nil
		}
	}
	return Package{}, Entrypoint{}, ErrInvalidManifest
}

// SyncRecords mirrors every manager record into the durable Store authority.
// This is used after trust, enable, health, disable, and removal operations so
// a previously ready Store record cannot outlive a revoked manager state.
type RecordCatalog interface {
	RecordSink
	ListEnvironmentRecords() ([]Record, error)
	DeleteEnvironmentRecord(string, string) error
}

func (m *Manager) SyncRecords(sink RecordSink) error {
	if sink == nil {
		return ErrTransaction
	}
	m.mu.Lock()
	records := make(map[string]Record, len(m.records))
	for k, record := range m.records {
		records[k] = record
	}
	m.mu.Unlock()
	keys := make([]string, 0, len(records))
	for k := range records {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := sink.PutEnvironmentRecord(records[k]); err != nil {
			return fmt.Errorf("%w: sync environment record", ErrTransaction)
		}
	}
	if catalog, ok := sink.(RecordCatalog); ok {
		existing, err := catalog.ListEnvironmentRecords()
		if err != nil {
			return fmt.Errorf("%w: list environment records", ErrTransaction)
		}
		for _, record := range existing {
			if _, ok := records[key(record.EnvironmentID, record.Version)]; ok {
				continue
			}
			if err := catalog.DeleteEnvironmentRecord(record.EnvironmentID, record.Version); err != nil {
				return fmt.Errorf("%w: remove stale environment record", ErrTransaction)
			}
		}
	}
	return nil
}
func (m *Manager) Remove(id, version string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := key(id, version)
	r, ok := m.records[k]
	root, err := m.packageRoot(id, version)
	if err != nil {
		return err
	}
	if !ok {
		if _, statErr := os.Lstat(root); os.IsNotExist(statErr) {
			return nil
		}
		return ErrExternalModification
	}
	if r.Ready {
		return ErrNotReady
	}
	if err := m.verifyExistingLocked(id, version, root); err != nil {
		return err
	}
	previousManifest, hadManifest := m.manifests[k]
	removed := root + ".remove"
	if _, statErr := os.Lstat(removed); statErr == nil {
		return ErrExternalModification
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	if err := os.Rename(root, removed); err != nil {
		return fmt.Errorf("%w: remove", ErrTransaction)
	}
	delete(m.records, k)
	delete(m.manifests, k)
	if err := m.save(); err != nil {
		m.records[k] = r
		if hadManifest {
			m.manifests[k] = previousManifest
		}
		if restoreErr := os.Rename(removed, root); restoreErr != nil {
			return fmt.Errorf("%w: state update and restore package", ErrTransaction)
		}
		return fmt.Errorf("%w: state update", ErrTransaction)
	}
	if err := os.RemoveAll(removed); err != nil {
		return fmt.Errorf("%w: finalize remove", ErrTransaction)
	}
	return nil
}

// Rollback restores an interrupted installation from the service-owned
// rollback directory. Both the current and rollback trees must validate as
// signed packages before any rename, so unknown files are never overwritten.
// A rollback is deliberately untrusted until an operator re-enables it.
func (m *Manager) Rollback(id, version string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := key(id, version)
	current, ok := m.records[k]
	if !ok {
		return ErrRollbackUnavailable
	}
	if current.Ready {
		return ErrNotReady
	}
	root, err := m.packageRoot(id, version)
	if err != nil {
		return err
	}
	rollback := root + ".rollback"
	if _, err := os.Lstat(rollback); err != nil {
		if os.IsNotExist(err) {
			return ErrRollbackUnavailable
		}
		return err
	}
	currentManifest, err := ValidatePackage(root, m.target, m.trust)
	if err != nil || currentManifest.EnvironmentID != id || currentManifest.Version != version {
		return ErrExternalModification
	}
	if err := m.trust.Verify(currentManifest); err != nil {
		return ErrExternalModification
	}
	previousManifest, err := ValidatePackage(rollback, m.target, m.trust)
	if err != nil || previousManifest.EnvironmentID != id || previousManifest.Version != version {
		return ErrExternalModification
	}
	if err := m.trust.Verify(previousManifest); err != nil {
		return ErrExternalModification
	}
	if strings.EqualFold(currentManifest.ManifestDigest, previousManifest.ManifestDigest) {
		return ErrExternalModification
	}
	staging := root + ".rollback-current"
	if _, err := os.Lstat(staging); err == nil {
		return ErrExternalModification
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(root, staging); err != nil {
		return fmt.Errorf("%w: preserve current package", ErrTransaction)
	}
	if err := os.Rename(rollback, root); err != nil {
		_ = os.Rename(staging, root)
		return fmt.Errorf("%w: restore rollback package", ErrTransaction)
	}
	previous := current
	previousCached, hadCached := m.manifests[k]
	current = Record{
		EnvironmentID:  previousManifest.EnvironmentID,
		Version:        previousManifest.Version,
		Capabilities:   append([]string(nil), previousManifest.Capabilities...),
		ManifestDigest: previousManifest.ManifestDigest,
		Signer:         previousManifest.Signer,
		Installed:      true,
		Verified:       true,
		Generation:     nextGeneration(m.records, id),
		UpdatedAt:      m.clock().UTC(),
	}
	m.records[k] = current
	m.manifests[k] = previousManifest
	if err := m.save(); err != nil {
		m.records[k] = previous
		if hadCached {
			m.manifests[k] = previousCached
		} else {
			delete(m.manifests, k)
		}
		_ = os.Rename(root, rollback)
		_ = os.Rename(staging, root)
		return fmt.Errorf("%w: rollback state", ErrTransaction)
	}
	if err := os.RemoveAll(staging); err != nil {
		return fmt.Errorf("%w: finalize rollback", ErrTransaction)
	}
	return nil
}

// verifyExistingLocked proves that a package root still matches its durable
// record before a mutating operation can replace or remove it. This prevents
// cleanup and upgrades from deleting files that were introduced outside the
// service. The caller must hold m.mu.
func (m *Manager) verifyExistingLocked(id, version, root string) error {
	r, ok := m.records[key(id, version)]
	if !ok {
		return ErrExternalModification
	}
	manifest, err := ValidatePackage(root, m.target, m.trust)
	if err != nil || manifest.EnvironmentID != id || manifest.Version != version || !strings.EqualFold(manifest.ManifestDigest, r.ManifestDigest) || manifest.Signer != r.Signer {
		return ErrExternalModification
	}
	if err := m.trust.Verify(manifest); err != nil {
		return ErrExternalModification
	}
	return nil
}
