# CR-0039: allow the WebView2 embedded HTML navigation

Base: main
Head or Range: a6ac3d9
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: fix(launcher): allow WebView2 data HTML navigation
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: e13d2d7068aacf630234d870d1390e0089cec67e
Head OID: a6ac3d906c222c8160e38e12efbe100814d3eef3
Integrated Result: pending

## Summary

Allow the exact lower-case `data:text/html;charset=utf-8;base64,` navigation
that WebView2 reports for Wry's `load_html(UI_HTML)` call. Keep the existing
`about:blank` variants and reject external, file, non-HTML data, and
case-altered URLs.

## Motivation

The v0.0.2 Windows launcher creates WebView2 successfully, but its navigation
handler sees the embedded page as a data URL and cancels it because only
`about:blank` was allowed. The resulting window remains an empty blank page.
The runtime diagnostics captured the exact URI and established that the HTML
and WebView2 runtime are otherwise available.

## Test Evidence

`cargo fmt --manifest-path launcher-ui/Cargo.toml -- --check`

`cargo test --locked --manifest-path launcher-ui/Cargo.toml --features desktop-webview`

`cargo clippy --locked --manifest-path launcher-ui/Cargo.toml --all-targets --features desktop-webview -- -D warnings`

`scripts/test_launcher_ui.sh`

`go test ./...`

`go vet ./...`

`./scripts/validate_policy_manifest.sh`

`./scripts/validate_quality_profile.sh`

`./scripts/validate_supply_chain_profile.sh`

`./scripts/validate_action_pinning.sh`

`./scripts/validate_repository_shape.sh`

`./scripts/test_build_contract.sh`

`git diff --check`

The local Node 26 subprocess harness was also attempted, but its child stdin
closes before the protocol handshake in this environment; a direct worker
protocol smoke test succeeds. The unchanged Node worker suite remains covered
by the remote Node 20 build workflow.

## Risk

The change only broadens the local inline navigation predicate to the exact
lower-case data-HTML prefix emitted by WebView2. Matching remains
case-sensitive, and external, file, non-HTML data, and altered-parameter URLs
remain denied. The accepted data prefix is intentionally limited to the
embedded Wry HTML loading form; launcher IPC and backend operations are
unchanged.

## Rollback

Revert the launcher predicate and tests, remove the v0.0.3 release note, and
rebuild the release package. No storage, credentials, or persisted profile
migration is required.

## Breaking Change

None. The launcher protocol, package layout, and existing navigation behavior
for blank documents remain compatible.

## Backport Target

none
