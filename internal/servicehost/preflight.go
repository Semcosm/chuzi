package servicehost

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type FactState string

const (
	FactPass FactState = "pass"
	FactFail FactState = "fail"
	FactSkip FactState = "unavailable"
)

// Fact is stable and redacted. Reason and Action are bounded classification
// strings; native error text and paths never leave the host adapter.
type Fact struct {
	Name       string    `json:"name"`
	State      FactState `json:"state"`
	Reason     string    `json:"reason"`
	Action     string    `json:"action,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
}

type PreflightReport struct {
	Schema     string    `json:"schema"`
	Ready      bool      `json:"ready"`
	Facts      []Fact    `json:"facts"`
	ObservedAt time.Time `json:"observed_at"`
}

const PreflightSchema = "chuzi.host-preflight/v1"

var ErrPreflight = errors.New("servicehost: host preflight failed")

// PreflightFailure retains the redacted facts from a completed preflight and
// classifies whether another automatic attempt can reasonably change the
// result. Deterministic failed facts require operator action; unavailable
// facts may be retried after the underlying host service recovers.
type PreflightFailure struct {
	report PreflightReport
}

func (e *PreflightFailure) Error() string { return ErrPreflight.Error() }

func (e *PreflightFailure) Unwrap() error { return ErrPreflight }

func clonePreflightReport(report PreflightReport) PreflightReport {
	report.Facts = append([]Fact(nil), report.Facts...)
	return report
}

func (e *PreflightFailure) Report() PreflightReport {
	if e == nil {
		return PreflightReport{}
	}
	return clonePreflightReport(e.report)
}

func (e *PreflightFailure) Retryable() bool {
	if e == nil || e.report.Validate() != nil {
		return false
	}
	unavailable := false
	for _, fact := range e.report.Facts {
		switch fact.State {
		case FactFail:
			return false
		case FactSkip:
			unavailable = true
		}
	}
	return unavailable
}

var validationBuildVersion = regexp.MustCompile(`^(?:test|nightly)-[1-9][0-9]*-[0-9a-f]{12}$`)

const maxBuildManifestSize = 16 * 1024

func (r PreflightReport) Validate() error {
	if r.Schema != PreflightSchema || r.ObservedAt.IsZero() || len(r.Facts) == 0 {
		return ErrInvalidConfig
	}
	seen := make(map[string]struct{}, len(r.Facts))
	ready := true
	for _, fact := range r.Facts {
		if (fact.State != FactPass && fact.State != FactFail && fact.State != FactSkip) || strings.TrimSpace(fact.Name) == "" || strings.TrimSpace(fact.Reason) == "" || fact.ObservedAt.IsZero() || len(fact.Name) > 64 || len(fact.Reason) > 128 || len(fact.Action) > 128 || strings.ContainsAny(fact.Name+fact.Reason+fact.Action, "\r\n") {
			return ErrInvalidConfig
		}
		if _, exists := seen[fact.Name]; exists {
			return ErrInvalidConfig
		}
		seen[fact.Name] = struct{}{}
		if fact.State != FactPass {
			ready = false
		}
	}
	for _, name := range RequiredFactNames {
		if _, exists := seen[name]; !exists {
			return ErrInvalidConfig
		}
	}
	if r.Ready != ready {
		return ErrInvalidConfig
	}
	return nil
}

type FactSource interface {
	Check(context.Context) ([]Fact, error)
}

type FactSourceFunc func(context.Context) ([]Fact, error)

func (f FactSourceFunc) Check(ctx context.Context) ([]Fact, error) {
	return f(ctx)
}

func RunPreflight(ctx context.Context, source FactSource, now func() time.Time) (PreflightReport, error) {
	if ctx == nil || source == nil {
		return PreflightReport{}, ErrInvalidConfig
	}
	if now == nil {
		now = time.Now
	}
	facts, err := source.Check(ctx)
	if err != nil {
		return PreflightReport{}, ErrPreflight
	}
	observed := now().UTC()
	ready := len(facts) > 0
	for i := range facts {
		facts[i].ObservedAt = observed
		if facts[i].State != FactPass {
			ready = false
		}
	}
	report := PreflightReport{Schema: PreflightSchema, Ready: ready, Facts: facts, ObservedAt: observed}
	if err := report.Validate(); err != nil {
		return PreflightReport{}, err
	}
	if !ready {
		return report, &PreflightFailure{report: clonePreflightReport(report)}
	}
	return report, nil
}

// RequiredFactNames is the stable host evidence set used by installer/UI.
var RequiredFactNames = []string{
	"os_edition", "session_capacity", "privilege", "required_services",
	"rdp_listener", "nla_policy", "certificate_policy", "logon_rights",
	"disk_permissions", "runtime_signature", "helper_availability",
}

// WindowsPreflightOptions supplies only fixed installation facts and an
// optional authenticated helper monitor. It deliberately has no executable,
// shell, endpoint, or credential fields.
type WindowsPreflightOptions struct {
	Runtime          TrustedRuntime
	DataRoot         string
	ACL              ACLExpectationValidator
	RequiredServices []string
	MaxSessions      int
	LogonSID         string
	Helper           HelperMonitor
	// RequireAuthenticode is true for stable (and development) payloads. The
	// installer explicitly classifies test/nightly payloads as validation
	// builds, whose Go/Node executables are intentionally unsigned.
	RequireAuthenticode bool
}

// RuntimeSignaturePolicy reads the immutable build classification selected by
// the installer. Missing metadata fails closed to the production signature
// requirement; only explicit test-/nightly- versions use the unsigned
// validation path.
func RuntimeSignaturePolicy(root string) (bool, error) {
	rawRoot := strings.TrimSpace(root)
	cleanRoot := filepath.Clean(rawRoot)
	if rawRoot == "" || rawRoot != cleanRoot || !filepath.IsAbs(cleanRoot) {
		return true, ErrInvalidConfig
	}
	manifestPath := filepath.Join(cleanRoot, "build-manifest.json")
	info, err := os.Lstat(manifestPath)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxBuildManifestSize {
		return true, ErrInvalidConfig
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return true, ErrInvalidConfig
	}
	var manifest struct {
		Version string `json:"version"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if err := decoder.Decode(&manifest); err != nil {
		return true, ErrInvalidConfig
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return true, ErrInvalidConfig
	}
	version := strings.TrimSpace(manifest.Version)
	if version == "" {
		return true, ErrInvalidConfig
	}
	if validationBuildVersion.MatchString(version) {
		return false, nil
	}
	return true, nil
}

// RunWindowsPreflight executes the platform source and returns the stable,
// redacted report consumed by the installer or UI. It never returns native
// error text or filesystem paths in the report.
func RunWindowsPreflight(ctx context.Context, options WindowsPreflightOptions, now func() time.Time) (PreflightReport, error) {
	source, err := NewWindowsPreflightSource(options)
	if err != nil {
		return PreflightReport{}, err
	}
	return RunPreflight(ctx, source, now)
}
