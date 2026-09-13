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
	pathpkg "path"
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
	Progress    ProgressReporter
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
	progress      ProgressReporter
	data          managerState
}

type managerState struct {
	Initialized bool                       `json:"initialized"`
	Components  map[string]componentRecord `json:"components"`
	Plugins     map[string]pluginRecord    `json:"plugins"`
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
		requireSigned: options.Trust.RequireSigned, progress: options.Progress,
		data: managerState{Components: make(map[string]componentRecord), Plugins: make(map[string]pluginRecord)},
	}
	for _, signer := range options.Trust.AllowedSigners {
		if strings.TrimSpace(signer) != "" {
			manager.trust[signer] = struct{}{}
		}
	}
	if err := manager.load(); err != nil {
		return nil, err
	}
	manager.bootstrapExistingComponents()
	return manager, nil
}

// bootstrapExistingComponents recognizes files shipped with a standalone
// launcher (most importantly the launcher component itself) without turning
// arbitrary files into installed state. Only manifest-declared resources that
// fully verify are adopted, and an explicit state record always wins.
func (m *FilesystemManager) bootstrapExistingComponents() {
	for _, component := range m.manifest.Components {
		if _, recorded := m.data.Components[component.ID]; recorded || len(component.Resources) == 0 {
			continue
		}
		healthy := true
		for _, resource := range component.Resources {
			valid, err := verifyOne(context.Background(), mustJoin(m.root, resource.Path), resource)
			if err != nil || !valid {
				healthy = false
				break
			}
		}
		if healthy {
			m.data.Components[component.ID] = componentRecord{Installed: true, Version: component.Version, Enabled: true}
		}
	}
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
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode launcher state: trailing JSON")
		}
		return fmt.Errorf("decode launcher state trailing content: %w", err)
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
	return m.listComponentsLocked(ctx)
}

func (m *FilesystemManager) listComponentsLocked(ctx context.Context) ([]ComponentState, error) {
	states := make([]ComponentState, 0, len(m.manifest.Components))
	for _, component := range m.manifest.Components {
		record := m.data.Components[component.ID]
		health, err := m.componentHealth(ctx, component, record)
		if err != nil {
			return nil, err
		}
		states = append(states, ComponentState{ID: component.ID, Installed: record.Installed, Version: record.Version, Enabled: record.Enabled, Required: component.Required, Health: health})
	}
	sort.Slice(states, func(i, j int) bool { return states[i].ID < states[j].ID })
	return states, nil
}

