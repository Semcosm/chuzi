# CR-0005: implement the account state-machine contract

Base: main
Head or Range: 35afde3c5699d81eb5ecc5c94321ff26c09a449e..87bcd9b3aa57326c45147f9f6ce0474bc421ecb9
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(account): implement account state-machine contract
Revision: 3
Status: integrated
Decision: accepted
Policy Version: v0.3
Base OID: 87bcd9b3aa57326c45147f9f6ce0474bc421ecb9
Head OID: 87bcd9b3aa57326c45147f9f6ce0474bc421ecb9
Integrated Result: main@87bcd9b3aa57326c45147f9f6ce0474bc421ecb9

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

Local Go 1.25.14 verification passed `gofmt -d internal/account/*.go`,
`go test ./...`, and `go vet ./...`; Node 22 verification passed
`npm --prefix browser-worker test`. Repository policy, quality, supply-chain,
Action pinning, repository-shape, build-contract, adapter, CR, signer-role, and
`git diff --check` validators also passed. PR #9 passed `ugs-validate` run
`34222195790` and `chuzi-build` run `34222195741`, including
`windows-amd64`, `linux-amd64`, `linux-arm64`, and `darwin-arm64`. GitHub's
rebase integration produced `main@87bcd9b3aa57326c45147f9f6ce0474bc421ecb9`;
the first post-merge `ugs-validate` run `34222445959` correctly rejected this
pending record because it still named the topic SHA, while main
`chuzi-build` run `34222446079` passed. This closure revision binds the record
to the actual integrated main result.

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
