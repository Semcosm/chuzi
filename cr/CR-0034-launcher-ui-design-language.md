# CR-0034: add the CHUZI launcher design language

Base: main
Head or Range: 415789bbaaf351ea30ea66c9f1ffd297e03e6838
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(launcher): add CHUZI design language and appearance controls
Revision: 4
Status: integrated
Decision: accepted
Policy Version: v0.3
Base OID: 50b76d649c66ed37ed7083ad2457ff2ab2257a62
Head OID: 50b76d649c66ed37ed7083ad2457ff2ab2257a62
Integrated Result: main@50b76d649c66ed37ed7083ad2457ff2ab2257a62

## Summary

Refresh the Rust/Wry launcher surface with a compact CHUZI control-desk
layout. The page now has a workspace sidebar, release and resource overview,
clearer component/plugin rows, responsive settings, and a shared token system
for light and dark themes. Appearance controls expose Solid, Frosted Glass,
Mica, and Liquid Glass material treatments and persist the selection in the
launcher WebView profile.

The page keeps the existing `chuzi.launcher-ui/v1` request queue, component and
plugin actions, update checks, first-run flow, settings payload, and cancellation
behavior. Appearance state remains local UI state and is not sent to the Go
launcher backend. The stable release workflow also derives the package version
from a semver tag on both Unix and Windows runners, so `v0.0.1` archives retain
their stable version in manifests and indexes.

## Motivation

The existing launcher UI exposed the complete management contract but used a
plain document-style presentation with no coherent theme or material system.
The new visual language makes the existing operations easier to scan while
keeping policy, installation, trust, and rollback decisions in the existing Go
backend.

## Test Evidence

`./scripts/test_launcher_ui.sh`

`go test ./...`

`go vet ./...`

`cargo fmt --manifest-path launcher-ui/Cargo.toml -- --check`

`git diff --check`

The launcher UI contract harness also parses the embedded JavaScript with
Node.js and exercises refresh serialization, automatic update checks, plugin
trust, component queueing, cancellation, and failure recovery.

Playwright/Chromium smoke coverage rendered the embedded page at 1200x780 and
700x760. Both viewports had no horizontal overflow; the smoke flow switched to
Dark + Liquid Glass, verified the persisted `chuzi.appearance` value, and
captured the overview/settings surfaces with synthetic local IPC data.

The Linux `desktop-webview` feature also compiled and passed its Rust tests.
Under `xvfb-run` with WebKitGTK 4.1, the native Wry process stayed alive for
the smoke window and `strace` confirmed real UI bridge launches of
`initialize`, `component-list`, `settings`, and `plugin-list` against a local
synthetic release manifest. No network, account, or credential data was used.

The stable package workflow was checked for tag-version propagation, and
`releases/v0.0.1.md` documents the signed annotated release.

## Risk

The change is isolated to the embedded launcher HTML. The main runtime risk is
WebView-specific CSS feature support for backdrop blur and translucency; Solid
material remains the compatible fallback and all controls preserve semantic
HTML behavior when visual effects are unavailable.

## Rollback

Revert `launcher-ui/src/ui.html` and this change record. The Rust IPC bridge and
Go launcher backend are unchanged.

## Breaking Change

None. Existing UI action names, payloads, and backend boundaries remain
unchanged.

## Backport Target

none
