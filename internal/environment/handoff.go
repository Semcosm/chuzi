package environment

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Closed runtime entrypoint names. The manifest chooses the resource path for
// each name; callers never choose a path or command.
const (
	WorkerEntrypointName        = "worker"
	HeadlessEntrypointName      = "headless"
	AdapterBridgeEntrypointName = "adapter-bridge"
)

var ErrInvalidHandoff = errors.New("environment: invalid runtime handoff")

// RuntimeHandoff is the only package metadata that may cross into a platform
// provisioner or user agent. Its fields are private so a Core/Launcher DTO
// cannot manufacture an executable, package identity, or generation.
//
// PackageGeneration is the signed environment record generation. Slot
// generation remains an independent fence carried by slot.ProvisionRequest.
type RuntimeHandoff struct {
	root              string
	worker            string
	workerName        string
	adapter           string
	adapterName       string
	environmentID     string
	version           string
	digest            string
	signer            string
	packageGeneration uint64
}

// NewRuntimeHandoff validates the manager-selected package tree and binds its
// closed worker and adapter bridge entrypoints. An adapter bridge is required
// even when a particular service invocation does not use adapter mode: the
// package contract is then stable across a later adapter-enabled job.
func NewRuntimeHandoff(pkg Package, workerName string) (RuntimeHandoff, error) {
	if workerName != WorkerEntrypointName && workerName != HeadlessEntrypointName {
		return RuntimeHandoff{}, fmt.Errorf("%w: worker entrypoint", ErrInvalidHandoff)
	}
	if pkg.Root == "" || !filepath.IsAbs(pkg.Root) || filepath.Clean(pkg.Root) == string(filepath.Separator) || strings.ContainsAny(pkg.Root, "\x00\r\n") {
		return RuntimeHandoff{}, fmt.Errorf("%w: package root", ErrInvalidHandoff)
	}
	if err := pkg.Manifest.Validate(); err != nil || !pkg.Record.IsReady() || pkg.Record.EnvironmentID != pkg.Manifest.EnvironmentID || pkg.Record.Version != pkg.Manifest.Version || !strings.EqualFold(pkg.Record.ManifestDigest, pkg.Manifest.ManifestDigest) || pkg.Record.Signer != pkg.Manifest.Signer || pkg.Record.Generation == 0 {
		return RuntimeHandoff{}, fmt.Errorf("%w: package record", ErrInvalidHandoff)
	}
	manifest, err := ValidatePackage(pkg.Root, "", nil)
	if err != nil || !strings.EqualFold(manifest.ManifestDigest, pkg.Manifest.ManifestDigest) || manifest.EnvironmentID != pkg.Manifest.EnvironmentID || manifest.Version != pkg.Manifest.Version || manifest.Signer != pkg.Manifest.Signer {
		return RuntimeHandoff{}, fmt.Errorf("%w: package tree", ErrInvalidHandoff)
	}
	worker, ok := findEntrypoint(manifest, workerName, "browser-worker")
	if !ok {
		return RuntimeHandoff{}, fmt.Errorf("%w: %s entrypoint", ErrInvalidHandoff, workerName)
	}
	adapter, ok := findEntrypoint(manifest, AdapterBridgeEntrypointName, "adapter-bridge")
	if !ok {
		return RuntimeHandoff{}, fmt.Errorf("%w: adapter bridge entrypoint", ErrInvalidHandoff)
	}
	workerPath, err := handoffPath(pkg.Root, worker.Path)
	if err != nil {
		return RuntimeHandoff{}, err
	}
	adapterPath, err := handoffPath(pkg.Root, adapter.Path)
	if err != nil {
		return RuntimeHandoff{}, err
	}
	return RuntimeHandoff{root: filepath.Clean(pkg.Root), worker: workerPath, workerName: workerName, adapter: adapterPath, adapterName: AdapterBridgeEntrypointName, environmentID: manifest.EnvironmentID, version: manifest.Version, digest: manifest.ManifestDigest, signer: manifest.Signer, packageGeneration: pkg.Record.Generation}, nil
}

func findEntrypoint(manifest Manifest, name, runtime string) (Entrypoint, bool) {
	var selected Entrypoint
	for _, entry := range manifest.Entrypoints {
		if entry.Name != name {
			continue
		}
		if selected.Path != "" || entry.Runtime != runtime {
			return Entrypoint{}, false
		}
		selected = entry
	}
	return selected, selected.Path != ""
}

func handoffPath(root, relative string) (string, error) {
	if err := validateRelative(relative); err != nil {
		return "", fmt.Errorf("%w: entrypoint path", ErrInvalidHandoff)
	}
	path := filepath.Clean(filepath.Join(root, filepath.FromSlash(relative)))
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: entrypoint containment", ErrInvalidHandoff)
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: entrypoint resource", ErrInvalidHandoff)
	}
	return path, nil
}

func (h RuntimeHandoff) Valid() bool { return h.Validate() == nil }

// Matches reports whether this trusted handoff is bound to the durable
// environment identity selected by a pool or request. Metadata is compared
// here, at the handoff boundary, so platform provisioners cannot accidentally
// run a valid package for a different environment generation or signer.
func (h RuntimeHandoff) Matches(environmentID, version, digest, signer string) bool {
	if err := h.Validate(); err != nil {
		return false
	}
	return h.environmentID == strings.TrimSpace(environmentID) &&
		h.version == strings.TrimSpace(version) &&
		strings.EqualFold(h.digest, strings.TrimSpace(digest)) &&
		h.signer == strings.TrimSpace(signer)
}

func (h RuntimeHandoff) Validate() error {
	if h.root == "" || h.worker == "" || h.adapter == "" || h.workerName == "" || h.adapterName != AdapterBridgeEntrypointName || h.environmentID == "" || h.version == "" || h.digest == "" || h.signer == "" || h.packageGeneration == 0 {
		return ErrInvalidHandoff
	}
	if h.workerName != WorkerEntrypointName && h.workerName != HeadlessEntrypointName {
		return ErrInvalidHandoff
	}
	if !filepath.IsAbs(h.root) || !filepath.IsAbs(h.worker) || !filepath.IsAbs(h.adapter) || !pathContained(h.root, h.worker) || !pathContained(h.root, h.adapter) {
		return ErrInvalidHandoff
	}
	return nil
}

func pathContained(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func (h RuntimeHandoff) PackageRoot() string       { return h.root }
func (h RuntimeHandoff) WorkerPath() string        { return h.worker }
func (h RuntimeHandoff) WorkerName() string        { return h.workerName }
func (h RuntimeHandoff) AdapterBridgePath() string { return h.adapter }
func (h RuntimeHandoff) AdapterBridgeName() string { return h.adapterName }
func (h RuntimeHandoff) EnvironmentID() string     { return h.environmentID }
func (h RuntimeHandoff) Version() string           { return h.version }
func (h RuntimeHandoff) ManifestDigest() string    { return h.digest }
func (h RuntimeHandoff) Signer() string            { return h.signer }
func (h RuntimeHandoff) PackageGeneration() uint64 { return h.packageGeneration }
