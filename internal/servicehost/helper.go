package servicehost

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrHelperUnavailable = errors.New("servicehost: helper unavailable")
	ErrHelperMismatch    = errors.New("servicehost: helper identity mismatch")
	ErrHelperStopped     = errors.New("servicehost: helper stopped")
)

type HelperState string

const (
	HelperReady     HelperState = "ready"
	HelperCrashed   HelperState = "crashed"
	HelperLoggedOff HelperState = "logged_off"
)

// HelperRegistration is populated only after the OS has authenticated the
// helper process and bound it to an interactive session. SID/session text in a
// request is never accepted as proof of this registration.
type HelperRegistration struct {
	SID        string
	SessionID  uint32
	Generation uint64
	Executable string
	State      HelperState
	Heartbeat  time.Time
}

func (r HelperRegistration) Validate(runtime TrustedRuntime, now time.Time, ttl time.Duration) error {
	if runtime.Validate() != nil || !validSIDText(r.SID) || r.SessionID == 0 || r.Generation == 0 || r.State != HelperReady || r.Executable == "" || filepath.Clean(r.Executable) != r.Executable || !strings.EqualFold(filepath.Clean(r.Executable), filepath.Clean(runtime.Broker)) {
		return ErrHelperMismatch
	}
	if r.Heartbeat.IsZero() || ttl <= 0 || now.IsZero() || now.Sub(r.Heartbeat) < 0 || now.Sub(r.Heartbeat) > ttl {
		return ErrHelperUnavailable
	}
	return nil
}

func validSIDText(value string) bool {
	parts := strings.Split(strings.TrimSpace(value), "-")
	if len(parts) < 3 || parts[0] != "S" || parts[1] != "1" {
		return false
	}
	for _, part := range parts[2:] {
		if part == "" {
			return false
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return false
			}
		}
		if _, err := strconv.ParseUint(part, 10, 32); err != nil {
			return false
		}
	}
	return true
}

type HelperMonitor interface {
	Registration(context.Context) (HelperRegistration, error)
}

// HelperProcess is the narrow handle returned by a trusted helper starter.
// Implementations must authenticate the process before returning it; a PID or
// successful exec alone is not a registration. Registration is rechecked on
// every Health call so crash and logoff become unavailable immediately.
type HelperProcess interface {
	HelperMonitor
	Stop(context.Context) error
}

// HelperStarter is the management boundary used by the service host. It gets
// only fixed runtime identity, target SID, and session ID; it cannot receive
// an arbitrary executable, command line, or filesystem path.
type HelperStarter interface {
	Start(context.Context, TrustedRuntime, string, uint32) (HelperProcess, error)
}

// HelperSupervisor owns one authenticated interactive helper lifecycle. The
// supervisor is deliberately platform-neutral so Windows can supply a WTS/
// Winlogon implementation while tests use a deterministic fake.
type HelperSupervisor struct {
	runtime TrustedRuntime
	sid     string
	session uint32
	ttl     time.Duration
	now     func() time.Time
	starter HelperStarter
	mu      sync.Mutex
	startMu sync.Mutex
	process HelperProcess
	stopped bool
	last    HelperRegistration
}

func NewHelperSupervisor(runtime TrustedRuntime, sid string, session uint32, ttl time.Duration, starter HelperStarter, now func() time.Time) (*HelperSupervisor, error) {
	if runtime.Validate() != nil || !validSIDText(sid) || session == 0 || ttl <= 0 || starter == nil {
		return nil, ErrHelperMismatch
	}
	if now == nil {
		now = time.Now
	}
	return &HelperSupervisor{runtime: runtime, sid: sid, session: session, ttl: ttl, starter: starter, now: now}, nil
}

func (s *HelperSupervisor) Start(ctx context.Context) (HelperRegistration, error) {
	if s == nil || ctx == nil {
		return HelperRegistration{}, ErrHelperUnavailable
	}
	s.startMu.Lock()
	defer s.startMu.Unlock()
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return HelperRegistration{}, ErrHelperStopped
	}
	if s.process != nil {
		process := s.process
		s.mu.Unlock()
		return s.registration(ctx, process)
	}
	s.mu.Unlock()
	process, err := s.starter.Start(ctx, s.runtime, s.sid, s.session)
	if err != nil || process == nil {
		return HelperRegistration{}, ErrHelperUnavailable
	}
	registration, err := s.registration(ctx, process)
	if err != nil {
		_ = process.Stop(context.Background())
		return HelperRegistration{}, err
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		_ = process.Stop(context.Background())
		return HelperRegistration{}, ErrHelperStopped
	}
	s.process = process
	s.last = registration
	s.mu.Unlock()
	return registration, nil
}

func (s *HelperSupervisor) registration(ctx context.Context, process HelperMonitor) (HelperRegistration, error) {
	registration, err := RequireHelper(ctx, process, s.runtime, s.now().UTC(), s.ttl)
	if err != nil {
		s.invalidate()
		return HelperRegistration{}, err
	}
	if registration.SID != s.sid || registration.SessionID != s.session {
		s.invalidate()
		return HelperRegistration{}, ErrHelperMismatch
	}
	return registration, nil
}

func (s *HelperSupervisor) Health(ctx context.Context) (HelperRegistration, error) {
	if s == nil || ctx == nil {
		return HelperRegistration{}, ErrHelperUnavailable
	}
	s.mu.Lock()
	process := s.process
	stopped := s.stopped
	s.mu.Unlock()
	if stopped {
		return HelperRegistration{}, ErrHelperStopped
	}
	if process == nil {
		return HelperRegistration{}, ErrHelperUnavailable
	}
	registration, err := s.registration(ctx, process)
	if err != nil {
		return HelperRegistration{}, err
	}
	s.mu.Lock()
	s.last = registration
	s.mu.Unlock()
	return registration, nil
}

func (s *HelperSupervisor) Stop(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.stopped = true
	process := s.process
	s.process = nil
	s.mu.Unlock()
	if process == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return process.Stop(ctx)
}

func (s *HelperSupervisor) invalidate() {
	s.mu.Lock()
	process := s.process
	s.process = nil
	s.mu.Unlock()
	if process != nil {
		_ = process.Stop(context.Background())
	}
}

func RequireHelper(ctx context.Context, monitor HelperMonitor, runtime TrustedRuntime, now time.Time, ttl time.Duration) (HelperRegistration, error) {
	if ctx == nil || monitor == nil {
		return HelperRegistration{}, ErrHelperUnavailable
	}
	if err := ctx.Err(); err != nil {
		return HelperRegistration{}, ErrHelperUnavailable
	}
	result := make(chan struct {
		registration HelperRegistration
		err          error
	}, 1)
	go func() {
		registration, err := monitor.Registration(ctx)
		result <- struct {
			registration HelperRegistration
			err          error
		}{registration: registration, err: err}
	}()
	var response struct {
		registration HelperRegistration
		err          error
	}
	select {
	case <-ctx.Done():
		return HelperRegistration{}, ErrHelperUnavailable
	case response = <-result:
	}
	if err := ctx.Err(); err != nil {
		return HelperRegistration{}, ErrHelperUnavailable
	}
	if response.err != nil {
		return HelperRegistration{}, ErrHelperUnavailable
	}
	if err := response.registration.Validate(runtime, now, ttl); err != nil {
		return HelperRegistration{}, err
	}
	return response.registration, nil
}
