package launcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
)

const defaultDownloadDirectory = ".chuzi/downloads"

// NetworkComponentManager downloads only the explicitly selected component
// and its manifest-declared dependencies, then delegates installation to the
// existing filesystem manager. The downloaded source is temporary, so a
// failed extraction or resource repair cannot leave a partial installation.
type NetworkComponentManager struct {
	mu          sync.Mutex
	manager     *FilesystemComponentManager
	manifest    ReleaseManifest
	index       ReleaseIndex
	indexURL    string
	downloader  ArtifactDownloader
	downloadDir string
}

func NewNetworkComponentManager(options ManagerOptions, index ReleaseIndex, indexURL, downloadDir string, downloader ArtifactDownloader) (*NetworkComponentManager, error) {
	if err := index.Validate(); err != nil {
		return nil, err
	}
	if options.Manifest.Format == "" {
		options.Manifest = index.Manifest
	}
	if err := options.Manifest.Validate(); err != nil {
		return nil, err
	}
	if options.Manifest.Target != index.Target || options.Manifest.Channel != index.Channel || options.Manifest.Version != index.Version || options.Manifest.Commit != index.Commit {
		return nil, fmt.Errorf("%w: component index does not match installed manifest", ErrInvalidManifest)
	}
	if !reflect.DeepEqual(options.Manifest, index.Manifest) {
		return nil, fmt.Errorf("%w: component index manifest differs from installed manifest", ErrInvalidManifest)
	}
	if strings.TrimSpace(indexURL) == "" {
		return nil, fmt.Errorf("%w: release index URL is required", ErrInvalidPath)
	}
	if options.InstallRoot == "" || !filepath.IsAbs(options.InstallRoot) {
		return nil, fmt.Errorf("%w: install root must be absolute", ErrInvalidPath)
	}
	if strings.TrimSpace(downloadDir) == "" {
		downloadDir = filepath.Join(options.InstallRoot, defaultDownloadDirectory)
	}
	downloadDir, err := filepath.Abs(downloadDir)
	if err != nil {
		return nil, err
	}
	if relative, err := filepath.Rel(options.InstallRoot, downloadDir); err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("%w: download directory must stay under install root", ErrInvalidPath)
	}
	local, err := NewFilesystemComponentManager(options)
	if err != nil {
		return nil, err
	}
	return &NetworkComponentManager{manager: local, manifest: options.Manifest, index: index, indexURL: indexURL, downloader: downloader, downloadDir: downloadDir}, nil
}

func (m *NetworkComponentManager) List(ctx context.Context) ([]ComponentState, error) {
	return m.manager.List(ctx)
}

func (m *NetworkComponentManager) Remove(ctx context.Context, id string) error {
	return m.manager.Remove(ctx, id)
}

func (m *NetworkComponentManager) SetEnabled(ctx context.Context, id string, enabled bool) (ComponentState, error) {
	return m.manager.SetEnabled(ctx, id, enabled)
}

func (m *NetworkComponentManager) Install(ctx context.Context, id string) (ComponentState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := contextErr(ctx); err != nil {
		return ComponentState{}, err
	}
	if _, ok := m.manager.component(id); !ok {
		return ComponentState{}, fmt.Errorf("%w: component %q", ErrNotFound, id)
	}
	source, err := os.MkdirTemp("", ".chuzi-component-source-")
	if err != nil {
		return ComponentState{}, fmt.Errorf("create component source: %w", err)
	}
	defer os.RemoveAll(source)
	visiting := make(map[string]bool)
	downloaded := make(map[string]bool)
	if err := m.downloadDependencies(ctx, id, source, visiting, downloaded); err != nil {
		return ComponentState{}, err
	}
	// Reuse the manager that backs List/Remove/SetEnabled so its in-memory
	// state stays aligned with the state file after a network installation.
	// Network installs are serialized above; local management still takes the
	// manager mutex before reading or mutating this source root.
	previousSource := m.manager.source
	m.manager.source = source
	defer func() { m.manager.source = previousSource }()
	return m.manager.Install(ctx, id)
}

func (m *NetworkComponentManager) downloadDependencies(ctx context.Context, id, source string, visiting, downloaded map[string]bool) error {
	if visiting[id] {
		return fmt.Errorf("%w: component dependency cycle at %q", ErrInvalidManifest, id)
	}
	component, ok := m.manager.component(id)
	if !ok {
		return fmt.Errorf("%w: component %q", ErrNotFound, id)
	}
	visiting[id] = true
	defer delete(visiting, id)
	for _, dependency := range component.Dependencies {
		if err := m.downloadDependencies(ctx, dependency, source, visiting, downloaded); err != nil {
			return err
		}
	}
	if downloaded[id] {
		return nil
	}
	if strings.TrimSpace(component.Artifact) == "" {
		if len(component.Resources) > 0 {
			return fmt.Errorf("%w: component %s has no downloadable artifact", ErrInvalidManifest, id)
		}
		downloaded[id] = true
		return nil
	}
	artifact, ok := m.index.Artifact(id)
	if !ok {
		return fmt.Errorf("%w: component %s has no release artifact", ErrInvalidManifest, id)
	}
	archive, err := m.downloader.Download(ctx, m.indexURL, artifact, m.downloadDir)
	if err != nil {
		return fmt.Errorf("download component %s: %w", id, err)
	}
	if err := extractArchive(ctx, archive, source); err != nil {
		return fmt.Errorf("extract component %s: %w", id, err)
	}
	downloaded[id] = true
	return nil
}

func (m *NetworkComponentManager) Initialize(ctx context.Context) (InitializationStatus, error) {
	return m.manager.Initialize(ctx)
}

func (m *NetworkComponentManager) CompleteInitialization(ctx context.Context) error {
	return m.manager.CompleteInitialization(ctx)
}

var _ ComponentManager = (*NetworkComponentManager)(nil)
var _ InitializationManager = (*NetworkComponentManager)(nil)
