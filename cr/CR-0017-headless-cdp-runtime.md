# CR-0017: establish the external Chromium headless-CDP runtime boundary

Base: main
Head or Range: 7bd1999d15f7e10254cee0271f3df1d2295d3d9a
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(browser): add external Chromium headless CDP boundary
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 7bd1999d15f7e10254cee0271f3df1d2295d3d9a
Head OID: 7bd1999d15f7e10254cee0271f3df1d2295d3d9a
Integrated Result: pending

## Summary

Add an explicit `headless` browser backend backed by the Node worker protocol.
The worker launches a deployment-provided Chromium/Edge executable with fixed
headless and loopback CDP arguments, a dynamically selected port, and the
service-generated account Profile. It polls and validates `/json/version`, then
returns a redacted session handle and endpoint metadata for a future automation
adapter. It terminates the external browser on cancellation, shutdown, failure,
or session completion. The existing deferred Node backend remains the default;
Rust/Wry remains explicit.

This slice deliberately does not download or package Chromium, connect to real
accounts, inject credentials, execute page actions, or report business success.
Without an operation model the headless worker reports the classified
`configuration/automation_not_configured` fact after CDP discovery.

## Motivation

The project needs a true no-display runtime boundary without treating a hidden
desktop WebView as headless. Keeping external browser ownership in a separate
Node adapter preserves the Go Session Runner, lease, timeout, cancellation, and
Profile boundaries while leaving service-specific page automation to an
authorized adapter with its own contract.

## Test Evidence

Added a fake local CDP browser fixture and Node contract coverage for handshake,
loopback endpoint discovery, endpoint validation, discovery timeout, Profile
argument isolation, cancellation, shutdown, and relative-path rejection. Added
Go ProcessFactory coverage for safe script argument forwarding and service
backend selection/rejection. Updated the build contract to require the
headless worker and capability marker.

Local evidence:

- `git diff --check`
- `./scripts/test_build_contract.sh`
- `./scripts/validate_repository_shape.sh`
- `cargo fmt --manifest-path browser-runtime/Cargo.toml -- --check`
- `cargo test --offline --locked --manifest-path browser-runtime/Cargo.toml`

The local environment does not provide Go, Node.js, npm, or Linux GTK/WebKitGTK
development packages; Go/Node tests and the four-target native build remain
GitHub Actions evidence. No live browser, account, credential, production URL,
or Matrix token is used.

## Risk

The worker starts an external executable and exposes a loopback CDP endpoint to
the future automation adapter. The command is passed as one executable value,
never shell-parsed; fixed arguments bind CDP to `127.0.0.1`, use a service-
generated Profile, and suppress browser stdout/stderr. Endpoint metadata is
redacted to host, port, and protocol fields; full WebSocket URLs and arguments
are not emitted. Invalid paths, unavailable browsers, endpoint errors, process
crashes, and cancellation fail closed as classified runtime facts. The worker
does not access credentials or choose account business state.

## Rollback

Stop selecting `-browser-backend headless` and continue with the default Node
deferred worker or explicit Rust helper. Revert the worker, service option,
tests, build-contract, documentation, and this CR in a subsequent governance
change; no database migration or runtime data cleanup is required.

## Breaking Change

No existing protocol message or default backend changes. `ProcessConfig` gains
optional non-shell `ScriptArgs`; a new explicit `headless` backend and
`-headless-browser-command` option are available. Production automation remains
unavailable until a later adapter CR defines operations and success semantics.

## Backport Target

none
