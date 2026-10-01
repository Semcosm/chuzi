# CR-0084: implement account deletion lifecycle foundations

Base: main
Head or Range: feat/account-deletion-lifecycle
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(account): add account deletion lifecycle foundations
Revision: 2
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 6cfe46c2d7f9a7fdcb6507362ac91c6de5b4f305
Head OID: 0298225e397b3d50c4ff8863d66c9362833b2a77
Integrated Result: pending

## Summary

Implement the first executable increment of the account deletion lifecycle. The
change adds a pure deletion state machine, durable bbolt records and schema v5,
new-request blocking, deletion-aware credential wiping, and service-derived
Profile purge/retain boundaries. It preserves redacted metadata, deterministic
retries, checkpoint idempotency, and fail-closed credential ordering.

Session Runner lease fencing, Matrix/Core/CLI confirmation, unified orchestration,
request tombstones, and Matrix outbox coalescing remain follow-up work; this CR
does not claim those surfaces are fully wired.

## Motivation

`docs/security.md` now records the first deletion safeguards, while the
existing boundaries still lack durable orchestration and safe composition
points for the complete workflow:

- `credential.Service.Revoke` can invalidate a session boundary before wiping
  nonce and ciphertext, but it has no deletion workflow or durable checkpoint.
- `browser.Profiles` derives isolated directories and intentionally retains
  them; cleanup is not yet a policy-controlled operation.
- `store.Store` persists account projections, requests, audits, leases,
  credentials, and Matrix outbox records, but has no tombstone or deletion
  record.
- the Matrix adapter exposes request/status/cancel only, and the service CLI
  exposes credential revoke but not account deletion or confirmation.

This increment supplies the durable domain and boundary primitives; the
follow-up orchestration can compose them without widening unrelated APIs.

## Proposed Contract

### 1. Deletion is an account lifecycle owned by the domain

Add a deletion aggregate under `internal/account` rather than overloading the
existing login request state graph. The current business `account.Status`
remains the last valid request state and remains the source of truth for login
transitions. A deletion lifecycle is an orthogonal, monotonic state machine:

```text
NONE
  -> REQUESTED
  -> STOPPING_SESSIONS
  -> REVOKING_CREDENTIALS
  -> CLEANING_PROFILE
  -> TOMBSTONED

STOPPING_SESSIONS / REVOKING_CREDENTIALS / CLEANING_PROFILE
  -> RETRY_WAIT
  -> the recorded checkpoint

any non-terminal stage -> BLOCKED (permanent failure or operator hold)
BLOCKED -> the recorded checkpoint only through an authorized retry command
TOMBSTONED is terminal and cannot be restored.
```

The deletion record is identified by a caller-supplied `deletion_id` and has
an optimistic `revision`, `requested_at`, `updated_at`, `actor`, `reason_class`,
`attempt`, `next_attempt_at`, `last_failure_class`, and checkpoint booleans
for `sessions_stopped`, `credentials_revoked`, and `profile_action_applied`.
The record also stores the selected Profile policy (`purge` or `retain`) and a
stable redacted account label. It never stores credentials, Profile paths,
worker handles, raw Matrix room IDs, command text, or arbitrary user input.

The initial request is accepted only when the account exists, is not already
tombstoned, and has no earlier deletion in progress. Repeating the same
`deletion_id` and identical request is idempotent; a conflicting reuse is
rejected. Once `REQUESTED` is durably committed, new account requests are
rejected and queued work is cancelled through the existing request boundary.

### 2. Stop and fence every active session first

The Session Runner implements the deletion invalidation boundary used by the
Credential Store:

1. mark the deletion checkpoint as `STOPPING_SESSIONS`;
2. cancel the account's active request and worker using the existing bounded
   `session_cancel`/`shutdown` flow;
3. wait for a terminal worker result, process exit, and lease release;
4. fence stale heartbeats and late worker events with the `deletion_id` plus
   the current lease owner/generation; and
5. set `sessions_stopped` only after the durable store confirms there is no
   active lease or registered session for the account.

If cancellation or lease recovery cannot be confirmed, deletion does not wipe
credentials. The stage enters `RETRY_WAIT` with a transient runtime failure,
or `BLOCKED` when the operator must intervene. A late success/failure event
from a pre-deletion worker is ignored after the deletion fence is committed.

### 3. Revoke and clear credentials after session stop

The Credential Store adds a deletion-aware idempotent revoke operation that
reuses the existing `SessionInvalidator` ordering:

- no revoke write occurs until `sessions_stopped` is true;
- an active record is changed to a revoked metadata record with empty nonce
  and ciphertext, preserving only version, key ID, timestamps, and revoke
  audit metadata;
