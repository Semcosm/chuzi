# CR-0092: add Windows native job-pool acceptance gate

Base: main
Head or Range: f2d5f6e47e5afcd013adc3805022b04f5be98b92
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(service): add Windows native job pool smoke gate
Revision: 22
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: b57ce246ea8dbf64ad642414a096f4e9ada3e60a
Head OID: 2f120ad41a2f9401fb83c9525d508829f563a455
Integrated Result: pending

## Summary

Add a strict Windows native job-pool smoke entrypoint, an opt-in Windows-only
native provisioner test, a controlled self-hosted runner job, and operational
readiness/runbook documentation. Revision 2 closes the missing session
prerequisite with an optional controlled `SessionBootstrapper` boundary and
hardens staged cleanup without weakening the production `FindSession` fence.

## Motivation

Phase 2 and phase 3 provide the logical slot, controlled agent, signed
environment, generation fence, and opaque capability boundaries, but they did
not provide an executable Windows user/session/ACL/Job Object acceptance gate.
The release workflow must distinguish those logical guarantees from evidence
obtained on a Windows host with administrator rights and a disposable local
user session.

## Test Evidence

The new `scripts/test_windows_job_pool_smoke.ps1` requires a Windows
administrator runner, creates a unique temporary root, builds the fixed
`chuzi-user-agent.exe`, copies the runner's `node.exe` and the versioned worker
entrypoint, applies a read/execute runtime ACL, and removes only the current run's exact random-prefix user
after validating run-root and user ownership markers. Historical marker users are counted and reported,
but are never removed by a later run. The script still validates the current
CHUZI ownership marker. Revision 2 adds the SessionBootstrapper
contract: a runner-owned provider may establish the disposable user's WTS
session, but `Provision` always re-runs `FindSession(managed SID)` before
starting the agent. No password crosses the contract or enters Core, logs,
environment files, or preserved test artifacts; the local diagnostic console
is the explicit test-machine exception. Cleanup stops worker/agent trees before
session logoff and user/root removal, uses bounded retry/backoff, keeps user and
root results separate, preserves a redacted `test-output.log` on failure, and
refuses unknown ownership entries. The Windows-only test still covers
managed-user creation and reuse, Remote Desktop Users membership, Administrators
exclusion, ownership metadata, Profile ACL grant/revoke, reparse/path traversal
rejection, named-pipe token and stale-lease checks, worker handshake/start/stop,
active health and expired-lease fences, service-shutdown agent cleanup,
unknown-tree protection, and retirement.

The controlled runner job is present in `.github/workflows/chuzi-build.yml`
with the `self-hosted`, `windows`, `chuzi-job-pool` labels and remains required
for nightly and stable builds. A hosted `windows-2022` preflight now compiles
all Windows Go packages, runs `go vet`, checks the native smoke test entrypoint,
and builds the fixed user-agent; the manual
`test` channel uses that preflight and deliberately skips the
session-dependent native job when no dedicated runner is registered. On this
Linux host, the deterministic checks passed: `go test ./...`, `go test -race
./...`, `go vet ./...`, `GOOS=windows GOARCH=amd64 go build ./...`,
`GOOS=windows GOARCH=amd64 go vet ./...`, browser-worker tests (22), all
repository validators, `scripts/test_build_contract.sh`, and `git diff --check`.
The native smoke has not run at this revision. The fixed session-broker pipe was
checked on the target Windows host and was not listening; no managed-user active
WTS session or deployment-owned RDP/Winlogon authorizer was available. The
implementation now provides a strict cross-platform protocol decoder, fixed-pipe
ACL contract, ownership state machine, bounded stop wait, restart refusal, and a
`SessionLoginAdapter` credential boundary. The broker listener and real login
adapter remain deployment-owned dependencies. Until the real-machine smoke and
production RDP authorizer pass, this CR remains PARTIAL and must not be merged as
native acceptance.
The opt-in -LocalRdp mode can validate a host with multiple local RDP sessions
without a second device: the smoke script creates the disposable standard user,
stores a random password in the current interactive user's session-scoped
Credential Manager, initializes the user's Profile with a logon-with-profile
process, starts an RDP client against localhost, and the Windows test confirms
the exact managed SID has an active WTS session. It then exercises the native
agent, profile ACL, worker lifecycle, and retirement cleanup. This diagnostic
does not validate the session broker listener, pipe ACL, broker ownership
lifecycle, or production RDP authorizer. It has not yet run on the target host;
CR-0092 remains pending.

