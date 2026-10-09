//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Semcosm/chuzi/internal/browser"
	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/environment"
	"github.com/Semcosm/chuzi/internal/slot"
	"github.com/Semcosm/chuzi/internal/slotagent"
	"github.com/Semcosm/chuzi/internal/slotlifecycle"
	"github.com/Semcosm/chuzi/internal/slotwindows"
	"github.com/Semcosm/chuzi/internal/store"
	"golang.org/x/sys/windows"
)

type slotAgentBridge struct{ resolver slotAgentResolver }

func (b slotAgentBridge) ResolveAgent(slotID, handle string) (browser.AgentConnection, error) {
	path, token, err := b.resolver.AgentEndpoint(slotID, handle)
	if err != nil {
		return browser.AgentConnection{}, err
	}
	return browser.AgentConnection{Path: path, Token: token}, nil
}

func configureWorkerFactory(cfg config.Config, options serviceOptions, factory browser.WorkerFactory, access slotProfileAccess) (browser.WorkerFactory, error) {
	if !cfg.WindowsJobPool.Enabled || access == nil {
		return factory, nil
	}
	resolver, ok := access.(slotAgentResolver)
	if !ok {
		return nil, fmt.Errorf("service: Windows slot agent unavailable")
	}
	// The slot agent always launches the versioned browser worker. An
	// automation adapter is a separate service-side protocol client that
	// consumes the worker's loopback handle; selecting the Adapter job kind
	// here would make the agent launch a different protocol boundary.
	return browser.NewAgentProcessFactory(slotAgentBridge{resolver: resolver}, slotagent.BrowserWorker)
}

func resolveSessionBootstrapper(options serviceOptions) slotwindows.SessionBootstrapper {
	if options.sessionBootstrapper != nil {
		return options.sessionBootstrapper
	}
	// The service talks only to the fixed, ACL-protected broker boundary. The
	// broker owns the deployment-specific WTS/RDP login implementation.
	return slotwindows.NewRunnerSessionBootstrapper()
}

