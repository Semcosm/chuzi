# CR-0119: integrate Windows session runtime orchestration and recovery

Base: main
Head or Range: fad5ab1cd9ae2fadf3789f46ef204b3342d57a64
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: Windows session runtime orchestration and recovery
Revision: 9
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 3fbb027a175118e65ef5d645148d516db4ab16ff
Head OID: fad5ab1cd9ae2fadf3789f46ef204b3342d57a64
Integrated Result: pending

## Summary

Wire the durable Core slot-session operations into the Windows native runtime.
Start, Stop, Cancel, and Retry now create revision- and idempotency-fenced bbolt
operations that an independent service controller claims, renews, checkpoints,
and completes. The controller discovers all durable pools, allows independent
slots and pools to progress concurrently, and prevents the ordinary pool
reconciler from becoming a second owner while an explicit operation is active.

The complete entrypoint chain is:

```text
core.Service Start/Stop/Cancel/RetrySlotSession
-> store durable operation/idempotency/revision
-> independent runSlotSessionController background loop
-> windowsAllPoolReconciler.ReconcileSlotSessionOperations
-> fenced claim/renewal/deadline/cancellation watcher
-> per-pool provisioner UpdatePoolRuntime/Provision/Inspect/StopSession
-> managed identity/Profile
-> brokerHelperBootstrapper
-> private nonce-fenced binary credential handoff
-> fixed session broker
-> local RDP adapter/Credential Manager/mstsc
-> independent WTS verification
-> shell/desktop/Agent/worker readiness
-> redacted durable checkpoint/terminal operation
```

The native provisioner persists service-owned identity and runtime checkpoints,
uses the exact SID/session/generation/owner fence after restart, and resumes
retirement through an external cleanup journal after the slot root has been
removed. The fixed broker persists only credential-free protocol ownership and
tombstones in its authenticated operator state root. Credentials cross the
service/broker process boundary only through the fixed private pipe as a bounded
binary frame with a random nonce and exact ownership fence; both sides clear
their UTF-16 buffers.

Stop retains the managed identity, Profile, and ownership tree for a later
Start. Retire is the destructive lifecycle and deletes only exactly owned
resources through the cleanup journal.

Revision 2 closes four deployment blockers found during review. The interactive
broker no longer calls `WTSQueryUserToken`; the LocalSystem service, with the
documented TCB privilege, performs WTS query/logoff through an authenticated
callback bound to the original SID and exact session fence. Query errors are
classified as errors and never as session absence. A cold Profile is created by
`userenv!CreateProfile`, which is valid for the service identity;
`CreateProcessWithLogonW(LOGON_WITH_PROFILE)` remains only in the operator probe
harness.

Broker protocol, secret frames, and the broker ownership journal are version 2 and include
`pool_id`. A persistent LoopbackAllocator target journal prevents ordinal 1 in
two pools from sharing an RDP target. Missing pool identity in an old record is
rejected rather than adopted. Start/Stop recovery removes an old tombstone only
after a new generation commits ownership. Credential Manager deletion, WTS
query, target journal, or tombstone commit failures retain ownership for retry;
they cannot produce a stopped tombstone. Cleanup runs capability revoke,
Profile revoke, Agent/worker stop, exact session stop, WTS absence, and broker
credential/target cleanup before committing stopped.

CR-0119 belongs to Session 07. Session 08 UI work must receive the next unused
CR number when it begins, after fetching main; it must not reuse CR-0119.

Revision 3 fixes the installer lifecycle observed on a clean uninstall followed
by install. A missing Core service is now an idempotent stop state rather than an
installer error; upgrade preparation uses the elevated service-management path,
and successful installation registers the LocalSystem Core service and the
interactive Session Broker task. Empty fixed data directories left by an
interrupted first install are recoverable, while populated or unexpected data
still fails closed.

Revision 4 fixes the first-install path exposed by the Windows installer smoke
log. PrepareToInstall now performs only the idempotent stop before replacing the
payload; backup and ownership checks remain explicit recovery operations. Service
registration accepts the signed component files and runtime directories that the
installer places in the fixed data root before registration. A Session 0 build
agent registers the interactive Broker task without trying to start it, while an
interactive operator session starts it immediately. The installer smoke check
now fails when Inno reports a script exception despite returning exit code zero.

