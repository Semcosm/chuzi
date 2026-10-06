# CR-0096: extend Windows UI with job-pool and environment operations

Base: main
Head or Range: 723b46fc6a8b6aaa3110be9935a5aca444da932b
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(ui): add Windows job-pool and environment operations
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 4d6b63b2ed5a8dd0348e862ed79e3efe2def8aff
Head OID: 723b46fc6a8b6aaa3110be9935a5aca444da932b
Integrated Result: pending

## Summary

Extend the existing Windows Slint Sessions/Settings shell with structured
redacted job-pool and environment projections and controlled operations. The UI
uses a fixed Rust Core method allowlist through Launcher, reads expected
revisions, creates per-action idempotency keys, confirms capacity and
environment changes, and polls durable operations to terminal state.

## Motivation

The Core and Launcher Phase 5 control plane is already integrated, but the
Windows client only rendered a long read-only pool summary. Operators need a
bounded view of capacity, environment readiness, reconcile state, stable
failure codes, and operation progress without gaining Store, filesystem,
credential, profile, named-pipe, Windows identity, or RDP access.

## Test Evidence

The UI Rust tests cover redaction, fixed Core method names, opaque package
references, stable failure classification, operation states, and idempotency
key shape. Deterministic Slint snapshots cover empty/single/mixed pools,
provisioning, draining, quarantined, failed reconcile, untrusted environments,
operation polling, stale revision, package unavailable, unavailable Core, and
narrow confirmation fixtures at 800x600, 1120x760, and 1440x900 in Light and
Dark. `cargo test --manifest-path ui/windows/Cargo.toml` and the layout snapshot
example pass when using the documented shared Cargo target cache.

## Risk

This change is a native UI projection and launcher façade consumer. It does not
rewrite Core/Store reconciliation, add multi-pool runtime takeover, add a
database, provision Windows users, authorize RDP, execute signed packages in a
Windows slot, or expose audit internals. UI failures refresh redacted
projections and retain stable failure classes.

## Rollback

Revert this change and keep the additive Core/Launcher Phase 5 control plane.
Existing pool and environment records remain owned by Core and Store; no UI
operation deletes package trees, users, Profiles, leases, or credentials.

## Breaking Change

None. Core and Launcher methods are consumed additively through the existing
allowlisted `core-call` boundary.

## Backport Target

none
