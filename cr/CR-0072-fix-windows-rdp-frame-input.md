# CR-0072: fix Windows RDP frame delivery and mouse input

Base: main
Head or Range: feat/windows-ui-client-foundation
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: fix(ui): improve Windows RDP frame delivery and mouse input
Revision: 3
Status: integrated
Decision: accepted
Policy Version: v0.3
Base OID: c5558e3fac0531e6253aea730263840411f53f88
Head OID: c5558e3fac0531e6253aea730263840411f53f88
Integrated Result: main@c5558e3fac0531e6253aea730263840411f53f88

## Summary

Move BGRX-to-RGBA conversion off the Slint UI thread, apply bounded latest-frame
backpressure, reduce the FreeRDP worker wait interval, and forward RDP-window
mouse move/down/up events through a worker-owned input queue. Pointer coordinates
are mapped through the displayed aspect-ratio-preserving framebuffer before
calling FreeRDP's input API.

## Motivation

The first Windows runtime build connected successfully but the UI could spend
too much time converting full-size frames on the UI thread, while mouse clicks
had no effect because the display-only slice did not submit input events. The
worker must remain the sole owner of the FreeRDP session and process input in
the same event loop as network traffic.

## Test Evidence

`cargo fmt --manifest-path ui/windows/Cargo.toml -- --check`

`cargo check --manifest-path ui/windows/Cargo.toml --all-targets`

`cargo test --manifest-path ui/windows/Cargo.toml --no-fail-fast`

`go test ./...`

`go vet ./...`

`./scripts/validate_policy_manifest.sh && ./scripts/validate_quality_profile.sh && ./scripts/validate_supply_chain_profile.sh && ./scripts/validate_action_pinning.sh && ./scripts/validate_repository_shape.sh && ./scripts/test_build_contract.sh`

`git diff --check`

## Risk

The native wrapper now includes FreeRDP input declarations and pointer flag
constants, so the Windows runner must verify the exact FreeRDP 3.x ABI and
static-link closure. Mouse forwarding is limited to left, right, and middle
buttons; keyboard forwarding remains a separate follow-up. Frame backpressure
may drop intermediate frames intentionally, but it never drops the latest frame
that the UI has not yet consumed.

## Rollback

Revert this change record and the associated UI/native changes. The existing
certificate and connection lifecycle remain unchanged.

## Breaking Change

None to the Core API, launcher protocol, storage schema, or persisted runtime
state. The Windows RDP page gains an internal pointer-event callback only.

## Backport Target

none
