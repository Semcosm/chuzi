//go:build windows

package launcher

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Semcosm/chuzi/internal/servicehost"
)

type scmServiceController struct{ inner servicehost.Controller }

func newSCMServiceController(runtimeRoot, dataRoot, servicePath, configPath string) (ServiceController, error) {
	runtime, err := servicehost.NewTrustedRuntime(runtimeRoot)
	if err != nil || normalizeWindowsPath(servicePath) != normalizeWindowsPath(runtime.Core) {
		return nil, ErrInvalidPath
	}
	inner, err := servicehost.NewControllerWithDataRoot(runtime, dataRoot, configPath)
	if err != nil {
		return nil, err
	}
	return &scmServiceController{inner: inner}, nil
}

func (c *scmServiceController) Start(ctx context.Context) (ServiceState, error) {
	status, err := c.inner.Start(ctx)
	return mapSCMState(status), mapSCMError(err)
}
func (c *scmServiceController) Stop(ctx context.Context) error { return mapSCMError(c.inner.Stop(ctx)) }
func (c *scmServiceController) Status(ctx context.Context) (ServiceState, error) {
	status, err := c.inner.Status(ctx)
	return mapSCMState(status), mapSCMError(err)
}
func mapSCMState(status servicehost.Status) ServiceState {
	state := ServiceStopped
	switch status.State {
	case servicehost.StateRunning:
		state = ServiceRunning
	case servicehost.StateStartPending:
		state = ServiceStartPending
	case servicehost.StateStopPending:
		state = ServiceStopPending
	}
	return ServiceState{Status: state, PID: status.PID}
}
func mapSCMError(err error) error {
	if errors.Is(err, servicehost.ErrNotInstalled) {
		return ErrServiceNotInstalled
	}
	return err
}
func (m *CoreManager) serviceController() (ServiceController, error) {
	m.controllerMu.Lock()
	defer m.controllerMu.Unlock()
	if m.controller != nil {
		return m.controller, nil
	}
	runtimeRoot := m.RuntimeRoot
	if runtimeRoot == "" {
		runtimeRoot = m.Root
	}
	controller, err := newSCMServiceController(runtimeRoot, m.Root, m.ServicePath, m.ConfigPath)
	if err != nil {
		return nil, err
	}
	m.controller = controller
	return controller, nil
}

func serviceProcessMatches(pid int, expected string) bool {
	_ = pid
	_ = expected
	return false
}

func stopServiceProcess(ctx context.Context, pid int) error {
	_ = ctx
	_ = pid
	return fmt.Errorf("stop core is refused without SCM ownership")
}

func stopOrphanedService(ctx context.Context, executable string) error {
	_ = ctx
	_ = executable
	return fmt.Errorf("stop orphaned core is refused without SCM ownership")
}

func normalizeWindowsPath(value string) string {
	path := strings.TrimSpace(strings.ReplaceAll(value, "/", "\\"))
	if stripped := strings.TrimPrefix(path, `\\?\UNC\`); stripped != path {
		path = `\\` + stripped
	} else if stripped := strings.TrimPrefix(path, `\\?\`); stripped != path {
		path = stripped
	}
	return strings.ToLower(strings.TrimRight(filepath.Clean(path), `\`))
}
