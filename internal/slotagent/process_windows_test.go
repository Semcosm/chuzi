//go:build windows

package slotagent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Semcosm/chuzi/internal/environment"
	"github.com/Semcosm/chuzi/internal/protocol"
	"golang.org/x/sys/windows"
)

type testWriteCloser struct{ bytes.Buffer }

func (testWriteCloser) Close() error { return nil }

func TestProcessJobReportsReaderFailure(t *testing.T) {
	job := &processJob{
		stdin:    &testWriteCloser{},
		done:     make(chan struct{}),
		readDone: make(chan struct{}),
		messages: make(chan protocol.Envelope, 1),
	}
	job.readLoop(strings.NewReader("{malformed\n"))
	_, err := job.RoundTrip(context.Background(), protocol.Request("request-1", protocol.Ping, nil))
	if !errors.Is(err, ErrRuntimeStart) {
		t.Fatalf("RoundTrip error = %v, want ErrRuntimeStart", err)
	}
}

func TestProcessJobTreatsUnexpectedEOFAsRuntimeFailure(t *testing.T) {
	job := &processJob{
		stdin:    &testWriteCloser{},
		done:     make(chan struct{}),
		readDone: make(chan struct{}),
		messages: make(chan protocol.Envelope, 1),
	}
	job.readLoop(io.LimitReader(strings.NewReader(""), 1))
	_, err := job.RoundTrip(context.Background(), protocol.Request("request-1", protocol.Ping, nil))
	if !errors.Is(err, ErrRuntimeStart) {
		t.Fatalf("RoundTrip error = %v, want ErrRuntimeStart", err)
	}
}

func TestProcessPipesExposeSixDistinctHandles(t *testing.T) {
	stdinRead, stdinWrite, stdoutRead, stdoutWrite, stderrRead, stderrWrite, err := processPipes()
	if err != nil {
		t.Fatal(err)
	}
	defer closeProcessPipes(stdinRead, stdinWrite, stdoutRead, stdoutWrite, stderrRead, stderrWrite)
	seen := map[windows.Handle]bool{}
	for _, handle := range []windows.Handle{stdinRead, stdinWrite, stdoutRead, stdoutWrite, stderrRead, stderrWrite} {
		if handle == 0 || seen[handle] {
			t.Fatalf("invalid or reused pipe handle: %v", handle)
		}
		seen[handle] = true
	}
}

func TestControlledRuntimeEnvDoesNotPassAgentSecrets(t *testing.T) {
	t.Setenv("PATH", "C:\\\\Windows\\\\System32")
	t.Setenv("CHUZI_AGENT_TOKEN", "secret")
	t.Setenv("CHUZI_AGENT_LEASE_ID", "lease")
	values := controlledRuntimeEnv("C:\\\\ProgramData\\\\chuzi\\\\profiles\\\\account")
	joined := strings.Join(values, "\\x00")
	if strings.Contains(joined, "CHUZI_AGENT_TOKEN") || strings.Contains(joined, "CHUZI_AGENT_LEASE_ID") || !strings.Contains(joined, "CHUZI_SESSION_PROFILE_DIR=") {
		t.Fatalf("filtered environment = %q", joined)
	}
}

func TestEncodeEnvironmentBuildsNulTerminatedBlock(t *testing.T) {
	if _, err := encodeEnvironment([]string{"B=two", "A=one"}); err != nil {
		t.Fatalf("environment block encoding failed: %v", err)
	}
	if _, err := encodeEnvironment([]string{"A=one\x00invalid"}); err == nil {
		t.Fatal("embedded NUL environment entry was accepted")
	}
}

func TestWorkerCommandLineStartsWithNodeExecutable(t *testing.T) {
	command := windows.ComposeCommandLine(workerCommandArgs(`C:\runtime\node.exe`, `C:\runtime\worker.mjs`, nil))
	args, err := windows.DecomposeCommandLine(command)
	if err != nil {
		t.Fatalf("worker command line decode failed: %v", err)
	}
	if len(args) != 3 || args[0] != `C:\runtime\node.exe` || args[1] != `C:\runtime\worker.mjs` || args[2] != "--stdio" {
		t.Fatalf("worker command line args = %#v", args)
	}
}

