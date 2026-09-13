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
	"sync"
	"time"

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
	Status Status `json:"status"`
}

type Snapshot struct {
	Status Status                 `json:"status"`
	Checks map[string]CheckResult `json:"checks"`
}

type Checker struct {
	mu     sync.RWMutex
	probes map[string]Probe
}

func NewChecker(probes map[string]Probe) (*Checker, error) {
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
	return &Checker{probes: copyProbes}, nil
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
	for _, name := range names {
		status := Healthy
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if err := probes[name](probeCtx); err != nil {
			status = Unhealthy
			result.Status = Unhealthy
		}
		cancel()
		result.Checks[name] = CheckResult{Status: status}
	}
	return result
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