func newSlotReconciler(cfg config.Config, options serviceOptions, database *store.Store, now func() time.Time, revoker slotCapabilityRevoker, environmentRuntime *serviceEnvironmentRuntime, pool slot.PoolConfig) (slotReconciler, slotProfileAccess, error) {
	if !cfg.WindowsJobPool.Enabled {
		reconciler, err := newLogicalSlotReconciler(database, now, options.owner)
		if err != nil {
			return nil, nil, err
		}
		return reconciler, nil, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	runtimeRoot := filepath.Dir(executable)
	if environmentRuntime == nil {
		return nil, nil, fmt.Errorf("service: trusted environment runtime is unavailable")
	}
	workerCommand, err := fixedWindowsNodeRuntime(runtimeRoot, options.workerCommand)
	if err != nil {
		return nil, nil, fmt.Errorf("service: Windows job pool requires the service-owned node.exe runtime")
	}
	environmentID, environmentVersion, manifestDigest, signer := "", "", "", ""
	requireTrusted := false
	runtimeHandoff := environment.RuntimeHandoff{}
	runtimePath, workerScript, adapterScript := "", "", ""
	// A resolver-backed service creates one immutable provisioner per pool and
	// fills these signed runtime fields immediately before that pool's pass.
	// Direct/native fixtures without a resolver retain their validated handoff.
	if environmentRuntime.RuntimeResolver == nil {
		if !environmentRuntime.Handoff.Valid() && pool.PoolID != "" {
			return nil, nil, fmt.Errorf("service: trusted environment runtime is unavailable")
		}
		if environmentRuntime.Handoff.Valid() {
			workerCommand, workerScript, err = fixedWindowsWorkerRuntime(environmentRuntime.Handoff, runtimeRoot, options)
			if err != nil {
				return nil, nil, err
			}
			adapterScript = environmentRuntime.Handoff.AdapterBridgePath()
			runtimeHandoff = environmentRuntime.Handoff
			runtimePath = runtimePackageRoot(environmentRuntime.Handoff)
			environmentID, environmentVersion = pool.EnvironmentID, pool.EnvironmentVersion
			manifestDigest, signer, requireTrusted = pool.ManifestDigest, pool.Signer, pool.RequireTrusted
		}
	}
	browserMode, browserCommand := "", ""
	if options.backend == backendHeadless || options.backend == backendHeaded {
		browserMode, browserCommand = options.backend, options.headlessBrowserCommand
	}
	provisionOptions := slotwindows.Options{DataDir: cfg.DataDir, UserPrefix: cfg.WindowsJobPool.UserPrefix, EnvironmentID: environmentID, Version: environmentVersion, ManifestDigest: manifestDigest, Signer: signer, RequireTrusted: requireTrusted, RDPEnabled: cfg.WindowsJobPool.RDPEnabled, AgentPath: filepath.Join(runtimeRoot, "chuzi-user-agent.exe"), Runtime: runtimeHandoff, RuntimePath: runtimePath, WorkerRuntimeRoot: runtimeRoot, WorkerCommand: workerCommand, WorkerScript: workerScript, AdapterScript: adapterScript, BrowserMode: browserMode, BrowserCommand: browserCommand, SessionIdleTimeout: time.Duration(cfg.WindowsJobPool.SessionIdleTimeoutSeconds) * time.Second, CapabilityRevoker: revoker, RuntimeResolver: environmentRuntime.RuntimeResolver, SessionBootstrapper: resolveSessionBootstrapper(options)}
	provisioner, err := slotwindows.New(provisionOptions)
	if err != nil {
		return nil, nil, err
	}
	provisionTimeout := time.Duration(cfg.WindowsJobPool.ProvisionTimeoutSeconds) * time.Second
	cleanupTimeout := time.Duration(cfg.WindowsJobPool.CleanupTimeoutSeconds) * time.Second
	var leaseRevoker slotlifecycle.CapabilityRevoker
	if candidate, ok := revoker.(slotlifecycle.CapabilityRevoker); ok {
		leaseRevoker = candidate
	}
	allPools := &windowsAllPoolReconciler{database: database, provisioner: provisioner, provisionerFactory: func() (slotlifecycle.Provisioner, error) { return slotwindows.New(provisionOptions) }, now: now, provisionTimeout: provisionTimeout, cleanupTimeout: cleanupTimeout, leaseRevoker: leaseRevoker, owner: options.owner, items: make(map[string]*slotlifecycle.Reconciler), provisioners: make(map[string]slotlifecycle.Provisioner)}
	return allPools, allPools, nil
}

func runtimePackageRoot(runtime environment.RuntimeHandoff) string {
	if !runtime.Valid() {
		return ""
	}
	return runtime.PackageRoot()
}

// windowsAllPoolReconciler creates a dedicated OS provisioner for each pool
// while keeping one durable logical reconciler per pool. This prevents a
// signed runtime refresh for one pool from changing another pool's agents.
type windowsAllPoolReconciler struct {
	database                         *store.Store
	provisioner                      slotlifecycle.Provisioner
	provisionerFactory               func() (slotlifecycle.Provisioner, error)
	now                              func() time.Time
	provisionTimeout, cleanupTimeout time.Duration
	leaseRevoker                     slotlifecycle.CapabilityRevoker
	owner                            string
	mu                               sync.Mutex
	items                            map[string]*slotlifecycle.Reconciler
	provisioners                     map[string]slotlifecycle.Provisioner
}

func (r *windowsAllPoolReconciler) Reconcile(ctx context.Context) error { return r.ReconcileAll(ctx) }

func (r *windowsAllPoolReconciler) ReconcileAll(ctx context.Context) error {
	if r == nil || ctx == nil {
		return fmt.Errorf("service: invalid Windows slot reconciler")
	}
	pools, err := r.database.ListJobPools()
	if err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(pools))
	var first error
	for _, pool := range pools {
		if err := ctx.Err(); err != nil {
			return err
		}
		seen[pool.PoolID] = struct{}{}
		if _, err := r.database.ReconcileJobPoolControl(pool.PoolID, r.now().UTC()); err != nil && first == nil {
			first = err
			continue
		}
		reconciler, err := r.forPool(pool)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if err := reconciler.Reconcile(ctx); err != nil && first == nil {
			first = err
		}
		if _, err := r.database.ReconcileJobPoolControl(pool.PoolID, r.now().UTC()); err != nil && !errors.Is(err, slot.ErrPoolNotFound) && first == nil {
			first = err
		}
	}
	var retired []slotlifecycle.Provisioner
	r.mu.Lock()
	for poolID := range r.items {
		if _, ok := seen[poolID]; !ok {
			delete(r.items, poolID)
			if provisioner := r.provisioners[poolID]; provisioner != nil {
				retired = append(retired, provisioner)
				delete(r.provisioners, poolID)
			}
		}
	}
	r.mu.Unlock()
	for _, provisioner := range retired {
		if shutdowner, ok := provisioner.(interface{ Shutdown(context.Context) error }); ok {
			_ = shutdowner.Shutdown(ctx)
		}
	}
	return first
}

