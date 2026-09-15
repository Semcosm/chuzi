# CR-0038: fix Windows launcher UI blank page

Base: main
Head or Range: e8e311ecefcf56b7253a5a41cbd97d013bf63b8f
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: fix(launcher): load embedded UI after WebView creation
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: fe588949de600cdb899ab84e4be9d0a3b7e429ac
Head OID: e8e311ecefcf56b7253a5a41cbd97d013bf63b8f
Integrated Result: pending

## Summary

Fix the Rust/Wry launcher UI startup path that could leave the Windows WebView2
window at an empty `about:blank` document. Create the native WebView from an
explicit blank URL first, then load the embedded UI HTML after the WebView has
been created. Keep navigation restricted to the inline `about:blank` document,
and add opt-in diagnostics for runtime, page-load, navigation, and script/load
errors.

## Motivation

The v0.0.1 Windows launcher could create a visible WebView2 window while the
embedded `NavigateToString` page remained empty. The UI HTML and WebView2
runtime were present, so the failure was in the initial load timing and was
silent because load and script errors were discarded. Moving the HTML load after
creation makes the startup sequence observable and avoids weakening the local
navigation boundary.

## Test Evidence

`cargo fmt --manifest-path launcher-ui/Cargo.toml -- --check`

`cargo test --locked --manifest-path launcher-ui/Cargo.toml`

`cargo test --locked --manifest-path launcher-ui/Cargo.toml --features desktop-webview`

`cargo clippy --locked --manifest-path launcher-ui/Cargo.toml --all-targets --features desktop-webview -- -D warnings`

`scripts/test_launcher_ui.sh`

`git diff --check`

Windows WebView2 GUI verification remains a release-runner/user-machine check;
the release package exposes `CHUZI_LAUNCHER_UI_DIAGNOSTICS=1` for native stderr
diagnostics.

## Risk

The change affects only the desktop WebView construction and diagnostics. The
embedded page remains local, external/file navigation remains denied, and
launcher IPC and backend operations are unchanged. The load error is classified
as `webview_load_failed` without exposing native details unless diagnostics are
explicitly enabled.

## Rollback

Revert the launcher UI source and operations note, then rebuild the release
package. No storage, credentials, or persisted profile migration is required.

## Breaking Change

None. The launcher protocol and release package contents remain compatible.

## Backport Target

none
