// Package launcher defines the transport-neutral contract used by a future
// launcher UI. It deliberately does not choose a GUI toolkit, HTTP protocol,
// update host, archive format, or plugin execution model.
package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
)

const (
	ManifestFormat = "chuzi-release/v1"
	ChannelNightly = "nightly"
	ChannelStable  = "stable"
	PluginAPIV1    = "chuzi.plugin/v1"
)

var (
	ErrInvalidManifest = errors.New("launcher: invalid release manifest")
	ErrInvalidPath     = errors.New("launcher: invalid resource path")
	ErrUnsupported     = errors.New("launcher: unsupported launcher operation")
)

// ReleaseManifest is the signed/verified metadata a launcher consumes before
// presenting an update or install action. Hashes are over files inside the
// target installation, not over an archive container.
type ReleaseManifest struct {
	Format      string            `json:"format"`
	Channel     string            `json:"channel"`
	Version     string            `json:"version"`
	Commit      string            `json:"commit"`
	Target      string            `json:"target"`
	GeneratedAt time.Time         `json:"generated_at"`
	Components  []Component       `json:"components"`
	Plugins     []PluginDescriptor `json:"plugins"`
}

type Component struct {
	ID           string     `json:"id"`
	Version      string     `json:"version"`
	Required     bool       `json:"required"`
	Artifact     string     `json:"artifact,omitempty"`
	Dependencies []string   `json:"dependencies,omitempty"`
	Entrypoint   string     `json:"entrypoint,omitempty"`
	Resources    []Resource `json:"resources"`
}

type Resource struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// PluginDescriptor describes an adapter that can be managed independently of
// the core release. The launcher never grants permissions implicitly.
type PluginDescriptor struct {
	ID              string   `json:"id"`
	Version         string   `json:"version"`
	API             string   `json:"api"`
	Target          string   `json:"target,omitempty"`
	Archive         string   `json:"archive,omitempty"`
	SHA256          string   `json:"sha256,omitempty"`
	Permissions     []string `json:"permissions,omitempty"`
	SignedBy        string   `json:"signed_by,omitempty"`
	Installable     bool     `json:"installable"`
}

func (m ReleaseManifest) Validate() error {
	if m.Format != ManifestFormat || strings.TrimSpace(m.Channel) == "" ||
		strings.TrimSpace(m.Version) == "" || strings.TrimSpace(m.Target) == "" {
		return fmt.Errorf("%w: format, channel, version, and target are required", ErrInvalidManifest)
	}
	if m.Channel != ChannelNightly && m.Channel != ChannelStable {
		return fmt.Errorf("%w: unknown channel %q", ErrInvalidManifest, m.Channel)
	}
	seenComponents := make(map[string]struct{}, len(m.Components))
	for _, component := range m.Components {
		if strings.TrimSpace(component.ID) == "" || strings.TrimSpace(component.Version) == "" {
			return fmt.Errorf("%w: component id and version are required", ErrInvalidManifest)
		}
		if _, ok := seenComponents[component.ID]; ok {
			return fmt.Errorf("%w: duplicate component %q", ErrInvalidManifest, component.ID)
		}
		seenComponents[component.ID] = struct{}{}
		seenResources := make(map[string]struct{}, len(component.Resources))
		for _, resource := range component.Resources {
			if err := validateResource(resource); err != nil {
				return fmt.Errorf("%w: component %s: %v", ErrInvalidManifest, component.ID, err)
			}
			if _, ok := seenResources[resource.Path]; ok {
				return fmt.Errorf("%w: duplicate resource %q", ErrInvalidManifest, resource.Path)
			}
			seenResources[resource.Path] = struct{}{}
		}
	}
	seenPlugins := make(map[string]struct{}, len(m.Plugins))
	for _, plugin := range m.Plugins {
		if strings.TrimSpace(plugin.ID) == "" || strings.TrimSpace(plugin.Version) == "" || plugin.API != PluginAPIV1 {
			return fmt.Errorf("%w: plugin %q has invalid id, version, or api", ErrInvalidManifest, plugin.ID)
		}
		if _, ok := seenPlugins[plugin.ID]; ok {
			return fmt.Errorf("%w: duplicate plugin %q", ErrInvalidManifest, plugin.ID)
		}
		seenPlugins[plugin.ID] = struct{}{}
		if plugin.Archive != "" {
			if err := validateRelativePath(plugin.Archive); err != nil {
				return fmt.Errorf("%w: plugin %s archive: %v", ErrInvalidManifest, plugin.ID, err)
			}
		}
	}
	return nil
}

