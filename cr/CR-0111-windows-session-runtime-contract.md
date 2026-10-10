# CR-0111: define the Windows slot session runtime contract

Base: main
Head or Range: 87ccda2a10bd1a26d9cf219bd36b740ce9f1b980
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(windows): define slot session runtime contract
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 9d8de9352a2a14429a6b1039bd91f26683a94ff1
Head OID: 87ccda2a10bd1a26d9cf219bd36b740ce9f1b980
Integrated Result: pending

## Summary

Implementation is recorded in signed commit
87ccda2a10bd1a26d9cf219bd36b740ce9f1b980 on
feat/windows-slot-session-ui. It adds the platform-neutral
`internal/sessionruntime` contract and pure tests, the redacted Windows SYSTEM
WTS probe at `cmd/session-login-probe`, the controlled PowerShell login/re-login
harness at `scripts/probes/windows-session-login-probe.ps1`, and the A-K design
record at `docs/windows-session-runtime-contract.md`.

The selected production boundary is a registered interactive helper using a
controlled local RDP login adapter. Broker JSON remains credential-free; the
service-owned account, Profile, WTS session, desktop, Agent, worker, ownership
journal, singleton owner, readiness, capability, and stop/retry fences are
specified without claiming their native implementations are complete.

## Motivation

Logical slot readiness did not provide one stable contract for real Windows
identity, Profile, WTS, shell, Agent, worker, recovery, or ownership behavior.
The service needs an additive boundary that keeps business state in the account
state machine, makes runtime observations freshness-bound, prevents Session 0
login assumptions, and keeps passwords and raw host details out of Core/UI and
the fixed broker pipe.

## Test Evidence

Passed on the Linux development host:

- `go test ./...`
- `go vet ./...`
- `go test -race ./internal/sessionruntime ./internal/slotwindows ./internal/core ./internal/store`
- `GOOS=windows GOARCH=amd64 go test -exec true -run '^$' ./...`
- `GOOS=windows GOARCH=amd64 go vet ./...`
- `./scripts/validate_policy_manifest.sh`
- `./scripts/validate_quality_profile.sh`
- `./scripts/validate_supply_chain_profile.sh`
- `./scripts/validate_action_pinning.sh`
- `./scripts/validate_repository_shape.sh`
- `./scripts/test_build_contract.sh`
- `git diff --cached --check` before the implementation commit

Pure tests cover SID/session fencing, secret non-serialization and wiping,
nested sensitive-wire rejection, deny-by-default capabilities, readiness
freshness, fixed-pipe ownership, journal validation, and host singleton
validation. The Windows probe cross-compiles; its input accepts only fixed
redacted classifications and its report contains only hashes, IDs, and
readiness facts.

No Windows host or PowerShell runtime was available here. The native probe,
Authenticode trust, local RDP/Credential Manager login, logout and password
rotation, SYSTEM WTS/SID verification, production helper/broker ACL, Agent,
worker, and full cleanup remain pending native acceptance. The existing
`scripts/test_windows_job_pool_smoke.ps1 -LocalRdp` result remains a controlled
smoke fact and is not production broker proof.

## Risk

The contract is additive and intentionally leaves Stop/Cancel/Retry and durable
journal implementations unsupported until their Store operations land. The
controlled harness uses a run-scoped test signer and disposable local account;
it is opt-in and removes the account, Profile, Credential Manager target,
certificate trust, task, sessions, and runtime only when ownership checks pass.
An ownership or cleanup mismatch fails closed and may require operator repair.
Native RDP policy, certificate trust, helper registration, WTS privileges,
Authenticode behavior, and Agent/worker readiness require the controlled
Windows runner before production readiness can be claimed.

## Rollback

Stop any opt-in probe and wait for its owned sessions and cleanup to finish.
Revert signed implementation commit
87ccda2a10bd1a26d9cf219bd36b740ce9f1b980 while preserving existing bbolt
state, Profiles, account credentials, and ownership records. Do not manually
delete a user, Profile, Credential Manager entry, certificate, or session whose
ownership cannot be verified; quarantine it for explicit operator recovery.

## Breaking Change

No existing Core, Matrix, worker, or broker transport is changed. The new
package defines future additive request and projection names; its unsupported
methods must not be presented as implemented service operations. The probe is
explicitly test-only and does not establish production login support.

## Backport Target

None

## Handoff

- Contract: `chuzi.session/v1`; fixed pipe `\\.\pipe\chuzi-session-bootstrap-v1`; ownership journal version 1.
- Production login choice: registered interactive helper plus controlled local
  RDP adapter; Session 0 never launches `mstsc`.
- `SecretLease` has no exported fields or JSON secret and exposes only a wiped
  temporary UTF-16 callback view.
- `ProbeReport.NativePending` remains true until native broker, Agent, worker,
  and Authenticode acceptance is recorded.
- Native follow-up must run `scripts/probes/windows-session-login-probe.ps1`
  with `CHUZI_ENABLE_WINDOWS_SESSION_LOGIN_PROBE=1` and `-AllowTestTrust`, then
  execute the existing full Agent/worker smoke separately.
