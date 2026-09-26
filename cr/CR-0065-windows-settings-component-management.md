# CR-0065: add Windows Settings component management

Base: main
Head or Range: feat/windows-ui-client-foundation
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(windows): add Settings component management
Revision: 5
Status: integrated
Decision: accepted
Policy Version: v0.3
Base OID: c5558e3fac0531e6253aea730263840411f53f88
Head OID: c5558e3fac0531e6253aea730263840411f53f88
Integrated Result: main@c5558e3fac0531e6253aea730263840411f53f88

## Summary

Move Core installation out of the Windows Overview page and add a dedicated
Components section under Settings. The section reads the launcher's component
projection and exposes refresh, install, enable, disable, and remove actions,
including lifecycle state, health, version, and required-component protection.
Component operations remain available while Core is stopped because they use
the launcher boundary directly.

The Windows lifecycle path also keeps a Core process identifiable after the UI
restarts: persisted PID lookup passes the numeric PID directly to PowerShell
and normalizes canonical Windows path prefixes before comparing executables.

Component management now uses a release-manifest-backed selector in Settings,
so users choose supported component IDs from a dropdown instead of entering
free-form values.

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

`cargo check --manifest-path ui/windows/Cargo.toml --target x86_64-pc-windows-msvc` (not run: the local host does not have the Windows Rust target installed)

## Risk

The change is confined to the Windows client presentation and launcher command
bindings. It does not change the Core API, release manifest schema, component
manager transaction behavior, plugin trust policy, credentials, or persistence.
Component actions reuse the existing launcher lock and validation boundaries;
required components remain protected from disable/remove operations. Process
termination still requires a persisted PID and an executable-path match.

## Rollback

Revert commit `b6e579e2e8e8462cd75b990f2a22a05b0d92d914`,
`d17552b3666b14457d19b0b2c6834dfc4dcdf97a`, and
`85f25a40452d01a37bde34c4d30bac9b8550617a`. The prior Overview Core
installation action and client behavior remain compatible with the existing
launcher component contract.

## Breaking Change

No protocol or persistence breaking change. The Windows UI moves the Core
installation action from Overview to Settings and adds an additive component
projection in the client.

## Backport Target

none
