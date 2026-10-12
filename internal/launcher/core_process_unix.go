//go:build !windows

package launcher

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
)

func (m *CoreManager) serviceController() (ServiceController, error) {
	m.controllerMu.Lock()
	defer m.controllerMu.Unlock()
	if m.controller != nil {
		return m.controller, nil
	}
	m.controller = &ProcessServiceController{Command: m.ServicePath, Args: []string{"-config", m.ConfigPath}, Dir: m.Root, Env: []string{"CHUZI_DATA_DIR=" + m.Root}}
	return m.controller, nil
}

func serviceProcessMatches(pid int, expected string) bool {
	if pid <= 0 {
		return false
	}
	processPath, err := filepath.EvalSymlinks("/proc/" + strconv.Itoa(pid) + "/exe")
	if err != nil {
		return false
	}
	expectedPath, err := filepath.EvalSymlinks(expected)
	if err != nil {
		return false
	}
	return filepath.Clean(processPath) == filepath.Clean(expectedPath)
}

func stopServiceProcess(ctx context.Context, pid int) error {
	command := exec.CommandContext(ctx, "kill", "-TERM", strconv.Itoa(pid))
	if err := command.Run(); err != nil {
		return fmt.Errorf("stop core: %w", err)
	}
	return nil
}

func stopOrphanedService(ctx context.Context, executable string) error {
	return fmt.Errorf("stop orphaned core is unsupported for %s", executable)
}
