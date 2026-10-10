# CR-0109: configure Windows user pool and report current Agent readiness

Base: main
Head or Range: 8e966b680dc78998b130d82662bb523fb92b9d2a
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(ui): configure Windows user pool and report agent readiness
Revision: 2
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 9d8de9352a2a14429a6b1039bd91f26683a94ff1
Head OID: 8e966b680dc78998b130d82662bb523fb92b9d2a
Integrated Result: pending

## Summary

Add a Windows user pool card to Settings with saved mode, selected pool,
confirmation, Enable Windows users, and Use logical test mode. Route mode saves
through a fixed launcher command and an offline Core maintenance boundary.
Require stopped Core, an exclusive Store lock, zero desired capacity in every
pool, terminal pool operations, released account and slot leases, and cleanup
of all non-deleted slots before switching. Windows mode also requires the
selected revision and an installed, signed, trusted headless environment.
Atomically save validated configuration and preserve existing deployment
settings, including Windows timeouts, user prefix, and RDP policy.

Retain Start basic session and operation polling through launcher/Core. Expose
the active execution mode on pool and slot projections. Keep historical
operation state separate from current slot state and generation; report Windows
Agent readiness only from a Windows slot's latest reconciled health facts.
Logical capacity explicitly reports that no Windows user or Agent exists.
Unknown modes and unavailable slot reads cannot claim Agent readiness.

Preserve signed environment identity when applying or scaling pools, repair
Core refresh routing under the busy guard, and keep existing configuration on
managed restarts. Extend UI contracts, operating instructions, deterministic
layout fixtures, and the Windows CI layout matrix.

## Motivation

The previous client displayed logical pool readiness without a Windows user
pool configuration entry, so a ready card could appear despite no user-agent
process. Operators need explicit execution mode controls and readiness that
distinguishes logical test capacity from the latest Windows Agent health.

## Test Evidence

Passed: go test ./...; go vet ./...; cargo test --manifest-path
ui/windows/Cargo.toml --locked (46 tests); cargo nextest run --manifest-path
ui/windows/Cargo.toml --locked (46 tests); cargo fmt --manifest-path
ui/windows/Cargo.toml -- --check; cargo check --manifest-path
ui/windows/Cargo.toml --features slint/mcp --locked;
validate_policy_manifest.sh, validate_quality_profile.sh,
validate_supply_chain_profile.sh, validate_action_pinning.sh,
validate_repository_shape.sh, test_build_contract.sh; and git diff --check.

Go coverage verifies slot-state and execution-mode projections, current versus
historical generation, unavailable reads, offline cleanup requirements, pending
operations, active and expired leases, exclusive Store ownership, revision and
signed environment checks, configuration preservation, fixed launcher arguments,
and redacted maintenance failures.

The Slint testing backend and temporary launcher fixtures verify confirmation,
slot-session polling, idempotency, busy rejection, refresh routing, failed and
stale results, and an unchanged business Sessions list. Mode callback coverage
includes successful Windows and logical saves, running and unknown Core state,
cleanup failure, a running-state race, missing revision, malformed results,
and preservation of the previous mode on failure. Fixtures create no native
Windows sessions and standard cargo test passes with the shared Slint event loop.

Generated 60 dimension/non-blank layout snapshots: mixed, Settings, five native
slot-session fixtures, logical-ready, and two Windows mode fixtures, each at
800x600, 1120x760, and 1440x900 in light and dark themes. Inspected compact
Windows mode, native-ready, and logical-ready images. This is not a pixel-level
golden comparison. Images and check logs remain under ignored dist/. The
conditional document-map validator is not applicable because
.ugs/document-map.json is absent.

Native Windows WTS session acceptance was not performed on the Linux host.
Remote four-target build and artifact validation run on this topic branch with
the test channel; production Windows session acceptance remains deployment-owned.

## Risk

Mode changes require cleanup under the previous runtime before stopping Core.
The maintenance boundary validates this again and refuses to save if any
capacity, resources, or leases remain. Windows saves update the selected
environment while preserving existing deployment privileges and timeout policy.
Invalid or foreign-root configuration is rejected instead of overwritten.

The current package does not include a complete Windows session broker login
adapter. Saving Windows mode only validates configuration; actual provisioning
still requires the deployment login service and Windows service privileges and
fails closed when they are missing. Agent readiness reflects the latest
reconcile, not a synchronous WTS probe. No credentials, Profile paths, or raw
maintenance errors cross the UI boundary. Busy ownership prevents overlapping
UI actions.

## Rollback

Before reverting, scale every pool to zero under its current runtime and wait
for cleanup. Use the logical-mode control if changing the deployed mode, then
restart Core. Revert the implementation and associated documentation; do not
discard deployment configuration or database state. Existing slot-session
operations remain queryable.

## Breaking Change

None. Execution mode fields are additive; older Core versions render unknown
mode and cannot claim Windows Agent readiness. The offline maintenance command
is available only with the updated launcher and Core payload.

## Backport Target

None