Revision 5 accepts the fixed top-level `chuzi.db` created by the installed Core
when the service registration resource gate validates the data root.

Revision 6 adjusts the first-install resource gate: populated
signed component directories are accepted, while mutable data directories must
still be empty before ownership is established.

Revision 7 aligns the nightly installer validation path. Authenticode payload
validation is now required only for formal stable versions matching `vX.Y.Z`;
nightly and test artifacts use the unsigned validation path and no longer fail
service registration because the production validator is absent. This is not
evidence for the cause of the preceding test-channel registration failure:
test versions were already exempt, and that failed run did not capture the
PowerShell error output.

Revision 8 records service-management stdout/stderr and exit codes in the Inno
log, so a registration failure can be diagnosed without guessing from the
generic dialog. Uninstall removes the fixed Core service and Broker task before
deleting their management payload, while retaining data by default. Core is
stopped before the Broker so service shutdown can finish session and credential
cleanup. Resource-directory probes use File.Open with CreateNew instead of the
nonexistent File.OpenNew API. Historical Broker-service conversion is removed;
there are no released versions to migrate. A hosted Windows test now verifies
the LocalSystem service command, limited interactive task, uninstall resource
removal, preserved ownership/configuration, and reinstall registration. Its
native Actions result remains pending.

Revision 9 isolates the foreground Core smoke data from the installer's fixed
service data root. Actions run 38568670636 captured
`resource_ownership_unknown` at the first-install top-level resource check:
the preceding foreground test had placed CI-only configuration and log files
in that root. Both foreground tests now use independent runner temporary
directories, including the post-install smoke so it preserves the service data
checked by uninstall/reinstall. The production ownership gate is unchanged.
The same log confirmed that Inno returned zero after the service-management
process returned one; the installer now preserves post-install failure in its
custom exit code. Uninstall cleanup starts only after confirmation, so Cancel
leaves registrations intact. Lifecycle assertions also check the Broker task's
exact executable and absence of arguments. Local build-contract and repository
validators pass; a new exact-commit native installer run remains pending.

## Motivation

The Core operation APIs, Windows account/Profile/session providers, fixed
broker, and service lifecycle existed as separate boundaries. They did not form
one production assembly that could recover a claimed operation, interrupt a
blocked login after Stop or Cancel, prove readiness independently of the login
provider, or drain sessions during service shutdown. A restart could also lose
process-local Agent and broker maps while durable slot ownership remained.

This change gives the service one durable owner for each operation and one
native ownership fence for each slot generation. Provider results remain
runtime facts; the durable operation state machine decides the externally
visible outcome. Ambiguous users, Profiles, sessions, claims, cleanup records,
and late results fail closed instead of being adopted by SID or pathname alone.

## Runtime State and Recovery

The bbolt operation state and the native ownership journal have different
roles. Operation states are `requested`, `provisioning`, `retrying`, `ready`,
`stopping`, `cancelling`, `stopped`, `cancelled`, and `failed`. A claim owner and
expiry fence one controller; an expired claim can be recovered, while a late
completion from the old owner is rejected. Retry backoff, retry budget, explicit
deadline, action idempotency, revision, desired state, and bounded terminal
history are durable. Referenced operations are not pruned.

The operation phase enum and corresponding recovery behavior are:

