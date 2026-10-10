# Windows session runtime contract

Status: contract selected on 2026-10-10. The Linux checkout proves the
platform-neutral package and cross-compiles the probe. Native Windows login,
WTS, broker, desktop, Authenticode, and Agent acceptance are pending until the
controlled Windows runner executes the probe and the existing native smoke.

This document fixes the boundary for the route:

~~~text
installer UI -> launcher/core -> registered login helper -> WTS session
  -> user Profile and signed shell -> service-owned desktop -> Agent ready
  -> browser worker capability -> cancel/stop/recovery
~~~

The source baseline inspected before this contract is commit
1705e425d1234b355079a3c6bec539dd4b22cf46. The current checkout is
feat/windows-slot-session-ui with a clean tree before this change. Existing
logical slot operations, fixed broker JSON, provisioner ownership checks, and
scripts/test_windows_job_pool_smoke.ps1 -LocalRdp are inputs to this contract;
the LocalRdp result is not installed production acceptance.

## Decisions

### A. Supported host and capacity

The supported production host is Windows 11 Pro/Enterprise 23H2 or later,
Windows Server 2022/2025 with Desktop Experience, amd64, with current
security updates. Home, Server Core, domain-controller mode, nested or
unsupported RDP patching, and license bypass are unsupported. The installer
preflight records edition/build class and rejects unsupported editions.

A host has one Chuzi installation and one service-owned single-node bbolt
writer. Capacity is the lower of configured desired slots, host memory/CPU
policy, and the explicit per-host maximum of 32 slots. A deployment may choose a
smaller limit. There is no multi-host or multi-writer claim. Existing operator
sessions are never evicted or reused as a slot.

RDP is optional for non-interactive operation but required by the selected login
scheme. TermService, RPCSS, ProfSvc, active firewall policy, and the local
Remote Desktop Users right must be present. NLA remains enabled; the probe
requires CredSSP/NLA and the standard security negotiation. The RDP server
certificate must chain to the host's configured trust policy. No automatic
certificate acceptance, trust bypass, RDP patch, or weakened security level is
allowed. The test harness may install a run-scoped test signer only when the
operator passes AllowTestTrust; it removes that signer in cleanup.

The interactive helper is a deployment prerequisite. It runs in the
operator's interactive session, registers its process SID/session with the
service, and is allowed to request only the fixed broker operation. It cannot
choose a pipe name, user, Profile, command, endpoint, or executable.

Native status: host edition/capacity policy is documented but not executed on
this Linux host; RDP/NLA/certificate and helper registration are pending native
preflight.

### B. Process ownership and singleton

The service is the sole owner of durable Core state, account lifecycle,
Profile policy, WTS verification, desktop, Agent, worker, stop, and cleanup.
The Launcher/UI is a caller and receives only redacted DTOs. The login helper
owns the interactive user action and short-lived login handoff; it does not own
Store or Profile data. The fixed broker listener owns only broker protocol
validation and delegates to the service's registered login adapter. The
managed slot user owns its interactive shell/desktop process but has no access
to bbolt, credentials, other Profiles, the install tree beyond read/execute,
or the broker pipe.

Expected process identities and rights:

| Process | Identity | Rights and accessible objects |
| --- | --- | --- |
| Launcher/UI | logged-in operator | launcher lock and Core DTOs only; no Store, pipe, Profile, or secret |
| Core/service | LocalSystem or dedicated service SID with WTS/TCB rights | one bbolt writer, fixed broker listener, service runtime, slot roots, credential boundary |
| Login helper | registered operator SID/session | interactive RDP request and helper registration only; no database writer |
| Broker listener | broker SID, ACL plus service SID | fixed pipe protocol; no secret or arbitrary command |
| Managed shell/Agent | disposable non-admin local SID | its SID Profile, derived desktop, Agent pipe, assigned Job Object; no Core DB |
| Browser worker | managed slot SID | worker capability and granted account Profile only; no password or host control |

Installation acquires an exclusive owner record at the service data root and
opens the bbolt file once. The record contains owner ID, service SID, broker SID,
database hash, fixed pipe name, lease ID, and expiry. The fixed pipe ACL allows
only the registered service SID and broker SID; Everyone, Users, Remote Desktop
Users, and arbitrary pipe names are rejected. A second install fails closed with
owner-conflict. Upgrade/migration must stop the old owner, verify its process is
gone, acquire the same owner record, and keep the database writer exclusive.
Uninstall keeps the database, ownership journal, and Profiles by default;
explicit purge requires a separate operator action and ownership proof.

