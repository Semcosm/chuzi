//go:build windows

package slotwindows

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/protocol"
	"github.com/Semcosm/chuzi/internal/slot"
	"github.com/Semcosm/chuzi/internal/slotagent"
)

// TestWindowsJobPoolNativeSmoke is deliberately opt-in. The PowerShell entry
// point supplies only disposable paths and packaged runtime files; ordinary
// Windows unit-test runs must never create an operating-system user.
func TestWindowsJobPoolNativeSmoke(t *testing.T) {
	if os.Getenv("CHUZI_RUN_WINDOWS_JOB_POOL_SMOKE") != "1" {
		t.Skip("native job-pool smoke is opt-in")
	}
	root := requiredSmokeEnv(t, "CHUZI_WINDOWS_JOB_POOL_SMOKE_ROOT")
	agentPath := requiredSmokeEnv(t, "CHUZI_WINDOWS_JOB_POOL_SMOKE_AGENT")
	nodePath := requiredSmokeEnv(t, "CHUZI_WINDOWS_JOB_POOL_SMOKE_NODE")
	workerPath := requiredSmokeEnv(t, "CHUZI_WINDOWS_JOB_POOL_SMOKE_WORKER")
	userPrefix := requiredSmokeValue(t, "CHUZI_WINDOWS_JOB_POOL_SMOKE_USER_PREFIX")

	dataDir := filepath.Join(root, "data")
	runtimeRoot := filepath.Join(root, "runtime")
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

	options := Options{
		DataDir:             dataDir,
		UserPrefix:          userPrefix,
		EnvironmentID:       "chuzi-environment/v1",
		Version:             "smoke-1",
		ManifestDigest:      strings.Repeat("a", 64),
		Signer:              "smoke-signer",
		RequireTrusted:      true,
		RDPEnabled:          true,
		AgentPath:           agentPath,
		RuntimePath:         runtimeRoot,
		WorkerRuntimeRoot:   runtimeRoot,
		WorkerCommand:       nodePath,
		WorkerScript:        workerPath,
		SessionIdleTimeout:  5 * time.Second,
		SessionBootstrapper: NewRunnerSessionBootstrapper(),
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

	result, err := provisioner.Provision(ctx, request)
	if err != nil {
		t.Fatalf("native Windows provision failed: %s", nativeSmokeFailureClass(err))
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
		t.Fatal("native profile grant failed")
	}
	if _, err := provisioner.Inspect(ctx, request); err != nil {
		t.Fatal("native inspect after profile grant failed")
	}
	if err := provisioner.RevokeProfile(ctx, request.SlotID, profilePath); err != nil {
		t.Fatal("native profile revoke failed")
	}
	if reused, err := provisioner.Provision(ctx, request); err != nil || reused.AgentHandle != result.AgentHandle {
		t.Fatal("native managed user reuse failed")
	}
	if _, err := provisioner.validateProfilePath(filepath.Join(profileRoot, "..", "outside")); err == nil {
		t.Fatal("profile path traversal was accepted")
	}
	if _, err := options.DerivePaths("..\\escape", 1, 1); err == nil {
		t.Fatal("slot path traversal was accepted")
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
	durableLease := slot.Lease{LeaseID: lease.LeaseID, SlotID: request.SlotID, PoolID: request.PoolID, RequestID: lease.RequestID, AccountID: lease.AccountID, Owner: lease.Owner, EnvironmentGeneration: 1, AcquiredAt: leaseNow, LastHeartbeat: leaseNow, ExpiresAt: leaseNow.Add(time.Minute)}
	if err := provisioner.Health(ctx, request, durableLease); err != nil {
		t.Fatal("native session health check failed")
	}
	expiredLease := durableLease
	expiredLease.ExpiresAt = leaseNow.Add(-time.Second)
	if err := provisioner.Health(ctx, request, expiredLease); !errors.Is(err, slot.ErrLeaseExpired) {
		t.Fatal("native expired lease was accepted")
	}
	stop := lease
	stop.CommandID, stop.Command = "smoke-stop", slotagent.StopJob
	if _, err := client.Call(ctx, stop); err != nil {
		t.Fatal("native worker stop failed")
	}
	stale := lease
	stale.CommandID, stale.LeaseID, stale.Command, stale.AccountID = "smoke-stale", "stale-lease", slotagent.Health, ""
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

func nativeSmokeFailureClass(err error) string {
	classes := make([]string, 0, 4)
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

func requiredSmokeEnv(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" || !filepath.IsAbs(value) || strings.ContainsAny(value, "\x00\r\n") {
		t.Fatal("native smoke configuration is unavailable")
	}
	return filepath.Clean(value)
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
