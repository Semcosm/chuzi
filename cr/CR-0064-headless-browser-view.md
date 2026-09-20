# CR-0064: add on-demand headless browser views

Base: main
Head or Range: feat/windows-ui-client-foundation
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(browser): add on-demand headless browser views
Revision: 2
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 6f92f28bb70d52adbd9576fcdfef4103c30d0898
Head OID: aa1feda74e3a4b6b328a7311a3fd970e1e92d0c0
Integrated Result: pending

## Summary

Add a read-only browser-view MVP for active headless-CDP sessions. Core exposes
`get_browser_view`, the adapter captures a bounded JPEG through the existing
loopback CDP session, and the Windows Slint Tasks page renders the frame on
demand. The browser remains headless, no second browser is started, and no
continuous screenshot stream is maintained.

The request carries only a service-derived request ID and optional bounded
dimensions. URL, CDP endpoint, Profile path, credentials, mouse input, and
keyboard input remain outside the client contract. Frames are ephemeral and are
not persisted, logged, audited, or sent through Matrix.

Browser startup readiness is event-driven at the process and loopback TCP
boundaries, followed by a CDP version handshake. A bounded deadline remains
as a failure guard, so slow CI or host startup does not turn a fixed 500 ms
assumption into a transient browser failure.

## Motivation

Users need to inspect an active browser page without changing the worker to a
headed browser or paying the resource cost of a permanent video stream. A
single snapshot when explicitly requested preserves the headless resource model
while providing enough visibility for the Windows operational client.

## Test Evidence

`GOTMPDIR="$PWD/.gotmp" go test ./...`

`GOTMPDIR="$PWD/.gotmp" go vet ./...`

`npm test --prefix browser-worker` (17 Node tests, including delayed CDP readiness and on-demand JPEG capture)

`cargo fmt --manifest-path ui/windows/Cargo.toml -- --check`

`cargo test --manifest-path ui/windows/Cargo.toml` (6 UI tests)

`cargo run --manifest-path ui/windows/Cargo.toml --example layout_snapshot -- --page tasks --output dist/windows-layout-browser-view`

`./scripts/validate_policy_manifest.sh`

`./scripts/validate_quality_profile.sh`

`./scripts/validate_supply_chain_profile.sh`

`./scripts/validate_repository_shape.sh`

`./scripts/test_build_contract.sh`

`git diff --check`

## Risk

The new path adds a bounded image payload to the local Core IPC contract and
uses a second short-lived CDP connection to the browser already owned by the
active worker. Dimensions and payload size are validated at adapter, plugin,
Core, and transport boundaries. Adapter failures are redacted as
`unavailable`; cancellation and deadlines retain their stable transport
classification. The UI remains read-only and cannot navigate or inject input.

The build-contract check was updated to inspect `ui/windows/src/core_client.rs`,
where the existing named-pipe derivation now lives after the Windows UI
refactor. The browser process integration test now allows a bounded multi-
second startup window suitable for cross-platform CI runners.

## Rollback

Revert commit `aa1feda74e3a4b6b328a7311a3fd970e1e92d0c0`, then commit
`bfcb881c442a2ed810be6d5dde39a1c5f4a00c3a`,
and this CR. Existing
Core methods, headless worker lifecycle, and Windows Tasks status operations
remain compatible; clients that do not advertise `get_browser_view` continue
to use the prior method list.

## Breaking Change

No persistence or business-state change. `get_browser_view` is an additive Core
API method advertised through the existing handshake method list. Interactive
browser control and headed Chromium are intentionally out of scope.

## Backport Target

none
