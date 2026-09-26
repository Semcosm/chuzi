# CR-0075: add Windows RDP wheel and keyboard input

Base: main
Head or Range: feat/windows-ui-client-foundation
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(ui): add Windows RDP wheel and keyboard input
Revision: 3
Status: integrated
Decision: accepted
Policy Version: v0.3
Base OID: c5558e3fac0531e6253aea730263840411f53f88
Head OID: c5558e3fac0531e6253aea730263840411f53f88
Integrated Result: main@c5558e3fac0531e6253aea730263840411f53f88

## Summary

Forward vertical and horizontal wheel input and keyboard press/release events
from the focused RDP framebuffer to the FreeRDP session worker. Map physical
keyboard input using the local Windows layout, send unmappable printable text
through FreeRDP's Unicode input API, and release remote keys if the framebuffer
loses focus.

## Motivation

The existing RDP input boundary forwards pointer movement and the left, right,
and middle buttons. It does not subscribe to Slint scroll events or keyboard
events, leaving remote scrolling and typing unavailable.

## Test Evidence

`cargo fmt --manifest-path ui/windows/Cargo.toml -- --check`

`cargo check --manifest-path ui/windows/Cargo.toml --all-targets`

`cargo test --manifest-path ui/windows/Cargo.toml --no-fail-fast` (13 tests)

`git diff --check`

The FreeRDP FFI backend is Windows-only and was not compiled on this Linux
host. The Windows Actions build must verify generated FreeRDP bindings and
static linking.

## Risk

The change adds FreeRDP keyboard and wheel flags to the native wrapper and uses
the Windows keyboard-layout APIs to translate Slint key text to RDP scan codes.
The Windows workflow must verify the FreeRDP 3.x bindings. Unicode fallback is
limited to printable key events; IME composition behavior depends on Slint's
key event delivery.

## Rollback

Revert the RDP input callback, worker queue, wrapper constants, and this change
record. Existing pointer buttons and framebuffer presentation remain available.

## Breaking Change

None to Core API, launcher protocol, storage schema, or persisted runtime state.
The Windows RDP window adds internal wheel and keyboard callbacks.

## Backport Target

none
