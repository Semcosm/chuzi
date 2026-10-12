// Package servicehost owns the controlled service boundary. It contains the
// fixed installation identity and lifecycle contracts shared by the launcher
// and the platform adapters; business requests remain in Core.
package servicehost

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	ServiceName          = "ChuziCore"
	ServiceDisplayName   = "Chuzi Core Service"
	ServiceSIDName       = "NT SERVICE\\ChuziCore"
	ServiceRunAs         = "LocalSystem"
	ServiceModeFlag      = "-service"
	CoreExecutableName   = "chuzi.exe"
	BrokerExecutableName = "chuzi-session-broker.exe"
	AgentExecutableName  = "chuzi-user-agent.exe"
)

var (
	ErrInvalidConfig       = errors.New("servicehost: invalid configuration")
	ErrHostUnavailable     = errors.New("servicehost: host unavailable")
	ErrForcedShutdown      = errors.New("servicehost: forced shutdown")
	ErrNotInstalled        = errors.New("servicehost: service is not installed")
	ErrIncompatibleService = errors.New("servicehost: existing service is incompatible")
)

// Identity is the only supported production service identity. UI processes
// never receive this identity or its privileges.
type Identity struct {
	Name       string
	SIDName    string
	RunAs      string
	Privileges []string
}

func ProductionIdentity() Identity {
	return Identity{Name: ServiceName, SIDName: ServiceSIDName, RunAs: ServiceRunAs, Privileges: []string{"SeTcbPrivilege", "SeAssignPrimaryTokenPrivilege", "SeIncreaseQuotaPrivilege"}}
}

func (i Identity) Validate() error {
	if i.Name != ServiceName || i.SIDName != ServiceSIDName || i.RunAs != ServiceRunAs || len(i.Privileges) == 0 {
		return ErrInvalidConfig
	}
	return nil
}

// TrustedRuntime is derived from the installed root. Callers cannot supply
// executable names, helper paths, pipe names, or endpoints.
type TrustedRuntime struct {
	Root   string
	Core   string
	Broker string
	Agent  string
	Node   string
	Shell  string
}

func NewTrustedRuntime(root string) (TrustedRuntime, error) {
	rawRoot := strings.TrimSpace(root)
	root = filepath.Clean(rawRoot)
	if rawRoot != root {
		return TrustedRuntime{}, fmt.Errorf("%w: runtime root must be clean", ErrInvalidConfig)
	}
	if root == "" || !filepath.IsAbs(root) || root == string(filepath.VolumeName(root))+string(filepath.Separator) {
		return TrustedRuntime{}, fmt.Errorf("%w: runtime root must be an absolute installation directory", ErrInvalidConfig)
	}
	r := TrustedRuntime{Root: root, Core: filepath.Join(root, CoreExecutableName), Broker: filepath.Join(root, BrokerExecutableName), Agent: filepath.Join(root, AgentExecutableName), Node: filepath.Join(root, "node.exe"), Shell: filepath.Join(root, "session-shell.ps1")}
	return r, r.Validate()
}

func (r TrustedRuntime) Validate() error {
	if r.Root == "" || !filepath.IsAbs(r.Root) || filepath.Clean(r.Root) != r.Root {
		return ErrInvalidConfig
	}
	for _, path := range []string{r.Core, r.Broker, r.Agent, r.Node, r.Shell} {
		if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || !within(r.Root, path) {
			return ErrInvalidConfig
		}
	}
	return nil
}

func within(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "."
}

type Health struct {
	HostRunning           bool      `json:"host_running"`
	CoreHandshake         bool      `json:"core_handshake"`
	LoginAdapterAvailable bool      `json:"login_adapter_available"`
	HelperAvailable       bool      `json:"helper_available"`
	RuntimeReady          bool      `json:"runtime_ready"`
	Forced                bool      `json:"forced"`
	ObservedAt            time.Time `json:"observed_at"`
}

func (h Health) Ready() bool {
	return h.HostRunning && h.CoreHandshake && h.LoginAdapterAvailable && h.HelperAvailable && h.RuntimeReady && !h.Forced
}

// Hook is an ordered shutdown action. Each hook must own only the resource
// named by Name; it must not discover or kill unrelated processes.
type Hook struct {
	Name string
	Fn   func(context.Context) error
}

type ShutdownResult struct {
	Completed []string `json:"completed"`
	Failed    []string `json:"failed"`
	Forced    bool     `json:"forced"`
}

// Shutdown runs the fixed drain/revoke/worker/session/flush sequence. A
// deadline or hook failure yields an explicit forced result for recovery.
func Shutdown(ctx context.Context, hooks []Hook) ShutdownResult {
	result := ShutdownResult{}
	if ctx == nil {
		ctx = context.Background()
	}
	for _, hook := range hooks {
		if strings.TrimSpace(hook.Name) == "" || hook.Fn == nil {
			result.Forced = true
			if hook.Name != "" {
				result.Failed = append(result.Failed, hook.Name)
			}
			continue
		}
		if err := ctx.Err(); err != nil {
			result.Forced = true
			result.Failed = append(result.Failed, hook.Name)
			continue
		}
		if err := hook.Fn(ctx); err != nil {
			result.Forced = true
			result.Failed = append(result.Failed, hook.Name)
			continue
		}
		result.Completed = append(result.Completed, hook.Name)
	}
	return result
}

// OnceShutdown wraps an ordered shutdown function so SCM duplicate stop and
// console cancellation are idempotent.
type OnceShutdown struct {
	once   sync.Once
	result ShutdownResult
}

func (s *OnceShutdown) Run(ctx context.Context, hooks []Hook) ShutdownResult {
	if s == nil {
		return Shutdown(ctx, hooks)
	}
	s.once.Do(func() { s.result = Shutdown(ctx, hooks) })
	return s.result
}
