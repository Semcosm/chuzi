package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/coretransport"
)

var (
	ErrCoreUnavailable        = errors.New("core_unavailable")
	ErrCoreCapabilityMismatch = errors.New("core_capability_mismatch")
	ErrCoreStartTimeout       = errors.New("core_start_timeout")
	ErrCoreStopTimeout        = errors.New("core_stop_timeout")
	ErrCoreProcessUnavailable = errors.New("core_stop_unavailable")
)

type CoreStatus struct {
	ConfiguredPoolMode string   `json:"configured_pool_mode"`
	Installed          bool     `json:"installed"`
	Ready              bool     `json:"ready"`
	Running            bool     `json:"running"`
	PID                int      `json:"pid,omitempty"`
	Status             string   `json:"status"`
	Protocol           string   `json:"protocol,omitempty"`
	Methods            []string `json:"methods,omitempty"`
	MissingMethods     []string `json:"missing_methods,omitempty"`
	CapabilityStatus   string   `json:"capability_status,omitempty"`
}

// CoreManager is the launcher-owned boundary for Core lifecycle and IPC.
// Callers do not need to know the endpoint format or service process details.
type CoreManager struct {
	Root        string
	ServicePath string
	ConfigPath  string
	PIDPath     string
	StartWait   time.Duration
}

func NewCoreManager(root string) (*CoreManager, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	root = filepath.Clean(root)
	if root == "." || !filepath.IsAbs(root) {
		return nil, fmt.Errorf("%w: core root must be absolute", ErrInvalidPath)
	}
	service := "chuzi"
	if runtime.GOOS == "windows" {
		service += ".exe"
	}
	return &CoreManager{
		Root:        root,
		ServicePath: filepath.Join(root, service),
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
	pid := m.readPID()
	endpointReady := m.ready(ctx)
	ready := endpointReady
	running := endpointReady
	if !running && pid > 0 {
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
	result := CoreStatus{Installed: installed, Ready: ready, Running: running, PID: pid, Status: status}
	result.ConfiguredPoolMode = "logical"
	if cfg, err := m.loadConfig(); err == nil {
		if cfg.WindowsJobPool.Enabled {
			result.ConfiguredPoolMode = "windows"
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		result.ConfiguredPoolMode = "unknown"
	}
	if endpointReady {
		capabilities, err := m.capabilities(ctx)
		if err != nil {
			result.CapabilityStatus = "probe_error"
		} else {
			result.Protocol = capabilities.Version
			result.Methods = capabilities.Methods
			result.CapabilityStatus = "supported"
			result.MissingMethods = missingMethods(capabilities.Methods, coretransport.SupportedMethods())
			if len(result.MissingMethods) > 0 {
				result.CapabilityStatus = "incompatible"
				result.Ready = false
				result.Status = "incompatible"
			}
		}
	}
	return result, nil
}

type coreCapabilities struct {
	Version string   `json:"version"`
	Methods []string `json:"methods"`
}

func (m *CoreManager) capabilities(ctx context.Context) (coreCapabilities, error) {
	client, err := coretransport.Connect(ctx, coretransport.EndpointPath(m.Root), coretransport.Config{})
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
		if status.PID <= 0 {
			return status, ErrCoreCapabilityMismatch
		}
		if err := m.Stop(ctx); err != nil {
			return status, err
		}
	}
	if !fileExists(m.ServicePath) {
		return CoreStatus{}, fmt.Errorf("%w: core service is not installed", ErrNotFound)
	}
	if pid := m.readPID(); pid > 0 && serviceProcessMatches(pid, m.ServicePath) {
		status, _ := m.Status(ctx)
		return status, ErrCoreUnavailable
	}
	if err := m.writeConfig(); err != nil {
		return CoreStatus{}, err
	}
	command := exec.Command(m.ServicePath, "-config", m.ConfigPath)
	command.Dir = m.Root
	command.Env = append(os.Environ(), "CHUZI_DATA_DIR="+m.Root)
	command.Stdin = nil
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return CoreStatus{}, fmt.Errorf("start core: %w", err)
	}
	if err := os.WriteFile(m.PIDPath, []byte(strconv.Itoa(command.Process.Pid)+"\n"), 0o600); err != nil {
		_ = command.Process.Kill()
		return CoreStatus{}, fmt.Errorf("write core pid: %w", err)
	}
	if err := m.waitReady(ctx); err != nil {
		_ = stopServiceProcess(context.Background(), command.Process.Pid)
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
	pid := m.readPID()
	if pid <= 0 {
		if !m.ready(ctx) {
			return nil
		}
		if err := stopOrphanedService(ctx, filepath.Base(m.ServicePath)); err != nil {
			return ErrCoreProcessUnavailable
		}
		return m.waitStopped(ctx)
	}
	if !serviceProcessMatches(pid, m.ServicePath) {
		_ = os.Remove(m.PIDPath)
		if m.ready(ctx) {
			if err := stopOrphanedService(ctx, filepath.Base(m.ServicePath)); err != nil {
				return ErrCoreProcessUnavailable
			}
			return m.waitStopped(ctx)
		}
		return nil
	}
	if err := stopServiceProcess(ctx, pid); err != nil {
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
	client, err := coretransport.Connect(ctx, coretransport.EndpointPath(m.Root), coretransport.Config{})
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
	client, err := coretransport.Connect(ctx, coretransport.EndpointPath(m.Root), coretransport.Config{})
	if err != nil {
		return false
	}
	_ = client.Close()
	return true
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
	command.Dir = m.Root
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