| Phase | Durable fact and recovery behavior |
| --- | --- |
| `environment` | Resolve the durable pool and signed runtime. Retry only failures explicitly classified retryable; a disabled, draining, or missing pool fails closed. |
| `host_preflight` | Retain the phase for host-gate reporting. Native host validation remains inside the provisioner boundary and cannot be replaced by a provider success result. |
| `profile` | The native journal binds slot, ordinal, pool, owner, generation, username, and SID before service-compatible CreateProfile and Profile policy continue. Existing ownership is revalidated; unknown or partial ownership is quarantined. |
| `login` | The helper and broker use a bounded context. The public JSON stays credential-free; the one-shot private secret uses the exact ownership plus nonce fence. |
| `wts` | The provisioner independently queries WTS and atomically records the exact active session ID. SID-only discovery never authorizes adoption or cleanup. |
| `desktop` | The fixed managed desktop and signed shell policy remain service-derived. A restart proceeds only from the exact journaled session. |
| `shell` | Winlogon uses the fixed signed PowerShell shell command from the managed Profile. Context failure enters the same fenced cleanup path. |
| `agent` | Agent startup and shutdown are bound to slot, owner, generation, SID, and session. Process-local state is insufficient after restart. |
| `worker` | Provision is followed by an independent Inspect gate. Agent and worker readiness remain false until both provisioned and inspected summaries pass. |
| `stopping` | Revoke capabilities, revoke Profile access, stop the Agent/worker, stop the exact session, verify WTS absence and broker credential/target cleanup. A cleanup failure remains non-terminal and retryable and quarantines an unleased slot. |
| `stopped` | The native session and access grant are absent. The identity, Profile, and ownership tree remain for Start; terminal operation state is committed only after this proof. |

The native slot ownership phases actually written by this implementation are
`intent`, `profile`, `wts`, `agent`, `worker`, `stopping`, and `stopped`.
`desktop`, `shell`, and `unknown` remain valid journal vocabulary for the wider
runtime contract. Cleanup substeps are written after every destructive boundary,
including `begin`, `capabilities_revoked`, `profile_revoked`, `agent_stopped`,
`session_stopped`, `wts_verified`, `control_plane_revoked`, `profile_deleted`,
`root_deleted`, and `user_deleted`.

### Crash Windows

| Crash window | Restart behavior |
| --- | --- |
| Reserved identity, user absent | The derived username is not authority. If the root and user are both absent, Stop is an idempotent no-op and a new provision may begin. Any orphan root, user, or unreadable record fails ownership validation. |
| User created, ownership commit absent | Failure to commit `ownership.json` first writes the external cleanup journal with the exact username and SID, then attempts the compensating user deletion. A surviving account is not adopted on restart; cleanup resumes or the slot is quarantined. |
| Profile half-created | Provision rollback stops any exactly owned session, removes only resources absent at attempt start, and preserves pre-existing resources. An account without matching durable ownership or a malformed/reparse Profile path fails closed. |
| Login complete, session not journaled | The live attempt stops the just-created session if the WTS checkpoint cannot be committed. After a process crash, an active same-SID session without the exact durable session ID is ambiguous and is preserved for quarantine or explicit repair. |
| Agent created, Ready not published | The native `agent` or `worker` checkpoint authorizes restart inspection. Readiness stays false until the independent Inspect result passes; failure, deadline, Stop, or Cancel enters fenced cleanup before a terminal operation is written. |
| Logoff complete, tombstone absent | The service can prove WTS absence from the exact durable session fence and finish `stopped`. The broker journal rechecks WTS on restart and records or accepts a tombstone only for its exact ownership; it cannot adopt a reused session ID. |
| Cleanup failed, then restart | Stop/Cancel remains `stopping` or `cancelling` without `CompletedAt`; the next controller may reclaim it. Retire resumes only from an exact cleanup journal, including the post-root `root_deleted` step, and never generically deletes unknown ownership. |

### Cancellation Boundaries

The operation watcher polls the durable record outside bbolt transactions,
renews the claim every five seconds, and cancels the provider context when the
claim or generation changes or when Stop/Cancel commits the stopped desired
state. The durable owner/generation check rejects a late provider result even if
the provider ignores cancellation.