Native status: existing broker ACL/protocol and bbolt single-writer behavior
are covered by package tests; service/helper identity and installer singleton
acceptance remain pending.

### C. Login scheme

The selected production scheme is a registered interactive helper driving a
controlled local RDP login adapter. The service does not start mstsc from
Session 0, does not assume that a Session 0 process creates an interactive
session, and does not use runas, tscon, CreateProcessAsUser, or a fabricated
pipe response as a login substitute. The helper is registered with an
OS-authenticated SID/session binding; the service rejects helper restart until
it re-registers and the identity is checked again. User logout invalidates the
registration and the next login requires a fresh registration. Helper crash
causes the operation to fail or retry under its deadline; it never makes an
old session ready.

The adapter creates or reuses only the service-derived disposable slot user,
then the service re-queries WTS by SID and requires the expected active session
ID. A provider response is not a fact. The fixed broker request carries only
version, operation, slot, ordinal, generation, SID, owner, and stop session ID.
Start success requires nonzero active session; stop success requires state
= stopped. Unknown ownership, changed SID/session, missing broker, invalid
response, or timeout returns session_unavailable/session_changed and fails
closed.

Native status: the repository's fixed broker state machine and protocol tests
pass; the controlled login adapter and helper registration are unverified.

### D. Credential ownership

The service-owned account provider owns disposable-user creation and local
password generation. The selected login adapter is the only consumer of a
SecretLease. SecretLease has no exported password field and marshals as an
empty JSON object. WithPassword exposes a temporary UTF-16 copy and clears it
after the callback; Wipe clears the retained buffer. Core/UI DTOs, broker JSON,
logs, Matrix events, environment variables, command lines, and ownership.json
never contain username, password, endpoint, Profile path, or certificate
material.

If a deployment cannot keep login in the same trusted process, it must use an
OS-authenticated private IPC channel with both endpoint SIDs checked, a
short-lived request nonce, bounded memory buffers, and zeroization on both
sides. Adding a password field to broker JSON is forbidden.

Login proof (the service's SID/WTS/Shell/Agent fact) and Workspace (the UI's
interactive presentation) are different capabilities. DenyAuthorizer is the
default and returns capability_denied; an implementation cannot infer a
Workspace capability from a login proof.

### E. Re-login and crash recovery

The normal path keeps the disposable local password only for first login and
then releases it. On crash or service restart, the service does not read a
password from ownership.json or ordinary configuration and never retries an
empty password. Recovery uses AccountProvider.Recover to rotate a one-time
local user password, writes the new value only to the OS-authenticated login
adapter boundary, and clears it after use. It never rotates a business account
credential.

A crash window with an intent or committed journal record is unknown
ownership. The service first fences any recorded session/Agent, verifies the
SID and generation, then either completes owned cleanup or leaves the record
quarantined for operator recovery. It never silently adopts a session from a
previous broker process.

### F. Durable ownership journal and fences

The single service process is the only writer of the durable OwnershipJournal
and bbolt ownership projection. The OS journal is service-derived at
job-slots/<slot-id>/ownership.json (or the platform's equivalent protected
store); the path is not a Core input. Journal phases are intent, committed,
checkpoint, stopped, and unknown. Every OS side effect follows:

1. write and fsync intent with pool/slot/ordinal/SID/generation/owner/revision;
2. perform the side effect;
3. re-query the side effect (WTS SID/session, desktop, shell, Agent heartbeat);
4. commit/checkpoint the observed IDs and generation;
5. mark stopped only after stop and cleanup are confirmed.

The generation and revision fence every broker, Agent, lease, and worker
operation. Unknown or malformed ownership is fail closed; it is never deleted
by a generic cleanup pass. A human must inspect and explicitly repair it. The
broker/helper never opens the bbolt file or writes the journal.

Native status: the new OwnershipRecord/OwnershipJournal/HostOwner interfaces
compile and have pure validation coverage; durable implementation and restart
recovery remain pending.

### G. Start, Stop, Cancel, Retry, and pool interaction

