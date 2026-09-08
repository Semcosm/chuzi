# CR-0005: implement the account state-machine contract

Base: main
Head or Range: 35afde3c5699d81eb5ecc5c94321ff26c09a449e..22085cac0d6af12a5202b09324ee3dd9b7035a1b
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(account): implement account state-machine contract
Revision: 2
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 35afde3c5699d81eb5ecc5c94321ff26c09a449e
Head OID: 22085cac0d6af12a5202b09324ee3dd9b7035a1b
Integrated Result: pending

## Summary

Implement the pure Go account domain state machine described by the project
documents. Add deterministic event application with strict transition and
request correlation checks, event-idempotency and conflict detection, revisioned
audit records, a configurable exponential-backoff retry policy, and a
deterministic lease acquire/heartbeat/recovery primitive. Expand the state
machine document with the implementation-level contract.

## Motivation

The account state machine is the single source of externally visible business
state, but the repository previously contained only the state diagram. The
queue, browser, credential, storage, and Matrix components need a stable,
testable domain boundary before they are implemented. Deterministic time and
explicit event identity prevent duplicate delivery, stale writes, and lease
recovery from becoming component-specific behavior.

## Test Evidence

Required before integration: run the account package unit tests, `go test ./...`,
all repository policy, quality, supply-chain, Action pinning, repository-shape,
build-contract, adapter, and CR validators, and `git diff --check`. Tests must
cover every documented transition and invalid transition, duplicate and
conflicting events, request/account correlation, revisioned audit output,
retryable and non-retryable failures, exponential-backoff capping, lease
contention, heartbeat ownership, expiry, and restart-style recovery using an
injected time.

## Risk

This change adds only an in-memory/pure domain package and does not persist
state or execute external processes. The state machine exposes the documented
`LOGIN_FAILED -> QUEUED` retry transition but does not create requests or
schedule attempts; retry policy output is available for the later Request
Service/Queue phase to apply. Incorrect transition semantics
could affect all future components, so the document and table-driven tests are
kept in the same change. No credentials, browser profiles, Matrix tokens, or
live accounts are used.

## Rollback

Revert this change through a subsequent UGS CR. Since no storage schema or
runtime deployment is changed, rollback consists of removing the domain package
and restoring the previous state-machine document.

## Breaking Change

None for the current executable service boundary. This introduces the first
internal account domain API and makes the documented transition, event, audit,
retry, and lease rules normative for future components.

## Backport Target

None.
