# CR-0028: add the Rust cross-platform launcher UI MVP

Base: main
Head or Range: 1b4060079b7fe3d684738b76e933a2d3f5fb790f..dd73527416b86de1cf2d3c8f490ac20ef7712456
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(launcher): add Rust/Wry launcher UI MVP
Revision: 3
Status: integrated
Decision: accepted
Policy Version: v0.3
Base OID: 17a7609834077f56287351fb066f1890297b4d1f
Head OID: 17a7609834077f56287351fb066f1890297b4d1f
Integrated Result: main@17a7609834077f56287351fb066f1890297b4d1f

## Summary

Add the first Rust/Wry launcher UI for the componentized nightly package.
`chuzi-launcher-ui` embeds a small local management surface with first-run
component selection, install progress and cancellation, component
enable/disable/remove/repair actions, and durable behavior settings. The UI
uses a versioned `chuzi.launcher-ui/v1` IPC message shape and invokes the
existing `chuzi-launcher` Go CLI with direct, shell-free arguments.
Requests are serialized in the UI bridge so refreshes and repeated clicks do
not race the install lock; cancellation is an out-of-band control message and
does not impose a business timeout.

The launcher component now carries both binaries. The UI bridge never owns
manifest parsing, archive download, hashing, installation locks, plugin trust,
or rollback behavior; those policies remain in `internal/launcher`.

## Motivation

CR-0027 completed network-backed component downloads and the nightly release
index, while CR-0026 supplied durable settings, operation locks, progress, and
the process boundary needed by a desktop caller. A user still has to assemble
the same workflow by hand from CLI commands. This increment provides the first
usable desktop entry point without introducing a second implementation of
filesystem or release policy.

## Test Evidence

Local verification must include:

`cargo fmt --manifest-path launcher-ui/Cargo.toml -- --check`

`cargo test --locked --manifest-path launcher-ui/Cargo.toml`

`scripts/test_launcher_ui.sh`

The launcher UI script includes a Node VM interaction harness that exercises
serialized refresh/action ordering, cancellation, and error recovery with a
fake IPC host; it never starts a WebView or a real launcher process.

`go test ./... -count=1`

`go vet ./...`

`scripts/test_build_contract.sh`

`scripts/test_nightly_package.sh`

The nightly package contract also verifies that manifest generation fails when
the required launcher UI binary is absent.

`git diff --check`

Native desktop-feature compilation and GUI smoke remain target-runner work.
Tests use only local HTML, synthetic launcher responses, temporary paths, and
fake release artifacts. No Chromium, real account, credential, Matrix
endpoint, or production release host is contacted.

## Risk

The UI can request mutations through a sibling executable. The bridge passes a
fixed allowlist of launcher commands, uses direct process arguments without a
shell, validates request IDs and paths, and reports only classified process
errors. Settings input is streamed through stdin rather than written to a
second persistent file. Child processes are tracked and can be cancelled or
terminated when the window closes; there is no operation timeout that can
misclassify a slow install as a business failure.

Wry/WebKitGTK/WebView2/WKWebView runtime availability remains a platform
deployment prerequisite. The UI does not download a browser runtime and does
not turn endpoint discovery or component installation into account success.

## Rollback

Continue using `chuzi-launcher` CLI and the existing local/network component
managers, and stop shipping `chuzi-launcher-ui` from the launcher component.
Revert the UI crate, package wiring, CLI stdin marker, tests, and documentation
in a later governance change. Existing launcher state and settings are
unchanged, so no migration or data cleanup is required.

## Breaking Change

None to the service, Worker protocols, Rust browser runtime, account state
machine, storage schema, release index format, or existing launcher commands.
The launcher component gains an additional executable and the additive UI IPC
contract.

## Backport Target

none