| Boundary | Cancellation and cleanup behavior |
| --- | --- |
| Credential | Pending secrets are memory-only, expire quickly, and are consumed once. Cancellation drops only the exact ownership and nonce entry; service, helper, adapter, and frame copies are cleared. No password is written to JSON, bbolt, journal, command line, environment, or log. |
| Profile | Context cancellation stops Profile bootstrap or policy work. Rollback removes only a user/root created by the current attempt; an existing or ambiguous Profile is preserved and isolated. |
| Login | Context bounds helper, private-pipe, broker, Credential Manager, and `mstsc` work. Cancellation closes the active request path, deletes the owned credential/profile staging where possible, and routes any established exact session through Stop. |
| WTS | A provider session ID is never sufficient. The provisioner re-queries WTS and requires exact SID, session ID, and active state. A session established before cancellation is stopped only when that fence is available; otherwise cleanup fails closed. |
| Shell | The shell path and command come only from the signed runtime and managed Profile. Cancellation after login uses the journaled session cleanup and waits for WTS disappearance before `stopped`. |
| Agent | Agent shutdown uses the authenticated control pipe and then terminates/closes the owned process tree. Generation, SID, session, and owner mismatches block cleanup. Capabilities are revoked before Agent/session drain. |

Stop and Cancel remain cleanup intents even after their original deadline. A
deadline or exhausted retry budget is retained as the final failure code, but
the operation becomes terminal only after native cleanup succeeds. An active
queue lease defers Stop to lease recovery; shutdown releases a lease only after
the exact owned Agent has been fenced.

## Shutdown and Upgrade Lifecycle

Graceful service shutdown follows this sequence:

```text
servicehost shutdown hook
-> windowsAllPoolReconciler.Shutdown
-> exact durable lease fence and release
-> StopSession for durable slots
-> provisioner Shutdown for in-memory agents, boot-only sessions,
   and journal-discovered sessions
-> close broker helper
```

If an Agent lease fence fails, the durable lease is retained and that slot is
protected from the later StopSession pass. The provisioner then closes known
Agent trees, boot-only sessions, and exact journal-discovered WTS sessions.
Malformed or unknown ownership is left intact and reported as cleanup failure.
The public `StopSession` takes the stable runtime read lock; its internal
`stopSession` path lets shutdown and lease recovery retain that snapshot without
recursively taking the lock while a runtime update writer is waiting.

An upgrade must use the same servicehost shutdown path before replacing the
signed payload. Durable operations, slot ownership, Profiles, broker journal,
and cleanup journal remain available to the next service instance. A forced
shutdown is observable and does not authorize deletion or claim transfer.

## Test Evidence

The following checks passed on the Linux development host for the original
implementation and Revision 2 working tree; final rebased implementation OIDs
are recorded above:

- `go test -count=1 ./...`
- `go test -count=1 -race ./...`
- `go vet ./...`
- `GOOS=windows GOARCH=amd64 go test -c -o /tmp/slotwindows.test.exe ./internal/slotwindows`
- `GOOS=windows GOARCH=amd64 go test -c -o /tmp/service.test.exe ./cmd/service`
- `GOOS=windows GOARCH=amd64 go test -c -o /tmp/session-broker.test.exe ./cmd/session-broker`
- `./scripts/validate_policy_manifest.sh`
- `./scripts/validate_quality_profile.sh`
- `./scripts/validate_supply_chain_profile.sh`
- `./scripts/validate_action_pinning.sh`
- `./scripts/validate_repository_shape.sh`
- `./scripts/test_build_contract.sh`
- `git diff --check`

`.ugs/document-map.json` is absent, so its optional validator does not apply.

Deterministic coverage includes operation idempotency and revision conflicts,
claim expiry and late-result fencing, deadlines, bounded retry and backoff,
cleanup-before-terminal behavior, operation pruning with referenced-history
retention, pool independence during slow login, readiness held false until
Inspect, non-retryable readiness cleanup, Stop/prewarm races, lease-aware
shutdown, and shutdown behavior when a lease fence fails. Broker tests cover
credential-free JSON, narrow ACL declarations, per-slot concurrency, restart
recovery, unknown-session rejection, session-ID reuse, bounded stop, atomic
credential-free journal replacement, malformed private frames, one-shot secret
consumption, exact nonce/ownership drop, and password-buffer clearing. Revision 2
adds cross-pool ordinal-1 target isolation, persistent reservation recovery,
service-mediated WTS callbacks with cancellation, query-vs-absence
classification, cleanup failure without tombstone, new-generation tombstone
replacement, failed ownership commit with failed compensation recovery, and
ordered cleanup with legacy checkpoint recovery.