StartSlotSession creates a durable operation with a caller idempotency key,
expected revision, attempt, and deadline. It does not synchronously promise
ready. The operation state is independent from runtime phase: requested,
provisioning, stopping, cancelling, retrying, ready, stopped, failed, or
cancelled. Pool reconcile may prewarm slots, but a manual Stop records a
stop intent and moves the slot to draining/quarantined so the next
desired_slots reconcile cannot start it until Resume creates a new generation.

Stop wins over a concurrent Start when its durable generation fence commits
first. A committed ready result wins over a later Cancel; a Cancel that commits
before ready wins and cleanup is still awaited. Cancel is terminal only after
the operation has recorded cancellation and the service has fenced the Agent;
cleanup failure is reported as cleanup_failed and keeps the resource
quarantined. Transport context timeout requests cancellation but does not
claim that the background operation was cancelled.

Retry is an explicit operation with a new idempotency key and incremented
attempt. Transport retries reuse the original key. Retrying an operation with
a stale generation, released lease, unknown ownership, or expired deadline
fails closed. Leased slots are never stopped by prewarm reconciliation;
draining slots accept no new work; quarantined slots require explicit recovery;
deleted slots cannot be revived.

Current code has durable StartSlotSession and reconcile projection only. The
new StopSlotSession/CancelSlotSession/RetrySlotSession signatures are the
selected contract and remain unsupported until their Store operations are
implemented.

### H. API and errors

The platform-neutral package is internal/sessionruntime, API version
chuzi.session/v1. Its selected request names are StartRequest, StopRequest,
CancelRequest, RetryRequest, and its projection names are Operation, Status,
ReadinessCheck, and ProbeReport. The service's existing public Core names
remain StartSlotSessionRequest and SlotSessionOperation until an additive
transport revision is approved.

Every mutating request has actor, idempotency_key, expected revision where
applicable, attempt where applicable, and deadline. An idempotent replay has
the same operation ID and idempotent=true. Retry always has a new key. Stable
error codes are invalid_argument, unsupported, unauthorized,
ownership_unknown, ownership_mismatch, stale_generation,
session_unavailable, session_changed, deadline_exceeded, cancelled, conflict,
already_started, cleanup_failed, and readiness_stale. Native error text never
crosses the boundary.

### I. Readiness layers

Readiness is a vector of timestamped checks, not one durable boolean:

| Layer | Fact required | Freshness/owner |
| --- | --- | --- |
| environment | signed package, digest, signer, enabled and healthy | service environment manager |
| host_preflight | supported edition, capacity, RPC/ProfSvc/RDP/NLA/cert policy | host adapter, bounded TTL |
| profile | SID-derived Profile exists, ACL and signed shell policy match | service Profile provider |
| wts | active WTS session's SID matches expected SID and generation | service WTS query, short TTL |
| shell | exact AllSigned session-shell readiness event | target SID/session |
| agent | Agent handshake, generation fence, heartbeat, desktop | service Agent provider |
| worker | selected browser capability and heartbeat | Agent/worker provider |

A check is ready only before FreshUntil; an old ready projection becomes stale
and cannot keep a slot schedulable forever. The existing logical ready count is
capacity test data and never substitutes for WTS, desktop, Agent, or worker
readiness.

### J. Shutdown, crash, rollback, upgrade, uninstall

Graceful shutdown stops boot-only/prewarm sessions first, fences leased work,
stops workers and Agent Job Objects, stops the owned WTS session through the
broker, waits for Find(SID) to disappear, and only then marks ownership stopped.
A host crash leaves the journal and database; the next owner performs
fail-closed inspection and never deletes an object it cannot match by owner,
SID, slot, and generation. Partial rollback leaves the old signed environment
and old generation available until the new generation passes all readiness
gates.

Upgrade is in-place under the owner lock and fixed pipe name. It preserves
account business credentials, Profiles, bbolt, trust metadata, and ownership
journal. It stops boot-only sessions, drains active leases, and rejects a
second writer. Uninstall stops service-owned sessions and Agents and removes
only objects with a matching ownership marker; it preserves state and Profiles
by default. Purge is an explicit, separately authorized operation.

### K. Capability authorizer

A login proof capability proves only the selected SID/session/generation
readiness. A Workspace capability authorizes presentation/input separately and
is short-lived, actor-bound, generation-bound, revision-bound, and revocable
when Stop/Cancel/lease expiry/quarantine occurs. DenyAuthorizer remains the
production default until a deployment authorizer is reviewed. Neither
capability contains endpoint, username, password, Profile path, certificate,
raw pixels, or arbitrary input commands.

