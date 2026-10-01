# CR-0086: persist the Matrix sync cursor across restarts

Base: main
Head or Range: ec2690a48d0893abf4c21fb2cc8e7721b4d04771
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(matrix): persist sync cursor and replay incomplete batches
Revision: 3
Status: integrated
Decision: accepted
Policy Version: v0.3
Base OID: ec2690a48d0893abf4c21fb2cc8e7721b4d04771
Head OID: ec2690a48d0893abf4c21fb2cc8e7721b4d04771
Integrated Result: main@ec2690a48d0893abf4c21fb2cc8e7721b4d04771

## Summary

Persist the Matrix `/sync` `next_batch` cursor in the existing bbolt Store and
advance it only after a complete sync batch has been handled successfully.
Load the cursor during gateway startup, replay an incomplete batch after a
restart or send failure, and deduplicate repeated event IDs within a batch.
Advance the schema to v5 with a dedicated `matrix_sync_cursors` bucket and
validate stored cursor records during database integrity checks.

## Motivation

The sync gateway previously started from an empty cursor on every process start.
A disconnect after handling a command but before its reply was sent could lose
the batch position or force an operator to reset the integration manually. A
durable cursor plus stable event-derived request and reply IDs keeps restart and
retry behavior deterministic while preserving the Matrix adapter and Store
boundaries.

## Test Evidence

Covered by `go test ./...`, `go vet ./...`, `./scripts/test_runtime.sh`,
`./scripts/test_build_contract.sh`, all policy and repository validators,
`git diff --check`, schema v4-to-v5 migration tests, store restart and cursor
validation tests, invalid `next_batch` protocol tests, duplicate event-ID
handling, and send-failure replay tests. Tests use local httptest servers, fake
accounts, and temporary bbolt databases only.

## Risk

The bbolt schema advances to v5 and adds one singleton cursor record. A failed
reply send leaves the previous cursor in place, so the next run can replay the
whole batch. Adapter request idempotency, stable reply transaction IDs, and the
notification outbox prevent duplicate business effects or downstream messages.
Malformed Matrix events and cursors are ignored or rejected at the protocol
boundary; access tokens and raw Matrix payloads remain outside durable cursor
state.

## Rollback

Stop the service and preserve the bbolt database before reverting through a
subsequent CR. The v5 binary must remain available while any database contains
the `matrix_sync_cursors` bucket; reverting to a v4-only binary is not a safe
database rollback. Clearing the singleton cursor intentionally starts a fresh
Matrix sync and is supported by the Store API.

## Breaking Change

The bbolt schema advances from v4 to v5. `matrix.NewGateway` now requires a
durable `SyncCursorStore`, and production/runtime call sites pass the existing
Store instance. No Matrix command grammar or external protocol changes.

## Backport Target

none
