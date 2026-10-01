# CR-0082: remove legacy Windows UI pages

Base: main
Head or Range: feat/ui-refactor
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(ui): remove legacy Windows pages
Revision: 2
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: dd126d00dce6d33d3c4329fcccea0b0158f38f0a
Head OID: 77134d892e38fd535f9915e1f18761e6e9fd57e8
Integrated Result: pending

## Summary

Make Sessions the only main-window destination. Remove the former Overview,
Accounts, Tasks, Adapters, Settings, and RDP login routes, callbacks, DTOs, and
snapshot fixtures. Keep the separate Desktop RDP runtime surface and Core,
diagnostic-consent, and local theme boundaries. Update the Windows layout
workflow and documentation to cover the current Sessions implementation.

## Motivation

The Sessions redesign should replace the previous main-window pages instead of
leaving them reachable as compatibility screens. Their Rust glue, snapshot
branches, navigation descriptions, and CI fixtures also need to match the new
UI.

## Test Evidence

`cargo fmt --manifest-path ui/windows/Cargo.toml` completed. `cargo test
--manifest-path ui/windows/Cargo.toml` passed (19 tests); `go test ./...` and
`go vet ./...` passed. `./scripts/test_build_contract.sh` and
`git diff --check` passed. The policy-manifest, quality-profile,
supply-chain-profile, action-pinning, and repository-shape validators remain
the previously recorded passing checks; `.ugs/document-map.json` is absent.

The layout snapshot example generated all 30 combinations for mixed, empty,
loading, error, and unavailable states in Light and Dark at 800x600, 1120x760,
and 1440x900 under
`dist/windows-layout-final-1790827675/sessions/`. The example rejects blank or
mis-sized output. Visual review confirmed the compact Inspector overlay and
Close action, bounded narrow-window text, and side-by-side Inspector at
1440x900. Cargo still reports unused-code warnings in the separate RDP runtime
and two `BrowserView` fields; all 19 tests pass.

## Risk

The Windows client no longer exposes account lookup/submission, standalone task
lookup, adapter lifecycle, general settings, or the legacy RDP login page. Core
and launcher capabilities remain unchanged. The separate RDP runtime source,
capability boundary, and credential/performance boundaries remain in place. The
Sessions main window intentionally keeps Open RDP hidden until the service wires
an authorized capability provider and runtime material handoff.

## Rollback

Revert the scoped Windows UI, snapshot, workflow, and documentation changes to
restore the prior main-window surfaces. Preserve unrelated Core API and working
changes when applying a rollback in the shared checkout.

## Breaking Change

The Windows user-facing navigation and page actions are replaced by the Sessions
surface. No Core API, launcher contract, persisted service state, or RDP runtime
contract changes are introduced.

## Backport Target

none
