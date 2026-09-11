package browser

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/internal/protocol"
)

var (
	ErrInvalidProcessConfig = errors.New("browser: invalid worker process configuration")
	ErrWorkerNotRunning     = errors.New("browser: worker is not running")
)

// ProcessFactory starts a versioned JSON Lines worker process. It is a
// protocol adapter only; it does not download or launch a browser runtime.
type ProcessFactory struct {
	command    string
	args       []string
	stderr     io.Writer
	workerMode string
}

// ProcessConfig configures the executable boundary. WorkerMode is intended
// for local/fake protocol fixtures (success, failure, hold, crash); the
// default deferred mode reports that real browser automation is not enabled.
type ProcessConfig struct {
	Command    string
	Script     string
	// ScriptArgs are appended after the standard --stdio argument. They are
	// restricted to the worker's own startup configuration and are not shell
	// parsed. Args and ScriptArgs cannot be combined.
	ScriptArgs []string
	// Args contains the complete argument list for helpers that do not use a
	// script entry point, such as the Rust browser runtime. A nil Args value
	// retains compatibility with Script and invokes <command> <script> --stdio;
	// a non-nil empty slice intentionally invokes the command with no args.
	Args       []string
	Stderr     io.Writer
	WorkerMode string
}

func NewProcessFactory(config ProcessConfig) (*ProcessFactory, error) {
	if strings.TrimSpace(config.Command) == "" ||
		(config.Args == nil && strings.TrimSpace(config.Script) == "") ||
		(config.Args != nil && strings.TrimSpace(config.Script) != "") ||
		(config.Args != nil && len(config.ScriptArgs) != 0) {
		return nil, ErrInvalidProcessConfig
	}
	if config.Stderr == nil {
		config.Stderr = io.Discard
	}
	var args []string
	if config.Args == nil {
		args = []string{config.Script, "--stdio"}
		args = append(args, config.ScriptArgs...)
	} else {
		args = make([]string, len(config.Args))
		copy(args, config.Args)
	}
	return &ProcessFactory{
		command:    config.Command,
		args:       args,
		stderr:     config.Stderr,
		workerMode: config.WorkerMode,
	}, nil
}

func (f *ProcessFactory) Start(ctx context.Context, spec WorkerSpec) (Worker, error) {
	if f == nil || ctx == nil || ctx.Err() != nil {
		return nil, ErrInvalidProcessConfig
	}
	if err := spec.validate(); err != nil {
		return nil, err
	}
	if f.workerMode != "" {
		spec.Mode = f.workerMode
	}
	command := exec.Command(f.command, f.args...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("create worker stdout pipe: %w", err)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("create worker stdin pipe: %w", err)
	}
	command.Stderr = f.stderr
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start browser worker: %w", err)
	}
	worker := &processWorker{
		cmd:       command,
		stdin:     stdin,
		messages:  make(chan protocol.Envelope, 8),
		readDone:  make(chan struct{}),
		processDone: make(chan struct{}),
	}
	go worker.readLoop(stdout)
	go worker.waitLoop()
	hello := protocol.Request(worker.nextID("hello"), protocol.Hello, map[string]string{
		"service": "chuzi-session-runner",
		"version": protocol.Version,
	})
	if _, err := worker.await(ctx, hello, func(response protocol.Envelope) bool {
		return response.Type == protocol.HelloAck
	}); err != nil {
		_ = worker.kill()
		return nil, err
	}
	worker.spec = spec
	return worker, nil
}

type processWorker struct {
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	writeMu     sync.Mutex
	stateMu     sync.Mutex
	closed      bool
	closing     bool
	next        uint64
	commandErr  error
	processDone chan struct{}
	readErr     error
	readDone    chan struct{}
	messages    chan protocol.Envelope
	spec        WorkerSpec
}

func (w *processWorker) nextID(prefix string) string {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	w.next++
	return fmt.Sprintf("%s-%d", prefix, w.next)
}

func (w *processWorker) readLoop(stdout io.Reader) {
	decoder := json.NewDecoder(bufio.NewReader(stdout))
	for {
		var message protocol.Envelope
		if err := decoder.Decode(&message); err != nil {
			w.stateMu.Lock()
			if !errors.Is(err, io.EOF) {
				w.readErr = fmt.Errorf("%w: read worker message: %v", ErrWorkerCrashed, err)
			}
			close(w.readDone)
			w.stateMu.Unlock()
			return
		}
		select {
		case w.messages <- message:
		case <-w.processDone:
			return
		}
	}
}

func (w *processWorker) waitLoop() {
	err := w.cmd.Wait()
	w.stateMu.Lock()
	w.commandErr = err
	close(w.processDone)
	w.stateMu.Unlock()
}

func (w *processWorker) send(request protocol.Envelope) error {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	w.stateMu.Lock()
	closed := w.closed
	w.stateMu.Unlock()
	if closed {
		return ErrWorkerNotRunning
	}
	if err := json.NewEncoder(w.stdin).Encode(request); err != nil {
		return fmt.Errorf("send worker message: %w", err)
	}
	return nil
}

func (w *processWorker) await(ctx context.Context, request protocol.Envelope, match func(protocol.Envelope) bool) (protocol.Envelope, error) {
	if err := w.send(request); err != nil {
		return protocol.Envelope{}, err
	}
	return w.waitFor(ctx, request, match)
}

