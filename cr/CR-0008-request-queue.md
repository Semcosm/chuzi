# CR-0008: implement deterministic request service and queue scheduling

Base: main
Head or Range: pending
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(queue): implement deterministic request service and scheduling
Revision: 2
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: a131dcd87e00cd4d10c30cf876262d8700688f91
Head OID: a131dcd87e00cd4d10c30cf876262d8700688f91
Integrated Result: pending

## Summary

Implement the first durable request-service and queue-scheduling slice. The
slice adds a versioned request scheduling projection, atomic request submission
and queue claim operations, deterministic FIFO scheduling, cancellation,
timeouts, bounded transient retries, and lease recovery. It uses a fake runner
in tests and does not add credentials, Matrix integration, or a real browser.

## Motivation

The account state machine and single-node storage foundation are integrated,
but no component currently turns an accepted request into a durable queued
operation. A request can be created separately from its first state event, and
the store has no deterministic way to enumerate or atomically claim queued
work. This change supplies the next roadmap boundary while keeping the account
state machine as the only business-state authority.

## Test Evidence

Go 1.25.14 verification passed `gofmt`, `go test ./...`, `go vet ./...`, and
the focused `go test -race ./internal/queue ./internal/store` run. Tests cover
idempotent submission and claim, CreatedAt FIFO with delayed retries, account
and global concurrency limits, atomic cancellation, lease expiry and
replacement races, runner timeout/expiry handling, duplicate events, and
restart recovery. Repository policy, quality, supply-chain, Action pinning,
repository-shape, build-contract, CR, and `git diff --check` validators pass.
The local restricted shell cannot complete the Node worker child-process
stdio handshake; `npm --prefix browser-worker test` remains a required GitHub
Actions check. No live accounts, credentials, Matrix tokens, or browser
downloads are used.

## Risk

The request projection and schema migration become the durable contract used by
future session and Matrix components. Scheduling metadata is kept separate
from account business-state legality; every externally visible transition is
still applied by `internal/account`. The MVP supports one request service and
account/global concurrency limits; service-specific limits and external
adapters remain deferred.

## Rollback

Stop the service before rollback, preserve any database and backup files, and
revert through a subsequent CR. A database created with the new schema must be
restored from a compatible backup or migrated by a later explicit migration;
the service must not silently recreate it.

## Breaking Change

The storage request projection gains scheduling metadata and the schema version
advances. Existing v1 databases are upgraded by the repeatable migration before
being opened. No current executable workflow gains live account behavior.

## Backport Target

None.
