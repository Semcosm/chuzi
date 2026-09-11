# CR-0016: assemble the persistent service runtime entry point

Base: main
Head or Range: 09a7a6237a97ca6c6e87727cffa4948684f0ad1b..12d5f0bc43acb47b759848bb0fc7003e1057a4c6
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(service): assemble persistent scheduler runtime
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 09a7a6237a97ca6c6e87727cffa4948684f0ad1b
Head OID: 12d5f0bc43acb47b759848bb0fc7003e1057a4c6
Integrated Result: pending

## Summary

Assemble the existing configuration, bbolt Store, Request Service, Session
Runner, and Queue Scheduler in `cmd/service`. Normal startup now loads the
configured data directory, opens and migrates the persistent database, restores
expired leases and suspended requests through the scheduler loop, and closes
the Store on shutdown. The entry point does not create sample accounts or
requests and does not add Matrix networking, credential ingress/use, or real
account automation.

The Node process worker remains the default backend for compatibility with the
deferred/synthetic lifecycle contract. The Rust JSONL helper can be selected
explicitly with `-browser-backend rust` and `-browser-runtime`; its no-argument
process invocation is represented by the extended `ProcessConfig.Args` boundary.

## Motivation

The domain, storage, queue, and worker lifecycle packages were individually
implemented but the executable still needed a durable composition root. Making
this assembly explicit gives service restart, lease recovery, timeout, cancel,
retry, and profile ownership one runtime path while keeping business-state
decisions in the account state machine and scheduler. Explicit backend selection
also prevents the Rust desktop WebView helper from being mistaken for the
default or for a true headless browser.

## Test Evidence

Added service assembly tests covering configuration-derived Store opening,
schema migration visibility, close/reopen persistence, non-nil Request Service,
Session Runner and Scheduler boundaries, valid Node/Rust backend factory
selection, unknown backend rejection, and default option validation. Added
ProcessFactory tests covering the legacy `<command> <script> --stdio` argument
contract, explicit no-argument Rust helper mode, and invalid mixed arguments.

Local evidence:

- `git diff --check`
- `./scripts/test_build_contract.sh`
- `./scripts/validate_repository_shape.sh`
- `cargo fmt --manifest-path browser-runtime/Cargo.toml -- --check`
- `cargo test --offline --locked --manifest-path browser-runtime/Cargo.toml`

The local environment does not provide Go, gofmt, Node.js, or npm; therefore
`go test ./...`, `go vet ./...`, and the Node worker tests require the GitHub
Actions toolchain. The desktop-feature Cargo test was attempted but is blocked
locally by missing GTK/WebKitGTK/libsoup development packages; native target CI
remains authoritative for that feature and Linux X11/Wayland smoke coverage.

## Risk

The composition root introduces process startup and long-running scheduling to
the executable, so invalid paths, missing worker binaries, or runtime failures
now fail during startup or scheduler execution. The scheduler remains the sole
owner of durable state transitions, and all worker results stay redacted runtime
facts. Rust selection is opt-in; the default behavior continues to use the Node
deferred worker. No credentials, browser profiles from user input, network
clients, or production data are added.

## Rollback

Stop the new executable composition and revert this CR's Go/process changes;
continue using the existing library boundaries and Node self-test. The database
schema is unchanged, and an opened Store can be closed or reopened without a
data migration rollback.

## Breaking Change

Normal `cmd/service` startup changes from a one-shot placeholder path to a
persistent scheduler loop and now requires a readable configuration file; a
usable Node worker executable is required when a queued request is processed by
the default backend. `-self-test` retains its existing
temporary smoke-test behavior. The Worker JSONL protocol and storage schema are
unchanged.

## Backport Target

none