func (m *FilesystemManager) Initialize(ctx context.Context) (InitializationStatus, error) {
	if err := contextErr(ctx); err != nil {
		return InitializationStatus{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	components, err := m.listComponentsLocked(ctx)
	if err != nil {
		return InitializationStatus{}, err
	}
	status := InitializationStatus{FirstRun: !m.data.Initialized, Components: components, NextAction: "manage_components"}
	for _, component := range m.manifest.Components {
		if component.Required {
			status.Required = append(status.Required, component.ID)
		} else {
			status.Optional = append(status.Optional, component.ID)
		}
	}
	sort.Strings(status.Required)
	sort.Strings(status.Optional)
	if status.FirstRun {
		status.NextAction = "select_components"
	}
	return status, nil
}

func (m *FilesystemManager) CompleteInitialization(ctx context.Context) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data.Initialized {
		return nil
	}
	previous := cloneState(m.data)
	m.data.Initialized = true
	if err := m.save(); err != nil {
		m.data = previous
		return fmt.Errorf("%w: save initialization state: %v", ErrTransaction, err)
	}
	return nil
}

func (m *FilesystemManager) componentHealth(ctx context.Context, component Component, record componentRecord) (string, error) {
	if !record.Installed {
		return HealthNotInstalled, nil
	}
	if !record.Enabled {
		return HealthDisabled, nil
	}
	for _, resource := range component.Resources {
		ok, err := verifyOne(ctx, mustJoin(m.root, resource.Path), resource)
		if err != nil {
			if contextError := contextErr(ctx); contextError != nil {
				return "", contextError
			}
			return HealthMissing, nil
		}
		if !ok {
			return HealthMissing, nil
		}
	}
	return HealthHealthy, nil
}

func (m *FilesystemManager) Install(ctx context.Context, id string) (ComponentState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := contextErr(ctx); err != nil {
		return ComponentState{}, err
	}
	reportProgress(m.progress, ProgressEvent{Operation: "component-install", Stage: "start", Item: id, Total: 1})
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
	reportProgress(m.progress, ProgressEvent{Operation: "component-install", Stage: "complete", Item: id, Completed: 1, Total: 1})
	component, _ := m.component(id)
	record := m.data.Components[id]
	health, err := m.componentHealth(ctx, component, record)
	if err != nil {
		return ComponentState{}, err
	}
	return ComponentState{ID: id, Installed: record.Installed, Version: record.Version, Enabled: record.Enabled, Required: component.Required, Health: health}, nil
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
	result, err := (FileRepairer{SourceRoot: m.source, Progress: m.progress}).Repair(ctx, RepairRequest{InstallRoot: m.root, Manifest: ReleaseManifest{Format: m.manifest.Format, Channel: m.manifest.Channel, Version: m.manifest.Version, Target: m.manifest.Target, Components: []Component{componentForRepair}}})
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
	reportProgress(m.progress, ProgressEvent{Operation: "component-remove", Stage: "start", Item: id, Total: 1})
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
	reportProgress(m.progress, ProgressEvent{Operation: "component-remove", Stage: "complete", Item: id, Completed: 1, Total: 1})
	return nil
}

func (m *FilesystemManager) SetEnabled(ctx context.Context, id string, enabled bool) (ComponentState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := contextErr(ctx); err != nil {
		return ComponentState{}, err
	}
	operation := "component-disable"
	if enabled {
		operation = "component-enable"
	}
	reportProgress(m.progress, ProgressEvent{Operation: operation, Stage: "start", Item: id, Total: 1})
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
	reportProgress(m.progress, ProgressEvent{Operation: operation, Stage: "complete", Item: id, Completed: 1, Total: 1})
	health, err := m.componentHealth(ctx, component, record)
	if err != nil {
		return ComponentState{}, err
	}
	return ComponentState{ID: id, Installed: true, Version: record.Version, Enabled: enabled, Required: component.Required, Health: health}, nil
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
	reportProgress(m.progress, ProgressEvent{Operation: "plugin-install", Stage: "start", Item: id, Total: 1})
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
	if err := os.RemoveAll(backup); err != nil {
		return PluginState{}, fmt.Errorf("prepare plugin rollback: %w", err)
	}
	hadPrevious := false
	if _, err := os.Lstat(pluginRoot); err == nil {
		hadPrevious = true
		if err := os.Rename(pluginRoot, backup); err != nil {
			return PluginState{}, fmt.Errorf("backup plugin: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return PluginState{}, fmt.Errorf("inspect plugin: %w", err)
	}
	if err := os.Rename(stage, pluginRoot); err != nil {
		if hadPrevious {
			_ = os.Rename(backup, pluginRoot)
		}
		return PluginState{}, fmt.Errorf("install plugin: %w", err)
	}
	// A new archive always requires a fresh trust decision, even when the
	// plugin ID is unchanged.
	m.data.Plugins[id] = pluginRecord{Installed: true, Enabled: false, Trusted: false}
	if err := m.save(); err != nil {
		rollbackErr := os.RemoveAll(pluginRoot)
		if rollbackErr == nil && hadPrevious {
			rollbackErr = os.Rename(backup, pluginRoot)
		}
		m.data = previous
		if rollbackErr != nil {
			return PluginState{}, fmt.Errorf("%w: save plugin state: %v; rollback: %v", ErrTransaction, err, rollbackErr)
		}
		return PluginState{}, fmt.Errorf("%w: save plugin state: %v", ErrTransaction, err)
	}
	reportProgress(m.progress, ProgressEvent{Operation: "plugin-install", Stage: "complete", Item: id, Completed: 1, Total: 1})
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
	reportProgress(m.progress, ProgressEvent{Operation: "plugin-remove", Stage: "start", Item: id, Total: 1})
	if _, ok := m.plugin(id); !ok {
		return fmt.Errorf("%w: plugin %q", ErrNotFound, id)
	}
	previous := cloneState(m.data)
	pluginRoot, err := safeJoin(filepath.Join(m.root, pluginDirectory), id)
	if err != nil {
		return err
	}
	backup := pluginRoot + ".rollback"
	if err := os.RemoveAll(backup); err != nil {
		return fmt.Errorf("%w: prepare plugin rollback: %v", ErrTransaction, err)
	}
	hadPrevious := false
	if _, err := os.Lstat(pluginRoot); err == nil {
		hadPrevious = true
		if err := os.Rename(pluginRoot, backup); err != nil {
			return fmt.Errorf("%w: remove plugin: %v", ErrTransaction, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("%w: inspect plugin: %v", ErrTransaction, err)
	}
	delete(m.data.Plugins, id)
	if err := m.save(); err != nil {
		var rollbackErr error
		if hadPrevious {
			rollbackErr = os.Rename(backup, pluginRoot)
		}
		m.data = previous
		if rollbackErr != nil {
			return fmt.Errorf("%w: save plugin state: %v; rollback: %v", ErrTransaction, err, rollbackErr)
		}
		return fmt.Errorf("%w: save plugin state: %v", ErrTransaction, err)
	}
	reportProgress(m.progress, ProgressEvent{Operation: "plugin-remove", Stage: "complete", Item: id, Completed: 1, Total: 1})
	_ = os.RemoveAll(backup)
	return nil
}

func (m *FilesystemManager) SetPluginEnabled(ctx context.Context, id string, enabled bool) (PluginState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := contextErr(ctx); err != nil {
		return PluginState{}, err
	}
	operation := "plugin-disable"
	if enabled {
		operation = "plugin-enable"
	}
	reportProgress(m.progress, ProgressEvent{Operation: operation, Stage: "start", Item: id, Total: 1})
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
	reportProgress(m.progress, ProgressEvent{Operation: operation, Stage: "complete", Item: id, Completed: 1, Total: 1})
	return PluginState{Descriptor: descriptor, Installed: true, Enabled: enabled, Trusted: record.Trusted, Health: m.pluginHealth(descriptor, record)}, nil
}

func (m *FilesystemManager) SetTrusted(ctx context.Context, id string, trusted bool) (PluginState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := contextErr(ctx); err != nil {
		return PluginState{}, err
	}
	operation := "plugin-untrust"
	if trusted {
		operation = "plugin-trust"
	}
	reportProgress(m.progress, ProgressEvent{Operation: operation, Stage: "start", Item: id, Total: 1})
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
	reportProgress(m.progress, ProgressEvent{Operation: operation, Stage: "complete", Item: id, Completed: 1, Total: 1})
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
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("%w: existing resource %s is not a regular file", ErrInvalidPath, resource.Path)
			}
			entry.exists = true
			entry.mode = info.Mode()
			entry.data, err = os.ReadFile(entry.path)
			if err != nil {
				return nil, fmt.Errorf("snapshot resource %s: %w", resource.Path, err)
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
	clone := managerState{Initialized: state.Initialized, Components: make(map[string]componentRecord, len(state.Components)), Plugins: make(map[string]pluginRecord, len(state.Plugins))}
	for id, record := range state.Components {
		clone.Components[id] = record
	}
	for id, record := range state.Plugins {
		clone.Plugins[id] = record
	}
	return clone
}

const (
	defaultArchiveMaxEntries       = 10000
	defaultArchiveMaxBytes   int64 = 512 << 20
)

type ArchiveLimits struct {
	MaxEntries int
	MaxBytes   int64
}

func extractArchive(ctx context.Context, archivePath, destination string) error {
	return extractArchiveWithLimits(ctx, archivePath, destination, ArchiveLimits{MaxEntries: defaultArchiveMaxEntries, MaxBytes: defaultArchiveMaxBytes})
}

func extractArchiveWithLimits(ctx context.Context, archivePath, destination string, limits ArchiveLimits) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if limits.MaxEntries <= 0 {
		limits.MaxEntries = defaultArchiveMaxEntries
	}
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = defaultArchiveMaxBytes
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	if strings.HasSuffix(strings.ToLower(archivePath), ".zip") {
		return extractZip(ctx, archivePath, destination, limits)
	}
	return extractTarGz(ctx, archivePath, destination, limits)
}

func extractZip(ctx context.Context, archivePath, destination string, limits ArchiveLimits) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer reader.Close()
	seen := make(map[string]struct{}, len(reader.File))
	var totalBytes int64
	for index, entry := range reader.File {
		if index >= limits.MaxEntries {
			return fmt.Errorf("%w: archive has too many entries", ErrInvalidManifest)
		}
		if err := contextErr(ctx); err != nil {
			return err
		}
		name, err := validateArchiveEntryPath(entry.Name)
		if err != nil {
			return err
		}
		if name == "" {
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("%w: duplicate archive entry %q", ErrInvalidManifest, entry.Name)
		}
		seen[name] = struct{}{}
		target, _ := safeJoin(destination, name)
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			continue
		}
		if !entry.FileInfo().Mode().IsRegular() {
			return fmt.Errorf("%w: archive entry %q is not regular", ErrInvalidManifest, entry.Name)
		}
		if entry.UncompressedSize64 > uint64(limits.MaxBytes-totalBytes) {
			return fmt.Errorf("%w: archive exceeds maximum extracted size", ErrInvalidManifest)
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
			var count int64
			var copyErr error
			if entry.UncompressedSize64 > 0 {
				count, copyErr = copyWithContextLimit(ctx, output, input, limits.MaxBytes-totalBytes)
			}
			totalBytes += count
			err = copyErr
			if err == nil && count != int64(entry.UncompressedSize64) {
				err = fmt.Errorf("%w: archive entry %q size mismatch", ErrInvalidManifest, entry.Name)
			}
			_ = output.Close()
		}
		_ = input.Close()
		if err != nil || totalBytes > limits.MaxBytes {
			if err == nil {
				err = fmt.Errorf("%w: archive exceeds maximum extracted size", ErrInvalidManifest)
			}
			return err
		}
	}
	return nil
}

func extractTarGz(ctx context.Context, archivePath, destination string, limits ArchiveLimits) error {
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
	seen := make(map[string]struct{})
	var totalBytes int64
	entries := 0
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
		entries++
		if entries > limits.MaxEntries {
			return fmt.Errorf("%w: archive has too many entries", ErrInvalidManifest)
		}
		name, err := validateArchiveEntryPath(header.Name)
		if err != nil {
			return err
		}
		if name == "" {
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("%w: duplicate archive entry %q", ErrInvalidManifest, header.Name)
		}
		seen[name] = struct{}{}
		target, _ := safeJoin(destination, name)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > limits.MaxBytes-totalBytes {
				return fmt.Errorf("%w: archive exceeds maximum extracted size", ErrInvalidManifest)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
			if err != nil {
				return err
			}
			var count int64
			var copyErr error
			if header.Size > 0 {
				count, copyErr = copyWithContextLimit(ctx, output, reader, limits.MaxBytes-totalBytes)
			}
			totalBytes += count
			if copyErr == nil && count != header.Size {
				copyErr = fmt.Errorf("%w: archive entry %q size mismatch", ErrInvalidManifest, header.Name)
			}
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

func validateArchiveEntryPath(value string) (string, error) {
	original := value
	if strings.TrimSpace(value) == "" || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") ||
		(len(value) >= 2 && value[1] == ':') {
		return "", fmt.Errorf("%w: archive entry %q", ErrInvalidPath, original)
	}
	for strings.HasPrefix(value, "./") {
		value = strings.TrimPrefix(value, "./")
	}
	trimmed := strings.TrimRight(value, "/")
	if trimmed == "" || trimmed == "." {
		// tar archives made with `-C stage .` contain a harmless root entry.
		return "", nil
	}
	for _, segment := range strings.Split(trimmed, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("%w: archive entry %q", ErrInvalidPath, original)
		}
	}
	clean := pathpkg.Clean(trimmed)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: archive entry %q", ErrInvalidPath, original)
	}
	return clean, nil
}

var _ ComponentManager = (*FilesystemComponentManager)(nil)
var _ PluginManager = (*FilesystemPluginManager)(nil)
var _ InitializationManager = (*FilesystemManager)(nil)
