//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Semcosm/chuzi/internal/slotagent"
)

func requiredEnv(name string) (string, error) {
	value, ok := os.LookupEnv(name)
	if !ok || value == "" {
		return "", fmt.Errorf("user-agent: required service configuration unavailable")
	}
	return value, nil
}

func main() {
	slotID, err := requiredEnv("CHUZI_AGENT_SLOT_ID")
	if err != nil {
		fail(err)
	}
	pipe, err := requiredEnv("CHUZI_AGENT_PIPE")
	if err != nil {
		fail(err)
	}
	if pipe != "\\\\.\\pipe\\chuzi-slot-"+slotID {
		fail(fmt.Errorf("user-agent: invalid service configuration"))
	}
	leaseID, err := requiredEnv("CHUZI_AGENT_LEASE_ID")
	if err != nil {
		fail(err)
	}
	token, err := requiredEnv("CHUZI_AGENT_TOKEN")
	if err != nil {
		fail(err)
	}
	generationText, err := requiredEnv("CHUZI_AGENT_GENERATION")
	if err != nil {
		fail(err)
	}
	generation, err := strconv.ParseUint(generationText, 10, 64)
	if err != nil || generation == 0 {
		fail(fmt.Errorf("user-agent: invalid service configuration"))
	}
	requestID := os.Getenv("CHUZI_AGENT_REQUEST_ID")
	accountID := os.Getenv("CHUZI_AGENT_ACCOUNT_ID")
	owner, err := requiredEnv("CHUZI_AGENT_OWNER")
	if err != nil {
		fail(err)
	}
	version := os.Getenv("CHUZI_AGENT_VERSION")
	state := os.Getenv("CHUZI_AGENT_SESSION_STATE")
	runtimeRoot, err := requiredEnv("CHUZI_AGENT_RUNTIME_ROOT")
	if err != nil {
		fail(err)
	}
	profileRoot, err := requiredEnv("CHUZI_AGENT_PROFILE_ROOT")
	if err != nil {
		fail(err)
	}
	workerCommand := os.Getenv("CHUZI_AGENT_WORKER_COMMAND")
	if workerCommand == "" {
		workerCommand = "node.exe"
	}
	if !filepath.IsAbs(workerCommand) {
		workerCommand = filepath.Join(runtimeRoot, workerCommand)
	}
	workerScript := os.Getenv("CHUZI_AGENT_WORKER_SCRIPT")
	if workerScript == "" {
		workerScript = filepath.Join(runtimeRoot, "browser-worker", "src", "headless.mjs")
	} else if !filepath.IsAbs(workerScript) {
		workerScript = filepath.Join(runtimeRoot, workerScript)
	}
	adapterScript := os.Getenv("CHUZI_AGENT_ADAPTER_SCRIPT")
	if adapterScript != "" && !filepath.IsAbs(adapterScript) {
		adapterScript = filepath.Join(runtimeRoot, adapterScript)
	}
	if !strings.EqualFold(filepath.Base(workerCommand), "node.exe") {
		fail(fmt.Errorf("user-agent: unsupported worker runtime"))
	}
	workerRuntimeRoot := os.Getenv("CHUZI_AGENT_WORKER_RUNTIME_ROOT")
	if workerRuntimeRoot == "" {
		workerRuntimeRoot = filepath.Dir(workerCommand)
	}
	if !filepath.IsAbs(workerRuntimeRoot) || !runtimePathContained(workerRuntimeRoot, workerCommand) {
		fail(fmt.Errorf("user-agent: worker runtime outside service runtime"))
	}
	workerRelative, relErr := filepath.Rel(runtimeRoot, workerScript)
	if relErr != nil || (workerRelative != filepath.Join("browser-worker", "src", "worker.mjs") && workerRelative != filepath.Join("browser-worker", "src", "headless.mjs")) {
		fail(fmt.Errorf("user-agent: unsupported worker entry point"))
	}
	if adapterScript != "" {
		fail(fmt.Errorf("user-agent: unsupported adapter entry point"))
	}
	browserMode := os.Getenv("CHUZI_AGENT_BROWSER_MODE")
	browserCommand := os.Getenv("CHUZI_AGENT_BROWSER_COMMAND")
	browserArgs := []string(nil)
	switch browserMode {
	case "":
		if browserCommand != "" {
			fail(fmt.Errorf("user-agent: invalid browser configuration"))
		}
	case "headless", "headed":
		if strings.TrimSpace(browserCommand) == "" || strings.ContainsAny(browserCommand, "\x00\r\n") {
			fail(fmt.Errorf("user-agent: invalid browser configuration"))
		}
		browserArgs = []string{"--browser-command", browserCommand, "--browser-mode", browserMode}
	default:
		fail(fmt.Errorf("user-agent: invalid browser mode"))
	}
	workDir := os.Getenv("CHUZI_AGENT_WORK_DIR")
	if workDir == "" {
		workDir = runtimeRoot
	}
	if !filepath.IsAbs(profileRoot) || !filepath.IsAbs(workDir) || strings.ContainsAny(runtimeRoot+profileRoot+workDir, "\x00\r\n") {
		fail(fmt.Errorf("user-agent: invalid runtime configuration"))
	}
	launcher, err := slotagent.NewProcessLauncher(slotagent.RuntimeConfig{RuntimeRoot: runtimeRoot, WorkerRuntimeRoot: workerRuntimeRoot, WorkerCommand: workerCommand, WorkerScript: workerScript, AdapterScript: adapterScript, ProfileRoot: profileRoot, WorkDir: workDir, BrowserArgs: browserArgs})
	if err != nil {
		fail(err)
	}
	leases := &slotagent.LeaseState{}
	if err := leases.UpdateOwned(slotID, generation, leaseID, requestID, owner, accountID, token); err != nil {
		fail(err)
	}
	handler := &slotagent.RuntimeHandler{Version: version, SessionState: state, SessionStateFunc: slotagent.CurrentSessionState, Leases: leases, Launcher: launcher}
	server, err := slotagent.NewServer(slotID, generation, leases, handler)
	if err != nil {
		fail(err)
	}
	if err := slotagent.ServeNamedPipe(context.Background(), pipe, server); err != nil {
		fail(err)
	}
}

func runtimePathContained(root, value string) bool {
	if !filepath.IsAbs(root) || !filepath.IsAbs(value) {
		return false
	}
	rel, err := filepath.Rel(strings.ToLower(filepath.Clean(root)), strings.ToLower(filepath.Clean(value)))
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func fail(err error) { fmt.Fprintln(os.Stderr, "chuzi-user-agent: startup failed"); os.Exit(1) }
