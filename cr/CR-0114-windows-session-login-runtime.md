# CR-0114: add the Windows session login runtime coordinator

Base: main
Head or Range: f2989cd
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(windows): add session login runtime coordinator
Revision: 3
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 18746c8aec2686e1ad27c53c858c829f89dacfa6
Head OID: f2989cd3cce9829a0a4a0ffe57557579e899021d
Integrated Result: pending

## Summary

Add a platform-neutral `internal/sessionruntime.Runtime` coordinator for the
frozen Windows session contract. `RuntimeDependencies` wires the existing
`HostPreflight`, `ProfileProvider`, `AccountProvider`, `LoginAdapter`,
`SessionProvider`, `DesktopProvider`, `AgentProvider`, `WorkerProvider`, and
`OwnershipJournal` interfaces. `Runtime.Start` performs the ordered host,
profile, disposable-account, login, independently re-queried WTS, desktop,
Agent, and worker gates. `Runtime.Stop` fences the owned resources and marks
the journal stopped only after WTS disappearance is independently observed.

The coordinator keeps provider errors behind stable redacted `sessionruntime`
codes, serializes same-slot calls, writes intent/commit/checkpoint records,
rejects stale host facts and generation/SID/session changes, and quarantines
unknown ownership after cleanup failure. Secret leases are validated, consumed
only through the existing `LoginAdapter`, and wiped immediately after login;
the runtime never adds a credential DTO, broker field, command line, endpoint,
Profile path, or raw provider error. `StartOptions.Recover` selects the
explicit `AccountProvider.Recover` password rotation path.

The runtime now applies a bounded operation context, exposes a stable retryable
classification, and requires an explicit terminal WTS observation before marking
ownership stopped. `slotwindows.NewSessionRuntimeLoginAdapter` is the secure
bridge from the frozen credential adapter to the coordinator, while
`NewSessionRuntimeWTSProvider` performs the independent Windows WTS fact check
on native builds and fails closed on other platforms. `LoopbackAllocator`
claims installation-configured 127.0.0.x aliases through the owner-supplied
durable journal, detects collisions, and reclaims only exact expired records;
its durable reservation contains a target ID rather than an endpoint.

## Motivation

The contract and Windows provider boundaries existed without a deterministic
runtime owner. Service assembly needs one testable sequence that cannot treat a
login-helper result as a WTS fact, cannot delete a profile while an unrecorded
late session is active, and cannot adopt a different SID or reused session ID.

## Test Evidence

Passed on the Linux development host:

- `go test ./...`
- `go vet ./...`
- `go test -race ./internal/sessionruntime ./internal/slotwindows`
- `GOOS=windows GOARCH=amd64 go test -exec true -run ^$ ./internal/sessionruntime ./internal/slotwindows`
- `GOOS=windows GOARCH=amd64 go vet ./internal/sessionruntime ./internal/slotwindows`
- `./scripts/validate_policy_manifest.sh`
- `./scripts/validate_quality_profile.sh`
- `./scripts/validate_supply_chain_profile.sh`
- `./scripts/validate_action_pinning.sh`
- `./scripts/validate_repository_shape.sh`
- `./scripts/test_build_contract.sh`
- `git diff --check`

Fake-provider tests cover first login and full stop, independent WTS
revalidation, host certificate policy rejection, explicit account recovery,
session-ID reuse without stopping the other session, cancellation after login,
password-buffer zeroization, stop failure quarantine, journal phase ordering,
and redacted stable error paths.
Additional tests cover operation deadlines, ambiguous WTS disappearance,
endpoint-free loopback target collision/reclaim, the credential bridge, and the
retryable error policy.

No Windows runner was available. Real helper registration, local RDP/NLA and
certificate behavior, Credential Manager use, WTS permissions, Profile ACLs,
desktop/shell readiness, Agent/worker heartbeats, Authenticode, and native
cleanup/re-login remain pending acceptance. Linux tests and Windows cross-build
do not establish production login support.

## Risk

The coordinator depends on deployment-owned providers to enforce Windows
identity and OS permission details. A provider that violates the frozen
interfaces could still misbehave; the coordinator fails closed on all
redaction-safe observations it can independently verify. Unknown or failed
cleanup remains quarantined in the ownership journal and may require explicit
operator repair. The implementation does not allocate usernames, loopback
targets, or RDP profiles from request input; the allocator accepts only
installation-owned loopback policy and leaves endpoint resolution inside the
credential boundary.

## Rollback

Stop new runtime calls, drain owned sessions, then revert the implementation
and CR commits while preserving ownership records and Profiles. Do not delete a
user, session, Profile, or credential target whose slot/SID/generation ownership
cannot be verified; leave it quarantined for explicit repair.

## Breaking Change

No existing Core, Matrix, broker transport, provisioner, launcher, service
assembly, or UI path is changed. The exported constructor and runtime methods
are an additive internal boundary and are not wired into production service
assembly by this change.

## Backport Target

None

## Handoff

- Constructor: `sessionruntime.NewRuntime(RuntimeDependencies, RuntimeOptions)`
- Start: `Runtime.Start(ctx, Identity, StartOptions)`; use
  `StartOptions{Recover: true}` only for explicit disposable-account recovery
  and password rotation.
- Stop: `Runtime.Stop(ctx, Identity)`; it refuses `JournalUnknown` ownership and
  requires an exact pool, slot, ordinal, SID, owner, generation, and session
  record before stopping.
- `RuntimeOptions.Now` is a deterministic clock; `Freshness`,
  `CleanupTimeout`, `OperationTimeout`, and the service-derived profile policy
  are deployment settings.
- `slotwindows.NewSessionRuntimeLoginAdapter(SessionLoginAdapter, now)` bridges
  the fixed in-process credential boundary; it copies no password into a wire
  DTO and zeroes the temporary UTF-16 view after the native call.
- `slotwindows.NewSessionRuntimeWTSProvider()` is the native WTS fact provider;
  its `Stop` validates the exact SID/session before logging off and waits for
  WTS disappearance.
- `sessionruntime.NewLoopbackAllocator(TargetJournal, LoopbackAllocatorOptions)`
  assigns only configured loopback aliases. Persist `TargetReservation` through
  the existing owner journal and release it with the exact lease/identity.
- Native integration must keep the selected registered-helper/local-RDP
  adapter behind `LoginAdapter` and independently re-query WTS through
  `SessionProvider`; no password may be added to broker JSON or Core/UI DTOs.
- Native evidence still required: controlled first login and re-login, correct
  helper/service identities and ACLs, real WTS/SID/session checks, desktop and
  Agent/worker readiness, stop-after-disconnect behavior, and ownership-checked
  cleanup on the Windows runner.
