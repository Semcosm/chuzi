# CR-0014: add the Wry desktop WebView vertical slice

Base: main
Head or Range: fb34e81e9922aead7aae0518b0ef55bcdd419b4d..ffac3fb814090abf2aad13013cef1be35c10ba76
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(browser): add Wry desktop WebView vertical slice
Revision: 2
Status: accepted
Decision: accepted
Policy Version: v0.3
Base OID: 8051e82b4305ee280e0b0ba0f85502e46f4af488
Head OID: 8051e82b4305ee280e0b0ba0f85502e46f4af488
Integrated Result: pending

## Summary

Add the first real desktop WebView backend behind the existing Rust JSONL
helper. The helper is built with Rust 1.85-compatible code and uses Wry 0.57.
Windows 10/11 uses WebView2 and a service-derived
WebContext directory; macOS 11+ Apple Silicon uses WKWebView with an isolated
ephemeral data store for this local-page-only slice. The Linux helper remains
the deferred backend until the separate WebKitGTK CR.

The backend creates visible or hidden windows, loads only an embedded local
test page, denies navigation outside the inline page, denies permissions and
downloads, accepts one fixed readiness IPC message, and reports only redacted
protocol facts. Target-gated packaging includes the helper binary on all
targets, with the Wry feature enabled only for Windows and macOS.

## Motivation

The Rust helper boundary is now stable, but it still cannot exercise a real
platform WebView. A small Wry slice validates event-loop ownership, runtime
discovery, local-page IPC, cancellation, and packaging without introducing
network navigation, credentials, production URLs, or a Chromium download.

Wry's macOS data-store identifier API is only available on macOS 14+, so the
macOS 11+ baseline must use an ephemeral store until a later persistence design
is reviewed. This change therefore does not authorize real account credentials
or claim persistent macOS profiles.

## Test Evidence

Local checks pass for `cargo fmt --manifest-path browser-runtime/Cargo.toml
-- --check`, `cargo test --locked --manifest-path browser-runtime/Cargo.toml`,
`cargo test --locked --manifest-path browser-runtime/Cargo.toml --features
desktop-webview`, `cargo clippy --locked --manifest-path
browser-runtime/Cargo.toml --all-targets -- -D warnings`,
`cargo metadata --locked --manifest-path browser-runtime/Cargo.toml`,
`scripts/test_build_contract.sh`, all repository policy/quality/supply-chain,
action-pinning, and repository-shape validators, and `git diff --check`.
On the local Linux host the desktop feature is target-gated, so that feature
test intentionally exercises the deferred implementation; native Wry compile
and GUI execution require the Windows/macOS runners. The native four-target
Actions run must therefore verify Windows/macOS Wry compilation and helper
packaging, plus Linux amd64/arm64 deferred smoke tests, before integration.
Desktop runtime execution remains conditional on a GUI session and uses only
the embedded local page.

## Risk

Wry adds platform-native dependencies and can fail when WebView2, WKWebView, a
GUI session, or the event loop is unavailable. Startup and page failures map to
stable runtime/configuration errors and never expose native error text. The
macOS backend is intentionally ephemeral and cannot be used for persistent
credential sessions. Linux does not gain GTK dependencies or a false WebView
claim in this slice.

## Rollback

Stop packaging the Wry helper and use the deferred Rust/Node worker boundary.
Revert the target-gated dependency, backend source, packaging, CI, and document
changes in a subsequent CR; no database schema, credentials, or profile data
are migrated.

## Breaking Change

The Windows and macOS release stages gain a native Rust helper executable and
the build manifest records its backend. The existing Go/Node protocol remains
compatible. No production browser navigation or credential flow is enabled.

## Backport Target

none
