//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func newSlotReconciler(cfg config.Config, options serviceOptions, database *store.Store, now func() time.Time, revoker slotCapabilityRevoker, environmentRuntime *serviceEnvironmentRuntime, pool slot.PoolConfig) (slotReconciler, slotProfileAccess, error) {
	if !cfg.WindowsJobPool.Enabled || pool.PoolID == "" {
		return nil, nil, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	runtimeRoot := filepath.Dir(executable)
	if environmentRuntime == nil || !environmentRuntime.Handoff.Valid() {
		return nil, nil, fmt.Errorf("service: trusted environment runtime is unavailable")
	}
	workerCommand, workerScript, err := fixedWindowsWorkerRuntime(environmentRuntime.Handoff, runtimeRoot, options)
	if err != nil {
		return nil, nil, err
	}
	browserMode, browserCommand := "", ""
	if options.backend == backendHeadless || options.backend == backendHeaded {
		browserMode, browserCommand = options.backend, options.headlessBrowserCommand
	}
	provisionOptions := slotwindows.Options{DataDir: cfg.DataDir, UserPrefix: cfg.WindowsJobPool.UserPrefix, EnvironmentID: pool.EnvironmentID, Version: pool.EnvironmentVersion, ManifestDigest: pool.ManifestDigest, Signer: pool.Signer, RequireTrusted: pool.RequireTrusted, RDPEnabled: cfg.WindowsJobPool.RDPEnabled, AgentPath: filepath.Join(runtimeRoot, "chuzi-user-agent.exe"), Runtime: environmentRuntime.Handoff, RuntimePath: environmentRuntime.Handoff.PackageRoot(), WorkerRuntimeRoot: runtimeRoot, WorkerCommand: workerCommand, WorkerScript: workerScript, AdapterScript: environmentRuntime.Handoff.AdapterBridgePath(), BrowserMode: browserMode, BrowserCommand: browserCommand, SessionIdleTimeout: time.Duration(cfg.WindowsJobPool.SessionIdleTimeoutSeconds) * time.Second, CapabilityRevoker: revoker, RuntimeResolver: environmentRuntime.RuntimeResolver}
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
	reconciler, err := slotlifecycle.New(database, provisioner, pool, now, provisionTimeout, cleanupTimeout, leaseRevoker, database, options.owner)
	if err != nil {
		return nil, nil, err
	}
	return reconciler, provisioner, nil
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
