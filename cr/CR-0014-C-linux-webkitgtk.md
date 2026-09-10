# CR-0014: add the Linux WebKitGTK desktop WebView backend

Base: main
Head or Range: pending
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(browser): add Linux WebKitGTK desktop WebView
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 2ba510b9bdca27aefb654e4d0f200fca4b64d2aa
Head OID: 2ba510b9bdca27aefb654e4d0f200fca4b64d2aa
Integrated Result: pending

## Summary

Enable the existing Rust/Wry desktop helper on Ubuntu 24.04 LTS Linux targets.
The Linux backend uses WebKitGTK 4.1 and Tao's GTK container integration, which
supports both X11 and Wayland sessions. Linux amd64 and native Linux arm64
builds now carry the same `wry-desktop` helper as the other desktop targets.

The Go protocol, account state machine, Session Runner lease, credential store,
and Node worker boundary remain unchanged. The helper continues to load only the
embedded local test page and reports redacted runtime facts. This change does
not add a Chromium binary or claim true headless browser support.

## Motivation

Wry 0.57 provides one Rust API over platform WebViews, but Linux needs explicit
GTK/WebKitGTK dependencies. Its direct window-handle path is X11-only; the
official `WebViewBuilderExtUnix::build_gtk` path is required to support X11 and
Wayland through the GTK container owned by Tao. Keeping both display backends in
the same helper avoids a second Linux implementation while preserving the
desktop-WebView versus true-headless boundary.

The first supported Linux distribution remains Ubuntu 24.04 LTS on amd64 and
arm64. Ubuntu 22.04, Debian, other distributions, and display-server-specific
packaging remain outside this change until separate evidence is available.

## Test Evidence

Local checks completed:

- `cargo fmt --manifest-path browser-runtime/Cargo.toml -- --check`
- `cargo test --offline --locked --manifest-path browser-runtime/Cargo.toml`
- `cargo clippy --offline --locked --manifest-path browser-runtime/Cargo.toml --all-targets -- -D warnings`
- `cargo metadata --offline --locked --manifest-path browser-runtime/Cargo.toml --format-version 1 --no-deps`
- `scripts/test_build_contract.sh`
- `scripts/validate_repository_shape.sh`
- `bash -n scripts/build.sh scripts/package.sh scripts/test_build_contract.sh`
- `git diff --check`

The local Arch host does not provide root access or the GTK/WebKitGTK system
libraries, so Linux native linking and display execution are delegated to the
Ubuntu GitHub runners. The workflow installs `libgtk-3-dev`,
`libwebkit2gtk-4.1-dev`, `weston`, `xvfb`, `xauth`, and `dbus-x11`, then runs
the locked Cargo tests and local-page smoke tests under both X11/Xvfb and
Wayland/Weston. The native
`ubuntu-24.04-arm` job verifies the same build and smoke contract on arm64.

## Risk

Linux adds GTK/WebKitGTK native dependencies and requires a graphical session.
Missing libraries, an unavailable display, or WebKitGTK initialization failure
must remain classified runtime/configuration failures; they must not become
ordinary account failures or expose native error text. Weston is used only as a
Wayland test compositor in CI and does not turn the desktop helper into a true
headless backend.

The Linux WebContext uses the service-derived profile directory, matching the
existing Windows profile boundary. No credentials, production URLs, network
navigation, downloads, screenshots, or third-party accounts are introduced.

## Rollback

Remove the Linux target-specific Wry/Tao dependencies and feature flags, restore
the Linux deferred helper build and smoke test, and revert the Linux runtime and
documentation changes in a subsequent CR. No database schema, credential data,
profile migration, or published browser binary is changed by the rollback.

## Breaking Change

Linux release stages now contain a WebKitGTK-backed `wry-desktop` helper and
require WebKitGTK 4.1 plus a GUI session at runtime. The versioned Go/Node/Rust
JSON Lines protocol remains compatible. The artifact does not provide a true
headless mode.

## Backport Target

none
