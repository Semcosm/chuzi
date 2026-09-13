package launcher

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const (
	defaultStateDirectory = ".chuzi"
	defaultStateName      = "launcher-state.json"
	pluginDirectory       = "plugins"
)

// PluginTrustPolicy is intentionally explicit. An installed plugin remains
// disabled and untrusted until its declared signer is accepted here.
type PluginTrustPolicy struct {
	AllowedSigners []string
	RequireSigned  bool
}

type ManagerOptions struct {
	InstallRoot string
	SourceRoot  string
	StatePath   string
	Manifest    ReleaseManifest
	Trust       PluginTrustPolicy
}

// FilesystemManager implements both management interfaces without exposing a
// UI or transport. Its state file is metadata only; plugin archives are never
// treated as trusted merely because they appear in a manifest.
type FilesystemManager struct {
	mu            sync.Mutex
	root          string
	source        string
	state         string
	manifest      ReleaseManifest
	trust         map[string]struct{}
	requireSigned bool
	data          managerState
}

type managerState struct {
	Components map[string]componentRecord `json:"components"`
	Plugins    map[string]pluginRecord    `json:"plugins"`
}

type componentRecord struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	Enabled   bool   `json:"enabled"`
}

type pluginRecord struct {
	Installed bool `json:"installed"`
	Enabled   bool `json:"enabled"`
	Trusted   bool `json:"trusted"`
}

type resourceSnapshot struct {
	path   string
	exists bool
	data   []byte
	mode   os.FileMode
}

func NewFilesystemManager(options ManagerOptions) (*FilesystemManager, error) {
	if err := options.Manifest.Validate(); err != nil {
		return nil, err
	}
	if options.InstallRoot == "" || !filepath.IsAbs(options.InstallRoot) {
		return nil, fmt.Errorf("%w: install root must be absolute", ErrInvalidPath)
	}
	if options.SourceRoot == "" {
		options.SourceRoot = options.InstallRoot
	}
	if !filepath.IsAbs(options.SourceRoot) {
		return nil, fmt.Errorf("%w: source root must be absolute", ErrInvalidPath)
	}
	if options.StatePath == "" {
		options.StatePath = filepath.Join(options.InstallRoot, defaultStateDirectory, defaultStateName)
	}
	if !filepath.IsAbs(options.StatePath) {
		return nil, fmt.Errorf("%w: state path must be absolute", ErrInvalidPath)
	}
	if relative, err := filepath.Rel(options.InstallRoot, options.StatePath); err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("%w: state path must stay under install root", ErrInvalidPath)
	}
	manager := &FilesystemManager{
		root: options.InstallRoot, source: options.SourceRoot, state: options.StatePath,
		manifest: options.Manifest, trust: make(map[string]struct{}),
		requireSigned: options.Trust.RequireSigned,
		data:          managerState{Components: make(map[string]componentRecord), Plugins: make(map[string]pluginRecord)},
	}
	for _, signer := range options.Trust.AllowedSigners {
		if strings.TrimSpace(signer) != "" {
			manager.trust[signer] = struct{}{}
		}
	}
	if err := manager.load(); err != nil {
		return nil, err
	}
	return manager, nil
}

func (m *FilesystemManager) load() error {
	data, err := os.ReadFile(m.state)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read launcher state: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var state managerState
	if err := decoder.Decode(&state); err != nil {
		return fmt.Errorf("decode launcher state: %w", err)
	}
	if state.Components == nil {
		state.Components = make(map[string]componentRecord)
	}
	if state.Plugins == nil {
		state.Plugins = make(map[string]pluginRecord)
	}
	m.data = state
	return nil
}

func (m *FilesystemManager) save() error {
	if err := os.MkdirAll(filepath.Dir(m.state), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(m.state), ".launcher-state-")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	ok := false
	defer func() {
		_ = temporary.Close()
		if !ok {
			_ = os.Remove(temporaryName)
		}
	}()
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(m.data); err != nil {
		return err
	}
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, m.state); err != nil {
		return err
	}
	ok = true
	return nil
}

