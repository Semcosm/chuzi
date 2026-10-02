//go:build windows

package slotwindows

import (
	"context"
	"path/filepath"

	"github.com/Semcosm/chuzi/internal/slot"
)

// UpdatePoolRuntime follows the durable pool target before a provision pass.
// The resolver is supplied by the signed environment manager and returns only
// service-owned paths, so a Core update cannot redirect the Windows agent.
func (p *windowsProvisioner) UpdatePoolRuntime(ctx context.Context, pool slot.PoolConfig) error {
	if p == nil || ctx == nil {
		return ErrInvalidOptions
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	requirement, err := pool.Requirement().Normalize()
	if err != nil {
		return err
	}
	p.mu.Lock()
	resolver := p.options.RuntimeResolver
	p.mu.Unlock()
	if resolver == nil {
		return nil
	}
	runtimeRoot, workerScript, err := resolver(ctx, requirement)
	if err != nil || runtimeRoot == "" || workerScript == "" || !filepath.IsAbs(runtimeRoot) || !filepath.IsAbs(workerScript) {
		return ErrInvalidOptions
	}
	p.mu.Lock()
	p.options.EnvironmentID = pool.EnvironmentID
	p.options.Version = pool.EnvironmentVersion
	p.options.ManifestDigest = pool.ManifestDigest
	p.options.Signer = pool.Signer
	p.options.RequireTrusted = pool.RequireTrusted
	p.options.RuntimePath = filepath.Clean(runtimeRoot)
	p.options.WorkerScript = filepath.Clean(workerScript)
	p.mu.Unlock()
	return nil
}
