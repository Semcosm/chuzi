# CR-0042: production runtime validation slice

Base: main
Head or Range: 1a0df921685acb5a10da36799589f770742f1454
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: test(ops): add production runtime validation slice
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 1a0df921685acb5a10da36799589f770742f1454
Head OID: 1a0df921685acb5a10da36799589f770742f1454
Integrated Result: pending

## Summary

Add a repeatable phase-four runtime verification suite. The local integration
tests exercise a protocol-shaped Matrix test homeserver, durable sync/send and
outbox recovery across a store restart, credential injection/rotation/revocation,
worker timeout/crash handling, expired lease recovery, backup corruption
rejection, health status, metrics, audit, and log redaction. A separately
opt-in test drives the same Gateway and Notifier against a disposable real
Matrix homeserver when controlled test credentials are supplied.

The service maintenance boundary also exposes redaction-safe credential rotate
and revoke commands. The environment keyring can retain a bounded historical
key map during a controlled rotation window; key material remains outside the
database and ordinary configuration.

## Motivation

The implemented runtime boundaries previously had isolated unit tests but no
single executable evidence for restart and recovery behavior. Production
operators also had no command-line path to complete the credential lifecycle
after injection. This change closes those verification gaps without connecting
CI to real accounts, production Matrix, or production credentials.

## Test Evidence

The following pass locally:

- `go test -count=1 ./...`
- `go vet ./...`
- `scripts/test_runtime.sh`
- `git diff --check`

The controlled Matrix test was also run successfully against a disposable
local Synapse 1.161 homeserver on `127.0.0.1`, using separately registered
one-shot bot and actor users and a temporary room. It verified bot `whoami`,
actor command send, bot sync, gateway handling, store restart, and notifier
delivery. The test is skipped unless all `CHUZI_MATRIX_TEST_*` variables are
explicitly supplied together with `CHUZI_RUN_CONTROLLED_MATRIX=1`; CI runs
only the deterministic fake-homeserver tests and the existing local
worker/test-page checks.

## Risk

The new rotate/revoke flags mutate encrypted credential metadata and are
intended for a stopped-service maintenance window. Revoke still fails closed
when an optional session invalidator reports failure. Historical key material
is read only from a bounded environment JSON map and is never logged or
persisted. The live Matrix test can send one disposable message in its
configured room and must be supplied only a controlled test account/token.

## Rollback

Disable the runtime verification script and revert the maintenance flags and
historical-key adapter. Existing credential records remain readable with the
current key; no schema migration is introduced.

## Breaking Change

None to the storage, Matrix, Worker, or account-state protocols. New CLI flags
and the optional `CHUZI_CREDENTIAL_KEYS` environment variable are additive.

## Backport Target

none
