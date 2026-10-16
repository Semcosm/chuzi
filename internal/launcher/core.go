package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/coretransport"
	"github.com/Semcosm/chuzi/internal/servicehost"
)

var (
	ErrCoreUnavailable        = errors.New("core_unavailable")
	ErrCoreCapabilityMismatch = errors.New("core_capability_mismatch")
	ErrCoreStartTimeout       = errors.New("core_start_timeout")
	ErrCoreStopTimeout        = errors.New("core_stop_timeout")
	ErrCoreProcessUnavailable = errors.New("core_stop_unavailable")
)

type CoreStatus struct {
	ConfiguredPoolMode    string             `json:"configured_pool_mode"`
	Installed             bool               `json:"installed"`
	Ready                 bool               `json:"ready"`
	Running               bool               `json:"running"`
	PID                   int                `json:"pid,omitempty"`
	Status                string             `json:"status"`
	Protocol              string             `json:"protocol,omitempty"`
	Methods               []string           `json:"methods,omitempty"`
	MissingMethods        []string           `json:"missing_methods,omitempty"`
	CapabilityStatus      string             `json:"capability_status,omitempty"`
	Health                servicehost.Health `json:"health"`
	HostRunning           bool               `json:"host_running"`
	CoreHandshake         bool               `json:"core_handshake"`
	LoginAdapterAvailable bool               `json:"login_adapter_available"`
	HelperAvailable       bool               `json:"helper_available"`
	RuntimeReady          bool               `json:"runtime_ready"`
	Forced                bool               `json:"forced"`
}

// CoreManager is the launcher-owned boundary for Core lifecycle and IPC.
// Callers do not need to know the endpoint format or service process details.
type CoreManager struct {
	Root         string
	RuntimeRoot  string
	ServicePath  string
	ConfigPath   string
	PIDPath      string
	StartWait    time.Duration
	controllerMu sync.Mutex
	controller   ServiceController
}

func NewCoreManager(root string) (*CoreManager, error) {
	runtimeRoot := ""
	if runtime.GOOS == "windows" {
		executable, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("%w: resolve installed runtime", ErrInvalidPath)
		}
		runtimeRoot = filepath.Dir(executable)
		// The launcher root is the signed installation directory. Mutable
		// service data is always the machine-scoped root; accepting an arbitrary
		// caller directory here would let a UI redirect the SYSTEM service.
		rawRoot := strings.TrimSpace(root)
		if rawRoot == "" || filepath.Clean(rawRoot) != rawRoot || !filepath.IsAbs(rawRoot) {
			return nil, fmt.Errorf("%w: Windows install root must be absolute and clean", ErrInvalidPath)
		}
		installRoot := filepath.Clean(rawRoot)
		if !strings.EqualFold(installRoot, filepath.Clean(runtimeRoot)) {
			// A fixed data root is accepted for service-facing callers that do not
			// have an install-root argument, while all other roots must match the
			// executable's trusted installation directory.
			dataRoot, dataErr := servicehost.FixedDataRoot()
			if dataErr != nil || !strings.EqualFold(installRoot, filepath.Clean(dataRoot)) {
				return nil, fmt.Errorf("%w: Windows root is not the installed runtime or fixed data root", ErrInvalidPath)
			}
		}
		dataRoot, err := servicehost.FixedDataRoot()
		if err != nil {
			return nil, err
		}
		return NewCoreManagerWithRuntime(dataRoot, runtimeRoot)
	}
	return NewCoreManagerWithRuntime(root, runtimeRoot)
}

