# CR-0093: add Core and Launcher job-pool operations control plane

Base: main
Head or Range: ef44b3550cefa146a0d5a117443210a41e1b1184
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(service): add Core Launcher job pool phase 5 control plane
Revision: 3
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 762a44ac441fc7d646059564098593d11c208dd1
Head OID: 762a44ac441fc7d646059564098593d11c208dd1
Integrated Result: pending

## Summary

Add the Phase 5 control plane for job pools and signed environment lifecycle
operations. Core now exposes typed redacted pool, operation, environment, and
capacity projections; the local transport negotiates the additive methods; and
Launcher provides the typed job-pool and environment commands through Core.

The Store adds durable pool/environment operation, idempotency, and metadata-only
audit buckets. Pool configuration revisions are optimistic-concurrency checked,
reconciled into logical slot state, and projected with capacity and stable
failure classifications. A pending operation captures the prior config and slot
generation so failed environment validation or provisioning restores the old
ready generation atomically; stale operations are closed as superseded. The
running scheduler and slot lifecycle refresh their inputs from the durable pool
projection, so restart and later Core updates do not silently fall back to
static deployment values. Signed package install and upgrade resolve only opaque
service-owned catalog references; rollback swaps only the manager's verified
service-owned rollback tree. No lifecycle operation accepts a filesystem path,
shell, command, or executable, and missing catalog references fail with
`package_unavailable`. Gate completion commits the signed environment record,
operation state, and audit event in one Store transaction, and restart recovery
classifies incomplete package operations deterministically.

Successful same-version package upgrades retain one verified rollback tree; a
later rollback swaps that tree only after validating both signed package trees.
The current service instance is single-node and remains bound to the one pool
selected at startup. Core can persist multiple pool records, while automatic
runtime takeover or orchestration of a newly created second pool is outside
this change and is follow-up work. The Windows client now renders read-only
pool capacity and reconcile projections from `list_job_pools`; pool mutations
remain Core/Launcher operations.

## Motivation

Phases 2-4 provide logical slots, signed environments, generation fencing,
reconcile, capacity projection, and opaque RDP capabilities, but operators had
no stable Core/Launcher contract for configuration changes, drain/resume,
environment gates, operation status, or audit. This phase closes that boundary
without changing the account state-machine source of truth or exposing Store,
Windows identity, profile, agent, pipe, RDP, credential, or raw error data.

## Test Evidence

Historical scope evidence for the phase 5 implementation head included:

```text
go test ./...
go test -race ./...
go vet ./...
GOOS=windows GOARCH=amd64 go build ./...
GOOS=windows GOARCH=amd64 go vet ./...
npm --prefix browser-worker test
cargo test --manifest-path ui/windows/Cargo.toml
git diff --check
./scripts/validate_policy_manifest.sh
./scripts/validate_quality_profile.sh
./scripts/validate_supply_chain_profile.sh
./scripts/validate_action_pinning.sh
./scripts/validate_repository_shape.sh
./scripts/test_build_contract.sh
./scripts/validate_cr_record.sh cr/CR-0093-core-launcher-job-pool-phase5.md
```

The browser-worker suite passed 22 tests and the Windows UI suite passed 21
tests. Focused tests cover revision
conflicts, changed idempotency payloads, pool projection redaction/capacity,
drain retaining a leased slot, failed reconcile restoring a ready generation,
stale operation cleanup, environment operation audit/idempotency and atomic
completion, restart recovery, invalid catalog refs, corrupt and failed
rollbacks, consecutive upgrades, live scheduler and slot lifecycle refresh,
controlled package execution, Core error classification, additive hello method
negotiation, and Windows UI summary/redaction. These tests exercise Store,
Core, Launcher, environment lifecycle, executor, and UI behavior beyond DTO
serialization.

The Phase 5 logical control-plane and single-startup-pool runtime scope is
covered; runtime orchestration for multiple pools remains future work. Windows
UI Settings is currently a read-only status display from `list_job_pools`, with
mutations performed through Core/Launcher. The Phase 4 native Windows smoke
gate, a deployment-owned real RDP authorizer/broker, and Windows end-to-end
catalog acceptance were not run here. Linux cross-build and hosted preflight
evidence do not constitute those production gates. CR-0092 remains pending and
the real RDP and Windows E2E checks remain external blockers.

Latest integration validation at HEAD `ef44b35` passed the Go test and race
suites, `go vet ./...`, Windows amd64 build/vet, Windows package test
compilation, the 22-test browser-worker suite, repository policy/quality/
supply-chain/action-pinning/shape validators, the build contract, and CR
validation. The capacity regression
`TestEffectiveSlotCapacityRequiresReadyMatchingEnvironmentRecord` covers
missing, unready, and digest-mismatched environment records and yields zero
ready/effective capacity. Native Windows, broker/RDP authorizer, and
signed-package end-to-end gates remain pending.

## Risk

The single-node bbolt topology remains authoritative. Pool operations create
logical slot records and rely on the slot lifecycle reconciler for Windows-backed
provisioning, lease fencing, generation checks, health, and retirement. Catalog
execution remains service-owned and validates signed trees before Store sync; a
missing catalog reference receives the stable `package_unavailable`
classification. Dynamic Windows runtime selection is resolved through the signed
environment manager and never accepts Core paths or commands. RDP remains opaque
and deny-by-default. Audit and projections use stable metadata only; production
deployments still need Windows native smoke and an authorized RDP broker.

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