func TestWorkerStderrSummaryRedactsPathsAndNormalizesText(t *testing.T) {
	data := []byte("Error: cannot find module 'C:\\Users\\smoke user\\worker.mjs'\r\n\x00at\tC:\\runtime\\loader.js:1:2")
	summary := workerStderrSummary(data)
	if summary != "Error: cannot find module '<path>' at <path>" {
		t.Fatalf("worker stderr summary = %q", summary)
	}
	if strings.Contains(summary, `C:\`) || strings.Contains(summary, "\n") || strings.Contains(summary, "\r") {
		t.Fatalf("worker stderr summary leaked path or newline: %q", summary)
	}
}

func TestWorkerStderrClassifiesNodePermissionFailure(t *testing.T) {
	if got := workerStderrClass([]byte("Error: EPERM: operation not permitted, lstat 'C:\\runtime'")); got != "permission" {
		t.Fatalf("worker stderr class = %q, want permission", got)
	}
}

func TestNewProcessLauncherRejectsCallerSelectedRelativeRuntime(t *testing.T) {
	if _, err := NewProcessLauncher(RuntimeConfig{WorkerCommand: "node.exe", WorkerScript: "worker.mjs", ProfileRoot: "C:\\\\profiles", WorkDir: "C:\\\\work"}); err != ErrRuntimeConfig {
		t.Fatalf("relative runtime accepted: %v", err)
	}
}

func TestNewProcessLauncherRequiresNodeRuntime(t *testing.T) {
	config := RuntimeConfig{WorkerCommand: "C:\\\\runtime\\\\powershell.exe", WorkerScript: "C:\\\\runtime\\\\worker.mjs", ProfileRoot: "C:\\\\profiles", WorkDir: "C:\\\\work"}
	if _, err := NewProcessLauncher(config); err != ErrRuntimeConfig {
		t.Fatalf("non-Node runtime accepted: %v", err)
	}
}

func TestNewProcessLauncherRejectsRuntimeOutsidePackageRoot(t *testing.T) {
	config := RuntimeConfig{RuntimeRoot: `C:\runtime`, WorkerCommand: `C:\other\node.exe`, WorkerScript: `C:\runtime\browser-worker\src\worker.mjs`, ProfileRoot: `C:\profiles`, WorkDir: `C:\work`}
	if _, err := NewProcessLauncher(config); err != ErrRuntimeConfig {
		t.Fatalf("runtime outside package accepted: %v", err)
	}
}

func TestNewProcessLauncherRequiresSignedAdapterBridgeHandoff(t *testing.T) {
	base := RuntimeConfig{RuntimeRoot: `C:\environment`, PackageRoot: `C:\environment`, EnvironmentID: "env", EnvironmentVersion: "1.0.0", ManifestDigest: strings.Repeat("a", 64), Signer: "signer", PackageGeneration: 4, WorkerEntrypoint: environment.HeadlessEntrypointName, AdapterEntrypoint: environment.AdapterBridgeEntrypointName, WorkerRuntimeRoot: `C:\service`, WorkerCommand: `C:\service\node.exe`, WorkerScript: `C:\environment\headless.mjs`, AdapterScript: `C:\environment\adapter.mjs`, WindowsDesktop: `winsta0\ChuziSlot0123456789abcdef`, ProfileRoot: `C:\profiles`, WorkDir: `C:\work`}
	if _, err := NewProcessLauncher(base); err != nil {
		t.Fatalf("valid controlled runtime rejected: %v", err)
	}
	for name, mutate := range map[string]func(*RuntimeConfig){
		"adapter path outside package": func(c *RuntimeConfig) { c.AdapterScript = `C:\other\adapter.mjs` },
		"arbitrary worker entrypoint":  func(c *RuntimeConfig) { c.WorkerEntrypoint = `C:\other\run.cmd` },
		"missing adapter bridge":       func(c *RuntimeConfig) { c.AdapterEntrypoint = "" },
	} {
		t.Run(name, func(t *testing.T) {
			config := base
			mutate(&config)
			if _, err := NewProcessLauncher(config); !errors.Is(err, ErrRuntimeConfig) {
				t.Fatalf("illegal runtime accepted: %v", err)
			}
		})
	}
}
