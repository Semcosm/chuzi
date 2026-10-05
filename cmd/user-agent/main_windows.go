//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Semcosm/chuzi/internal/environment"
	"github.com/Semcosm/chuzi/internal/slotagent"
)

var startupStage = "configuration"

func requiredEnv(name string) (string, error) {
	value, ok := os.LookupEnv(name)
	if !ok || value == "" {
		return "", fmt.Errorf("user-agent: required service configuration unavailable")
	}
	return value, nil
}

func main() {
	if os.Getenv("CHUZI_AGENT_DESKTOP_BOOTSTRAP") == "1" {
		setStartupStage("desktop_bootstrap")
		desktop, err := requiredEnv("CHUZI_AGENT_DESKTOP")
		if err != nil {
			fail(err)
		}
		serviceSID, err := requiredEnv("CHUZI_AGENT_DESKTOP_SERVICE_SID")
		if err != nil {
			fail(err)
		}
		if err := slotagent.PrepareDesktop(desktop, serviceSID); err != nil {
			fail(err)
		}
		return
	}
	setStartupStage("configuration")
	desktop, err := requiredEnv("CHUZI_AGENT_DESKTOP")
	if err != nil {
		fail(err)
	}
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
	packageRoot, err := requiredEnv("CHUZI_AGENT_PACKAGE_ROOT")
	if err != nil {
		fail(err)
	}
	environmentID, err := requiredEnv("CHUZI_AGENT_ENVIRONMENT_ID")
	if err != nil {
		fail(err)
	}
	environmentVersion, err := requiredEnv("CHUZI_AGENT_ENVIRONMENT_VERSION")
	if err != nil {
		fail(err)
	}
	manifestDigest, err := requiredEnv("CHUZI_AGENT_MANIFEST_DIGEST")
	if err != nil {
		fail(err)
	}
	signer, err := requiredEnv("CHUZI_AGENT_SIGNER")
	if err != nil {
		fail(err)
	}
	packageGenerationText, err := requiredEnv("CHUZI_AGENT_PACKAGE_GENERATION")
	if err != nil {
		fail(err)
	}
	packageGeneration, err := strconv.ParseUint(packageGenerationText, 10, 64)
	if err != nil || packageGeneration == 0 {
		fail(fmt.Errorf("user-agent: invalid package generation"))
	}
	runtimeRoot := packageRoot
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
	workerEntrypoint := os.Getenv("CHUZI_AGENT_WORKER_ENTRYPOINT")
	adapterEntrypoint := os.Getenv("CHUZI_AGENT_ADAPTER_ENTRYPOINT")
	if workerEntrypoint != environment.WorkerEntrypointName && workerEntrypoint != environment.HeadlessEntrypointName || adapterEntrypoint != environment.AdapterBridgeEntrypointName {
		fail(fmt.Errorf("user-agent: unsupported worker entry point"))
	}
	if !filepath.IsAbs(packageRoot) || !runtimePathContained(packageRoot, workerScript) || !runtimePathContained(packageRoot, adapterScript) {
		fail(fmt.Errorf("user-agent: runtime entry point outside package"))
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
	setStartupStage("runtime_launcher")
	launcher, err := slotagent.NewProcessLauncher(slotagent.RuntimeConfig{RuntimeRoot: runtimeRoot, PackageRoot: packageRoot, EnvironmentID: environmentID, EnvironmentVersion: environmentVersion, ManifestDigest: manifestDigest, Signer: signer, PackageGeneration: packageGeneration, WorkerEntrypoint: workerEntrypoint, AdapterEntrypoint: adapterEntrypoint, WorkerRuntimeRoot: workerRuntimeRoot, WorkerCommand: workerCommand, WorkerScript: workerScript, AdapterScript: adapterScript, WindowsDesktop: desktop, ProfileRoot: profileRoot, WorkDir: workDir, BrowserArgs: browserArgs})
	if err != nil {
		fail(err)
	}
	leases := &slotagent.LeaseState{}
	setStartupStage("lease_state")
	if err := leases.UpdateOwned(slotID, generation, leaseID, requestID, owner, accountID, token); err != nil {
		fail(err)
	}
	setStartupStage("protocol_server")
	handler := &slotagent.RuntimeHandler{Version: version, SessionState: state, SessionStateFunc: slotagent.CurrentSessionState, Leases: leases, Launcher: launcher}
	server, err := slotagent.NewServer(slotID, generation, leases, handler)
	if err != nil {
		fail(err)
	}
	setStartupStage("named_pipe")
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

func fail(err error) {
	startupDiagnostic(startupStage)
	fmt.Fprintln(os.Stderr, "chuzi-user-agent: startup failed")
	os.Exit(1)
}

func setStartupStage(stage string) {
	startupStage = stage
	startupDiagnostic(stage)
}

func startupDiagnostic(stage string) {
	path := strings.TrimSpace(os.Getenv("CHUZI_AGENT_STARTUP_DIAGNOSTICS"))
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = fmt.Fprintf(file, "DIAGNOSTIC_VERSION=1\nSTAGE=%s\nEND=1\n", stage)
}