// NewCoreManagerWithRuntime keeps mutable service data separate from the
// signed installation runtime. Windows callers normally use NewCoreManager,
// which derives the runtime from the installed launcher executable.
func NewCoreManagerWithRuntime(root, runtimeRoot string) (*CoreManager, error) {
	rawRoot := strings.TrimSpace(root)
	if rawRoot == "" {
		return nil, fmt.Errorf("%w: core root is required", ErrInvalidPath)
	}
	if runtime.GOOS == "windows" && (filepath.Clean(rawRoot) != rawRoot || !filepath.IsAbs(rawRoot)) {
		return nil, fmt.Errorf("%w: Windows core root must be absolute and clean", ErrInvalidPath)
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	root = filepath.Clean(root)
	if root == "." || !filepath.IsAbs(root) {
		return nil, fmt.Errorf("%w: core root must be absolute", ErrInvalidPath)
	}
	if runtime.GOOS == "windows" {
		if err := servicehost.ValidateFixedDataRoot(root); err != nil {
			return nil, err
		}
		if strings.TrimSpace(runtimeRoot) == "" {
			executable, executableErr := os.Executable()
			if executableErr != nil {
				return nil, fmt.Errorf("%w: resolve installed runtime", ErrInvalidPath)
			}
			runtimeRoot = filepath.Dir(executable)
		}
		rawRuntimeRoot := strings.TrimSpace(runtimeRoot)
		if filepath.Clean(rawRuntimeRoot) != rawRuntimeRoot || !filepath.IsAbs(rawRuntimeRoot) {
			return nil, fmt.Errorf("%w: runtime root must be absolute and clean", ErrInvalidPath)
		}
		runtimeRoot, err = filepath.Abs(rawRuntimeRoot)
		if err != nil || filepath.Clean(runtimeRoot) != runtimeRoot {
			return nil, fmt.Errorf("%w: runtime root must be absolute and clean", ErrInvalidPath)
		}
		trusted, trustErr := servicehost.NewTrustedRuntime(runtimeRoot)
		if trustErr != nil {
			return nil, trustErr
		}
		return &CoreManager{Root: root, RuntimeRoot: trusted.Root, ServicePath: trusted.Core, ConfigPath: filepath.Join(root, "core-config.json"), PIDPath: filepath.Join(root, ".core.pid"), StartWait: 5 * time.Second}, nil
	}
	return &CoreManager{
		Root:        root,
		RuntimeRoot: root,
		ServicePath: filepath.Join(root, "chuzi"),
		ConfigPath:  filepath.Join(root, "core-config.json"),
		PIDPath:     filepath.Join(root, ".core.pid"),
		StartWait:   5 * time.Second,
	}, nil
}

func (m *CoreManager) Status(ctx context.Context) (CoreStatus, error) {
	if m == nil || m.Root == "" {
		return CoreStatus{}, fmt.Errorf("%w: missing core manager", ErrInvalidPath)
	}
	if err := contextErr(ctx); err != nil {
		return CoreStatus{}, err
	}
	installed := fileExists(m.ServicePath)
	controller, controllerErr := m.serviceController()
	serviceState := ServiceState{Status: ServiceStopped}
	if controllerErr == nil {
		serviceState, controllerErr = controller.Status(ctx)
	}
	pid := serviceState.PID
	if pid <= 0 {
		pid = m.readPID()
	}
	endpointReady := false
	if runtime.GOOS == "windows" {
		if controllerErr == nil && serviceState.PID > 0 && serviceState.Status != ServiceStopped {
			endpointReady = m.readyWithPID(ctx, uint32(serviceState.PID))
		}
	} else {
		endpointReady = m.readyWithPID(ctx, 0)
	}
	// Windows readiness is SCM-backed. A reachable pipe is not sufficient when
	// the service is missing or incompatible, because a manually started or
	// same-name process must never become the production host by accident.
	running := serviceState.Status == ServiceRunning || serviceState.Status == ServiceStartPending || serviceState.Status == ServiceStopPending
	if runtime.GOOS != "windows" {
		running = running || endpointReady
	}
	hostAccepted := runtime.GOOS != "windows" || (controllerErr == nil && running)
	ready := endpointReady && hostAccepted
	// Unix launchers are short-lived callers, so their in-memory process
	// controller does not survive into a later core-status/core-stop
	// invocation. Retain the PID-file ownership check for that boundary.
	if runtime.GOOS != "windows" && !running && pid > 0 {
		running = serviceProcessMatches(pid, m.ServicePath)
	}
	status := "stopped"
	if ready {
		status = "ready"
	} else if running {
		status = "unavailable"
	} else if installed {
		status = "stopped"
	} else {
		status = "not_installed"
	}
	configuredPoolMode := "logical"
	if cfg, err := m.loadConfig(); err == nil {
		if cfg.WindowsJobPool.Enabled {
			configuredPoolMode = "windows"
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		configuredPoolMode = "unknown"
	}
	health := servicehost.Health{HostRunning: running, RuntimeReady: installed && strings.TrimSpace(m.RuntimeRoot) != "", ObservedAt: time.Now().UTC()}
	result := CoreStatus{Installed: installed, Ready: ready, Running: running, PID: pid, Status: status, ConfiguredPoolMode: configuredPoolMode, Health: health, HostRunning: health.HostRunning, RuntimeReady: health.RuntimeReady}
	if controllerErr != nil && !errors.Is(controllerErr, ErrServiceNotInstalled) {
		result.CapabilityStatus = "host_unavailable"
		if errors.Is(controllerErr, servicehost.ErrIncompatibleService) {
			result.CapabilityStatus = "incompatible"
			result.Status = "incompatible"
		}
	}
	if endpointReady {
		servicePID := uint32(0)
		if serviceState.PID > 0 {
			servicePID = uint32(serviceState.PID)
		}
		capabilities, err := m.capabilities(ctx, servicePID)
		if err != nil {
			result.CapabilityStatus = "probe_error"
		} else {
			result.Protocol = capabilities.Version
			result.Methods = capabilities.Methods
			result.CapabilityStatus = "supported"
			result.MissingMethods = missingMethods(capabilities.Methods, coretransport.SupportedMethods())
			health.CoreHandshake = true
			health.LoginAdapterAvailable = configuredPoolMode == "logical" || containsMethod(capabilities.Methods, coretransport.MethodStartSlotSession)
			// Logical mode has no interactive helper requirement. Windows job
			// mode remains false until the Core/session host reports an
			// authenticated helper registration through its health surface.
			health.HelperAvailable = configuredPoolMode == "logical"
			if len(result.MissingMethods) > 0 {
				result.CapabilityStatus = "incompatible"
				result.Ready = false
				result.Status = "incompatible"
			}
		}
	}
	result.Health = health
	result.HostRunning = health.HostRunning
	result.CoreHandshake = health.CoreHandshake
	result.LoginAdapterAvailable = health.LoginAdapterAvailable
	result.HelperAvailable = health.HelperAvailable
	result.RuntimeReady = health.RuntimeReady
	result.Forced = health.Forced
	return result, nil
}

func containsMethod(methods []string, want string) bool {
	for _, method := range methods {
		if method == want {
			return true
		}
	}
	return false
}

type coreCapabilities struct {
	Version string   `json:"version"`
	Methods []string `json:"methods"`
}

func (m *CoreManager) capabilities(ctx context.Context, servicePID uint32) (coreCapabilities, error) {
	client, err := connectCore(ctx, coretransport.EndpointPath(m.Root), coretransport.Config{ExpectedServerExecutable: m.ServicePath, ExpectedServerPID: servicePID})
	if err != nil {
		return coreCapabilities{}, err
	}
	defer client.Close()
	hello, err := client.Hello(ctx)
	if err != nil {
		return coreCapabilities{}, err
	}
	return coreCapabilities{Version: hello.Version, Methods: hello.Methods}, nil
}

func missingMethods(actual, required []string) []string {
	seen := make(map[string]struct{}, len(actual))
	for _, method := range actual {
		seen[method] = struct{}{}
	}
	missing := make([]string, 0)
	for _, method := range required {
		if _, ok := seen[method]; !ok {
			missing = append(missing, method)
		}
	}
	return missing
}

func (m *CoreManager) Start(ctx context.Context) (CoreStatus, error) {
	if err := contextErr(ctx); err != nil {
		return CoreStatus{}, err
	}
	if status, _ := m.Status(ctx); status.Ready {
		return status, nil
	}
	if status, _ := m.Status(ctx); status.Running {
		if err := m.Stop(ctx); err != nil {
			return status, err
		}
	}
	if !fileExists(m.ServicePath) {
		return CoreStatus{}, fmt.Errorf("%w: core service is not installed", ErrNotFound)
	}
	if err := m.writeConfig(); err != nil {
		return CoreStatus{}, err
	}
	controller, err := m.serviceController()
	if err != nil {
		return CoreStatus{}, err
	}
	started, err := controller.Start(ctx)
	if err != nil {
		if errors.Is(err, ErrServiceNotInstalled) {
			return CoreStatus{}, fmt.Errorf("%w: core service is not registered with the host", ErrNotFound)
		}
		return CoreStatus{}, fmt.Errorf("start core host: %w", err)
	}
	if started.PID > 0 {
		_ = os.WriteFile(m.PIDPath, []byte(strconv.Itoa(started.PID)+"\n"), 0o600)
	}
	if err := m.waitReady(ctx); err != nil {
		_ = controller.Stop(context.Background())
		_ = os.Remove(m.PIDPath)
		return CoreStatus{}, err
	}
	status, err := m.Status(ctx)
	if err != nil {
		return CoreStatus{}, err
	}
	return status, nil
}

func (m *CoreManager) Stop(ctx context.Context) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		pid := m.readPID()
		if pid <= 0 {
			if !m.ready(ctx) {
				return nil
			}
			return ErrCoreProcessUnavailable
		}
		if !serviceProcessMatches(pid, m.ServicePath) {
			_ = os.Remove(m.PIDPath)
			if !m.ready(ctx) {
				return nil
			}
			return ErrCoreProcessUnavailable
		}
		if err := stopServiceProcess(ctx, pid); err != nil {
			return err
		}
		return m.waitStopped(ctx)
	}
	controller, err := m.serviceController()
	if err != nil {
		return err
	}
	state, statusErr := controller.Status(ctx)
	if statusErr != nil && !errors.Is(statusErr, ErrServiceNotInstalled) {
		return statusErr
	}
	if state.Status != ServiceRunning && state.Status != ServiceStartPending && state.Status != ServiceStopPending {
		if !m.ready(ctx) {
			_ = os.Remove(m.PIDPath)
			return nil
		}
		return ErrCoreProcessUnavailable
	}
	if err := controller.Stop(ctx); err != nil {
		return err
	}
	return m.waitStopped(ctx)
}

