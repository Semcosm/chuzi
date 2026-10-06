# CR-0098: add durable session lifecycle and Windows session product flow

Base: main
Head or Range: a547945110537a0a7b8975eba6f25534c3539e84..65345f43c000e646d04f4a4decf8db2ae34e0e6b
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(service): add durable session lifecycle and Windows session flow
Revision: 2
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: a547945110537a0a7b8975eba6f25534c3539e84
Head OID: 65345f43c000e646d04f4a4decf8db2ae34e0e6b
Integrated Result: pending

## Summary

Add the Stage 5C session product boundary. Core can start, inspect, list, and
stop an authorized session through the versioned local transport. The service
persists only redacted lifecycle metadata, claims the exact requested account
request together with a matching trusted execution slot, fences late runtime
callbacks by environment generation, recovers non-terminal sessions on restart,
and releases account and slot leases on stop or failure. The Windows runtime
reuses the authenticated slot-agent connection for the closed worker and
adapter start/stop commands; RDP remains an optional credential-bound
capability.

The bbolt schema advances to version 10 with a sessions bucket and database
integrity checks. A deterministic signed-environment contract describes the
service-owned base slot runtime without storing commands, paths, credentials,
endpoints, browser Profiles, or RDP material. The Windows Slint client adds
controlled identifier-only session start fields, session polling, workspace
selection, and workspace Stop through Core.

## Motivation

The previous control plane exposed job-pool capacity and environment
operations, but it did not provide a durable request-to-runtime product path.
Operators need lifecycle state and a controlled interactive workspace while the
account state machine remains the only business-state authority and runtime
providers remain behind closed interfaces.

## Test Evidence

The following checks pass on the Linux integration host:

go test ./...
go test -race ./...
go vet ./...
GOOS=windows GOARCH=amd64 go build ./...
GOOS=windows GOARCH=amd64 go vet ./...
GOOS=windows GOARCH=amd64 go test -c ./internal/slotagent
npm --prefix browser-worker test (22 tests)
cargo fmt --manifest-path ui/windows/Cargo.toml -- --check
cargo nextest run --manifest-path ui/windows/Cargo.toml --locked (26 tests)
cargo check --manifest-path ui/windows/Cargo.toml --locked
layout_snapshot matrix: 72 images across 12 states, both themes, and fixed sizes
git diff --check
./scripts/validate_policy_manifest.sh
./scripts/validate_quality_profile.sh
./scripts/validate_supply_chain_profile.sh
./scripts/validate_action_pinning.sh
./scripts/validate_repository_shape.sh
./scripts/test_build_contract.sh

Focused coverage includes atomic exact-request/slot claiming, request-account
mismatch rejection, durable session validation, lease cleanup, generation
fencing, RDP-unavailable degradation, restart recovery, Core redaction, all
four local transport session methods, and UI workspace/session projections.
The Windows slot-agent coverage also permits one browser-worker and one adapter
job for the same request, rejects same-kind duplicates, routes worker frames to
the browser-worker job, and stops both jobs together.
The optional document-map validator was not applicable because this checkout
has no .ugs/document-map.json. Native Windows user/session/ACL/RDP smoke,
production RDP authorization, live account automation, and a production
Matrix deployment remain external gates.

## Risk

The session manager owns runtime facts and lease cleanup but deliberately does
not choose account business outcomes. A failed or restarted session therefore
leaves the account transition to the existing queue/account recovery boundary.
Windows provisioning, worker/adapter execution, and RDP connection material
remain deployment-owned boundaries; the Core and durable session projections
contain only identifiers, phases, stable failure classes, readiness flags, and
environment generation metadata.

## Rollback

Stop or disable the affected pool, then revert the session manager, Core/UI,
transport, runtime-contract, and migration changes. Keep schema version 10 and
the empty sessions bucket when rolling back code against an already-upgraded
database; old binaries must fail closed rather than interpret session records.
Do not remove slot users, package trees, Profiles, leases, credentials, or RDP
capabilities as part of a code rollback.

## Breaking Change

None for existing request, queue, and job-pool consumers. The Core transport
methods and sessions bucket are additive. Session starts require service-owned
pool, environment, and adapter identifiers; callers cannot provide paths,
commands, executables, desktop names, endpoints, or credentials.

## Backport Target

none
