//go:build windows

package slotagent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

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

func TestProcessPipesExposeFourDistinctHandles(t *testing.T) {
	stdinRead, stdinWrite, stdoutRead, stdoutWrite, err := processPipes()
	if err != nil {
		t.Fatal(err)
	}
	defer closeProcessPipes(stdinRead, stdinWrite, stdoutRead, stdoutWrite)
	seen := map[windows.Handle]bool{}
	for _, handle := range []windows.Handle{stdinRead, stdinWrite, stdoutRead, stdoutWrite} {
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