## Process handoff

The fixed host executable and arguments are:

~~~text
chuzi-core.exe             # service; installation-owned configuration only
chuzi-session-broker.exe  # deployment helper; fixed pipe, no arguments from Core
chuzi-user-agent.exe      # service-owned, fixed slot/runtime selection
node.exe worker.mjs       # selected signed environment entrypoint only
~~~

The existing service broker names remain the implementation adapter:
slotwindows.SessionBrokerRequest, SessionBrokerResponse,
SessionBrokerPipeName, NewRunnerSessionBootstrapper, and
SessionLoginAdapter. The new package's BrokerPipeName and API version are the
platform-neutral source of truth for future additive migration.

Installation registers the service executable with the Windows Service Control
Manager, registers the helper only for an interactive operator session, and
creates the owner record and fixed broker ACL before accepting Core calls. No
installer option can change the pipe name, database path, user prefix, desktop,
shell command, or worker entrypoint.

## Probe harness

The independent harness is scripts/probes/windows-session-login-probe.ps1 and
the native SYSTEM probe is cmd/session-login-probe. It performs host preflight,
creates a random disposable non-admin user, initializes its Profile through
CreateProcessWithLogonW(LOGON_WITH_PROFILE), applies the fixed signed
PowerShell shell, stores a short-lived RDP credential in Credential Manager,
and starts mstsc with a run-scoped loopback profile. The profile keeps NLA and
standard certificate/security checks. It waits for an active WTS session and
SID-scoped shell readiness, runs the native probe as SYSTEM through a temporary
Task Scheduler task, logs off, verifies the old session disappears, rotates
the local password, and repeats the automatic login. Cleanup removes the
credential target, user only when its run marker matches, test trust, task,
profile/runtime root, and sessions. It never prints the password or raw
username/SID/path.

The harness is not proof that the production broker adapter exists. It is a
controlled login proof for the chosen identity combination and a regression
probe for cold Profile, first login, logout, and re-login. Native Agent/browser
checks remain pending and must still run scripts/test_windows_job_pool_smoke.ps1
without the LocalRdp handoff shortcut.
Credential Manager writes refuse an occupied target and cleanup verifies the
run-owned username before deletion; an ownership change fails closed.
The private run root grants the disposable SID traversal only, and grants that
SID read/execute on the signed shell file; manifests and reports remain
inaccessible to the managed user.

Validation-only command (no user/session is created):

~~~powershell
$env:CHUZI_SESSION_PROBE_BINARY = 'C:\\Program Files\\Chuzi\\chuzi-session-login-probe.exe'
PowerShell -NoProfile -File scripts/probes/windows-session-login-probe.ps1 -ValidateOnly
~~~

Opt-in native command (controlled Windows runner only):

~~~powershell
$env:CHUZI_ENABLE_WINDOWS_SESSION_LOGIN_PROBE = '1'
PowerShell -NoProfile -File scripts/probes/windows-session-login-probe.ps1 -AllowTestTrust
~~~

The probe's input fixture shape is
scripts/probes/windows-session-login-input.example.json. Its output is a
redacted ProbeReport with schema chuzi.session-probe/v1, privilege class, SID
hash, session ID, old-session result, shell/Agent/worker checkpoints, and
NativePending=true until the remaining native acceptance is complete.

## Native evidence ledger

| Evidence | State | What it proves |
| --- | --- | --- |
| internal/sessionruntime compile and pure tests | passed on Linux | DTOs, fences, secret non-serialization, deny authorizer, readiness freshness |
| GOOS=windows probe compile | passed on Linux | WTS/SYSTEM harness compiles for target |
| existing broker protocol/state tests | passed on Linux | fixed wire, ownership, generation, duplicate/stop rules |
| test_windows_job_pool_smoke.ps1 -LocalRdp | previously operator-verified | controlled smoke fact only; not production broker proof |
| windows-session-login-probe.ps1 native run | pending | automatic first login and re-login with cold Profile |
| broker ACL + production adapter | pending | service/helper identity and fixed-pipe login handoff |
| full Agent/worker native smoke | pending | desktop, Agent heartbeat, browser capability and cleanup |

No item in the pending rows may be represented as production ready.