Cross-platform tests cover unknown fields/operations/versions, identity and
generation validation, credential omission, narrow ACL identities, duplicate
start/stop, stale generation, wrong owner, broker restart adoption refusal, and
bounded stop waiting. `go test ./...`, `go test -race ./...`, `go vet ./...`,
Windows cross-build/vet, browser-worker tests (22), repository validators,
`scripts/test_build_contract.sh`, and this CR validator pass at the recorded
Head OID.
The first Windows checkout invocation after the hotfix fast-forward reached the
setup stage but the previous script exposed only the generic `setup` label; the
follow-up test commit now reports stable setup classifications such as
`administrator_required`, `toolchain_required`, `runtime_acl`, or
`session_unavailable` and preserves a redacted diagnostic log.
The elevated rerun reached the native provision path and returned
`cleanup_failed`; the follow-up commit now separates `cleanup_agent_failed`,
`cleanup_session_failed`, `cleanup_root_failed`, and `cleanup_user_failed` so
the next native run identifies the exact rollback boundary.
The hosted preflight is covered by `scripts/test_windows_job_pool_hosted_preflight.ps1`;
its first remote result is pending at this revision.
The Windows runner diagnostic showed that NetUserGetInfo(USER_INFO_1) can report
USER_PRIV_GUEST for this disposable account while UF_NORMAL_ACCOUNT is set;
the verifier now accepts only privilege levels 0 or 1 together with the normal-account,
enabled, SID, marker, group, and non-Administrator fences, and continues to reject
USER_PRIV_ADMIN.

Revision 8 adds an opt-in `-LocalRdp` path for a local administrator console:
the script creates the one-run marked user with a random password, keeps the
credential only in the current logon session's Credential Manager, starts the
fixed loopback `mstsc.exe` client, and waits for an active WTS session. The Go
test independently checks the exact SID, local account marker, group and
active-session state before provisioning the agent. Session enumeration and
cleanup use WTS APIs and numeric session states, so they do not depend on the
Windows display language. The Windows hosted preflight now invokes
`-ValidateOnly` to parse the script and compile its native helpers without
creating OS resources. This revision has passed Go tests/race/vet, Windows
cross-build/vet, browser-worker tests, repository validators, build contract,
and `git diff --check`; the current Linux host has no PowerShell, so the
Windows helper compilation and local RDP/native smoke are still pending.
`-LocalRdp` remains diagnostic evidence only; broker, pipe ACL, ownership
lifecycle and production RDP authorizer gates are unchanged.

Revision 9 fixes Windows PowerShell 5.1 result unrolling in the smoke cleanup:
all function output is captured as an array before reading `Count`, including
empty, single-item, and multi-item results. `-ValidateOnly` now asserts those
three cardinalities. If managed-user or session cleanup fails, the run-owned
temporary root and its ownership marker are preserved and the script prints
the root path for recovery. Linux Go, Windows cross-build/vet, Node, repository
validator, build-contract, and diff checks pass; Windows hosted preflight and
the target machine's `-LocalRdp` native smoke remain to be run. This CR remains
pending until the native acceptance and deployment-owned broker/RDP gates pass.

Revision 10 changes the diagnostic local-RDP target from fixed loopback
`127.0.0.1` to the host's active default-route non-loopback IPv4 address. Some
Windows/RDPWrap configurations reject loopback mstsc with error `0x708` because
it is treated as a second console connection. The script now derives the address
from `Get-NetIPConfiguration`, stores the short-lived Credential Manager entry
under the matching `TERMSRV/<address>` target, and keeps the connection on the
same machine. No broker, `runas`, `tscon`, `CreateProcessAsUser`, or WTS fence is
bypassed. Native smoke and production broker/RDP gates remain pending.

Revision 11 makes the local RDP client identity explicit. The smoke harness
writes a run-owned temporary RDP profile containing the exact
COMPUTERNAME\\<random-user> username and administrative session disabled, then
launches mstsc with that profile. The profile contains no password and is
removed during cleanup; Credential Manager remains the only password boundary.
This prevents the client from falling back to the interactive console identity.
The qwinsta/RDP client evidence from the target host showed no managed-user
session was created before this change, so native acceptance remains pending.

