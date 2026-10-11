//go:build windows

package servicehost

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

type scmController struct {
	runtime    TrustedRuntime
	dataRoot   string
	configPath string
}

func NewController(runtime TrustedRuntime, configPath string) (Controller, error) {
	dataRoot, err := FixedDataRoot()
	if err != nil {
		return nil, err
	}
	return NewControllerWithDataRoot(runtime, dataRoot, configPath)
}

func NewControllerWithDataRoot(runtime TrustedRuntime, dataRoot, configPath string) (Controller, error) {
	if err := runtime.Validate(); err != nil || !fixedDataRoot(dataRoot) || !fixedConfigPath(dataRoot, configPath) {
		return nil, ErrInvalidConfig
	}
	return scmController{runtime: runtime, dataRoot: filepath.Clean(dataRoot), configPath: filepath.Clean(configPath)}, nil
}

func fixedConfigPath(root, path string) bool {
	raw := strings.TrimSpace(path)
	if raw == "" || filepath.Clean(raw) != raw || !filepath.IsAbs(raw) {
		return false
	}
	path = filepath.Clean(raw)
	rel, err := filepath.Rel(root, path)
	return err == nil && rel == "core-config.json"
}

func fixedDataRoot(root string) bool {
	expected, err := FixedDataRoot()
	if err != nil {
		return false
	}
	raw := strings.TrimSpace(root)
	clean := filepath.Clean(raw)
	return raw == clean && strings.EqualFold(clean, expected)
}

// FixedDataRoot returns the only mutable data root accepted by the Windows
// service boundary. It is derived from the machine ProgramData known folder,
// never from a caller-controlled environment variable.
func FixedDataRoot() (string, error) {
	base, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil || strings.TrimSpace(base) == "" {
		return "", fmt.Errorf("%w: resolve ProgramData", ErrHostUnavailable)
	}
	return filepath.Clean(filepath.Join(base, "chuzi", "data")), nil
}

func ValidateFixedDataRoot(root string) error {
	if !fixedDataRoot(root) {
		return ErrInvalidConfig
	}
	return nil
}

func (c scmController) open() (*mgr.Mgr, *mgr.Service, error) {
	m, err := mgr.Connect()
	if err != nil {
		return nil, nil, ErrHostUnavailable
	}
	s, err := m.OpenService(ServiceName)
	if err != nil {
		_ = m.Disconnect()
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return nil, nil, ErrNotInstalled
		}
		return nil, nil, ErrHostUnavailable
	}
	return m, s, nil
}

func (c scmController) compatible(s *mgr.Service) error {
	cfg, err := s.Config()
	if err != nil {
		return ErrHostUnavailable
	}
	if !compatibleConfig(cfg, c.runtime, c.configPath) {
		return ErrIncompatibleService
	}
	return nil
}

func (c scmController) Status(ctx context.Context) (Status, error) {
	if err := ctx.Err(); err != nil {
		return Status{}, err
	}
	m, s, err := c.open()
	if err != nil {
		if errors.Is(err, ErrNotInstalled) {
			return Status{State: StateStopped}, err
		}
		return Status{}, err
	}
	defer m.Disconnect()
	defer s.Close()
	if err := c.compatible(s); err != nil {
		return Status{}, err
	}
	status, err := s.Query()
	if err != nil {
		return Status{}, ErrHostUnavailable
	}
	return Status{State: mapServiceState(status.State), PID: int(status.ProcessId)}, nil
}

func mapServiceState(state svc.State) ServiceState {
	switch state {
	case svc.Running:
		return StateRunning
	case svc.StartPending:
		return StateStartPending
	case svc.StopPending:
		return StateStopPending
	default:
		return StateStopped
	}
}

func (c scmController) Start(ctx context.Context) (Status, error) {
	if err := ctx.Err(); err != nil {
		return Status{}, err
	}
	m, s, err := c.open()
	if err != nil {
		return Status{}, err
	}
	defer m.Disconnect()
	defer s.Close()
	if err := c.compatible(s); err != nil {
		return Status{}, err
	}
	status, err := s.Query()
	if err == nil && (status.State == svc.Running || status.State == svc.StartPending) {
		return Status{State: mapServiceState(status.State), PID: int(status.ProcessId)}, nil
	}
	if err == nil && status.State != svc.Stopped {
		return Status{}, ErrHostUnavailable
	}
	if err := s.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return Status{}, ErrHostUnavailable
	}
	return waitSCMState(ctx, s, true)
}

func (c scmController) Stop(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m, s, err := c.open()
	if err != nil {
		if errors.Is(err, ErrNotInstalled) {
			return nil
		}
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	if err := c.compatible(s); err != nil {
		return err
	}
	status, queryErr := s.Query()
	if queryErr == nil && status.State == svc.Stopped {
		return nil
	}
	if _, err := s.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
		return ErrHostUnavailable
	}
	_, err = waitSCMState(ctx, s, false)
	return err
}