func (m *FilesystemManager) List(ctx context.Context) ([]ComponentState, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	states := make([]ComponentState, 0, len(m.manifest.Components))
	for _, component := range m.manifest.Components {
		record := m.data.Components[component.ID]
		states = append(states, ComponentState{ID: component.ID, Installed: record.Installed, Version: record.Version, Enabled: record.Enabled, Required: component.Required, Health: m.componentHealth(component, record)})
	}
	sort.Slice(states, func(i, j int) bool { return states[i].ID < states[j].ID })
	return states, nil
}

func (m *FilesystemManager) componentHealth(component Component, record componentRecord) string {
	if !record.Installed {
		return HealthNotInstalled
	}
	if !record.Enabled {
		return HealthDisabled
	}
	for _, resource := range component.Resources {
		ok, err := verifyOne(context.Background(), mustJoin(m.root, resource.Path), resource)
		if err != nil || !ok {
			return HealthMissing
		}
	}
	return HealthHealthy
}

func (m *FilesystemManager) Install(ctx context.Context, id string) (ComponentState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := contextErr(ctx); err != nil {
		return ComponentState{}, err
	}
	if _, ok := m.component(id); !ok {
		return ComponentState{}, fmt.Errorf("%w: component %q", ErrNotFound, id)
	}
	previous := cloneState(m.data)
	snapshot, snapshotErr := m.snapshotResources()
	if snapshotErr != nil {
		return ComponentState{}, snapshotErr
	}
	visiting := make(map[string]bool)
	if err := m.installComponent(ctx, id, visiting); err != nil {
		m.restoreResources(snapshot)
		m.data = previous
		return ComponentState{}, err
	}
	if err := m.save(); err != nil {
		m.restoreResources(snapshot)
		m.data = previous
		return ComponentState{}, fmt.Errorf("%w: save component state: %v", ErrTransaction, err)
	}
	component, _ := m.component(id)
	record := m.data.Components[id]
	return ComponentState{ID: id, Installed: record.Installed, Version: record.Version, Enabled: record.Enabled, Required: component.Required, Health: m.componentHealth(component, record)}, nil
}

func (m *FilesystemManager) installComponent(ctx context.Context, id string, visiting map[string]bool) error {
	if visiting[id] {
		return fmt.Errorf("%w: component dependency cycle at %q", ErrInvalidManifest, id)
	}
	component, ok := m.component(id)
	if !ok {
		return fmt.Errorf("%w: component %q", ErrNotFound, id)
	}
	if record := m.data.Components[id]; record.Installed && record.Version == component.Version {
		healthy := true
		for _, resource := range component.Resources {
			ok, err := verifyOne(ctx, mustJoin(m.root, resource.Path), resource)
			if err != nil || !ok {
				healthy = false
				break
			}
		}
		if healthy {
			return nil
		}
	}
	visiting[id] = true
	defer delete(visiting, id)
	for _, dependency := range component.Dependencies {
		if err := m.installComponent(ctx, dependency, visiting); err != nil {
			return err
		}
	}
	componentForRepair := component
	componentForRepair.Dependencies = nil
	result, err := (FileRepairer{SourceRoot: m.source}).Repair(ctx, RepairRequest{InstallRoot: m.root, Manifest: ReleaseManifest{Format: m.manifest.Format, Channel: m.manifest.Channel, Version: m.manifest.Version, Target: m.manifest.Target, Components: []Component{componentForRepair}}})
	_ = result
	if err != nil {
		return err
	}
	m.data.Components[id] = componentRecord{Installed: true, Version: component.Version, Enabled: true}
	return nil
}

