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

	"github.com/Semcosm/chuzi/internal/coretransport"
)

var (
	ErrCoreUnavailable        = errors.New("core_unavailable")
	ErrCoreStartTimeout       = errors.New("core_start_timeout")
	ErrCoreStopTimeout        = errors.New("core_stop_timeout")
	ErrCoreProcessUnavailable = errors.New("core_stop_unavailable")
)

type CoreStatus struct {
	Installed bool   `json:"installed"`
	Ready     bool   `json:"ready"`
	Running   bool   `json:"running"`
	PID       int    `json:"pid,omitempty"`
	Status    string `json:"status"`
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
	ready := m.ready(ctx)
	running := ready
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
	return CoreStatus{Installed: installed, Ready: ready, Running: running, PID: pid, Status: status}, nil
}

func (m *CoreManager) Start(ctx context.Context) (CoreStatus, error) {
	if err := contextErr(ctx); err != nil {
		return CoreStatus{}, err
	}
	if status, _ := m.Status(ctx); status.Ready {
		return status, nil
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
		if status, _ := m.Status(ctx); status.Ready {
			return ErrCoreProcessUnavailable
		}
		return nil
	}
	if !serviceProcessMatches(pid, m.ServicePath) {
		_ = os.Remove(m.PIDPath)
		return nil
	}
	if err := stopServiceProcess(ctx, pid); err != nil {
		return err
	}
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
	return os.WriteFile(m.ConfigPath, data, 0o600)
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