- a missing record or an already revoked record is treated as an idempotent
  cleared result for deletion; and
- any key, backend, or transaction failure leaves the previous encrypted
  record untouched and schedules a retry.

Credential audits remain metadata-only. The deletion actor, deletion ID,
version, key ID, operation class, and timestamp may be retained; plaintext,
tokens, cookies, key material, and error text never enter the record.

### 4. Profile cleanup is explicit and service-derived

After credentials are cleared, the workflow resolves the Profile path from the
service-owned `browser.Profiles` boundary. Callers cannot submit a filesystem
path. The policy is explicit:

- `purge` (the default) removes the account Profile directory after the
  worker has stopped;
- `retain` keeps the directory for an operator-approved forensic or recovery
  window, but the tombstone records that the Profile is retained and no new
  session may acquire it; and
- a purge failure never causes credential restoration. It records a classified
  filesystem failure, keeps the account inaccessible, and retries cleanup from
  the same checkpoint.

The Profile boundary must reject symlink/path traversal surprises, verify that
the target remains inside the service-derived Profile root, and make cleanup
idempotent when the directory is already absent. Raw paths stay out of logs,
audits, Matrix replies, Core DTOs, and tombstones.

### 5. Tombstones preserve only safe history

Deletion does not physically delete rows that are needed for durable history or
idempotency. On successful completion, the Store writes a tombstone projection
and keeps the following bounded history:

- **Account:** a tombstone with `deletion_id`, redacted account label, last
  business state, deletion timestamps, completion status, and Profile outcome;
  no credential metadata beyond the safe revoke audit remains readable through
  account APIs.
- **Requests:** existing request rows remain queryable only as redacted
  tombstones. New work for the account is rejected. Public results expose the
  request ID, redacted account label, terminal/cancelled state, and a classified
  `account_deleted` reason; they do not expose historical room IDs or actor
  identity.
- **Audits:** immutable domain and credential audit records remain for
  integrity and incident review, but external projections replace account and
  actor identifiers with stable redacted labels and retain only classified
  reasons. No deletion step may append secrets or raw provider errors.
- **Matrix outbox:** already delivered messages remain immutable; pending
  messages for the account are coalesced or rendered as a final redacted
  deletion event with the stable event ID. No message body is persisted. A
  deletion completion notification contains only the redacted account label,
  deletion event ID, terminal status, timestamp, and optional classified
  Profile outcome.

Tombstone reads are idempotent and safe after restart. A tombstoned account
cannot be recreated under the same account ID without a separate, future
account-reuse decision; this CR does not authorize account ID reuse.

### 6. Failure, retry, and rollback semantics

Each stage is committed in a Store transaction with the deletion record,
checkpoint, audit event, and any redacted notification outbox entry. The
workflow uses the existing deterministic retry model: only transient runtime,
lease, backend, or filesystem failures retry automatically with bounded
exponential backoff. Credential, authorization, configuration, malformed
request, and policy failures become `BLOCKED` unless an explicit operator
retry is allowed.

Rollback means restoring the last durable checkpoint after a failed attempt; it
does not restore wiped ciphertext, resurrect a stopped worker, or re-enable a
tombstoned account. Before irreversible credential revocation, an authorized
operator may cancel a still-`REQUESTED` challenge. After session stop or
credential wipe begins, the workflow is forward-only: retries finish cleanup or
leave a redacted `BLOCKED` tombstone requiring explicit intervention.

On restart, the service loads deletion records, reclaims expired claims,
re-validates account/profile/credential invariants, and resumes from the
recorded checkpoint. Duplicate stage execution and duplicate event IDs must be
idempotent; a conflicting deletion event or revision is rejected.

### 7. Matrix authorization and second confirmation

Add two admin-only Matrix commands with exact grammar:

```text
!ugs delete <account-id>
!ugs delete-confirm <deletion-id> DELETE
```

`delete` creates a short-lived challenge only. The reply shows a redacted
account label, challenge/deletion ID, selected default Profile policy, expiry,
and the exact confirmation phrase; it does not mutate the account.
`delete-confirm` must come from the same authorized user and room, must be
within the challenge TTL, and must match the challenge's account, policy, and
confirmation phrase. Regular users are denied even when they can view the
account's request status. Admin cross-room operation requires an explicit
policy grant and still binds confirmation to the initiating actor.

Challenge IDs, deletion IDs, and confirmation outcomes are recorded as
redacted audit metadata. Challenge replay, expiry, room/user mismatch, and
wrong confirmation are safe no-ops with stable `forbidden` or `conflict`
errors.

### 8. CLI and Core API authorization

