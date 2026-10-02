package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/environment"
)

const environmentTrustStoreName = "environment-trust.json"

// serviceEnvironmentRuntime is the only runtime material that crosses from
// the signed package manager into platform provisioners. It contains resolved
// service-owned paths, never caller-provided paths or credentials.
type serviceEnvironmentRuntime struct {
	Root         string
	WorkerScript string
	Manifest     environment.Manifest
	Record       environment.Record
}

func environmentInstallRoot(cfg config.Config) string {
	// Manager packageRoot owns the `environments/<id>/<version>` segment.
	// Keep its service root at the deployment data directory so the runtime
	// path is exactly `<data_dir>/environments/...`.
	return cfg.DataDir
}

func environmentStatePath(cfg config.Config) string {
	return filepath.Join(cfg.DataDir, ".chuzi", "environment-state.json")
}

func environmentTrustStorePath(cfg config.Config) string {
	return filepath.Join(cfg.DataDir, ".chuzi", environmentTrustStoreName)
}

func newConfiguredEnvironmentManager(cfg config.Config, target string) (*environment.Manager, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(target) == "" {
		return nil, fmt.Errorf("service: environment target is unavailable")
	}
	trust, err := environment.LoadTrustStore(environmentTrustStorePath(cfg))
	if err != nil {
		return nil, fmt.Errorf("service: environment trust store unavailable")
	}
	manager, err := environment.NewManager(environment.Options{
		InstallRoot: environmentInstallRoot(cfg),
		StatePath:   environmentStatePath(cfg),
		Target:      target,
		Trust:       trust,
		Health:      environmentHealthCheck,
	})
	if err != nil {
		return nil, fmt.Errorf("service: environment manager unavailable")
	}
	return manager, nil
}

// resolveServiceEnvironment proves the manager state and package tree again at
// startup. A Store record alone cannot turn an edited or missing package into
// a runnable Windows environment.
func resolveServiceEnvironment(cfg config.Config, manager *environment.Manager, entryName string) (*serviceEnvironmentRuntime, error) {
	if !cfg.WindowsJobPool.Enabled {
		return nil, nil
	}
	if manager == nil {
		return nil, fmt.Errorf("service: environment manager unavailable")
	}
	packageValue, entry, err := manager.ResolveEntrypoint(cfg.JobPool.EnvironmentID, cfg.JobPool.EnvironmentVersion, entryName, "browser-worker")
	if err != nil {
		return nil, fmt.Errorf("service: trusted browser-worker entrypoint unavailable")
	}
	entryPath := filepath.Join(packageValue.Root, filepath.FromSlash(entry.Path))
	if !filepath.IsAbs(entryPath) || filepath.Clean(entryPath) == packageValue.Root {
		return nil, fmt.Errorf("service: invalid environment entrypoint")
	}
	return &serviceEnvironmentRuntime{Root: packageValue.Root, WorkerScript: entryPath, Manifest: packageValue.Manifest, Record: packageValue.Record}, nil
}

func environmentHealthCheck(ctx context.Context, manifest environment.Manifest, root string) error {
	if ctx == nil {
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(root) == "" || !filepath.IsAbs(root) {
		return environment.ErrInvalidPath
	}
	for _, entry := range manifest.Entrypoints {
		if entry.Runtime != "browser-worker" && entry.Runtime != "agent" && entry.Runtime != "adapter-bridge" {
			return environment.ErrInvalidManifest
		}
		path := filepath.Join(root, filepath.FromSlash(entry.Path))
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return environment.ErrNotHealthy
		}
	}
	return nil
}
