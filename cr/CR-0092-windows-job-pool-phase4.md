# CR-0092: add Windows native job-pool acceptance gate

Base: main
Head or Range: fb3833b664754adc589db129f3696c29b41dda7b
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(service): add Windows native job pool smoke gate
Revision: 6
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: b57ce246ea8dbf64ad642414a096f4e9ada3e60a
Head OID: fb3833b664754adc589db129f3696c29b41dda7b
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
environment files, or test output. Cleanup stops worker/agent trees before
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
