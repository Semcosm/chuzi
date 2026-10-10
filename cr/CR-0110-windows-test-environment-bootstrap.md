# CR-0110: bootstrap a signed Windows test environment and enforce pool readiness

Base: main
Head or Range: b721549da56eeaa2e92401dc05f8a5a40ad0b5c3
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(environment): bootstrap signed Windows test packages and enforce pool readiness
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 9d8de9352a2a14429a6b1039bd91f26683a94ff1
Head OID: b721549da56eeaa2e92401dc05f8a5a40ad0b5c3
Integrated Result: pending

## Summary

Implementation is recorded in signed commit
b721549da56eeaa2e92401dc05f8a5a40ad0b5c3 on feat/windows-slot-session-ui.
CR-0109 remains the record for the existing mode controls and slot-session UI.
Integration into main remains pending.

Expose the signed environment manager while Core runs in logical mode. Reserve
the Windows-only opaque catalog key chuzi-windows-test-v1 for
chuzi/windows-test version 1.0.0. Construct its manifest from five fixed
installed CorePayload/browser-worker/src resources; reject missing, redirected,
or altered resources. Generate an Ed25519 test signer in memory, clear its
private key after use, and persist only its public key. Save trust before
publishing the catalog entry, retain orphan public keys across an interrupted
bootstrap, and revalidate existing catalog entries without silently replacing
them. Preserve Windows path casing and short-name aliases while rejecting
symlinks and reparse redirection.

Keep install, signature/file verification, trust, enablement, and health as
separate lifecycle gates. Persist readiness revocation after failed gates into
the Store projection. Bind trusted pool applies to the ready environment's
canonical digest and signer, reject mismatches, and forbid disabling trust in
Windows mode. Offline mode save rechecks the ready Store record and installed
signed headless entrypoint. Preserve explicit unsigned logical fixtures. Allow
scale-to-zero after environment health or trust fails, and wait for retiring
slot cleanup before completing the operation. Both Core apply (used by the UI)
and scale support that cleanup; apply retains only an existing durable signed
binding and rejects supplied identity mismatches or stale revisions.

Reuse the existing SessionBootstrapper, SessionLoginAdapter, broker ownership
state machine, managed Windows provisioner, fake providers, and native smoke
entry. Replace the fixed pipe client's blocking exchange with cancellable
connection and I/O, a maximum 30-second deadline, and bounded strict responses.
Require matching operations, an active nonzero start session, and stopped state
on successful stop. Failures retain the redacted session_unavailable class.

Add fixed test-package selection and lifecycle gate labels in the UI. Retain
separate logical capacity, signed environment readiness, and current Windows
session/Agent health. Document the installed PowerShell inspection commands
and external deployment prerequisites.

## Motivation

The installed logical pool could report ready while environment-list was empty
and no Windows user, WTS session, desktop, or Agent existed. The mode controls
needed a controlled way to prepare a signed environment before stopping Core,
and pool bindings needed durable package identity. The existing broker/login
adapter is deployment-owned; the UI must expose its absence as a failure rather
than treating logical capacity or package readiness as native acceptance.

## Test Evidence

Passed on the Linux development host: go test ./...; go vet ./...;
go test -race ./internal/slotwindows ./internal/environment;
go test -race ./internal/core ./internal/store; Windows amd64 compile-only
tests and vet for internal/environment, internal/slotwindows, internal/core,
and cmd/service. The compile-only command uses -run '^$' -exec true and does
not execute Windows tests.

Passed UI checks: cargo test --manifest-path ui/windows/Cargo.toml --locked
(47 tests), cargo nextest run with the same manifest and lock (47 tests),
cargo check --locked, cargo check --features slint/mcp --locked, and cargo fmt
--check. Generated the documented 21-state layout matrix in two themes and
three sizes (126 PNGs), with dimensions and nonblank validation. Inspected
compact logical-ready, Windows-ready, and mode-save views. No pixel golden
comparison is claimed. Generated images and logs remain under ignored dist/.

Passed repository checks: validate_policy_manifest.sh,
validate_quality_profile.sh, validate_supply_chain_profile.sh,
validate_action_pinning.sh, validate_repository_shape.sh,
test_build_contract.sh, validate_cr_record.sh for this record, and
git diff --check. The conditional document-map check is not applicable because
.ugs/document-map.json is absent.

New assembled Core/Store tests cover package bootstrap and retry, lifecycle
gates, canonical pool binding, restart persistence, mode-save rejection of
wrong digest/signer or unready Store records, tampered installed/catalog
resources, persisted readiness revocation, and unchanged logical apply/cleanup.
Store tests cover scale-to-zero after health, enablement, or trust revocation,
restart while cleanup is pending, and completion only after fake resource
deletion. Core/Store integration tests exercise the UI apply path after
verification, trust, enablement, health, or environment-port loss, preserving
the signed binding and retry identity while waiting for deletion; new pools,
changed identities, nonzero capacity, and stale revisions remain rejected.
Transport tests cover valid start/stop, missing broker/provider,
wrong operation/state, unknown private fields, truncated/oversized responses,
and cancellation during read and write. Existing tests continue covering
ownership, duplicate requests, stale generation/revision, leases, recovery,
cleanup failure, and broker restart refusal to adopt unknown sessions.

No Windows host was available for native acceptance or installed PowerShell
checks. scripts/test_windows_job_pool_smoke.ps1 remains the native smoke
entry for managed user/SID, WTS session, desktop, ACL, Agent pipe/heartbeat,
lease recovery, and cleanup. Fake provider results and Linux layout snapshots
do not establish those native facts. Remote build evidence is pending the
test-channel Actions run for the pushed topic head. No deployment or integration
is claimed.

## Risk

The local test signer attests locally assembled files, not a production
publisher. Package health verifies signed resources and supported entrypoints;
it does not launch Node/browser or prove session readiness. Existing test
catalog entries are immutable on retry and do not automatically follow later
installed payload updates. Service-owned data/catalog/trust paths and installed
runtime files require deployment ACL protection.

Production provisioning still requires a real controlled broker/login adapter,
restricted pipe ACLs, Windows service/WTS privileges, an Authenticode-signed
session-shell.ps1 trusted under AllSigned, and installed Node/browser
dependencies. Missing broker/session support fails closed. Existing Windows
ready projections describe the latest reconciled Agent health, not a new
synchronous WTS probe. No production end-to-end completion is claimed.

## Rollback

Scale every pool to zero in its current mode, wait for actual resource/lease
cleanup, stop Core, save logical mode through the existing launcher/UI
maintenance boundary, and restart. Revert this implementation and its docs
after cleanup. Preserve deployment configuration, bbolt state, public trust
metadata, and installed package history; do not delete account credentials or
Profiles to force a mode switch. Retained pending/quarantined resources require
explicit recovery or deletion.

## Breaking Change

Windows pool apply now explicitly requires require_trusted and a ready signed
record and rejects supplied identity mismatches. Successful broker stop
responses must include state=stopped; external adapters omitting it must update.
Logical unsigned fixtures retain their existing behavior. Fresh logical startup
allows an absent trust file; persisted manager state without trust fails
startup rather than discarding its verification authority.

## Backport Target

None
