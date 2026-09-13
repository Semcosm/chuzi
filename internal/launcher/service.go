package launcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

const (
	ServiceRunning ServiceStatus = "running"
	ServiceStopped ServiceStatus = "stopped"
)

type ServiceStatus string

type ServiceState struct {
	Status   ServiceStatus `json:"status"`
	PID      int           `json:"pid,omitempty"`
	ExitCode int           `json:"exit_code,omitempty"`
	HasExit  bool          `json:"has_exit"`
}

// ServiceController is intentionally a foreground process boundary. A
// platform service manager (systemd, launchd, or SCM) can implement the same
// interface later without making the launcher UI depend on one platform.
type ServiceController interface {
	Start(context.Context) (ServiceState, error)
	Stop(context.Context) error
	Status(context.Context) (ServiceState, error)
}

// ProcessServiceController manages a child service process for the lifetime
// of the caller. It never invokes a shell, never accepts credentials in args,
// and defaults child output to discard so service logs stay out of UI output.
type ProcessServiceController struct {
	Command string
	Args    []string
	Dir     string
	Env     []string
	Stdout  io.Writer
	Stderr  io.Writer

	mu      sync.Mutex
	process *exec.Cmd
	done    chan struct{}
	state   ServiceState
	waitErr error
}

func (c *ProcessServiceController) validate() error {
	if c == nil || strings.TrimSpace(c.Command) == "" {
		return fmt.Errorf("%w: service command is required", ErrInvalidPath)
	}
	if c.Dir != "" && (!filepath.IsAbs(c.Dir) || filepath.Clean(c.Dir) != c.Dir) {
		return fmt.Errorf("%w: service working directory must be absolute and clean", ErrInvalidPath)
	}
	for _, entry := range c.Env {
		if strings.IndexByte(entry, '=') <= 0 {
			return fmt.Errorf("%w: service environment entries must be KEY=VALUE", ErrInvalidPath)
		}
	}
	return nil
}

func (c *ProcessServiceController) Start(ctx context.Context) (ServiceState, error) {
	if err := contextErr(ctx); err != nil {
		return ServiceState{}, err
	}
	if err := c.validate(); err != nil {
		return ServiceState{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.process != nil {
		return ServiceState{}, ErrServiceRunning
	}
	if c.state.Status == "" {
		c.state = ServiceState{Status: ServiceStopped}
	}
	command := exec.Command(c.Command, c.Args...)
	command.Dir = c.Dir
	command.Env = append(os.Environ(), c.Env...)
	command.Stdout = c.Stdout
	if command.Stdout == nil {
		command.Stdout = io.Discard
	}
	command.Stderr = c.Stderr
	if command.Stderr == nil {
		command.Stderr = io.Discard
	}
	if err := command.Start(); err != nil {
		return ServiceState{}, fmt.Errorf("start launcher service: %w", err)
	}
	done := make(chan struct{})
	c.process = command
	c.done = done
	c.waitErr = nil
	c.state = ServiceState{Status: ServiceRunning, PID: command.Process.Pid}
	go c.wait(command, done)
	return c.state, nil
}

func (c *ProcessServiceController) wait(command *exec.Cmd, done chan struct{}) {
	err := command.Wait()
	exitCode := 0
	hasExit := false
	if command.ProcessState != nil {
		exitCode = command.ProcessState.ExitCode()
		hasExit = true
	}
	c.mu.Lock()
	if c.process == command {
		c.waitErr = err
		c.process = nil
		c.done = nil
		c.state = ServiceState{Status: ServiceStopped, ExitCode: exitCode, HasExit: hasExit}
	}
	c.mu.Unlock()
	close(done)
}

func (c *ProcessServiceController) Stop(ctx context.Context) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if err := c.validate(); err != nil {
		return err
	}
	c.mu.Lock()
	process, done := c.process, c.done
	c.mu.Unlock()
	if process == nil {
		return nil
	}
	if err := process.Process.Signal(os.Interrupt); err != nil {
		if killErr := process.Process.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
			return fmt.Errorf("stop launcher service: %w", killErr)
		}
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		if killErr := process.Process.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
			return fmt.Errorf("stop launcher service after cancellation: %w", killErr)
		}
		// Kill is asynchronous on some platforms. Reap the child before
		// returning so a caller can immediately start it again without a
		// transient ErrServiceRunning result.
		<-done
		return ctx.Err()
	}
}

func (c *ProcessServiceController) Status(ctx context.Context) (ServiceState, error) {
	if err := contextErr(ctx); err != nil {
		return ServiceState{}, err
	}
	if err := c.validate(); err != nil {
		return ServiceState{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.process != nil {
		return ServiceState{Status: ServiceRunning, PID: c.process.Process.Pid}, nil
	}
	if c.state.Status == "" {
		c.state = ServiceState{Status: ServiceStopped}
	}
	return c.state, nil
}