func (m *FilesystemManager) Remove(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := contextErr(ctx); err != nil {
		return err
	}
	component, ok := m.component(id)
	if !ok {
		return fmt.Errorf("%w: component %q", ErrNotFound, id)
	}
	if component.Required {
		return fmt.Errorf("%w: component %q", ErrRequired, id)
	}
	previous := cloneState(m.data)
	if err := os.MkdirAll(filepath.Dir(m.state), 0o700); err != nil {
		return fmt.Errorf("%w: create component rollback directory: %v", ErrTransaction, err)
	}
	backupDir, err := os.MkdirTemp(filepath.Join(m.root, defaultStateDirectory), ".component-remove-")
	if err != nil {
		return fmt.Errorf("%w: create component rollback: %v", ErrTransaction, err)
	}
	defer os.RemoveAll(backupDir)
	type removedResource struct{ target, backup string }
	var removed []removedResource
	for _, resource := range component.Resources {
		if m.resourceIsUsedByInstalledComponent(id, resource.Path) {
			continue
		}
		target := mustJoin(m.root, resource.Path)
		backup := filepath.Join(backupDir, fmt.Sprintf("%06d", len(removed)))
		if err := os.Rename(target, backup); err != nil && !os.IsNotExist(err) {
			for index := len(removed) - 1; index >= 0; index-- {
				_ = os.MkdirAll(filepath.Dir(removed[index].target), 0o755)
				_ = os.Rename(removed[index].backup, removed[index].target)
			}
			m.data = previous
			return fmt.Errorf("%w: remove component resource %s: %v", ErrTransaction, resource.Path, err)
		}
		if _, err := os.Stat(backup); err == nil {
			removed = append(removed, removedResource{target: target, backup: backup})
		}
	}
	delete(m.data.Components, id)
	if err := m.save(); err != nil {
		for index := len(removed) - 1; index >= 0; index-- {
			_ = os.MkdirAll(filepath.Dir(removed[index].target), 0o755)
			_ = os.Rename(removed[index].backup, removed[index].target)
		}
		m.data = previous
		return fmt.Errorf("%w: save component state: %v", ErrTransaction, err)
	}
	return nil
}

func (m *FilesystemManager) SetEnabled(ctx context.Context, id string, enabled bool) (ComponentState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := contextErr(ctx); err != nil {
		return ComponentState{}, err
	}
	component, ok := m.component(id)
	if !ok {
		return ComponentState{}, fmt.Errorf("%w: component %q", ErrNotFound, id)
	}
	if component.Required && !enabled {
		return ComponentState{}, fmt.Errorf("%w: component %q", ErrRequired, id)
	}
	record, ok := m.data.Components[id]
	if !ok || !record.Installed {
		return ComponentState{}, fmt.Errorf("%w: component %q is not installed", ErrNotFound, id)
	}
	previous := cloneState(m.data)
	record.Enabled = enabled
	m.data.Components[id] = record
	if err := m.save(); err != nil {
		m.data = previous
		return ComponentState{}, fmt.Errorf("%w: save component state: %v", ErrTransaction, err)
	}
	return ComponentState{ID: id, Installed: true, Version: record.Version, Enabled: enabled, Required: component.Required, Health: m.componentHealth(component, record)}, nil
}

func (m *FilesystemManager) ListPlugins(ctx context.Context) ([]PluginState, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	states := make([]PluginState, 0, len(m.manifest.Plugins))
	for _, descriptor := range m.manifest.Plugins {
		record := m.data.Plugins[descriptor.ID]
		states = append(states, PluginState{Descriptor: descriptor, Installed: record.Installed, Enabled: record.Enabled, Trusted: record.Trusted, Health: m.pluginHealth(descriptor, record)})
	}
	sort.Slice(states, func(i, j int) bool { return states[i].Descriptor.ID < states[j].Descriptor.ID })
	return states, nil
}

func (m *FilesystemManager) pluginHealth(descriptor PluginDescriptor, record pluginRecord) string {
	if !record.Installed {
		return HealthNotInstalled
	}
	if !record.Trusted {
		return HealthUntrusted
	}
	if !record.Enabled {
		return HealthDisabled
	}
	return HealthHealthy
}

