# CR-0092: add Windows native job-pool acceptance gate

Base: main
Head or Range: 7ff457512a076af17e9515757a1c6fc591331167
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(service): add Windows native job pool smoke gate
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: b57ce246ea8dbf64ad642414a096f4e9ada3e60a
Head OID: 7ff457512a076af17e9515757a1c6fc591331167
Integrated Result: pending

## Summary

Add a strict Windows native job-pool smoke entrypoint, an opt-in Windows-only
native provisioner test, a controlled self-hosted runner job, and operational
readiness/runbook documentation. The smoke gate uses only fixed service-owned
runtime inputs and disposable resources, and it is a required input to the
aggregate build check so cross-compilation cannot stand in for native
acceptance.

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
entrypoint, applies a read/execute runtime ACL, and removes only users bearing
the fixed CHUZI ownership marker. The Windows-only test covers managed-user
creation and reuse, Remote Desktop Users membership, Administrators exclusion,
ownership metadata, Profile ACL grant/revoke, reparse/path traversal rejection,
named-pipe token and stale-lease checks, worker handshake/start/stop, active
health and expired-lease fences, service-shutdown agent cleanup, unknown-tree
protection, and retirement. It never prints passwords, SIDs, usernames,
Profile paths, pipe paths, or raw Win32 errors.

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
The native smoke has not run because this checkout has no Windows job-pool
runner with administrator rights and an interactive disposable user session; a
successful test-channel package therefore still needs the user's real-machine
native smoke. The production RDP bridge remains deny-by-default pending a
deployment-owned authorizer and broker.
The hosted preflight is covered by `scripts/test_windows_job_pool_hosted_preflight.ps1`;
its first remote result is pending at this revision.

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
job. Environments without the designated runner cannot pass the release gate.

## Backport Target

none
