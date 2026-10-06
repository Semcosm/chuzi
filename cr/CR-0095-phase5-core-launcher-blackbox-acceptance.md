# CR-0095: accept Phase 5 Core and Launcher control-plane black-box pass

Base: main
Head or Range: 8370e73d4b70a5fc894e0efe0f4356438651355b..76774bbc1b1cbfed37810e2d81380465631c9bbf
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: test(service): record Phase 5 Core and Launcher black-box acceptance
Revision: 2
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 805747589895a40c9f941764dc59265ab80c4b20
Head OID: 805747589895a40c9f941764dc59265ab80c4b20
Integrated Result: pending

## Summary

Record the Linux black-box acceptance of the Phase 5 Core and Launcher control plane from the latest `origin/main` baseline. The acceptance uses a temporary bbolt data directory, the real service process, Unix Core IPC, and the Launcher CLI. It also records the one defect fixed in this pass: control-plane Launcher commands no longer require a co-located release manifest.

## Motivation

Phase 5 already provides the typed Core API, durable job-pool/environment operations, revision fencing, idempotency, redacted projections, and read-only UI projection. This pass verifies those boundaries through the running service and Launcher rather than relying only on DTO or Store tests, while keeping Windows and deployment-owned gates explicit.

## Test Evidence

- Baseline was fetched from `origin/main` at `8370e73d4b70a5fc894e0efe0f4356438651355b`; the root worktree's unrelated modifications were left untouched.
- A real Linux `cmd/service` process and `cmd/launcher` binary were built in a temporary root. Launcher `job-pool-list` and `environment-list` returned redacted empty projections with no `release-manifest.json`; this reproduces and verifies the fixed component-boundary defect.
- Through Launcher -> Core IPC, `apply`, operation query, repeated apply with the same payload/key, stale revision rejection (`chuzi core: conflict`), scale, drain, resume, get/list projection, desired capacity, environment readiness, `unprovisioned`, `retiring`, `provisioning`, and `effective_capacity: 0` were observed. Actor output was redacted to an `id_` value.
- A second run stopped and restarted the real service against the same temporary bbolt directory. The operation ID and `provisioning` state were returned identically before and after restart, proving durable operation lookup and reconnect.
- Existing repository tests cover ready/leased/quarantined/draining slot states, leased-slot drain retention, environment missing/unready/digest mismatch capacity fencing, failed reconcile rollback, stale-operation recovery, package lifecycle gates, signed catalog reference validation, upgrade/rollback failure preservation, Core hello negotiation, transport redaction, and the read-only Windows UI projection.
- `TestManagerCatalogReferenceLifecycle` now exercises a service-owned signed catalog key through install, trust, enable, health, a changed-content upgrade, rollback, and missing-reference failure.
- `go test ./...`, `go test -race ./...`, `go vet ./...`, `GOOS=windows GOARCH=amd64 go build ./...`, `GOOS=windows GOARCH=amd64 go vet ./...`, `npm --prefix browser-worker test` (22 passing), `CARGO_TARGET_DIR=/home/chen/.cache/chuzi-phase5-ui-target cargo test --manifest-path ui/windows/Cargo.toml` (21 passing), all repository policy/quality/supply-chain/action-pinning/shape/build-contract validators, `validate_cr_record` for CR-0093, and `git diff --check` passed.

## Risk

This evidence proves the Linux logical control-plane and local IPC boundary only. It does not prove native Windows provisioning, RDP authorization/broker behavior, signed package execution inside a Windows slot, real adapters, Windows service restart, power-loss recovery, or production cleanup. The logical Linux run intentionally leaves slots unprovisioned, so it reports zero ready/effective capacity.

## Rollback

Revert implementation commit `780a89c8e13f754270b8bb37752b739135db4ae0` if the launcher manifest-loading behavior must be restored. No production data or package tree was changed; each acceptance run used a temporary data directory.

## Breaking Change

None. Manifest-backed component commands retain their existing manifest requirement. Core, job-pool, and environment commands now use the service-owned Core endpoint without requiring a manifest in the Launcher root.

## Backport Target

none
