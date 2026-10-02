# CR-0093: add Core and Launcher job-pool operations control plane

Base: main
Head or Range: 0782270
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(service): add Core Launcher job pool phase 5 control plane
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 000086c232ed46c07b4a8e493b14e240f845f2c4
Head OID: 0782270be10fdd2881b1161052b6eb688b20ab5f
Integrated Result: pending

## Summary

Add the Phase 5 control plane for job pools and signed environment lifecycle
operations. Core now exposes typed redacted pool, operation, environment, and
capacity projections; the local transport negotiates the additive methods; and
Launcher provides the typed job-pool and environment commands through Core.

The Store adds durable pool/environment operation, idempotency, and metadata-only
audit buckets. Pool configuration revisions are optimistic-concurrency checked,
reconciled into logical slot state, and projected with capacity and stable
failure classifications. Environment gate operations are durable; package
install/upgrade/rollback remain explicitly unavailable from Core until a
service-owned catalog executor is deployed.

## Motivation

Phases 2-4 provide logical slots, signed environments, generation fencing,
reconcile, capacity projection, and opaque RDP capabilities, but operators had
no stable Core/Launcher contract for configuration changes, drain/resume,
environment gates, operation status, or audit. This phase closes that boundary
without changing the account state-machine source of truth or exposing Store,
Windows identity, profile, agent, pipe, RDP, credential, or raw error data.

## Test Evidence

Passed locally: `go test ./...`, `go test -race ./...`, `go vet ./...`,
`GOOS=windows GOARCH=amd64 go build ./...`, `GOOS=windows GOARCH=amd64 go vet
./...`, `npm --prefix browser-worker test` (22 tests), `git diff --check`, and
the policy, quality, supply-chain, action-pinning, repository-shape, and
build-contract validators. Focused tests cover revision conflicts, changed
idempotency payloads, pool projection redaction/capacity, drain retaining a
leased slot, environment operation audit/idempotency, invalid catalog refs,
Core error classification, and additive hello method negotiation.

The native Windows smoke gate and real RDP authorizer were not run here. Linux
cross-build and hosted preflight evidence do not constitute native Windows
production acceptance. CR-0092 remains pending and its dedicated runner is an
external blocker.

## Risk

The single-node bbolt topology remains authoritative. Pool operations create
logical slot records and rely on the existing slot lifecycle reconciler for
Windows-backed provisioning, lease fencing, generation checks, health, and
retirement. Core install/upgrade/rollback requests fail closed with the stable
`package_unavailable` classification when no catalog executor is injected;
existing service maintenance commands remain the signed package executor.
RDP remains opaque and deny-by-default. Audit and projections use stable
metadata only, but production deployments still need a catalog resolver,
Windows native smoke, and an authorized RDP broker.

## Rollback

Disable or drain affected pools, then revert the Phase 5 implementation and
documentation while retaining additive schema migrations for stores that have
seen the new buckets. Existing logical slot and environment records remain
readable; package rollback continues through the signed environment manager.
Do not remove package trees, users, profiles, or credentials without the
existing ownership and trust checks.

## Breaking Change

Core v1 is extended additively through hello method discovery. New bbolt schema
versions 8 and 9 add operation, idempotency, and audit buckets. Existing Core
methods and transport clients remain compatible. Launcher package references
reject path separators and are not filesystem paths.

## Backport Target

none
