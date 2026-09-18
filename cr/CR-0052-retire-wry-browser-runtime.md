# CR-0052: retire the Rust/Wry browser runtime

Base: main
Head or Range: 61f244390bc52de749eda42a706b89560b2f2294..1e74027c8ef3e3fc34e9d2f8e4683b385ea68fe7
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: refactor: retire rust wry browser runtime
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 61f244390bc52de749eda42a706b89560b2f2294
Head OID: 1e74027c8ef3e3fc34e9d2f8e4683b385ea68fe7
Integrated Result: pending

## Summary

Retire the abandoned Rust/Wry desktop browser runtime path. Remove the Rust
helper source and Cargo metadata, the `rust` browser backend and
`-browser-runtime` service option, runtime build and WebKitGTK smoke scripts,
CI jobs, runtime artifacts, and release manifest fields. Keep the Node deferred
and explicit Node headless-CDP workers as the only service browser backends.

Native WinUI, SwiftUI/AppKit, and GTK clients use the `chuzi.core/v1` Core API;
they do not depend on Wry or any browser WebView runtime.

## Motivation

The original Rust/Wry component was planned for a cross-platform UI path that
has since been abandoned. The repository's native-client direction is a Core
API boundary, while browser automation is a service-side Node worker boundary.
Leaving the old helper selectable and publishable made the CLI, build matrix,
release artifacts, and architecture documents claim a supported path that is
no longer part of the design.

## Test Evidence

The implementation commit was verified with:

`./scripts/test_build_contract.sh`

`./scripts/test_nightly_package.sh`

`./scripts/test_nightly_artifact_validator.sh`

`./scripts/validate_policy_manifest.sh`

`./scripts/validate_quality_profile.sh`

`./scripts/validate_supply_chain_profile.sh`

`./scripts/validate_action_pinning.sh`

`./scripts/validate_repository_shape.sh`

`GOCACHE=/tmp/chuzi-go-cache go test ./... -run '^$'`

`GOCACHE=/tmp/chuzi-go-cache go vet ./...`

`git diff --check`

The full Go test run remains environment-limited in this sandbox: the service
httptest case cannot open an IPv6 loopback listener, and the browser fake-CDP
case cannot establish its local runtime connection. These failures do not
involve the removed Rust/Wry path.

## Risk

This is a deliberate breaking change for callers that selected
`-browser-backend rust` or supplied `-browser-runtime`; those options now fail
validation. Existing release consumers that install a `desktop-runtime`
component must stop requesting that component. Native UI clients are not
affected because they already use the Core API boundary. Node worker protocol,
storage, account state, credentials, Matrix, and Core API contracts remain
unchanged.

## Rollback

Revert the implementation commit through a subsequent UGS CR. A rollback would
restore the Rust helper, build jobs, runtime artifacts, and CLI options; no
runtime data or credentials are modified by this change.

## Breaking Change

Yes. The `rust` browser backend, `-browser-runtime` flag, Rust/Wry runtime
artifacts, `desktop-runtime` component, and their build/smoke entry points are
removed. Deployments must use the Node deferred or explicit headless-CDP
backend, and native clients must use Core API.

## Backport Target

none
