# CR-0014: establish the Rust browser runtime boundary

Base: main
Head or Range: b4fb833c4a8e173b4802285705a119e733fde8fe
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(browser): establish Rust runtime helper boundary
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: b4fb833c4a8e173b4802285705a119e733fde8fe
Head OID: b4fb833c4a8e173b4802285705a119e733fde8fe
Integrated Result: pending

## Summary

Split the real browser-runtime roadmap into explicit desktop WebView and
headless backends. Add the Rust helper protocol boundary that can be managed by
the existing Go Session Runner, while retaining the Node worker as a fake/deferred
lifecycle fixture. The helper has no platform GUI dependency in this slice and
does not claim real WebView or headless support.

Update architecture, operations, security, roadmap, and README documentation to
record Wry as the planned desktop WebView layer, WebView2/WKWebView/WebKitGTK
platform prerequisites, the Ubuntu 24.04 X11 Linux baseline, and the separate
CDP/WebDriver headless boundary.

## Motivation

The existing Node worker proves process and lease lifecycle behavior but cannot
represent the platform runtime prerequisites or the distinction between a hidden
desktop WebView and a true headless browser. A small Rust process boundary keeps
GUI event-loop ownership out of Go, gives future Wry code a stable capability and
error contract, and avoids coupling the domain state machine to a desktop UI
framework. Separating the backends also prevents an unsupported Linux or macOS
headless claim from leaking into scheduling and security behavior.

## Test Evidence

The Rust helper's protocol tests cover versioned hello, capability reporting,
session start, deferred failure, synthetic success/hold modes, cancellation, and
shutdown. Existing Go and Node lifecycle tests remain the fake-worker contract.
Documentation and CR validators must pass, with `cargo fmt --check`, `cargo test
--locked --manifest-path browser-runtime/Cargo.toml`, `go test ./...`, `npm
--prefix browser-worker test`, and `git diff --check` recorded when the relevant
toolchains are available.

## Risk

The helper introduces a second implementation of the Worker protocol, so message
and capability drift could break the Runner. The implementation is intentionally
dependency-light and keeps the deferred backend explicit; it cannot report a
browser success without a future backend. No credentials, arbitrary paths,
production URLs, browser binaries, GUI libraries, or network clients are added.
The local machine may lack Go/Node or Linux GTK development packages, so remote
CI remains authoritative for those checks.

## Rollback

Stop using the helper and continue with the existing Node fake worker. Revert the
documentation and helper source in a subsequent CR; no database schema, profile
data, credentials, or release artifact is changed by this boundary slice.

## Breaking Change

No existing Go/Node protocol message changes. A new Rust helper source tree and
documented runtime contract are added, but the current build/package scripts do
not enable a real WebView backend or change the published target artifacts.

## Backport Target

none
