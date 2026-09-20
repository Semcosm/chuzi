# CR-0065: add Windows Settings component management

Base: main
Head or Range: feat/windows-ui-client-foundation
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(windows): add Settings component management
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 6f92f28bb70d52adbd9576fcdfef4103c30d0898
Head OID: 85f25a40452d01a37bde34c4d30bac9b8550617a
Integrated Result: pending

## Summary

Move Core installation out of the Windows Overview page and add a dedicated
Components section under Settings. The section reads the launcher's component
projection and exposes refresh, install, enable, disable, and remove actions,
including lifecycle state, health, version, and required-component protection.
Component operations remain available while Core is stopped because they use
the launcher boundary directly.

## Motivation

Overview should remain a Core readiness and lifecycle surface rather than a
package-management surface. Users need a predictable location for managing the
launcher, service, and browser-worker components, while the existing explicit
Core installation path must remain available from Settings.

## Test Evidence

`cargo fmt --manifest-path ui/windows/Cargo.toml -- --check`

`cargo test --manifest-path ui/windows/Cargo.toml` (7 UI tests)

`cargo run --manifest-path ui/windows/Cargo.toml --example layout_snapshot -- --output dist/windows-layout-components --page settings`

`cargo run --manifest-path ui/windows/Cargo.toml --example layout_snapshot -- --output dist/windows-layout-components --page overview`

`./scripts/test_build_contract.sh`

`git diff --check`

## Risk

The change is confined to the Windows client presentation and launcher command
bindings. It does not change the Core API, release manifest schema, component
manager transaction behavior, plugin trust policy, credentials, or persistence.
Component actions reuse the existing launcher lock and validation boundaries;
required components remain protected from disable/remove operations.

## Rollback

Revert commit `85f25a40452d01a37bde34c4d30bac9b8550617a`. The prior Overview
Core installation action and client behavior remain compatible with the
existing launcher component contract.

## Breaking Change

No protocol or persistence breaking change. The Windows UI moves the Core
installation action from Overview to Settings and adds an additive component
projection in the client.

## Backport Target

none