func (m *CoreManager) waitStopped(ctx context.Context) error {
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		if !m.ready(ctx) {
			_ = os.Remove(m.PIDPath)
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return ErrCoreStopTimeout
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (m *CoreManager) Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	if strings.TrimSpace(method) == "" {
		return nil, fmt.Errorf("%w: core method is required", ErrInvalidPath)
	}
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	if !json.Valid(params) {
		return nil, fmt.Errorf("%w: invalid core parameters", ErrInvalidPath)
	}
	servicePID := uint32(0)
	if runtime.GOOS == "windows" {
		var pidErr error
		servicePID, pidErr = m.runningServicePID(ctx)
		if pidErr != nil {
			return nil, ErrCoreUnavailable
		}
	}
	client, err := connectCore(ctx, coretransport.EndpointPath(m.Root), coretransport.Config{ExpectedServerExecutable: m.ServicePath, ExpectedServerPID: servicePID})
	if err != nil {
		return nil, ErrCoreUnavailable
	}
	defer client.Close()
	var result json.RawMessage
	if err := client.Call(ctx, method, params, &result); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		result = json.RawMessage(`null`)
	}
	return result, nil
}

func (m *CoreManager) ready(ctx context.Context) bool {
	if runtime.GOOS == "windows" {
		servicePID, err := m.runningServicePID(ctx)
		if err != nil {
			return false
		}
		return m.readyWithPID(ctx, servicePID)
	}
	return m.readyWithPID(ctx, 0)
}