Revision 12 adds pre-connect identity diagnostics. The script prints the
interactive runner username and session ID, target host, exact target username
and SID, Credential Manager target, temporary RDP profile path, and an explicit
password-printed-to-console marker. The generated one-time password is printed
only to the interactive console by the local diagnostic path; the same
redacted summary is preserved in the failure log when the RDP session cannot be
established. No credential material is written to the summary or preserved
output.

Revision 13 changes the diagnostic `-LocalRdp` target to `127.0.0.2`, matching
the `mstsc` target used by the sebaxakerhtc RDPWrap `RDP_CnC` self-test. The
Credential Manager target now uses `TERMSRV/127.0.0.2`; the exact disposable
computer-local username remains pinned in the temporary profile and
`administrative session:i:0` remains disabled. This is still only a diagnostic
local-RDP path: the Go provisioner must observe the exact active WTS session,
and no session, broker, or authorizer fence is bypassed.

Revision 14 adds an explicit identity fence to the local-RDP wait. The target
SID must differ from the interactive runner SID, and any newly observed active
WTS session for the runner account causes `local RDP authenticated as the
interactive runner identity` failure. The native smoke therefore cannot
accept a runner-account session as evidence for the disposable target.

Revision 15 makes the generated RDP profile request a fresh connection with
`disableconnectionsharing:i:1` and disables credential reuse across gateway and
remote-host boundaries with `promptcredentialonce:i:0`. The previous profile
omitted both settings, leaving the documented `disableconnectionsharing`
default of `0`, which permits mstsc to reconnect an existing disconnected
session. The explicit settings close that profile-level session-reuse path while
the exact username, Credential Manager target, runner identity fence, and
production WTS `FindSession` validation remain unchanged.

Revision 18 removes the manual RDP branch. The local diagnostic now always
generates one disposable password, prints the exact target username, password,
SID, address, and profile path for test-machine debugging, stores the same
password only in the current interactive user's Credential Manager, and starts
the generated RDP profile through `mstsc.exe`. Failure summaries and preserved
logs continue to contain only the redacted
`rdp_password=printed_to_console` marker; the password is never written to the
profile, test log, or CR. After the target WTS session becomes active, the
harness also waits for the user's Profile and `NTUSER.DAT` to exist before
starting native provisioning, matching the disposable-user initialization
sequence used by the Windows session experiment.

Revision 20 preserves an `rdp-diagnostics.log` under the run-owned smoke root
when the local RDP stage fails. The diagnostic captures the actual `mstsc`
process command line, `cmdkey` target state, WTS and `qwinsta` snapshots, and
available RDP/Security events around the connection attempt. This makes a
loopback console reconnect distinguishable from ignored credentials or a target
authentication failure without putting the password in the diagnostic file.

Revision 21 changes the automatic diagnostic target back from the loopback alias
to the host's active default-route non-loopback IPv4 address. The preceding
loopback run authenticated successfully but never created the disposable user's
WTS session; the RDP client then disconnected during SSL/session establishment
and the server reported the loopback source address. The generated profile and
Credential Manager entry now use the selected interface address, while the
exact username, target SID fence, automatic-only flow, and redacted diagnostics
remain unchanged.

Revision 22 aligns the automatic path with the known-good local RDP procedure:
the target is localhost, the profile username is .\\<random-user>, and
CreateProcessWithLogonW(LOGON_WITH_PROFILE) initializes the disposable user's
Profile and NTUSER.DAT before mstsc starts. The generated RDP file is passed as
an explicitly quoted mstsc argument, and the exact launch command is preserved
in the redacted diagnostics.

## Risk

The native smoke job is intentionally required for nightly and stable builds
and will remain queued or fail when the designated runner is absent; this
prevents a release from claiming Windows acceptance from Linux cross-build
output. The test channel is limited to hosted Windows preflight evidence and
must not be treated as native acceptance. The current service still cannot
issue interactive RDP capabilities because it constructs
`credential.DenyRDPAuthorizer{}`. No production RDP endpoint, password, SID,
Profile, or named-pipe material is introduced by this change.

## Rollback

Revert the smoke test, workflow gate, and operational documentation commit.
Keep phase 2/3 logical and environment records intact; disable the Windows
pool before removing any managed package, user, or slot directory.

## Breaking Change

The full build aggregate now requires the controlled Windows job-pool smoke
job. Environments without the designated runner or session provider cannot pass
the release gate.

## Backport Target

none
