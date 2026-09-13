// Package health provides redaction-safe liveness/readiness checks for the
// assembled service. Probes expose status only; implementation errors never
// leave the process through the health response.
package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Semcosm/chuzi/internal/observability"
	"github.com/Semcosm/chuzi/internal/store"
)

type Status string

const (
	Healthy   Status = "healthy"
	Unhealthy Status = "unhealthy"
)

var ErrInvalidChecker = errors.New("health: invalid checker")

type Probe func(context.Context) error

type CheckResult struct {
	Status     Status `json:"status"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

type Snapshot struct {
	Status Status                 `json:"status"`
	Checks map[string]CheckResult `json:"checks"`
}

type Checker struct {
	mu      sync.RWMutex
	probes  map[string]Probe
	timeout time.Duration
	sink    observability.Sink
	clock   func() time.Time
}

type probeResult struct {
	name     string
	status   Status
	duration time.Duration
	err      error
}

// Config controls probe execution. ProbeTimeout is a safety bound for probes
// that fail to honor cancellation; set it to zero to use only the caller's
// context. Sink receives classified probe outcomes and never receives errors.
type Config struct {
	ProbeTimeout time.Duration
	Sink         observability.Sink
	Clock        func() time.Time
}

func NewChecker(probes map[string]Probe) (*Checker, error) {
	return NewCheckerWithConfig(probes, Config{ProbeTimeout: 5 * time.Second})
}

// NewCheckerWithConfig constructs a checker with an explicit cancellation
// policy. Probes are always started concurrently, so one slow dependency does
// not delay independent readiness results. Probe implementations must honor
// context cancellation; a non-cooperative probe may outlive this call, but its
// result cannot block the checker after cancellation.
func NewCheckerWithConfig(probes map[string]Probe, config Config) (*Checker, error) {
	if config.ProbeTimeout < 0 {
		return nil, ErrInvalidChecker
	}
	if len(probes) == 0 {
		return nil, ErrInvalidChecker
	}
	copyProbes := make(map[string]Probe, len(probes))
	for name, probe := range probes {
		if name == "" || probe == nil {
			return nil, ErrInvalidChecker
		}
		copyProbes[name] = probe
	}
	if config.Sink == nil {
		config.Sink = observability.NopSink{}
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	return &Checker{probes: copyProbes, timeout: config.ProbeTimeout, sink: config.Sink, clock: config.Clock}, nil
}

func (c *Checker) Check(ctx context.Context) Snapshot {
	result := Snapshot{Status: Healthy, Checks: map[string]CheckResult{}}
	if c == nil || ctx == nil {
		result.Status = Unhealthy
		return result
	}
	c.mu.RLock()
	names := make([]string, 0, len(c.probes))
	probes := make(map[string]Probe, len(c.probes))
	for name, probe := range c.probes {
		names = append(names, name)
		probes[name] = probe
	}
	c.mu.RUnlock()
	sort.Strings(names)
	results := make(chan probeResult, len(names))
	finished := make(chan struct{})
	defer close(finished)
	cancels := make(map[string]context.CancelFunc, len(names))
	starts := make(map[string]time.Time, len(names))
	for _, name := range names {
		name, probe := name, probes[name]
		probeCtx := ctx
		var cancel context.CancelFunc
		if c.timeout > 0 {
			probeCtx, cancel = context.WithTimeout(ctx, c.timeout)
		} else {
			probeCtx, cancel = context.WithCancel(ctx)
		}
		cancels[name] = cancel
		started := time.Now()
		starts[name] = started
		go func() {
			err := probe(probeCtx)
			cancel()
			status := Healthy
			if err != nil {
				status = Unhealthy
			}
			result := probeResult{name: name, status: status, duration: time.Since(started), err: err}
			select {
			case results <- result:
			case <-finished:
			}
		}()
	}
	pending := make(map[string]struct{}, len(names))
	for _, name := range names {
		pending[name] = struct{}{}
	}
	var timeout <-chan time.Time
	var timer *time.Timer
	if c.timeout > 0 {
		timer = time.NewTimer(c.timeout)
		defer timer.Stop()
		timeout = timer.C
	}
	for len(pending) > 0 {
		if err := ctx.Err(); err != nil {
			c.finishCancelled(&result, pending, cancels, starts, err)
			return result
		}
		select {
		case probe := <-results:
			if _, ok := pending[probe.name]; !ok {
				continue
			}
			delete(pending, probe.name)
			c.recordResult(&result, probe)
		case <-ctx.Done():
			c.finishCancelled(&result, pending, cancels, starts, ctx.Err())
			return result
		case <-timeout:
			c.finishCancelled(&result, pending, cancels, starts, context.DeadlineExceeded)
			return result
		}
	}
	return result
}

func (c *Checker) recordResult(snapshot *Snapshot, probe probeResult) {
	if probe.status != Healthy {
		snapshot.Status = Unhealthy
	}
	snapshot.Checks[probe.name] = CheckResult{Status: probe.status, DurationMS: durationMillis(probe.duration)}
	event := observability.Event{
		At:        c.clock().UTC(),
		Component: "health",
		Operation: "probe",
		Outcome:   strings.ToLower(string(probe.status)),
		Resource:  probe.name,
		Duration:  probe.duration,
	}
	if probe.err != nil {
		event.ErrorClass = classifyProbeError(probe.err)
	}
	c.sink.Record(event)
}

func (c *Checker) finishCancelled(snapshot *Snapshot, pending map[string]struct{}, cancels map[string]context.CancelFunc, starts map[string]time.Time, err error) {
	if err == nil {
		err = context.Canceled
	}
	snapshot.Status = Unhealthy
	names := make([]string, 0, len(pending))
	for name := range pending {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if cancel := cancels[name]; cancel != nil {
			cancel()
		}
		probe := probeResult{name: name, status: Unhealthy, duration: time.Since(starts[name]), err: err}
		c.recordResult(snapshot, probe)
	}
}

func durationMillis(duration time.Duration) int64 {
	if duration <= 0 {
		return 0
	}
	value := duration.Milliseconds()
	if value == 0 {
		return 1
	}
	return value
}

func classifyProbeError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	return "probe_failed"
}

// Handler exposes a JSON readiness endpoint. It deliberately omits probe
// errors and internal details, returning 503 when any dependency is unhealthy.
func (c *Checker) Handler() http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		snapshot := c.Check(request.Context())
		statusCode := http.StatusOK
		if snapshot.Status != Healthy {
			statusCode = http.StatusServiceUnavailable
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(statusCode)
		_ = json.NewEncoder(writer).Encode(snapshot)
	})
}

func StoreProbe(database *store.Store) Probe {
	return func(context.Context) error {
		if database == nil {
			return ErrInvalidChecker
		}
		_, err := database.SchemaVersion()
		return err
	}
}

func StaticProbe(err error) Probe {
	return func(context.Context) error { return err }
}
