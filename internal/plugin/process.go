// Package plugin owns isolated plugin process startup. It deliberately does
// not know BetterGI or any other automation product protocol.
package plugin

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

type LaunchMode string

const (
	Native LaunchMode = "native"
	Wine   LaunchMode = "wine"
)

var (
	ErrInvalidConfig = errors.New("plugin: invalid process configuration")
	ErrNotRunning    = errors.New("plugin: process is not running")
)

// Command describes one plugin process. Arguments are passed directly to
// exec.Command; no shell expansion or string command line is supported.
type Command struct {
	Mode          LaunchMode
	Executable    string
	Args          []string
	WineExecutable string
	WinePrefix    string
	Environment   []string
	Stderr        io.Writer
}

func (c Command) Validate() error {
	if c.Mode != Native && c.Mode != Wine {
		return fmt.Errorf("%w: unknown launch mode %q", ErrInvalidConfig, c.Mode)
	}
	if strings.TrimSpace(c.Executable) == "" {
		return fmt.Errorf("%w: executable is required", ErrInvalidConfig)
	}
	if c.Mode == Native && (c.WineExecutable != "" || c.WinePrefix != "") {
		return fmt.Errorf("%w: native mode cannot include Wine settings", ErrInvalidConfig)
	}
	if c.Mode == Wine {
		if strings.TrimSpace(c.WineExecutable) == "" || strings.TrimSpace(c.WinePrefix) == "" {
			return fmt.Errorf("%w: Wine executable and prefix are required", ErrInvalidConfig)
		}
		if !filepath.IsAbs(c.WinePrefix) || filepath.Clean(c.WinePrefix) != c.WinePrefix {
			return fmt.Errorf("%w: Wine prefix must be an absolute service-derived path", ErrInvalidConfig)
		}
	}
	for _, entry := range c.Environment {
		if strings.IndexByte(entry, '=') <= 0 {
			return fmt.Errorf("%w: environment entries must be KEY=VALUE", ErrInvalidConfig)
		}
		if c.Mode == Wine && strings.EqualFold(entry[:strings.IndexByte(entry, '=')], "WINEPREFIX") {
			return fmt.Errorf("%w: WINEPREFIX is derived from WinePrefix", ErrInvalidConfig)
		}
	}
	return nil
}

// Invocation returns the executable and argv that will be passed to the OS.
// It is exposed for deterministic tests and for platform-specific diagnostics.
func (c Command) Invocation() (string, []string, error) {
	if err := c.Validate(); err != nil {
		return "", nil, err
	}
	args := append([]string(nil), c.Args...)
	if c.Mode == Wine {
		args = append([]string{c.Executable}, args...)
		return c.WineExecutable, args, nil
	}
	return c.Executable, args, nil
}

type Process struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   io.ReadCloser
	waitDone chan struct{}
	waitErr  error
	mu       sync.Mutex
	closed   bool
}

// Start launches a native or Wine-backed process without a shell. The caller
// remains responsible for speaking the plugin protocol over the process I/O.
func Start(ctx context.Context, config Command) (*Process, error) {
	return start(ctx, config, false)
}

func start(ctx context.Context, config Command, withIO bool) (*Process, error) {
	if ctx == nil {
		return nil, ErrInvalidConfig
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name, args, err := config.Invocation()
	if err != nil {
		return nil, err
	}
	command := exec.Command(name, args...)
	command.Stderr = config.Stderr
	if config.Stderr == nil {
		command.Stderr = io.Discard
	}
	command.Env = append(os.Environ(), config.Environment...)
	if config.Mode == Wine {
		command.Env = append(command.Env, "WINEPREFIX="+config.WinePrefix)
	}
	var stdin io.WriteCloser
	var stdout io.ReadCloser
	if withIO {
		var err error
		stdin, err = command.StdinPipe()
		if err != nil {
			return nil, fmt.Errorf("create plugin stdin pipe: %w", err)
		}
		stdout, err = command.StdoutPipe()
		if err != nil {
			_ = stdin.Close()
			return nil, fmt.Errorf("create plugin stdout pipe: %w", err)
		}
	}
	if err := command.Start(); err != nil {
		if stdin != nil {
			_ = stdin.Close()
		}
		if stdout != nil {
			_ = stdout.Close()
		}
		return nil, fmt.Errorf("start plugin process: %w", err)
	}
	process := &Process{cmd: command, stdin: stdin, stdout: stdout, waitDone: make(chan struct{})}
	go func() {
		waitErr := command.Wait()
		process.mu.Lock()
		process.waitErr = waitErr
		process.mu.Unlock()
		close(process.waitDone)
	}()
	return process, nil
}

func (p *Process) Wait(ctx context.Context) error {
	if p == nil || ctx == nil {
		return ErrNotRunning
	}
	select {
	case <-p.waitDone:
		p.mu.Lock()
		err := p.waitErr
		p.mu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Process) Cancel() error {
	if p == nil {
		return ErrNotRunning
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.cmd.Process == nil {
		return nil
	}
	if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	p.closed = true
	return nil
}

func (p *Process) Close(ctx context.Context) error {
	if p == nil || ctx == nil {
		return ErrNotRunning
	}
	_ = p.Cancel()
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
	if p.stdout != nil {
		_ = p.stdout.Close()
	}
	return p.Wait(ctx)
}