func (m *FilesystemManager) InstallPlugin(ctx context.Context, id string) (PluginState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := contextErr(ctx); err != nil {
		return PluginState{}, err
	}
	descriptor, ok := m.plugin(id)
	if !ok {
		return PluginState{}, fmt.Errorf("%w: plugin %q", ErrNotFound, id)
	}
	if !descriptor.Installable || descriptor.Archive == "" {
		return PluginState{}, fmt.Errorf("%w: plugin %q is not installable", ErrUnsupported, id)
	}
	archivePath, err := safeJoin(m.source, descriptor.Archive)
	if err != nil {
		return PluginState{}, err
	}
	if descriptor.SHA256 != "" {
		actual, err := hashFile(ctx, archivePath)
		if err != nil || !strings.EqualFold(actual, descriptor.SHA256) {
			return PluginState{}, fmt.Errorf("%w: plugin archive %q", ErrInvalidManifest, id)
		}
	}
	pluginRoot, err := safeJoin(filepath.Join(m.root, pluginDirectory), id)
	if err != nil {
		return PluginState{}, err
	}
	if err := os.MkdirAll(filepath.Dir(pluginRoot), 0o700); err != nil {
		return PluginState{}, fmt.Errorf("create plugin directory: %w", err)
	}
	stageParent, err := os.MkdirTemp(filepath.Join(m.root, pluginDirectory), ".plugin-")
	if err != nil {
		return PluginState{}, fmt.Errorf("create plugin stage: %w", err)
	}
	defer os.RemoveAll(stageParent)
	stage := filepath.Join(stageParent, "payload")
	if err := extractArchive(ctx, archivePath, stage); err != nil {
		return PluginState{}, err
	}
	previous := cloneState(m.data)
	backup := pluginRoot + ".rollback"
	_ = os.RemoveAll(backup)
	if _, err := os.Lstat(pluginRoot); err == nil {
		if err := os.Rename(pluginRoot, backup); err != nil {
			return PluginState{}, fmt.Errorf("backup plugin: %w", err)
		}
	}
	if err := os.Rename(stage, pluginRoot); err != nil {
		_ = os.Rename(backup, pluginRoot)
		return PluginState{}, fmt.Errorf("install plugin: %w", err)
	}
	// A new archive always requires a fresh trust decision, even when the
	// plugin ID is unchanged.
	m.data.Plugins[id] = pluginRecord{Installed: true, Enabled: false, Trusted: false}
	if err := m.save(); err != nil {
		_ = os.RemoveAll(pluginRoot)
		_ = os.Rename(backup, pluginRoot)
		m.data = previous
		return PluginState{}, fmt.Errorf("%w: save plugin state: %v", ErrTransaction, err)
	}
	_ = os.RemoveAll(backup)
	record := m.data.Plugins[id]
	return PluginState{Descriptor: descriptor, Installed: true, Enabled: record.Enabled, Trusted: record.Trusted, Health: m.pluginHealth(descriptor, record)}, nil
}