func waitSCMState(ctx context.Context, s *mgr.Service, running bool) (Status, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return Status{}, err
		}
		status, err := s.Query()
		if err != nil {
			return Status{}, ErrHostUnavailable
		}
		if (running && status.State == svc.Running) || (!running && status.State == svc.Stopped) {
			return Status{State: mapServiceState(status.State), PID: int(status.ProcessId)}, nil
		}
		select {
		case <-ctx.Done():
			return Status{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

// Install registers only the fixed service executable and fixed config path.
// It never accepts caller-provided command text or service identity.
func Install(ctx context.Context, runtime TrustedRuntime, configPath string) error {
	dataRoot, err := FixedDataRoot()
	if err != nil {
		return err
	}
	return InstallWithDataRoot(ctx, runtime, dataRoot, configPath)
}

func InstallWithDataRoot(ctx context.Context, runtime TrustedRuntime, dataRoot, configPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := runtime.Validate(); err != nil || !fixedDataRoot(dataRoot) || !fixedConfigPath(dataRoot, configPath) {
		return ErrInvalidConfig
	}
	m, err := mgr.Connect()
	if err != nil {
		return ErrHostUnavailable
	}
	defer m.Disconnect()
	if existing, openErr := m.OpenService(ServiceName); openErr == nil {
		defer existing.Close()
		cfg, cfgErr := existing.Config()
		if cfgErr != nil {
			return ErrHostUnavailable
		}
		if !compatibleConfig(cfg, runtime, configPath) {
			return ErrIncompatibleService
		}
		return nil
	} else if !errors.Is(openErr, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return ErrHostUnavailable
	}
	s, err := m.CreateService(ServiceName, runtime.Core, mgr.Config{DisplayName: ServiceDisplayName, StartType: mgr.StartAutomatic, ServiceStartName: ServiceRunAs, SidType: windows.SERVICE_SID_TYPE_UNRESTRICTED}, ServiceModeFlag, "-config", filepath.Clean(configPath))
	if err != nil {
		return ErrHostUnavailable
	}
	defer s.Close()
	return nil
}

func Uninstall(ctx context.Context) error {
	// The parameterless entrypoint is retained for compatibility with older
	// callers, but it must fail closed when a service exists: deleting a
	// same-named service without validating its installed runtime would allow
	// an unrelated ChuziCore to be removed. A missing service remains
	// idempotently successful. The signed management entrypoint should call
	// UninstallWithDataRoot with the runtime and config it owns.
	if err := ctx.Err(); err != nil {
		return err
	}
	m, err := mgr.Connect()
	if err != nil {
		return ErrHostUnavailable
	}
	defer m.Disconnect()
	s, err := m.OpenService(ServiceName)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return nil
	}
	if err != nil {
		return ErrHostUnavailable
	}
	_ = s.Close()
	return ErrInvalidConfig
}

// UninstallWithDataRoot removes only a service whose complete SCM
// configuration matches the fixed runtime/config identity supplied by the
// caller. A missing service remains idempotently successful.
func UninstallWithDataRoot(ctx context.Context, runtime TrustedRuntime, dataRoot, configPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := runtime.Validate(); err != nil || !fixedDataRoot(dataRoot) || !fixedConfigPath(dataRoot, configPath) {
		return ErrInvalidConfig
	}
	m, s, err := func() (*mgr.Mgr, *mgr.Service, error) {
		m, err := mgr.Connect()
		if err != nil {
			return nil, nil, ErrHostUnavailable
		}
		s, err := m.OpenService(ServiceName)
		if err != nil {
			_ = m.Disconnect()
			if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
				return nil, nil, nil
			}
			return nil, nil, ErrHostUnavailable
		}
		return m, s, nil
	}()
	if err != nil || s == nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	if cfg, cfgErr := s.Config(); cfgErr != nil {
		return ErrHostUnavailable
	} else if !compatibleConfig(cfg, runtime, configPath) {
		return ErrIncompatibleService
	}
	status, queryErr := s.Query()
	if queryErr != nil {
		return ErrHostUnavailable
	}
	if status.State != svc.Stopped {
		if _, stopErr := s.Control(svc.Stop); stopErr != nil && !errors.Is(stopErr, windows.ERROR_SERVICE_NOT_ACTIVE) && !errors.Is(stopErr, windows.ERROR_SERVICE_CANNOT_ACCEPT_CTRL) {
			return ErrHostUnavailable
		}
		if _, waitErr := waitSCMState(ctx, s, false); waitErr != nil {
			return waitErr
		}
	}
	if err := s.Delete(); err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
			return nil
		}
		return ErrHostUnavailable
	}
	return nil
}

func compatibleConfig(cfg mgr.Config, runtime TrustedRuntime, configPath string) bool {
	if !strings.EqualFold(cfg.ServiceStartName, ServiceRunAs) || cfg.DisplayName != ServiceDisplayName ||
		cfg.SidType != windows.SERVICE_SID_TYPE_UNRESTRICTED ||
		cfg.ServiceType != windows.SERVICE_WIN32_OWN_PROCESS ||
		cfg.StartType != windows.SERVICE_AUTO_START {
		return false
	}
	want := []string{runtime.Core, ServiceModeFlag, "-config", filepath.Clean(configPath)}
	got, ok := parseWindowsCommandLine(cfg.BinaryPathName)
	if !ok || len(got) != len(want) {
		return false
	}
	for index := range want {
		if index == 0 || index == len(want)-1 {
			if !strings.EqualFold(filepath.Clean(got[index]), filepath.Clean(want[index])) {
				return false
			}
			continue
		}
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
