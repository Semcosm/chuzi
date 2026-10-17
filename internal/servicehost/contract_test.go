package servicehost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestTrustedRuntimeDerivesFixedExecutables(t *testing.T) {
	runtime, err := NewTrustedRuntime(filepath.Join(t.TempDir(), "install"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(runtime.Core) != CoreExecutableName || filepath.Base(runtime.Broker) != BrokerExecutableName {
		t.Fatalf("runtime = %#v", runtime)
	}
	if _, err := NewTrustedRuntime(t.TempDir() + string(filepath.Separator) + ".." + string(filepath.Separator) + "install"); err == nil {
		t.Fatal("unclean root accepted")
	}
	for _, bad := range []TrustedRuntime{{Root: runtime.Root, Core: "/tmp/other.exe", Broker: runtime.Broker, Agent: runtime.Agent, Node: runtime.Node, Shell: runtime.Shell}, {Root: runtime.Root, Core: runtime.Core, Broker: runtime.Broker, Agent: runtime.Agent, Node: runtime.Node, Shell: filepath.Join(runtime.Root, "..", "shell.ps1")}} {
		if err := bad.Validate(); err == nil {
			t.Fatal("untrusted runtime path accepted")
		}
	}
}

func TestRuntimeSignaturePolicyFailsClosed(t *testing.T) {
	root := t.TempDir()
	if requireAuthenticode, err := RuntimeSignaturePolicy(root); err != nil || !requireAuthenticode {
		t.Fatalf("missing build manifest policy = %v, %v; want Authenticode required", requireAuthenticode, err)
	}
	manifest := filepath.Join(root, "build-manifest.json")
	for _, test := range []struct {
		version string
		want    bool
	}{
		{version: "test-20261016-818963bb7388", want: false},
		{version: "nightly-1234-0123456789ab", want: false},
		{version: "dev", want: true},
		{version: "v1.2.3", want: true},
	} {
		t.Run(test.version, func(t *testing.T) {
			if err := os.WriteFile(manifest, []byte(`{"version":"`+test.version+`"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := RuntimeSignaturePolicy(root)
			if err != nil || got != test.want {
				t.Fatalf("RuntimeSignaturePolicy() = %v, %v; want %v", got, err, test.want)
			}
		})
	}
	for _, malformed := range []string{"", `{"version":"test-1-0123456789ab"} {}`, `{"version":42}`} {
		if err := os.WriteFile(manifest, []byte(malformed), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := RuntimeSignaturePolicy(root); err == nil {
			t.Fatalf("malformed build manifest %q was accepted", malformed)
		}
	}
}

func TestBuildACLExpectationsSeparatesRuntimeAndMutableData(t *testing.T) {
	runtime, err := NewTrustedRuntime(filepath.Join(t.TempDir(), "install"))
	if err != nil {
		t.Fatal(err)
	}
	expectations, err := BuildACLExpectations(runtime, filepath.Join(t.TempDir(), "data"))
	if err != nil || len(expectations) != 5 {
		t.Fatalf("expectations=%#v err=%v", expectations, err)
	}
	for _, expectation := range expectations {
		if contains(expectation.Modify, ACLUsers) || contains(expectation.ReadExecute, ACLUsers) {
			t.Fatalf("ordinary users can modify/read runtime ACL expectation: %#v", expectation)
		}
	}
	if _, err := BuildACLExpectations(runtime, filepath.Join(runtime.Root, "data")); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("overlapping ACL roots error=%v, want ErrInvalidConfig", err)
	}
	if err := ValidateACLExpectations([]ACLExpectation{{Name: "broad", Path: filepath.Join(t.TempDir(), "data"), Owner: ACLSystem, ReadExecute: []string{ACLEveryone}}}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("broad read ACL error=%v, want ErrInvalidConfig", err)
	}
}

func TestShutdownOrderAndForcedResult(t *testing.T) {
	var got []string
	hooks := []Hook{{Name: "drain", Fn: func(context.Context) error { got = append(got, "drain"); return nil }}, {Name: "revoke", Fn: func(context.Context) error { got = append(got, "revoke"); return errors.New("failed") }}, {Name: "worker", Fn: func(context.Context) error { got = append(got, "worker"); return nil }}}
	result := Shutdown(context.Background(), hooks)
	if !reflect.DeepEqual(got, []string{"drain", "revoke", "worker"}) || !result.Forced || len(result.Failed) != 1 {
		t.Fatalf("got=%v result=%#v", got, result)
	}
}

type fakeFacts struct{}

func (fakeFacts) Check(context.Context) ([]Fact, error) {
	facts := make([]Fact, 0, len(RequiredFactNames))
	for _, name := range RequiredFactNames {
		facts = append(facts, Fact{Name: name, State: FactPass, Reason: "supported"})
	}
	return facts, nil
}

func TestPreflightRedactsAndClassifies(t *testing.T) {
	now := time.Unix(100, 0)
	report, err := RunPreflight(context.Background(), fakeFacts{}, func() time.Time { return now })
	if err != nil || !report.Ready || report.Schema != PreflightSchema || report.Facts[0].ObservedAt != now.UTC() {
		t.Fatalf("report=%#v err=%v", report, err)
	}
}

func TestPreflightRejectsMissingRequiredFact(t *testing.T) {
	_, err := RunPreflight(context.Background(), FactSourceFunc(func(context.Context) ([]Fact, error) {
		return []Fact{{Name: "os_edition", State: FactPass, Reason: "supported"}}, nil
	}), func() time.Time { return time.Unix(100, 0) })
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("missing fact error = %v, want ErrInvalidConfig", err)
	}
}

func TestPreflightRejectsInconsistentReadyState(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	report := PreflightReport{Schema: PreflightSchema, Ready: true, ObservedAt: now}
	for _, name := range RequiredFactNames {
		report.Facts = append(report.Facts, Fact{Name: name, State: FactPass, Reason: "supported", ObservedAt: now})
	}
	report.Facts[0].State = FactFail
	if err := report.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("inconsistent ready state error = %v, want ErrInvalidConfig", err)
	}
}

func TestHelperRegistrationFailsClosedOnMismatchOrStaleness(t *testing.T) {
	runtime, err := NewTrustedRuntime(filepath.Join(t.TempDir(), "install"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	base := HelperRegistration{SID: "S-1-5-21-1", SessionID: 7, Generation: 2, Executable: runtime.Broker, State: HelperReady, Heartbeat: now}
	if err := base.Validate(runtime, now, time.Minute); err != nil {
		t.Fatalf("valid registration rejected: %v", err)
	}
	for _, registration := range []HelperRegistration{
		base,
		func() HelperRegistration {
			value := base
			value.Executable = filepath.Join(runtime.Root, "other.exe")
			return value
		}(),
		func() HelperRegistration { value := base; value.State = HelperCrashed; return value }(),
		func() HelperRegistration { value := base; value.Heartbeat = now.Add(-time.Hour); return value }(),
		func() HelperRegistration { value := base; value.SID = "operator"; return value }(),
	} {
		if registration == base {
			continue
		}
		if err := registration.Validate(runtime, now, time.Minute); err == nil {
			t.Fatalf("invalid registration accepted: %#v", registration)
		}
	}
}

type blockingHelperMonitor struct{}

func (blockingHelperMonitor) Registration(ctx context.Context) (HelperRegistration, error) {
	<-ctx.Done()
	return HelperRegistration{}, ctx.Err()
}

func TestRequireHelperHonorsCallerDeadline(t *testing.T) {
	runtime, err := NewTrustedRuntime(filepath.Join(t.TempDir(), "install"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := RequireHelper(ctx, blockingHelperMonitor{}, runtime, time.Unix(100, 0), time.Minute); !errors.Is(err, ErrHelperUnavailable) {
		t.Fatalf("RequireHelper() error = %v, want ErrHelperUnavailable", err)
	}
}

type fakeHelperProcess struct {
	registration HelperRegistration
	stopped      bool
}

func (p *fakeHelperProcess) Registration(context.Context) (HelperRegistration, error) {
	if p.stopped {
		return HelperRegistration{}, ErrHelperUnavailable
	}
	return p.registration, nil
}
func (p *fakeHelperProcess) Stop(context.Context) error { p.stopped = true; return nil }

type fakeHelperStarter struct{ process *fakeHelperProcess }

func (s fakeHelperStarter) Start(context.Context, TrustedRuntime, string, uint32) (HelperProcess, error) {
	return s.process, nil
}

func TestHelperSupervisorInvalidatesAfterHeartbeatFailure(t *testing.T) {
	runtime, err := NewTrustedRuntime(filepath.Join(t.TempDir(), "install"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0).UTC()
	process := &fakeHelperProcess{registration: HelperRegistration{SID: "S-1-5-21-1", SessionID: 7, Generation: 1, Executable: runtime.Broker, State: HelperReady, Heartbeat: now}}
	supervisor, err := NewHelperSupervisor(runtime, "S-1-5-21-1", 7, time.Minute, fakeHelperStarter{process: process}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	process.registration.Heartbeat = now.Add(-time.Hour)
	if _, err := supervisor.Health(context.Background()); !errors.Is(err, ErrHelperUnavailable) {
		t.Fatalf("Health() error = %v, want ErrHelperUnavailable", err)
	}
	if !process.stopped {
		t.Fatal("stale helper was not stopped")
	}
}
