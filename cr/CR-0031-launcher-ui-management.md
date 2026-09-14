# CR-0031: extend the Rust launcher UI with update and plugin management

Base: main
Head or Range: 082ab63a7d367022901b6604cdc10ca4fcf1e542..e7b8e0298d1636b119b1991f38551bd6315ab2ba
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(launcher): extend Rust UI management surface
Revision: 2
Status: accepted
Decision: accepted
Policy Version: v0.3
Base OID: e5cfab83535b1fa0e4ec39f8b284b4a8dfbe155a
Head OID: e5cfab83535b1fa0e4ec39f8b284b4a8dfbe155a
Integrated Result: pending

## Summary

Extend the existing Rust/Wry launcher UI MVP with an update status surface and
a plugin management view. The UI now calls the existing `check-update`,
`plugin-list`, plugin install/remove/enable/disable/trust/untrust CLI actions
through the versioned shell-free IPC bridge. It renders the candidate nightly
version and commit hash, plugin signer/capability/permission metadata, and
untrusted/disabled/healthy states without implementing release or trust policy
in the UI.

Refresh requests now remain in flight until initialize, component-list,
settings, and plugin-list have all completed. Automatic update checks are
requested at most once per refresh when the persisted setting enables them.
The CLI can infer the installed version from the local manifest for update
checks, while explicit `-current-version` remains supported.

## Motivation

CR-0028 supplied first-run component installation and settings, but left
existing backend update and plugin contracts inaccessible from the desktop
surface. Users otherwise need to leave the launcher to inspect a nightly
candidate or perform the explicit plugin trust workflow. This increment keeps
all download, manifest validation, signer allowlist, lock, extraction, and
rollback decisions in the Go launcher backend while making the already-tested
operations discoverable from the UI.

## Test Evidence

Local checks passed:

`cargo fmt --manifest-path launcher-ui/Cargo.toml -- --check`

`cargo test --locked --manifest-path launcher-ui/Cargo.toml`

`scripts/test_launcher_ui.sh`

`awk '/<script>/{inside=1; next} /<\\/script>/{inside=0} inside' launcher-ui/src/ui.html | node --check`

`go test ./... -count=1`

`go vet ./...`

`scripts/test_launcher_consumer.sh`

`scripts/test_nightly_package.sh`

`scripts/test_build_contract.sh`

`./scripts/validate_policy_manifest.sh`, `./scripts/validate_quality_profile.sh`,
`./scripts/validate_supply_chain_profile.sh`, `./scripts/validate_action_pinning.sh`,
`./scripts/validate_repository_shape.sh`, and `git diff --check`.

The UI harness uses a fake IPC host and synthetic plugin/update payloads. Go
tests use local temporary manifests and release fixtures only. No Chromium or
Edge is downloaded; no real account, credential, Matrix token, production URL,
CDP endpoint, or GitHub Release is contacted. Linux desktop-feature compilation
was not run locally because the machine lacks GTK/WebKitGTK development
packages; target-native compilation remains covered by the existing Actions
matrix.

## Risk

The UI gains more mutation buttons, but every operation remains serialized and
passes through the existing CLI allowlist. The UI does not parse manifests,
download archives, grant plugin trust, execute plugins, or decide business
success from endpoint discovery. Candidate update metadata is displayed only
after the Go checker validates target, channel, and version ordering. Signer
allowlists are explicit process configuration and are never inferred from UI
input.

## Rollback

Continue using the existing `chuzi-launcher` CLI and CR-0028 component/settings
UI, then revert this additive UI/protocol change. Launcher state, installed
components, plugin archives, settings, and the release-index format require no
migration or cleanup.

## Breaking Change

None. Existing CLI commands, manifest/index formats, service, Worker, browser,
credential, Matrix, account-state, and stable signed-release contracts are
unchanged. The UI IPC action allowlist is additive within
`chuzi.launcher-ui/v1`.

## Backport Target

none
