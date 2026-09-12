// Package automation defines the transport-neutral contract implemented by
// business automation adapters. Adapters report runtime facts; the account
// state machine remains the only owner of durable business state.
package automation

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	APIVersion = "chuzi.adapter/v1"
)

var (
	ErrInvalidContract = errors.New("automation: invalid contract")
	ErrUnsupported     = errors.New("automation: unsupported operation")
)

// FailureClass is intentionally small and safe to expose in status events.
// Adapter implementations must not put native error text or credentials in
// Failure.Code or Failure.Detail.
type FailureClass string

const (
	FailureConfiguration FailureClass = "configuration"
	FailureRuntime       FailureClass = "runtime"
	FailurePermission    FailureClass = "permission"
	FailureAuthentication FailureClass = "authentication"
	FailureBusiness      FailureClass = "business"
	FailureTransient     FailureClass = "transient"
	FailureCancelled     FailureClass = "cancelled"
)

type Failure struct {
	Class     FailureClass `json:"class"`
	Code      string       `json:"code"`
	Retryable bool         `json:"retryable"`
}

func (f Failure) Validate() error {
	switch f.Class {
	case FailureConfiguration, FailureRuntime, FailurePermission,
		FailureAuthentication, FailureBusiness, FailureTransient, FailureCancelled:
	default:
		return fmt.Errorf("%w: unknown failure class %q", ErrInvalidContract, f.Class)
	}
	if strings.TrimSpace(f.Code) == "" {
		return fmt.Errorf("%w: failure code is required", ErrInvalidContract)
	}
	return nil
}

type Capability struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

func (c Capability) Validate() error {
	if strings.TrimSpace(c.ID) == "" || strings.TrimSpace(c.Version) == "" {
		return fmt.Errorf("%w: capability id and version are required", ErrInvalidContract)
	}
	return nil
}

type Descriptor struct {
	ID           string       `json:"id"`
	Version      string       `json:"version"`
	API          string       `json:"api"`
	Capabilities []Capability `json:"capabilities"`
}

func (d Descriptor) Validate() error {
	if strings.TrimSpace(d.ID) == "" || strings.TrimSpace(d.Version) == "" || d.API != APIVersion {
		return fmt.Errorf("%w: adapter id, version, and api are required", ErrInvalidContract)
	}
	seen := make(map[string]struct{}, len(d.Capabilities))
	for _, capability := range d.Capabilities {
		if err := capability.Validate(); err != nil {
			return err
		}
		if _, ok := seen[capability.ID]; ok {
			return fmt.Errorf("%w: duplicate capability %q", ErrInvalidContract, capability.ID)
		}
		seen[capability.ID] = struct{}{}
	}
	return nil
}

// Session contains only service-derived identifiers and paths. Credentials
// are intentionally absent; an adapter receives them through a separate
// least-privilege callback owned by the caller.
type Session struct {
	SessionID  string `json:"session_id"`
	AccountID  string `json:"account_id"`
	RequestID  string `json:"request_id"`
	ProfileDir string `json:"profile_dir"`
	Runtime    string `json:"runtime,omitempty"`
	Handle     string `json:"handle,omitempty"`
}

func (s Session) Validate() error {
	if strings.TrimSpace(s.SessionID) == "" || strings.TrimSpace(s.AccountID) == "" ||
		strings.TrimSpace(s.RequestID) == "" || strings.TrimSpace(s.ProfileDir) == "" {
		return fmt.Errorf("%w: session identifiers and profile_dir are required", ErrInvalidContract)
	}
	if !filepath.IsAbs(s.ProfileDir) || filepath.Clean(s.ProfileDir) != s.ProfileDir {
		return fmt.Errorf("%w: profile_dir must be an absolute service-derived path", ErrInvalidContract)
	}
	return nil
}

type Operation struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Parameters map[string]string `json:"parameters,omitempty"`
	Deadline   time.Time         `json:"deadline,omitempty"`
}

func (o Operation) Validate() error {
	if strings.TrimSpace(o.ID) == "" || strings.TrimSpace(o.Name) == "" {
		return fmt.Errorf("%w: operation id and name are required", ErrInvalidContract)
	}
	for key := range o.Parameters {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("%w: operation parameter names cannot be empty", ErrInvalidContract)
		}
		if sensitiveField(key) {
			return fmt.Errorf("%w: operation parameter %q cannot carry credentials", ErrInvalidContract, key)
		}
	}
	return nil
}

type Result struct {
	Succeeded bool              `json:"succeeded"`
	Failure   *Failure          `json:"failure,omitempty"`
	Facts     map[string]string `json:"facts,omitempty"`
}

func (r Result) Validate() error {
	if r.Succeeded && r.Failure != nil {
		return fmt.Errorf("%w: successful result cannot contain failure", ErrInvalidContract)
	}
	if !r.Succeeded && r.Failure == nil {
		return fmt.Errorf("%w: failed result must contain failure", ErrInvalidContract)
	}
	if r.Failure != nil {
		if err := r.Failure.Validate(); err != nil {
			return err
		}
	}
	for key := range r.Facts {
		if sensitiveField(key) {
			return fmt.Errorf("%w: result fact %q cannot carry credentials", ErrInvalidContract, key)
		}
	}
	return nil
}

func sensitiveField(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.NewReplacer("-", "_", ".", "_").Replace(normalized)
	switch normalized {
	case "credential", "credentials", "password", "secret", "token", "cookie",
		"authorization", "access_token", "refresh_token", "client_secret", "private_key":
		return true
	default:
		return false
	}
}

// Adapter is the host-facing business automation contract. Execute may be
// backed by an in-process implementation or an isolated plugin process; the
// caller owns context deadlines and durable state transitions.
type Adapter interface {
	Describe(context.Context) (Descriptor, error)
	Execute(context.Context, Session, Operation) (Result, error)
	Cancel(context.Context, string) error
	Close(context.Context) error
}

// SortedCapabilities returns a deterministic copy for manifests and logs.
func (d Descriptor) SortedCapabilities() []Capability {
	capabilities := append([]Capability(nil), d.Capabilities...)
	sort.Slice(capabilities, func(i, j int) bool {
		if capabilities[i].ID == capabilities[j].ID {
			return capabilities[i].Version < capabilities[j].Version
		}
		return capabilities[i].ID < capabilities[j].ID
	})
	return capabilities
}