The Windows runtime test binaries compiled successfully. Hosted Windows Actions
run 38568670636 passed Core named-pipe tests, foreground Core lifecycle smoke,
job-pool preflight, Slint layout checks, and all four target package builds. Its
installed-client smoke failed during service registration with the diagnostic
recorded in Revision 9, so uninstall/reinstall did not execute. The corrected
installer lifecycle still needs a successful native run. Production Broker
login, Credential Manager cleanup, WTS transitions, cold Profile provisioning,
Agent/worker readiness, service restart/recovery, and multi-pool sessions remain
unverified on the user's Windows installation; hosted and compile-only evidence
does not replace that acceptance.

### Sessions 09 and 10 Test Hooks

Session 09 can exercise deterministic recovery without live credentials by
using the store clock, claim owner/TTL, provisioner factory, injected
SessionBootstrapper, readiness gate, cleanup failure, lease-fence failure, and
file broker-journal seams. These hooks cover the seven crash windows as
repeatable process/store restart tests and do not add production fault switches.

Session 10 retains the existing native acceptance entrypoints:
`scripts/test_windows_job_pool_smoke.ps1 -LocalRdp` for the controlled diagnostic
path and `scripts/probes/windows-session-login-probe.ps1` for first login, logout, and
re-login. Production acceptance must additionally run the installed fixed
broker/helper path without the LocalRdp shortcut and prove cold Profile, private
pipe ACLs and caller identity, Credential Manager cleanup, WTS/SID/session
fences, shell/desktop readiness, Agent/worker heartbeat, Stop, service restart,
shutdown, retry, and Retire cleanup on a disposable Windows installation.

## Risk

The main remaining risk is unexecuted native Windows behavior. Local RDP and
Credential Manager policy, helper task identity, named-pipe DACLs, Winlogon
Profile timing, WTS reuse/disconnect behavior, Agent shutdown, file ACL and
reparse checks, and service/upgrade interruption can differ from cross-compiled
and fake-provider tests. Until native acceptance passes, deployment must report
the runtime unavailable and must not claim production session support.

Cleanup deliberately favors preservation over availability. An ambiguous or
malformed record, active same-SID session without an exact session ID, failed
capability revoke, or failed user/Profile deletion can leave a slot quarantined
for explicit repair. This may reduce pool capacity, but prevents adoption or
deletion of resources whose ownership is not proven.

The broker state root is derived from the authenticated operator SID under the
Windows temporary directory. Deployment ACL or atomic-replace failure makes the
session unavailable. The broker journal is not a substitute for the service
slot journal or bbolt operation record.

## Rollback

Drain scheduler admission and invoke the signed service shutdown path. Preserve
the bbolt store, `job-slots` ownership records, cleanup journal, Profiles, and
broker state root. Confirm that exact leased Agents and owned sessions are
stopped before restoring the previous signed payload. Do not delete an account,
Profile, credential target, session, or slot root whose slot, SID, generation,
owner, session ID, and journal state cannot be verified.

Revert the implementation and this CR through a reviewed change if source
rollback is required. Retained non-terminal operations and ownership records
must be resolved by the compatible recovery implementation or explicit
operator repair; they must not be discarded to make the old binary start.

## Breaking Change

There is no Core wire-format, database-version, or configuration-key break. The
private broker JSON, secret frame, and ownership journal move to version 2 with
a required pool fence. The fixed pipe names keep their v1 suffix. Broker v1
records without `pool_id` fail closed; deployments must complete old-version
Stop, preserve the old journals, and explicitly repair incompatible state
before enabling the new payload. Automatic adoption or deletion is forbidden. On
Windows with the job-user pool enabled, existing Start, Stop, Cancel, and Retry
operations now drive the assembled native runtime and shutdown drains owned
sessions. Deployments therefore need the documented fixed helper registration,
ACLs, privileges, and signed payload before enabling that mode.

## Backport Target

None.