func (m *CoreManager) readyWithPID(ctx context.Context, servicePID uint32) bool {
	client, err := connectCore(ctx, coretransport.EndpointPath(m.Root), coretransport.Config{ExpectedServerExecutable: m.ServicePath, ExpectedServerPID: servicePID})
	if err != nil {
		return false
	}
	_ = client.Close()
	return true
}

func (m *CoreManager) runningServicePID(ctx context.Context) (uint32, error) {
	controller, err := m.serviceController()
	if err != nil {
		return 0, err
	}
	state, err := controller.Status(ctx)
	if err != nil {
		return 0, err
	}
	if state.PID <= 0 || (state.Status != ServiceRunning && state.Status != ServiceStartPending && state.Status != ServiceStopPending) {
		return 0, ErrCoreUnavailable
	}
	return uint32(state.PID), nil
}

func (m *CoreManager) waitReady(ctx context.Context) error {
	wait := m.StartWait
	if wait <= 0 {
		wait = 5 * time.Second
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		if m.ready(ctx) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return ErrCoreStartTimeout
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (m *CoreManager) writeConfig() error {
	if _, err := m.loadConfig(); err == nil {
		return nil // Deployment settings survive every managed restart.
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("core_config_invalid")
	}
	if err := os.MkdirAll(m.Root, 0o700); err != nil {
		return err
	}
	content := map[string]any{
		"data_dir": m.Root,
		"credentials": map[string]string{
			"key_env":     "CHUZI_CREDENTIAL_KEY",
			"key_id_env":  "CHUZI_CREDENTIAL_KEY_ID",
			"history_env": "CHUZI_CREDENTIAL_KEYS",
		},
		"health":        map[string]string{"listen": ""},
		"observability": map[string]any{"metrics_listen": "", "log_path": filepath.Join(m.Root, "core-service.log"), "log_max_bytes": 10485760, "log_max_files": 5},
	}
	data, err := json.MarshalIndent(content, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(m.ConfigPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		_, loadErr := m.loadConfig()
		if loadErr != nil {
			return errors.New("core_config_invalid")
		}
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	return file.Sync()
}

func (m *CoreManager) loadConfig() (config.Config, error) {
	cfg, err := config.Load(m.ConfigPath)
	if err != nil {
		return config.Config{}, err
	}
	if cfg.DataDir != m.Root {
		return config.Config{}, errors.New("core_config_invalid")
	}
	return cfg, nil
}

// SavePoolMode delegates offline validation to Core. The service owns Store
// and package validation; the launcher owns lifecycle and the mutation lock.
func (m *CoreManager) SavePoolMode(ctx context.Context, mode, poolID string, revision uint64) (json.RawMessage, error) {
	if mode != "logical" && mode != "windows" {
		return nil, errors.New("invalid_pool_mode")
	}
	status, err := m.Status(ctx)
	if err != nil {
		return nil, err
	}
	if status.Running {
		return nil, errors.New("core_stop_required")
	}
	if !status.Installed {
		return nil, ErrNotFound
	}
	if err := m.writeConfig(); err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, m.ServicePath, "-config", m.ConfigPath, "-configure-pool-mode", mode, "-pool-id", poolID, "-expected-revision", strconv.FormatUint(revision, 10))
	command.Dir = m.RuntimeRoot
	var stderr strings.Builder
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		// Only closed failure classes cross the launcher/UI boundary.
		for _, code := range []string{"pool_cleanup_required", "signed_environment_required", "environment_unavailable", "stale_revision", "pool_not_found", "windows_required", "core_config_invalid", "core_stop_required"} {
			if strings.TrimSpace(stderr.String()) == "chuzi: "+code {
				return nil, errors.New(code)
			}
		}
		return nil, errors.New("pool_mode_save_failed")
	}
	if !json.Valid(output) {
		return nil, errors.New("pool_mode_save_failed")
	}
	return output, nil
}

func (m *CoreManager) readPID() int {
	data, err := os.ReadFile(m.PIDPath)
	if err != nil {
		return 0
	}
	value, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || value <= 0 {
		return 0
	}
	return value
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