func (m *FilesystemManager) RemovePlugin(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := contextErr(ctx); err != nil {
		return err
	}
	if _, ok := m.plugin(id); !ok {
		return fmt.Errorf("%w: plugin %q", ErrNotFound, id)
	}
	previous := cloneState(m.data)
	pluginRoot, err := safeJoin(filepath.Join(m.root, pluginDirectory), id)
	if err != nil {
		return err
	}
	backup := pluginRoot + ".rollback"
	_ = os.RemoveAll(backup)
	if _, err := os.Lstat(pluginRoot); err == nil {
		if err := os.Rename(pluginRoot, backup); err != nil {
			return fmt.Errorf("%w: remove plugin: %v", ErrTransaction, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("%w: inspect plugin: %v", ErrTransaction, err)
	}
	delete(m.data.Plugins, id)
	if err := m.save(); err != nil {
		_ = os.Rename(backup, pluginRoot)
		m.data = previous
		return fmt.Errorf("%w: save plugin state: %v", ErrTransaction, err)
	}
	_ = os.RemoveAll(backup)
	return nil
}

func (m *FilesystemManager) SetPluginEnabled(ctx context.Context, id string, enabled bool) (PluginState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := contextErr(ctx); err != nil {
		return PluginState{}, err
	}
	descriptor, ok := m.plugin(id)
	if !ok {
		return PluginState{}, fmt.Errorf("%w: plugin %q", ErrNotFound, id)
	}
	record, ok := m.data.Plugins[id]
	if !ok || !record.Installed {
		return PluginState{}, fmt.Errorf("%w: plugin %q is not installed", ErrNotFound, id)
	}
	if enabled && !record.Trusted {
		return PluginState{}, fmt.Errorf("%w: plugin %q", ErrNotTrusted, id)
	}
	previous := cloneState(m.data)
	record.Enabled = enabled
	m.data.Plugins[id] = record
	if err := m.save(); err != nil {
		m.data = previous
		return PluginState{}, fmt.Errorf("%w: save plugin state: %v", ErrTransaction, err)
	}
	return PluginState{Descriptor: descriptor, Installed: true, Enabled: enabled, Trusted: record.Trusted, Health: m.pluginHealth(descriptor, record)}, nil
}

func (m *FilesystemManager) SetTrusted(ctx context.Context, id string, trusted bool) (PluginState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := contextErr(ctx); err != nil {
		return PluginState{}, err
	}
	descriptor, ok := m.plugin(id)
	if !ok {
		return PluginState{}, fmt.Errorf("%w: plugin %q", ErrNotFound, id)
	}
	record, ok := m.data.Plugins[id]
	if !ok || !record.Installed {
		return PluginState{}, fmt.Errorf("%w: plugin %q is not installed", ErrNotFound, id)
	}
	if trusted {
		if m.requireSigned && descriptor.SignedBy == "" {
			return PluginState{}, fmt.Errorf("%w: plugin %q has no signer", ErrNotTrusted, id)
		}
		if descriptor.SignedBy == "" {
			return PluginState{}, fmt.Errorf("%w: plugin %q has no signer", ErrNotTrusted, id)
		}
		if _, allowed := m.trust[descriptor.SignedBy]; !allowed {
			return PluginState{}, fmt.Errorf("%w: signer %q is not allowed", ErrNotTrusted, descriptor.SignedBy)
		}
	}
	previous := cloneState(m.data)
	record.Trusted = trusted
	if !trusted {
		record.Enabled = false
	}
	m.data.Plugins[id] = record
	if err := m.save(); err != nil {
		m.data = previous
		return PluginState{}, fmt.Errorf("%w: save plugin trust: %v", ErrTransaction, err)
	}
	return PluginState{Descriptor: descriptor, Installed: record.Installed, Enabled: record.Enabled, Trusted: record.Trusted, Health: m.pluginHealth(descriptor, record)}, nil
}

// FilesystemComponentManager exposes component operations through the
// transport-neutral interface. A separate wrapper is required because Go
// cannot overload List/Install/Remove for the component and plugin result
// types.
type FilesystemComponentManager struct{ *FilesystemManager }

func NewFilesystemComponentManager(options ManagerOptions) (*FilesystemComponentManager, error) {
	manager, err := NewFilesystemManager(options)
	if err != nil {
		return nil, err
	}
	return &FilesystemComponentManager{FilesystemManager: manager}, nil
}

func NewComponentManager(options ManagerOptions) (ComponentManager, error) {
	return NewFilesystemComponentManager(options)
}

// FilesystemPluginManager exposes plugin operations through the plugin
// interface while sharing the same durable state format and lock.
type FilesystemPluginManager struct{ *FilesystemManager }

func NewFilesystemPluginManager(options ManagerOptions) (*FilesystemPluginManager, error) {
	manager, err := NewFilesystemManager(options)
	if err != nil {
		return nil, err
	}
	return &FilesystemPluginManager{FilesystemManager: manager}, nil
}

func NewPluginManager(options ManagerOptions) (PluginManager, error) {
	return NewFilesystemPluginManager(options)
}

func (m *FilesystemPluginManager) List(ctx context.Context) ([]PluginState, error) {
	return m.ListPlugins(ctx)
}
func (m *FilesystemPluginManager) Install(ctx context.Context, id string) (PluginState, error) {
	return m.InstallPlugin(ctx, id)
}
func (m *FilesystemPluginManager) Remove(ctx context.Context, id string) error {
	return m.RemovePlugin(ctx, id)
}
func (m *FilesystemPluginManager) SetEnabled(ctx context.Context, id string, enabled bool) (PluginState, error) {
	return m.SetPluginEnabled(ctx, id, enabled)
}

func (m *FilesystemManager) component(id string) (Component, bool) {
	for _, component := range m.manifest.Components {
		if component.ID == id {
			return component, true
		}
	}
	return Component{}, false
}

func (m *FilesystemManager) plugin(id string) (PluginDescriptor, bool) {
	for _, plugin := range m.manifest.Plugins {
		if plugin.ID == id {
			return plugin, true
		}
	}
	return PluginDescriptor{}, false
}

func (m *FilesystemManager) resourceIsUsedByInstalledComponent(excluded, resourcePath string) bool {
	for _, component := range m.manifest.Components {
		if component.ID == excluded || !m.data.Components[component.ID].Installed {
			continue
		}
		for _, resource := range component.Resources {
			if resource.Path == resourcePath {
				return true
			}
		}
	}
	return false
}

func (m *FilesystemManager) snapshotResources() (map[string]resourceSnapshot, error) {
	snapshot := make(map[string]resourceSnapshot)
	for _, component := range m.manifest.Components {
		for _, resource := range component.Resources {
			if _, seen := snapshot[resource.Path]; seen {
				continue
			}
			entry := resourceSnapshot{path: mustJoin(m.root, resource.Path)}
			info, err := os.Lstat(entry.path)
			if os.IsNotExist(err) {
				snapshot[resource.Path] = entry
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("snapshot resource %s: %w", resource.Path, err)
			}
			entry.exists = true
			entry.mode = info.Mode()
			if info.Mode().IsRegular() {
				entry.data, err = os.ReadFile(entry.path)
				if err != nil {
					return nil, fmt.Errorf("snapshot resource %s: %w", resource.Path, err)
				}
			}
			snapshot[resource.Path] = entry
		}
	}
	return snapshot, nil
}

func (m *FilesystemManager) restoreResources(snapshot map[string]resourceSnapshot) {
	for _, entry := range snapshot {
		_ = os.Remove(entry.path)
		if !entry.exists || !entry.mode.IsRegular() {
			continue
		}
		if os.MkdirAll(filepath.Dir(entry.path), 0o755) != nil {
			continue
		}
		if os.WriteFile(entry.path, entry.data, entry.mode.Perm()) != nil {
			continue
		}
	}
}

func mustJoin(root, relative string) string {
	joined, _ := safeJoin(root, relative)
	return joined
}

func cloneState(state managerState) managerState {
	clone := managerState{Components: make(map[string]componentRecord, len(state.Components)), Plugins: make(map[string]pluginRecord, len(state.Plugins))}
	for id, record := range state.Components {
		clone.Components[id] = record
	}
	for id, record := range state.Plugins {
		clone.Plugins[id] = record
	}
	return clone
}

func extractArchive(ctx context.Context, archivePath, destination string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if strings.HasSuffix(strings.ToLower(archivePath), ".zip") {
		return extractZip(ctx, archivePath, destination)
	}
	return extractTarGz(ctx, archivePath, destination)
}

func extractZip(ctx context.Context, archivePath, destination string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer reader.Close()
	for _, entry := range reader.File {
		if err := contextErr(ctx); err != nil {
			return err
		}
		if err := validateRelativePath(entry.Name); err != nil {
			return err
		}
		target, _ := safeJoin(destination, entry.Name)
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			continue
		}
		if !entry.FileInfo().Mode().IsRegular() {
			return fmt.Errorf("%w: archive entry %q is not regular", ErrInvalidManifest, entry.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		input, err := entry.Open()
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
		if err == nil {
			_, err = io.Copy(output, input)
			_ = output.Close()
		}
		_ = input.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func extractTarGz(ctx context.Context, archivePath, destination string) error {
	input, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer input.Close()
	compressed, err := gzip.NewReader(input)
	if err != nil {
		return err
	}
	defer compressed.Close()
	reader := tar.NewReader(compressed)
	for {
		if err := contextErr(ctx); err != nil {
			return err
		}
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := validateRelativePath(header.Name); err != nil {
			return err
		}
		target, _ := safeJoin(destination, header.Name)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(output, reader)
			closeErr := output.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("%w: archive entry %q is not a regular file", ErrInvalidManifest, header.Name)
		}
	}
}

var _ ComponentManager = (*FilesystemComponentManager)(nil)
var _ PluginManager = (*FilesystemPluginManager)(nil)