func validateResource(resource Resource) error {
	if err := validateRelativePath(resource.Path); err != nil {
		return err
	}
	if len(resource.SHA256) != 64 {
		return fmt.Errorf("resource %q has invalid sha256", resource.Path)
	}
	for _, character := range resource.SHA256 {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return fmt.Errorf("resource %q has invalid sha256", resource.Path)
		}
	}
	if resource.Size < 0 {
		return fmt.Errorf("resource %q has negative size", resource.Path)
	}
	return nil
}

func validateRelativePath(value string) error {
	if strings.TrimSpace(value) == "" || path.IsAbs(value) || strings.Contains(value, "\\") ||
		(len(value) >= 2 && value[1] == ':') {
		return fmt.Errorf("%w: %q", ErrInvalidPath, value)
	}
	clean := path.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("%w: %q", ErrInvalidPath, value)
	}
	return nil
}

// SortedResources returns a stable copy for deterministic UI and logging.
func (m ReleaseManifest) SortedResources() []Resource {
	var resources []Resource
	for _, component := range m.Components {
		resources = append(resources, component.Resources...)
	}
	sort.Slice(resources, func(i, j int) bool { return resources[i].Path < resources[j].Path })
	return resources
}

func (m ReleaseManifest) MarshalJSON() ([]byte, error) {
	type alias ReleaseManifest
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(alias(m))
}

type UpdateRequest struct {
	CurrentVersion string
	Target         string
	Channel        string
}

type UpdateInfo struct {
	Available bool
	Manifest  *ReleaseManifest
	Reason    string
}

type UpdateChecker interface {
	Check(context.Context, UpdateRequest) (UpdateInfo, error)
}

type VerificationIssue struct {
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
}

type VerificationResult struct {
	Valid  bool                `json:"valid"`
	Issues []VerificationIssue `json:"issues,omitempty"`
}

type ResourceVerifier interface {
	Verify(context.Context, string, ReleaseManifest) (VerificationResult, error)
}

type RepairRequest struct {
	InstallRoot string
	Manifest    ReleaseManifest
	Paths       []string
}

type RepairResult struct {
	Repaired []string
	Skipped  []string
}

type ResourceRepairer interface {
	Repair(context.Context, RepairRequest) (RepairResult, error)
}

type ComponentState struct {
	ID          string `json:"id"`
	Installed   bool   `json:"installed"`
	Version     string `json:"version,omitempty"`
	Enabled     bool   `json:"enabled"`
	Required    bool   `json:"required"`
	Health      string `json:"health"`
}

type ComponentManager interface {
	List(context.Context) ([]ComponentState, error)
	Install(context.Context, string) (ComponentState, error)
	Remove(context.Context, string) error
	SetEnabled(context.Context, string, bool) (ComponentState, error)
}

type PluginState struct {
	Descriptor PluginDescriptor `json:"descriptor"`
	Installed  bool             `json:"installed"`
	Enabled    bool             `json:"enabled"`
	Trusted    bool             `json:"trusted"`
	Health     string           `json:"health"`
}

type PluginManager interface {
	List(context.Context) ([]PluginState, error)
	Install(context.Context, string) (PluginState, error)
	Remove(context.Context, string) error
	SetEnabled(context.Context, string, bool) (PluginState, error)
}

type BehaviorSettings struct {
	AutoCheckUpdates bool          `json:"auto_check_updates"`
	AutoRepair       bool          `json:"auto_repair"`
	UpdateChannel    string        `json:"update_channel"`
	LaunchOnLogin    bool          `json:"launch_on_login"`
	CloseToTray      bool          `json:"close_to_tray"`
	CheckInterval    time.Duration `json:"check_interval"`
}

func (s BehaviorSettings) Validate() error {
	if s.UpdateChannel != ChannelNightly && s.UpdateChannel != ChannelStable {
		return fmt.Errorf("%w: update channel %q", ErrInvalidManifest, s.UpdateChannel)
	}
	if s.CheckInterval < 0 {
		return fmt.Errorf("%w: negative update interval", ErrInvalidManifest)
	}
	return nil
}

type SettingsStore interface {
	Load(context.Context) (BehaviorSettings, error)
	Save(context.Context, BehaviorSettings) error
}
