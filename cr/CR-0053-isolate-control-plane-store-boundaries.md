# CR-0053: retire Wry and isolate control-plane boundaries

Base: main
Head or Range: 0ecaadfa791ffa82a6db687e8a8856f02030e5e5..ad03ccc199d9a36d6e954d6acd98c36d7e451132
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: refactor: retire Wry and isolate control-plane boundaries
Revision: 3
Status: integrated
Decision: accepted
Policy Version: v0.3
Base OID: ac82dce85add95fae54821311fd683a51889b73f
Head OID: ac82dce85add95fae54821311fd683a51889b73f
Integrated Result: main@ac82dce85add95fae54821311fd683a51889b73f

## Summary

Retire the abandoned Rust/Wry browser runtime and keep native clients on the
Core API boundary. Remove its source, backend selection, build/package jobs,
release component, and smoke path. Tighten the remaining control-plane
boundaries by moving notification filtering, ordering, and pagination into the
durable store; pushing audit request filtering into the store; and exposing only
the cancellation fact required by a running browser session. Replace concrete
store dependencies in request, queue, and Matrix notifier services with focused
capability interfaces, and split service supervision, endpoint wiring, and
maintenance operations into separate modules.

## Motivation

The native-client direction is a Core API boundary and no longer needs a
desktop WebView runtime. The old Rust/Wry path made the CLI, build matrix,
release artifacts, and architecture documents claim a supported backend that
was not part of the design. Separately, orchestration packages depended directly
on the full bbolt Store or performed durable-query work in memory. Browser
cancellation was discovered by type assertion against the lease store, and the
service entry point combined runtime assembly, endpoint lifecycle, scheduling,
and maintenance commands. These couplings made focused tests and alternate
implementations harder while allowing unrelated persistence capabilities to
cross package boundaries.

## Test Evidence

The implementation was verified with:

`go test ./...`

`go vet ./...`

`go test -race ./internal/store ./internal/core ./internal/request ./internal/queue ./internal/matrix ./internal/browser ./internal/coretransport`

`./scripts/validate_policy_manifest.sh`

`./scripts/validate_quality_profile.sh`

`./scripts/validate_supply_chain_profile.sh`

`./scripts/validate_action_pinning.sh`

`./scripts/validate_repository_shape.sh`

`./scripts/test_build_contract.sh`

`git diff --check`

The Wry retirement also passed the nightly package and artifact contract
validators before this rebase.

No live account, external credential, or production Matrix service was used.

## Risk

The Rust/Wry backend, `-browser-runtime` flag, runtime artifacts, and
`desktop-runtime` release component are removed; callers must use the Node
deferred or explicit headless-CDP backend. The Core API notification query gains
an additive `offset` field and now rejects offsets above the bounded query
maximum. Store-backed callers must implement the new focused capability
interfaces when they use request, queue, or Matrix packages directly. Query
behavior remains stable creation-time ordering with bounded filtering before DTO
projection. The account state machine, credential storage, Matrix protocol,
and database schema are otherwise unchanged.

## Rollback

Revert the implementation through a subsequent UGS change record. No migration,
credential, profile, or runtime data change is required; rollback restores the
prior Node/Rust selection surface, concrete Store dependencies, and service file
layout.

## Breaking Change

Yes for callers selecting the removed Rust/Wry backend or release component.
There is no storage-schema change. The additive Core API `offset` parameter and
internal capability interfaces may require source updates for external
in-process consumers that construct these packages directly.

## Backport Target

none