func (r *windowsAllPoolReconciler) forPool(pool slot.PoolConfig) (*slotlifecycle.Reconciler, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if value, ok := r.items[pool.PoolID]; ok {
		return value, nil
	}
	provisioner := r.provisioner
	var err error
	if r.provisionerFactory != nil {
		provisioner, err = r.provisionerFactory()
		if err != nil {
			return nil, err
		}
	}
	value, err := slotlifecycle.New(r.database, provisioner, pool, r.now, r.provisionTimeout, r.cleanupTimeout, r.leaseRevoker, r.database, r.owner)
	if err != nil {
		return nil, err
	}
	r.items[pool.PoolID] = value
	if r.provisioners == nil {
		r.provisioners = make(map[string]slotlifecycle.Provisioner)
	}
	r.provisioners[pool.PoolID] = provisioner
	return value, nil
}

func (r *windowsAllPoolReconciler) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	provisioners := make([]slotlifecycle.Provisioner, 0, len(r.provisioners))
	for _, provisioner := range r.provisioners {
		provisioners = append(provisioners, provisioner)
	}
	if len(provisioners) == 0 && r.provisionerFactory == nil && r.provisioner != nil {
		provisioners = append(provisioners, r.provisioner)
	}
	r.mu.Unlock()
	var first error
	for _, provisioner := range provisioners {
		if shutdowner, ok := provisioner.(interface{ Shutdown(context.Context) error }); ok {
			if err := shutdowner.Shutdown(ctx); err != nil && first == nil {
				first = err
			}
		}
	}
	return first
}

func (r *windowsAllPoolReconciler) provisionerForSlot(slotID string) (slotlifecycle.Provisioner, error) {
	if r == nil || r.database == nil {
		return nil, slot.ErrSlotNotFound
	}
	value, err := r.database.GetSlot(slotID)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	provisioner := r.provisioners[value.PoolID]
	if provisioner == nil && r.provisionerFactory == nil {
		provisioner = r.provisioner
	}
	r.mu.Unlock()
	if provisioner == nil {
		return nil, slot.ErrSlotUnavailable
	}
	return provisioner, nil
}

func (r *windowsAllPoolReconciler) GrantProfile(ctx context.Context, slotID, profilePath string) error {
	provisioner, err := r.provisionerForSlot(slotID)
	if err != nil {
		return err
	}
	access, ok := provisioner.(interface {
		GrantProfile(context.Context, string, string) error
	})
	if !ok {
		return slot.ErrSlotUnavailable
	}
	return access.GrantProfile(ctx, slotID, profilePath)
}

func (r *windowsAllPoolReconciler) RevokeProfile(ctx context.Context, slotID, profilePath string) error {
	provisioner, err := r.provisionerForSlot(slotID)
	if err != nil {
		return err
	}
	access, ok := provisioner.(interface {
		RevokeProfile(context.Context, string, string) error
	})
	if !ok {
		return slot.ErrSlotUnavailable
	}
	return access.RevokeProfile(ctx, slotID, profilePath)
}

func (r *windowsAllPoolReconciler) AgentEndpoint(slotID, handle string) (string, string, error) {
	provisioner, err := r.provisionerForSlot(slotID)
	if err != nil {
		return "", "", err
	}
	resolver, ok := provisioner.(interface {
		AgentEndpoint(string, string) (string, string, error)
	})
	if !ok {
		return "", "", slot.ErrSlotUnavailable
	}
	return resolver.AgentEndpoint(slotID, handle)
}