The CLI and local Core API use the same deletion service; they must not call
Store, Credential, or Profile internals directly. The implementation CR will
add a two-step command/API pair:

- create a deletion challenge for an authenticated local operator;
- confirm using the challenge ID plus an explicit typed account confirmation
  (or an equivalent native-client confirmation gesture);
- expose status and retry for the deletion ID with stable redacted DTOs; and
- reject non-interactive or missing-confirmation invocations by default.

The local Core endpoint remains the OS authorization boundary. Public DTOs
return only deletion state, redacted labels, classified failure, timestamps,
attempt/backoff state, and Profile outcome; they never return account raw IDs,
credential metadata, Profile paths, room IDs, confirmation secrets, or worker
handles.

## Durable Schema and Boundary Changes

This increment advances the bbolt schema from v4 to v5 and adds a repeatable
`account_deletions` bucket. The bucket stores one validated deletion record per
account plus an event/idempotency index. Existing account, request, audit,
credential, lease, and Matrix notification buckets remain so restart recovery
and audit integrity remain possible. The migration writes no secrets, does not
infer deletion state from missing rows, and never silently deletes Profiles.

This increment adds narrow interfaces rather than widening existing ones:

- `account.DeletionSnapshot` and pure event application/validation;
- `store.RequestDeletion`, `AdvanceDeletion`, `GetDeletion`, and deletion lookup;
- `credential.RevokeForDeletion` with an explicit idempotent result;
- `browser.Profiles.Remove`/`Retain` with service-derived path, busy/symlink
  checks, and cleanup policy boundaries.

Session Runner invalidation and deletion-fence checks, Matrix/Core/CLI challenge,
confirm, status, retry operations, request tombstones, and outbox linkage are
deferred to a follow-up implementation increment.

All interfaces must accept caller-provided timestamps and IDs in tests.

## Acceptance Criteria for This Increment

This increment is complete when tests prove the following:

1. The pure deletion state machine validates monotonic checkpoints, retries,
   blocks, tombstones, duplicate events, and conflicting revisions.
2. Store schema v4-to-v5 migration is repeatable; deletion records survive
   restart, block new requests, and reject conflicting IDs or stale events.
3. Credential deletion refuses to wipe until `sessionsStopped` is true, clears
   nonce/ciphertext exactly once, and records only metadata.
4. Profile purge, retain, already-absent, busy, and symlink cases are
   deterministic and never accept caller-supplied paths.
5. Validation and tests preserve redaction and do not persist credentials,
   Profile paths, worker handles, or raw Matrix identifiers.

The follow-up increment will cover active-session cancellation and lease fencing,
unified orchestration, Matrix/Core/CLI challenge confirmation, request tombstones,
and Matrix outbox linkage.

## Test Evidence

This CR was reviewed against `docs/security.md`,
`docs/account-state-machine.md`, `docs/storage.md`, `docs/architecture.md`,
`docs/matrix-api.md`, `cr/CR-0005-account-state-machine.md`,
`cr/CR-0006-storage-recovery.md`, `cr/CR-0009-session-runner.md`,
`cr/CR-0010-credential-store.md`, and `cr/CR-0011-matrix-adapter.md`. The
existing code confirms the proposed ordering points: Credential Store revoke
already requires session invalidation before wiping ciphertext, Session Runner
already owns bounded cancellation and leases, and Profiles already derive
service-controlled paths. The implementation changes only executable boundaries
and tests; no live account data or credentials are used.

Local checks: `go test ./...`, `go vet ./...`, `git diff --check`,
`./scripts/validate_repository_shape.sh`, and `./scripts/test_build_contract.sh`.

## Risk

Deletion is irreversible once credential ciphertext is wiped, so an incomplete
workflow could strand an account with no usable credential or leave Profile
data behind. The checkpointed, forward-only contract limits that risk: session
stop is confirmed before wipe, every stage is idempotent, failures are
classified, and external surfaces expose only tombstones. The implementation
must preserve single-node bbolt ownership and must not claim multi-instance
coordination.

## Rollback

The change adds a durable deletion bucket and irreversible credential wiping
entry point. Before rollout, back up the database and retain compatible external
key material. Revert via a subsequent CR; a failed stage resumes from its last
checkpoint, while a successful revoke never restores ciphertext. Follow-up
orchestration must keep deletion forward-only after session stop.

## Breaking Change

The bbolt schema advances to v5 and adds the `account_deletions` bucket. New
internal deletion APIs and Profile cleanup methods are additive. Existing request,
status, cancel, credential revoke, and notification behavior remains unchanged;
new requests for accounts with a deletion record are rejected.

## Backport Target

none
