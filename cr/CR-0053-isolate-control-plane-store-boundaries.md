# CR-0053: isolate control-plane store boundaries

Base: main
Head or Range: 61f244390bc52de749eda42a706b89560b2f2294..3caea0afa6e309113316b1e3fef4051df7ba4785
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: refactor: isolate control-plane store boundaries
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 61f244390bc52de749eda42a706b89560b2f2294
Head OID: 3caea0afa6e309113316b1e3fef4051df7ba4785
Integrated Result: pending

## Summary

Tighten the control-plane module boundaries after the native-client/Core API
direction became authoritative. Move notification filtering, ordering, and
pagination into the durable store; push audit request filtering into the store;
and expose only the cancellation fact required by a running browser session.
Replace concrete store dependencies in request, queue, and Matrix notifier
services with focused capability interfaces. Split the service command's
supervisor, endpoint wiring, and maintenance operations into separate modules.

## Motivation

Several orchestration packages depended directly on the full bbolt Store or
performed durable-query work in memory. Browser cancellation was discovered by
type assertion against the lease store, and the service entry point combined
runtime assembly, endpoint lifecycle, scheduling, and maintenance commands.
Those couplings made focused tests and alternate implementations harder while
allowing unrelated persistence capabilities to cross package boundaries.

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

No live account, external credential, or production Matrix service was used.

## Risk

The Core API notification query gains an additive `offset` field and now
rejects offsets above the bounded query maximum. Store-backed callers must
implement the new focused capability interfaces when they use request, queue,
or Matrix packages directly. Query behavior remains stable creation-time
ordering with bounded filtering before DTO projection. The service runtime,
account state machine, credential storage, Matrix protocol, and database
schema are otherwise unchanged.

## Rollback

Revert the implementation and CR commits through a subsequent UGS change
record. No migration, credential, profile, or runtime data change is required;
rollback restores the prior concrete Store dependencies and service file layout.

## Breaking Change

No wire or storage-schema breaking change. The additive Core API `offset`
parameter and internal capability interfaces may require source updates for
external in-process consumers that construct these packages directly.

## Backport Target

none
