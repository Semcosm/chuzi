# CR-0070: rebuild Windows desktop-clone UI shell

Base: main
Head or Range: feat/windows-ui-client-foundation
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: refactor(ui): rebuild desktop clone shell
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 6f92f28bb70d52adbd9576fcdfef4103c30d0898
Head OID: d4b883b6c139802a0b50b5045179941428447188
Integrated Result: pending

## Summary

Replace the failed native RDP and Win32 child-window implementation with a
standalone Rust + Slint desktop-clone UI shell. The shell mirrors the BGI
desktop-clone structure: a 32px title bar, responsive controls, state-aware
connection card, control-center menu, notification layer, and a black content
surface reserved for a future remote display.

The controller now owns only window creation, display, hide, and close-request
behavior. The desktop-clone window is a real frameless Slint window with a
draggable custom title bar that combines neutral RDP controls with functional
Windows 11-style minimize, maximize/restore, and close buttons. The connection
card uses explicit responsive geometry and neutral RDP terminology instead of
product-specific branding. It does not start MSTSC, inspect native HWNDs,
embed child windows, create sessions, or forward input.

## Motivation

The previous implementation coupled the presentation window to an unverified
native RDP embedding path. That path is explicitly discarded for this change.
Keeping the UI shell independent gives the next remote-surface design a stable
visual target and prevents transport assumptions from leaking into the Slint
window or the main application callback.

## Test Evidence

`cargo fmt --manifest-path ui/windows/Cargo.toml`

`cargo check --manifest-path ui/windows/Cargo.toml`

`cargo test --manifest-path ui/windows/Cargo.toml --no-fail-fast`

`cargo run --manifest-path ui/windows/Cargo.toml --example layout_snapshot -- --page desktop-clone --output /tmp/chuzi-desktop-clone-layout`

`cargo run --manifest-path ui/windows/Cargo.toml --example layout_snapshot -- --output /tmp/chuzi-layout-check`

`git diff --check`

## Risk

This change intentionally removes the previous Windows RDP behavior from the
desktop-clone window. The central black area is a placeholder and has no remote
desktop transport yet. The BGI visual shell uses Slint-native glyphs and shapes
instead of importing BGI's external icon/font assets, so exact glyph metrics can
vary by platform font fallback. Frameless-window resize and system-button
behavior also depend on the host window backend honoring Slint's native window
properties.

## Rollback

Revert the commits containing this CR and the desktop-clone UI rewrite. The
existing Core, launcher, and other Windows client pages remain available; no
storage schema, credential data, or service protocol migration is involved.

## Breaking Change

The previous native RDP/MSTSC embedding behavior is removed from the Windows
desktop-clone window. No Core API, launcher protocol, or persisted storage
format changes.

## Backport Target

none
