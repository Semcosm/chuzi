package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/environment"
	"github.com/Semcosm/chuzi/internal/slot"
)

const environmentTrustStoreName = "environment-trust.json"

// serviceEnvironmentRuntime is the only runtime material that crosses from
// the signed package manager into platform provisioners. It contains resolved
// service-owned paths, never caller-provided paths or credentials.
type serviceEnvironmentRuntime struct {
	Handoff         environment.RuntimeHandoff
	Manifest        environment.Manifest
	Record          environment.Record
	RuntimeResolver func(context.Context, slot.EnvironmentRequirement) (environment.RuntimeHandoff, error)
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
		if !os.IsNotExist(err) || cfg.WindowsJobPool.Enabled {
			return nil, fmt.Errorf("service: environment trust store unavailable")
		}
		if _, stateErr := os.Stat(environmentStatePath(cfg)); !os.IsNotExist(stateErr) {
			return nil, fmt.Errorf("service: environment trust store unavailable")
		}
		trust = environment.TrustStore{}
	}
	manager, err := environment.NewManager(environment.Options{
		InstallRoot: environmentInstallRoot(cfg),
		StatePath:   environmentStatePath(cfg),
		CatalogRoot: filepath.Join(cfg.DataDir, ".chuzi", "environment-catalog"),
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
func resolveServiceEnvironment(cfg config.Config, manager *environment.Manager, pool slot.PoolConfig, entryName string) (*serviceEnvironmentRuntime, error) {
	if !cfg.WindowsJobPool.Enabled {
		return nil, nil
	}
	if manager == nil {
		return nil, fmt.Errorf("service: environment manager unavailable")
	}
	resolver := func(ctx context.Context, requirement slot.EnvironmentRequirement) (environment.RuntimeHandoff, error) {
		if err := ctx.Err(); err != nil {
			return environment.RuntimeHandoff{}, err
		}
		resolved, _, resolveErr := manager.ResolveEntrypoint(requirement.EnvironmentID, requirement.Version, entryName, "browser-worker")
		if resolveErr != nil {
			return environment.RuntimeHandoff{}, resolveErr
		}
		handoff, handoffErr := environment.NewRuntimeHandoff(resolved, entryName)
		if handoffErr != nil {
			return environment.RuntimeHandoff{}, handoffErr
		}
		if !handoff.Matches(requirement.EnvironmentID, requirement.Version, requirement.ManifestDigest, requirement.Signer) {
			return environment.RuntimeHandoff{}, fmt.Errorf("service: resolved runtime does not match requirement")
		}
		return handoff, nil
	}
	if pool.PoolID == "" {
		// The Windows provisioner can be assembled before the first pool exists.
		// Its first reconcile resolves the durable pool's signed package through
		// this closure, so Core-created pools are handled after startup.
		return &serviceEnvironmentRuntime{RuntimeResolver: resolver}, nil
	}
	packageValue, _, err := manager.ResolveEntrypoint(pool.EnvironmentID, pool.EnvironmentVersion, entryName, "browser-worker")
	if err != nil {
		return nil, fmt.Errorf("service: trusted browser-worker entrypoint unavailable")
	}
	handoff, err := environment.NewRuntimeHandoff(packageValue, entryName)
	if err != nil {
		return nil, fmt.Errorf("service: invalid trusted environment runtime")
	}
	if !handoff.Matches(pool.EnvironmentID, pool.EnvironmentVersion, pool.ManifestDigest, pool.Signer) {
		return nil, fmt.Errorf("service: trusted environment runtime does not match pool")
	}
	return &serviceEnvironmentRuntime{Handoff: handoff, Manifest: packageValue.Manifest, Record: packageValue.Record, RuntimeResolver: resolver}, nil
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