func (w *processWorker) waitFor(ctx context.Context, request protocol.Envelope, match func(protocol.Envelope) bool) (protocol.Envelope, error) {
	for {
		select {
		case <-ctx.Done():
			return protocol.Envelope{}, ctx.Err()
		case message := <-w.messages:
			if message.Protocol != protocol.Version {
				return protocol.Envelope{}, fmt.Errorf("%w: protocol %q", ErrWorkerProtocol, message.Protocol)
			}
			if message.Error != "" {
				return protocol.Envelope{}, fmt.Errorf("%w: worker rejected %s", ErrWorkerProtocol, request.Type)
			}
			if message.ID != request.ID || !match(message) {
				continue
			}
			return message, nil
		case <-w.readDone:
			w.stateMu.Lock()
			err := w.readErr
			w.stateMu.Unlock()
			if err != nil {
				return protocol.Envelope{}, err
			}
			return protocol.Envelope{}, ErrWorkerCrashed
		case <-w.processDone:
			w.stateMu.Lock()
			err := w.commandErr
			w.stateMu.Unlock()
			if err != nil {
				return protocol.Envelope{}, fmt.Errorf("%w: %v", ErrWorkerCrashed, err)
			}
			return protocol.Envelope{}, ErrWorkerCrashed
		}
	}
}

func (w *processWorker) Run(ctx context.Context) (WorkerResult, error) {
	if w == nil {
		return WorkerResult{}, ErrWorkerNotRunning
	}
	request := protocol.Request(w.nextID("session"), protocol.SessionStart, map[string]string{
		"session_id":  w.spec.SessionID,
		"account_id":  w.spec.AccountID,
		"request_id":  w.spec.RequestID,
		"profile_dir": w.spec.ProfileDir,
	})
	if w.spec.Mode != "" {
		request.Payload["mode"] = w.spec.Mode
	}
	started, err := w.await(ctx, request, func(message protocol.Envelope) bool {
		return message.Type == protocol.SessionStarted ||
			message.Type == protocol.SessionFailure ||
			message.Type == protocol.SessionCancelled
	})
	if err != nil {
		return WorkerResult{}, err
	}
	if started.Type == protocol.SessionFailure {
		return WorkerResult{Failure: failureClass(started.Payload["failure"])}, nil
	}
	if started.Type == protocol.SessionCancelled {
		return WorkerResult{Failure: account.TransientFailure}, context.Canceled
	}
	for {
		select {
		case <-ctx.Done():
			return WorkerResult{}, ctx.Err()
		case message := <-w.messages:
			if message.Protocol != protocol.Version {
				return WorkerResult{}, fmt.Errorf("%w: protocol %q", ErrWorkerProtocol, message.Protocol)
			}
			if message.ID != request.ID {
				continue
			}
			switch message.Type {
			case protocol.SessionSuccess:
				return WorkerResult{Succeeded: true}, nil
			case protocol.SessionFailure:
				return WorkerResult{Failure: failureClass(message.Payload["failure"])}, nil
			case protocol.SessionCancelled:
				return WorkerResult{Failure: account.TransientFailure}, context.Canceled
			default:
				return WorkerResult{}, fmt.Errorf("%w: unexpected session event %q", ErrWorkerProtocol, message.Type)
			}
		case <-w.readDone:
			return WorkerResult{}, ErrWorkerCrashed
		case <-w.processDone:
			return WorkerResult{}, ErrWorkerCrashed
		}
	}
}

func (w *processWorker) Cancel(ctx context.Context) error {
	if w == nil {
		return ErrWorkerNotRunning
	}
	request := protocol.Request(w.nextID("cancel"), protocol.SessionCancel, map[string]string{
		"session_id": w.spec.SessionID,
	})
	return w.sendWithContext(ctx, request)
}

func (w *processWorker) sendWithContext(ctx context.Context, request protocol.Envelope) error {
	if ctx == nil {
		return ErrWorkerNotRunning
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return w.send(request)
}

func (w *processWorker) Close(ctx context.Context) error {
	if w == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	w.stateMu.Lock()
	if w.closed || w.closing {
		w.stateMu.Unlock()
		return nil
	}
	w.closing = true
	w.stateMu.Unlock()

	request := protocol.Request(w.nextID("shutdown"), protocol.Shutdown, nil)
	if err := w.send(request); err == nil {
		_, _ = w.waitFor(ctx, request, func(message protocol.Envelope) bool {
			return message.Type == protocol.ShutdownAck
		})
	}
	select {
	case <-w.processDone:
		w.stateMu.Lock()
		w.closed = true
		w.stateMu.Unlock()
		return nil
	case <-ctx.Done():
		_ = w.kill()
		return ctx.Err()
	}
}

func (w *processWorker) kill() error {
	w.stateMu.Lock()
	if w.closed {
		w.stateMu.Unlock()
		return nil
	}
	w.closed = true
	w.stateMu.Unlock()
	if w.cmd.Process == nil {
		return nil
	}
	if err := w.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

func failureClass(value string) account.FailureClass {
	switch account.FailureClass(value) {
	case account.TransientFailure, account.CredentialFailure,
		account.PermissionFailure, account.ConfigurationFailure,
		account.UnknownFailure:
		return account.FailureClass(value)
	default:
		return account.UnknownFailure
	}
}
