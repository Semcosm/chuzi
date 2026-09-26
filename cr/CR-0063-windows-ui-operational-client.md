# CR-0063: advance the Windows UI to an operational desktop client

Base: main
Head or Range: feat/windows-ui-client-foundation
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(windows): advance Slint client workflows
Revision: 2
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 2a69ed603dd8acbb2a61af16f25999f7b7b0946f
Head OID: 2a69ed603dd8acbb2a61af16f25999f7b7b0946f
Integrated Result: pending

## Summary

Advance the existing Rust + Slint Windows client from a static foundation to
an operational Core, launcher, and settings surface. Overview now communicates
installed, stopped, running, and unavailable Core states with guarded lifecycle
actions. Accounts and Tasks render redacted Core projections as structured
fields, validate IDs, prevent duplicate background operations, and expose
retryable feedback. Plugins use an explicit ID and display installed, trusted,
enabled, and health state without promoting an untrusted plugin to enabled.
Settings persist the selected update channel and interval while keeping UI theme
preferences separate from launcher behavior settings.

The shared Slint page header, status badge, and feedback bar establish a small
visual vocabulary. Layout snapshot diagnostics can select every page and verify
800x600, 1120x760, and 1440x900 without opening a native window.

## Motivation

The prior client proved the Core and launcher boundaries but presented most
results as concatenated developer strings. It did not reliably validate empty
inputs, expose plugin identity, or show useful success/error/empty states. The
desktop client needs to make the existing boundaries usable without reading
storage, credentials, browser profiles, or implementing business state logic.

## Test Evidence

`cargo fmt --manifest-path ui/windows/Cargo.toml -- --check`

`cargo check --manifest-path ui/windows/Cargo.toml`

`cargo test --manifest-path ui/windows/Cargo.toml` (3 UI behavior tests)

`cargo run --manifest-path ui/windows/Cargo.toml --example layout_snapshot -- --output dist/windows-layout`

The snapshot was also run with `--page plugins`, `--page accounts`,
`--page tasks`, and `--page settings` at all three supported sizes.

`go test ./internal/coreapi ./internal/coretransport ./internal/launcher`

`go test ./...`

`go vet ./...`

`./scripts/test_build_contract.sh`

`git diff --check`

## Risk

The change is confined to the Windows client and its snapshot example. Core
and launcher protocols, storage, credentials, and plugin trust policy are not
changed. Background operations still use the existing child-process and local
IPC boundaries; user-facing errors are classified before reaching Slint and do
not expose paths, stack traces, or secret material.

## Rollback

Revert the implementation changes and this CR. Existing Core API and launcher
state remain compatible with the prior Slint client.

## Breaking Change

No protocol or persistence breaking change. The plugin callbacks now require a
selected plugin ID, which is an internal UI callback contract and is validated
before the launcher command is issued.

## Backport Target

none
