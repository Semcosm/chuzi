//go:build windows

package slotwindows

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/environment"
	"github.com/Semcosm/chuzi/internal/protocol"
	"github.com/Semcosm/chuzi/internal/slot"
	"github.com/Semcosm/chuzi/internal/slotagent"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// TestWindowsJobPoolNativeSmoke is deliberately opt-in. The PowerShell entry
// point supplies only disposable paths and packaged runtime files; ordinary
// Windows unit-test runs must never create an operating-system user.
func TestWindowsJobPoolNativeSmoke(t *testing.T) {
	if os.Getenv("CHUZI_RUN_WINDOWS_JOB_POOL_SMOKE") != "1" {
		t.Skip("native job-pool smoke is opt-in")
	}
	tokenUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || tokenUser.User.Sid.String() != "S-1-5-18" {
		t.Fatal("native Windows job-pool smoke must run as LocalSystem")
	}
	root := requiredSmokeEnv(t, "CHUZI_WINDOWS_JOB_POOL_SMOKE_ROOT")
	agentPath := requiredSmokeEnv(t, "CHUZI_WINDOWS_JOB_POOL_SMOKE_AGENT")
	nodePath := requiredSmokeEnv(t, "CHUZI_WINDOWS_JOB_POOL_SMOKE_NODE")
	workerPath := requiredSmokeEnv(t, "CHUZI_WINDOWS_JOB_POOL_SMOKE_WORKER")
	userPrefix := requiredSmokeValue(t, "CHUZI_WINDOWS_JOB_POOL_SMOKE_USER_PREFIX")

	dataDir := filepath.Join(root, "data")
	runtimeRoot := filepath.Join(root, "runtime")
	packageRoot := filepath.Join(runtimeRoot, "environment")
	profileRoot := filepath.Join(dataDir, "profiles")
	if err := os.MkdirAll(profileRoot, 0o700); err != nil {
		t.Fatal("native smoke setup failed")
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "backups"), 0o700); err != nil {
		t.Fatal("native smoke setup failed")
	}
	if err := os.WriteFile(filepath.Join(dataDir, "chuzi.db"), nil, 0o600); err != nil {
		t.Fatal("native smoke setup failed")
	}
	workerContent, err := os.ReadFile(workerPath)
	if err != nil {
		t.Fatal("native smoke worker read failed")
	}
	if err := os.MkdirAll(packageRoot, 0o700); err != nil {
		t.Fatal("native smoke package setup failed")
	}
	packageWorker := filepath.Join(packageRoot, "worker.mjs")
	if err := os.WriteFile(packageWorker, workerContent, 0o600); err != nil {
		t.Fatal("native smoke package worker setup failed")
	}
	adapterContent := []byte("export default {};\n")
	packageAdapter := filepath.Join(packageRoot, "adapter.mjs")
	if err := os.WriteFile(packageAdapter, adapterContent, 0o600); err != nil {
		t.Fatal("native smoke package adapter setup failed")
	}
	workerDigest := sha256.Sum256(workerContent)
	adapterDigest := sha256.Sum256(adapterContent)
	manifest := environment.Manifest{
		API:           environment.API,
		EnvironmentID: "chuzi-smoke-environment",
		Version:       "smoke-1",
		Targets:       []string{"windows-amd64"},
		Capabilities:  []string{"browser"},
		Resources: []environment.Resource{
			{Path: "worker.mjs", SHA256: hex.EncodeToString(workerDigest[:]), Size: int64(len(workerContent))},
			{Path: "adapter.mjs", SHA256: hex.EncodeToString(adapterDigest[:]), Size: int64(len(adapterContent))},
		},
		Entrypoints: []environment.Entrypoint{
			{Name: environment.WorkerEntrypointName, Path: "worker.mjs", Runtime: "browser-worker"},
			{Name: environment.AdapterBridgeEntrypointName, Path: "adapter.mjs", Runtime: environment.AdapterBridgeEntrypointName},
		},
		Signer:        "chuzi-smoke-signer",
		InstallPolicy: environment.InstallPolicy{Atomic: true},
		CleanupPolicy: environment.CleanupPolicy{RemoveResources: true},
	}
	manifest.ManifestDigest, err = manifest.ComputeDigest()
	if err != nil {
		t.Fatal("native smoke package digest failed")
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal("native smoke package manifest failed")
	}
	if err := os.WriteFile(filepath.Join(packageRoot, environment.ManifestName), manifestData, 0o600); err != nil {
		t.Fatal("native smoke package manifest setup failed")
	}
	handoff, err := environment.NewRuntimeHandoff(environment.Package{
		Root:     packageRoot,
		Manifest: manifest,
		Record: environment.Record{
			EnvironmentID:  manifest.EnvironmentID,
			Version:        manifest.Version,
			ManifestDigest: manifest.ManifestDigest,
			Signer:         manifest.Signer,
			Installed:      true,
			Verified:       true,
			Trusted:        true,
			Enabled:        true,
			Healthy:        true,
			Ready:          true,
			Generation:     1,
		},
	}, environment.WorkerEntrypointName)
	if err != nil {
		t.Fatal("native smoke runtime handoff failed")
	}

	localRDP := os.Getenv("CHUZI_WINDOWS_JOB_POOL_SMOKE_LOCAL_RDP") == "1"
	options := Options{
		DataDir:            dataDir,
		UserPrefix:         userPrefix,
		EnvironmentID:      handoff.EnvironmentID(),
		Version:            handoff.Version(),
		ManifestDigest:     handoff.ManifestDigest(),
		Signer:             handoff.Signer(),
		RequireTrusted:     true,
		RDPEnabled:         true,
		AgentPath:          agentPath,
		Runtime:            handoff,
		RuntimePath:        handoff.PackageRoot(),
		WorkerRuntimeRoot:  runtimeRoot,
		WorkerCommand:      nodePath,
		WorkerScript:       handoff.WorkerPath(),
		AdapterScript:      handoff.AdapterBridgePath(),
		SessionIdleTimeout: 5 * time.Second,
	}
	if !localRDP {
		options.SessionBootstrapper = NewRunnerSessionBootstrapper()
	}
	provisionerValue, err := New(options)
	if err != nil {
		t.Fatal("native smoke provisioner setup failed")
	}
	provisioner, ok := provisionerValue.(*windowsProvisioner)
	if !ok {
		t.Fatal("native smoke provisioner type mismatch")
	}
	request := slot.ProvisionRequest{
		SlotID:                "smoke-001",
		PoolID:                "smoke-pool",
		Ordinal:               1,
		Owner:                 "smoke-owner",
		EnvironmentGeneration: 1,
		PackageGeneration:     handoff.PackageGeneration(),
		Requirement: slot.EnvironmentRequirement{
			EnvironmentID:  options.EnvironmentID,
			Version:        options.Version,
			ManifestDigest: options.ManifestDigest,
			Signer:         options.Signer,
			RequireTrusted: true,
			Capabilities:   []string{"windows-desktop"},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if localRDP {
		prepareLocalRDPTestSession(t, root, options, request, userPrefix)
	}
	machineShellBefore := readNativeMachineShell(t)

	result, err := provisioner.Provision(ctx, request)
	if err != nil {
		t.Fatalf("native Windows provision failed: %s", nativeSmokeFailureClass(err))
	}
	provisioner.mu.Lock()
	provisionedProcess := provisioner.agents[request.SlotID].process
	provisioner.mu.Unlock()
	if provisionedProcess == nil {
		t.Fatal("native service did not retain the agent process")
	}
	retired := false
	t.Cleanup(func() {
		if !retired {
			_ = provisioner.Retire(context.Background(), request)
		}
		_ = provisioner.Shutdown(context.Background())
	})
	if result.AgentHandle != "slot:smoke-001" || !result.Summary.Trusted || result.Summary.Generation != 1 {
		t.Fatal("native Windows provision returned an invalid redacted summary")
	}

	paths, err := options.DerivePaths(request.SlotID, request.Ordinal, request.EnvironmentGeneration)
	if err != nil {
		t.Fatal("native smoke path derivation failed")
	}
	record, err := readOwnership(paths.Metadata)
	if err != nil || record.SlotID != request.SlotID || record.Ordinal != request.Ordinal || record.Generation != request.EnvironmentGeneration || record.SID == "" {
		t.Fatal("native ownership metadata did not reconcile")
	}
	policy, err := NewProfilePolicy(filepath.Dir(agentPath))
	if err != nil {
		t.Fatal("native profile policy setup failed")
	}
	wantShell, err := policy.ShellCommand()
	if err != nil {
		t.Fatal("native profile shell command derivation failed")
	}
	actualShell, err := ReadProfileShellCommand(record.SID)
	if err != nil || actualShell != wantShell || ValidateSessionShellCommand(filepath.Dir(agentPath), actualShell) != nil {
		t.Fatal("native profile policy did not apply to the target user HKCU")
	}
	if machineShellAfter := readNativeMachineShell(t); machineShellAfter != machineShellBefore {
		t.Fatal("native profile policy changed the machine Winlogon Shell")
	}
	session, err := FindSession(record.SID)
	if err != nil || verifySessionDesktop(record.SID, session.ID, paths.Desktop) != nil {
		t.Fatal("native agent custom desktop was not verified")
	}
	provisioner.mu.Lock()
	initialAgentProcess := provisioner.agents[request.SlotID].process
	var jobAssigned bool
	if initialAgentProcess != nil {
		initialAgentProcess.mu.Lock()
		jobAssigned = initialAgentProcess.job != 0
		initialAgentProcess.mu.Unlock()
	}
	provisioner.mu.Unlock()
	if !jobAssigned {
		t.Fatal("native agent is not owned by a Job Object")
	}
	if hasAdministratorsMembership(paths.UserName) {
		t.Fatal("managed smoke user belongs to Administrators")
	}
	if err := verifyRemoteDesktopMembership(paths.UserName); err != nil {
		t.Fatal("managed smoke user is not authorized for Remote Desktop Users")
	}

	profileName := profileDirectoryName("smoke-account")
	profilePath := filepath.Join(profileRoot, profileName)
	if err := os.MkdirAll(profilePath, 0o700); err != nil {
		t.Fatal("native profile setup failed")
	}
	if err := provisioner.GrantProfile(ctx, request.SlotID, profilePath); err != nil {
		t.Fatalf("native profile grant failed: %s", nativeSmokeFailureClass(err))
	}
	if _, err := provisioner.Inspect(ctx, request); err != nil {
		t.Fatal("native inspect after profile grant failed")
	}
	provisioner.mu.Lock()
	inspectedProcess := provisioner.agents[request.SlotID].process
	provisioner.mu.Unlock()
	if inspectedProcess != provisionedProcess {
		t.Fatal("native inspect started another agent")
	}
	if err := provisioner.RevokeProfile(ctx, request.SlotID, profilePath); err != nil {
		t.Fatal("native profile revoke failed")
	}
	provisioner.mu.Lock()
	firstProcess := provisioner.agents[request.SlotID].process
	provisioner.mu.Unlock()
	if reused, err := provisioner.Provision(ctx, request); err != nil || reused.AgentHandle != result.AgentHandle {
		t.Fatal("native managed user reuse failed")
	}
	provisioner.mu.Lock()
	secondProcess := provisioner.agents[request.SlotID].process
	provisioner.mu.Unlock()
	if firstProcess == nil || firstProcess != secondProcess {
		t.Fatal("native repeated provision started another agent")
	}
	if _, err := provisioner.validateProfilePath(filepath.Join(profileRoot, "..", "outside")); err == nil {
		t.Fatal("profile path traversal was accepted")
	}
	if _, err := options.DerivePaths("..\\escape", 1, 1); err == nil {
		t.Fatal("slot path traversal was accepted")
	}
	// The worker runs as the managed user and therefore needs the same
	// short-lived profile grant that the production runner holds around
	// StartJob. The earlier ACL test intentionally revoked that grant.
	if err := provisioner.GrantProfile(ctx, request.SlotID, profilePath); err != nil {
		t.Fatal("native profile regrant failed")
	}

	pipe, token, err := provisioner.AgentEndpoint(request.SlotID, result.AgentHandle)
	if err != nil || pipe == "" || token == "" {
		t.Fatal("native agent endpoint was not available")
	}
	client, err := slotagent.Dial(ctx, pipe)
	if err != nil {
		t.Fatal("native agent pipe connection failed")
	}
	defer client.Close()
	lease := slotagent.Request{CommandID: "smoke-prepare", RequestID: "smoke-request", Owner: "smoke-owner", AccountID: "smoke-account", SlotID: request.SlotID, LeaseID: "smoke-lease", EnvironmentGeneration: 1, Auth: token, Command: slotagent.PrepareSlot}
	if _, err := client.Call(ctx, lease); err != nil {
		t.Fatal("native agent prepare failed")
	}
	start := lease
	start.CommandID, start.Command, start.JobKind = "smoke-start", slotagent.StartJob, slotagent.BrowserWorker
	if _, err := client.Call(ctx, start); err != nil {
		t.Fatal("native browser worker start failed")
	}
	hello := slotagent.Frame{Kind: "worker_request", SlotID: request.SlotID, RequestID: lease.RequestID, Owner: lease.Owner, LeaseID: lease.LeaseID, EnvironmentGeneration: 1, Worker: protocolEnvelope(protocol.Hello, "hello-1", map[string]string{"service": "chuzi-session-runner", "version": protocol.Version})}
	if response, err := client.WorkerCall(ctx, hello); err != nil || response.Worker == nil || response.Worker.Type != protocol.HelloAck {
		t.Fatal("native browser worker handshake failed")
	}
	startSession := slotagent.Frame{Kind: "worker_request", SlotID: request.SlotID, RequestID: lease.RequestID, Owner: lease.Owner, LeaseID: lease.LeaseID, EnvironmentGeneration: 1, Worker: protocolEnvelope(protocol.SessionStart, "session-1", map[string]string{"session_id": "session-1", "account_id": "smoke-account", "request_id": "smoke-request", "mode": "failure"})}
	if response, err := client.WorkerCall(ctx, startSession); err != nil || response.Worker == nil || response.Worker.Type != protocol.SessionStarted {
		t.Fatal("native browser worker session start failed")
	}
	leaseNow := time.Now().UTC()
	durableLease := slot.Lease{LeaseID: lease.LeaseID, SlotID: request.SlotID, PoolID: request.PoolID, RequestID: lease.RequestID, AccountID: lease.AccountID, Owner: lease.Owner, EnvironmentGeneration: 1, AcquiredAt: leaseNow.Add(-time.Minute), LastHeartbeat: leaseNow, ExpiresAt: leaseNow.Add(time.Minute)}
	if err := provisioner.Health(ctx, request, durableLease); err != nil {
		t.Fatal("native session health check failed")
	}
	expiredLease := durableLease
	expiredLease.ExpiresAt = leaseNow.Add(-time.Second)
	expiredLease.LastHeartbeat = leaseNow.Add(-2 * time.Second)
	if err := provisioner.Health(ctx, request, expiredLease); !errors.Is(err, slot.ErrLeaseExpired) {
		t.Fatal("native expired lease was accepted")
	}
	stop := lease
	stop.CommandID, stop.Command, stop.JobKind, stop.AccountID = "smoke-stop", slotagent.StopJob, "", ""
	if _, err := client.Call(ctx, stop); err != nil {
		t.Fatal("native worker stop failed")
	}
	if err := provisioner.RevokeProfile(ctx, request.SlotID, profilePath); err != nil {
		t.Fatal("native profile revoke after worker failed")
	}
	stale := lease
	stale.CommandID, stale.LeaseID, stale.Command, stale.JobKind, stale.AccountID = "smoke-stale", "stale-lease", slotagent.Health, "", ""
	if _, err := client.Call(ctx, stale); !errors.Is(err, slotagent.ErrStaleLease) {
		t.Fatal("native stale lease was accepted")
	}
	if err := provisioner.Shutdown(ctx); err != nil {
		t.Fatal("native service shutdown did not fence the agent")
	}
	if _, _, err := provisioner.AgentEndpoint(request.SlotID, result.AgentHandle); !errors.Is(err, ErrSessionUnavailable) {
		t.Fatal("native agent remained available after service shutdown")
	}

	// The provisioner must reject an ownership tree containing an unknown entry
	// and then recover once the test removes only that disposable entry.
	unknown := filepath.Join(paths.Root, "unexpected.bin")
	if err := os.WriteFile(unknown, []byte("smoke"), 0o600); err != nil {
		t.Fatal("native ownership guard setup failed")
	}
	if err := provisioner.Retire(ctx, request); err == nil {
		t.Fatal("unknown managed-tree entry was deleted")
	}
	if err := os.Remove(unknown); err != nil {
		t.Fatal("native ownership guard cleanup failed")
	}

	if err := provisioner.Retire(ctx, request); err != nil {
		t.Fatal("native managed resource retirement failed")
	}
	retired = true
}

func prepareLocalRDPTestSession(t *testing.T, root string, options Options, request slot.ProvisionRequest, userPrefix string) {
	t.Helper()
	runID := strings.TrimSpace(os.Getenv("CHUZI_WINDOWS_JOB_POOL_SMOKE_RUN_ID"))
	runBytes, runErr := hex.DecodeString(runID)
	if runErr != nil || len(runBytes) != 16 || runID != strings.ToLower(runID) {
		t.Fatal("local RDP smoke run identity unavailable")
	}
	markerPath := filepath.Join(root, ".chuzi-smoke-user-ownership")
	marker, err := os.ReadFile(markerPath)
	if err != nil || strings.TrimSpace(string(marker)) != "CHUZI-SMOKE-USER-OWNERSHIP:"+runID+":"+userPrefix {
		t.Fatal("local RDP smoke user ownership marker mismatch")
	}
	paths, err := options.DerivePaths(request.SlotID, request.Ordinal, request.EnvironmentGeneration)
	if err != nil {
		t.Fatal("local RDP smoke path derivation failed")
	}
	sid, err := lookupSID(paths.UserName)
	if err != nil {
		t.Fatal("local RDP smoke user identity unavailable")
	}
	info, err := getUserInfo(paths.UserName)
	if err != nil || info.comment != "CHUZI-MANAGED:"+request.SlotID+":1" || !managedUserPrivilegeAllowed(info.priv) || info.flags&userFlagNormalAccount == 0 || info.flags&userFlagDisabled != 0 || hasAdministratorsMembership(paths.UserName) {
		t.Fatal("local RDP smoke user ownership validation failed")
	}
	if err := verifyRemoteDesktopMembership(paths.UserName); err != nil {
		t.Fatal("local RDP smoke user is not authorized for Remote Desktop Users")
	}
	// RDP authentication and WTS activation are separate transitions. The
	// client can be authenticated while the session is still in connection
	// query state, so wait for the same production FindSession fence to become
	// active instead of treating that short transition as a failed login.
	deadline := time.Now().Add(30 * time.Second)
	var session Session
	var sessionErr error
	for time.Now().Before(deadline) {
		session, sessionErr = FindSession(sid)
		if sessionErr == nil && session.State == "active" {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if sessionErr != nil || session.State != "active" {
		t.Fatalf("local RDP smoke requires an active WTS session for the managed user (last state=%q, error=%v)", session.State, sessionErr)
	}
	if exists, err := managedPathExists(paths.Root); err != nil || exists {
		t.Fatal("local RDP smoke ownership root is not fresh")
	}
	if err := writeOwnership(paths.Metadata, ownershipRecord{Version: 1, SlotID: request.SlotID, Ordinal: request.Ordinal, SID: sid, Generation: request.EnvironmentGeneration}); err != nil {
		t.Fatal("local RDP smoke ownership record setup failed")
	}
}

func nativeSmokeFailureClass(err error) string {
	classes := make([]string, 0, 8)
	appendClass := func(match bool, value string) {
		if match {
			classes = append(classes, value)
		}
	}
	appendClass(errors.Is(err, ErrSessionChanged), "session_changed")
	appendClass(errors.Is(err, ErrSessionDisconnected), "session_disconnected")
	appendClass(errors.Is(err, ErrSessionIdentity), "session_identity_mismatch")
	appendClass(errors.Is(err, ErrSessionGroupLookup), "session_group_lookup_failed")
	appendClass(errors.Is(err, ErrSessionGroupAdd), "session_group_add_failed")
	appendClass(errors.Is(err, ErrSessionGroupVerify), "session_group_verify_failed")
	appendClass(errors.Is(err, ErrSessionPolicy), "session_policy_failed")
	appendClass(errors.Is(err, ErrSessionPolicyLookup), "session_policy_lookup_failed")
	appendClass(errors.Is(err, ErrSessionPolicyOpen), "session_policy_open_failed")
	appendClass(errors.Is(err, ErrSessionPolicyEnumerate), "session_policy_enumerate_failed")
	appendClass(errors.Is(err, ErrSessionPolicyMissing), "session_policy_allow_missing")
	appendClass(errors.Is(err, ErrSessionPolicyDenied), "session_policy_denied")
	appendClass(errors.Is(err, ErrSessionUserLookup), "session_user_lookup_failed")
	appendClass(errors.Is(err, ErrManagedUserSID), "managed_user_sid_unavailable")
	appendClass(errors.Is(err, ErrSessionUnavailable) || errors.Is(err, ErrSessionBootstrapUnavailable), "session_unavailable")
	appendClass(errors.Is(err, ErrACLDrift), "acl_drift")
	appendClass(errors.Is(err, ErrACLSlotDirectories), "acl_slot_directories")
	appendClass(errors.Is(err, ErrACLRuntime), "acl_runtime")
	appendClass(errors.Is(err, ErrACLProfile), "acl_profile")
	appendClass(errors.Is(err, ErrProcessStart), "process_start_failed")
	appendClass(errors.Is(err, errProcessStartValidation), "process_start_validation_failed")
	appendClass(errors.Is(err, errProcessStartExecutablePath), "process_start_executable_path_failed")
	appendClass(errors.Is(err, errProcessStartWorkingDirectory), "process_start_working_directory_failed")
	appendClass(errors.Is(err, errProcessStartEnvironment), "process_start_environment_failed")
	appendClass(errors.Is(err, errProcessStartTokenDuplicate), "process_start_token_duplicate_failed")
	appendClass(errors.Is(err, errProcessStartTokenPrivilege), "process_start_token_privilege_failed")
	appendClass(errors.Is(err, errProcessStartCommandLine), "process_start_command_line_failed")
	appendClass(errors.Is(err, errProcessStartJobCreate), "process_start_job_create_failed")
	appendClass(errors.Is(err, errProcessStartJobConfigure), "process_start_job_configure_failed")
	appendClass(errors.Is(err, errProcessStartDesktopValidation), "process_start_desktop_validation_failed")
	appendClass(errors.Is(err, errProcessStartDesktopImpersonate), "process_start_desktop_impersonation_failed")
	appendClass(errors.Is(err, errProcessStartDesktopOpen), "process_start_desktop_open_failed")
	appendClass(errors.Is(err, errProcessStartDesktopAuthorize), "process_start_desktop_authorization_failed")
	appendClass(errors.Is(err, errProcessStartCreateProcess), "create_process_as_user_failed")
	appendClass(errors.Is(err, errProcessStartBootstrapCreate), "bootstrap_create_process_as_user_failed")
	appendClass(errors.Is(err, errProcessStartAgentCreate), "agent_create_process_as_user_failed")
	appendClass(errors.Is(err, errProcessStartWin32AccessDenied), "create_process_as_user_access_denied")
	appendClass(errors.Is(err, errProcessStartWin32FileMissing), "create_process_as_user_file_missing")
	appendClass(errors.Is(err, errProcessStartWin32PathMissing), "create_process_as_user_path_missing")
	appendClass(errors.Is(err, errProcessStartWin32InvalidArg), "create_process_as_user_invalid_parameter")
	appendClass(errors.Is(err, errProcessStartWin32BadExecutable), "create_process_as_user_bad_executable")
	appendClass(errors.Is(err, errProcessStartWin32Privilege), "create_process_as_user_privilege_not_held")
	appendClass(errors.Is(err, errProcessStartWin32Environment), "create_process_as_user_environment_missing")
	appendClass(errors.Is(err, errProcessStartWin32Token), "create_process_as_user_token_invalid")
	appendClass(errors.Is(err, errProcessStartWin32Unknown), "create_process_as_user_error_unknown")
	appendClass(errors.Is(err, errProcessStartJobAssign), "process_start_job_assign_failed")
	appendClass(errors.Is(err, errProcessStartResume), "process_start_resume_failed")
	appendClass(errors.Is(err, ErrOwnership), "ownership_failed")
	appendClass(errors.Is(err, ErrCleanupAgent), "cleanup_agent_failed")
	appendClass(errors.Is(err, ErrCleanupSession), "cleanup_session_failed")
	appendClass(errors.Is(err, ErrCleanupRoot), "cleanup_root_failed")
	appendClass(errors.Is(err, ErrCleanupUserOwnership), "cleanup_user_ownership_failed")
	appendClass(errors.Is(err, ErrCleanupUserMarker), "cleanup_user_marker_failed")
	appendClass(errors.Is(err, ErrCleanupUserPrivilege), "cleanup_user_privilege_failed")
	appendClass(errors.Is(err, ErrCleanupUserDisabled), "cleanup_user_disabled")
	appendClass(errors.Is(err, ErrCleanupUserAdmin), "cleanup_user_admin_membership")
	appendClass(errors.Is(err, ErrCleanupUserSID), "cleanup_user_sid_failed")
	appendClass(errors.Is(err, ErrCleanupUserInspect), "cleanup_user_inspect_failed")
	appendClass(errors.Is(err, ErrCleanupUserDelete), "cleanup_user_delete_failed")
	appendClass(errors.Is(err, ErrCleanupUser), "cleanup_user_failed")
	appendClass(errors.Is(err, ErrCleanup), "cleanup_failed")
	if len(classes) == 0 {
		return "provision_failed"
	}
	return strings.Join(classes, "+")
}

func TestNativeSmokeFailureClassReportsSafeProcessStartStage(t *testing.T) {
	err := processStartFailure(errProcessStartCreateProcess)
	if got, want := err.Error(), ErrProcessStart.Error(); got != want {
		t.Fatalf("process start error text = %q, want generic %q", got, want)
	}
	got := nativeSmokeFailureClass(err)
	if want := "process_start_failed+create_process_as_user_failed"; got != want {
		t.Fatalf("native smoke failure class = %q, want %q", got, want)
	}
}

func TestNativeSmokeFailureClassReportsCreateProcessBoundary(t *testing.T) {
	for _, test := range []struct {
		name  string
		stage error
		want  string
	}{
		{name: "bootstrap", stage: errProcessStartBootstrapCreate, want: "bootstrap_create_process_as_user_failed"},
		{name: "agent", stage: errProcessStartAgentCreate, want: "agent_create_process_as_user_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := processStartFailure(test.stage)
			wantStageOnly := "process_start_failed+" + test.want
			if got := nativeSmokeFailureClass(err); got != wantStageOnly {
				t.Fatalf("native smoke failure class = %q, want %s", got, wantStageOnly)
			}
			wrapped := processStartFailureWithWin32Error(errors.Join(errProcessStartCreateProcess, test.stage), windows.ERROR_ACCESS_DENIED)
			got := nativeSmokeFailureClass(wrapped)
			want := "process_start_failed+create_process_as_user_failed+" + test.want + "+create_process_as_user_access_denied"
			if got != want {
				t.Fatalf("native smoke failure class = %q, want %q", got, want)
			}
		})
	}
}

func requiredSmokeEnv(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" || !filepath.IsAbs(value) || strings.ContainsAny(value, "\x00\r\n") {
		t.Fatal("native smoke configuration is unavailable")
	}
	return filepath.Clean(value)
}

func readNativeMachineShell(t *testing.T) string {
	t.Helper()
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return ""
		}
		t.Fatal("machine Winlogon policy could not be inspected")
	}
	defer key.Close()
	value, _, err := key.GetStringValue("Shell")
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return ""
		}
		t.Fatal("machine Winlogon policy could not be inspected")
	}
	return value
}

func requiredSmokeValue(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" || len(value) > 12 || !userPrefixPattern.MatchString(value) {
		t.Fatal("native smoke configuration is unavailable")
	}
	return value
}

func profileDirectoryName(accountID string) string {
	digest := sha256.Sum256([]byte(accountID))
	return hex.EncodeToString(digest[:])
}

func protocolEnvelope(kind, id string, payload map[string]string) *protocol.Envelope {
	message := protocol.Request(id, kind, payload)
	return &message
}