// fixedWindowsWorkerRuntime is the allowlist for the user-agent process. The
// Windows slot boundary runs only the packaged Node worker entry points; a
// service flag cannot turn a managed slot into a shell or arbitrary launcher.
func fixedWindowsWorkerRuntime(runtime environment.RuntimeHandoff, serviceRuntimeRoot string, options serviceOptions) (string, string, error) {
	if !runtime.Valid() || serviceRuntimeRoot == "" || !filepath.IsAbs(serviceRuntimeRoot) {
		return "", "", fmt.Errorf("service: invalid Windows runtime root")
	}
	packageRoot := runtime.PackageRoot()
	workerScript := runtime.WorkerPath()
	serviceRuntimeRoot = filepath.Clean(serviceRuntimeRoot)
	if !windowsRuntimeDirectory(packageRoot) || !windowsRuntimeDirectory(serviceRuntimeRoot) {
		return "", "", fmt.Errorf("service: invalid Windows runtime root")
	}
	resolved, err := fixedWindowsNodeRuntime(serviceRuntimeRoot, options.workerCommand)
	if err != nil {
		return "", "", fmt.Errorf("service: Windows job pool requires the service-owned node.exe runtime")
	}
	script := filepath.Clean(workerScript)
	if options.backend == backendNode && runtime.WorkerName() != environment.WorkerEntrypointName {
		return "", "", fmt.Errorf("service: signed environment has no deferred worker entrypoint")
	}
	if options.backend != backendNode && runtime.WorkerName() != environment.HeadlessEntrypointName {
		return "", "", fmt.Errorf("service: signed environment has no CDP worker entrypoint")
	}
	if !windowsRuntimeFile(packageRoot, script) {
		return "", "", fmt.Errorf("service: packaged browser worker entry point is unavailable")
	}
	return filepath.Clean(resolved), script, nil
}

func fixedWindowsNodeRuntime(runtimeRoot, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", fmt.Errorf("empty worker runtime")
	}
	resolved := requested
	if filepath.IsAbs(requested) {
		resolved = filepath.Clean(requested)
	} else {
		if requested != "node" && !strings.EqualFold(requested, "node.exe") {
			return "", fmt.Errorf("unsupported worker runtime")
		}
		resolved = filepath.Join(runtimeRoot, "node.exe")
	}
	resolved, err := filepath.Abs(resolved)
	if err != nil || !strings.EqualFold(filepath.Base(resolved), "node.exe") || !windowsRuntimeFile(runtimeRoot, resolved) {
		return "", fmt.Errorf("worker runtime is outside package")
	}
	return resolved, nil
}

func resolveServiceWorkerCommand(options serviceOptions, windowsPool bool) (string, error) {
	if windowsPool {
		executable, err := os.Executable()
		if err != nil {
			return "", err
		}
		return fixedWindowsNodeRuntime(filepath.Dir(executable), options.workerCommand)
	}
	return exec.LookPath(options.workerCommand)
}

func windowsRuntimeDirectory(path string) bool {
	attrs, err := windowsRuntimeAttributes(path)
	return err == nil && attrs&0x10 != 0
}

func windowsRuntimeFile(root, path string) bool {
	if !filepath.IsAbs(path) || !filepath.IsAbs(root) {
		return false
	}
	root, path = filepath.Clean(root), filepath.Clean(path)
	rel, err := filepath.Rel(strings.ToLower(root), strings.ToLower(path))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false
	}
	if !windowsRuntimeDirectory(root) {
		return false
	}
	volume := filepath.VolumeName(path)
	if volume == "" {
		return false
	}
	rest := strings.TrimLeft(strings.TrimPrefix(path, volume), `\/`)
	current := volume + string(filepath.Separator)
	if _, err := windowsRuntimeAttributes(current); err != nil {
		return false
	}
	for _, component := range strings.FieldsFunc(rest, func(r rune) bool { return r == '\\' || r == '/' }) {
		current = filepath.Join(current, component)
		attrs, err := windowsRuntimeAttributes(current)
		if err != nil || attrs&0x400 != 0 {
			return false
		}
		if current != path && attrs&0x10 == 0 {
			return false
		}
	}
	attrs, err := windowsRuntimeAttributes(path)
	return err == nil && attrs&0x10 == 0 && attrs&0x400 == 0
}

func windowsRuntimeAttributes(path string) (uint32, error) {
	wide, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.GetFileAttributes(wide)
}
