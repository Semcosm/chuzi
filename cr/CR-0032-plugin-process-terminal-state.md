# CR-0032: make plugin process termination deterministic

Base: main
Head or Range: e799591f24c891ed25289a7ad83e200a46bd58a8
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: fix(plugin): make process pipe termination deterministic
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: e799591f24c891ed25289a7ad83e200a46bd58a8
Head OID: e799591f24c891ed25289a7ad83e200a46bd58a8
Integrated Result: pending

## Summary

Make the isolated plugin process protocol stream independent of `exec.Cmd`
pipe ownership. The client now creates and closes explicit parent/child pipes,
so `Cmd.Wait` cannot close the protocol reader while the JSONL read loop is
decoding a terminal message. EOF and an explicitly closed protocol pipe are
classified as `ErrProcessExited`, while malformed data remains an `ErrProtocol`
failure. A regression test covers the reader state after process cancellation.

## Motivation

The Linux build matrix exposed a timing-dependent failure in which a crashed
plugin was reported as `plugin: protocol failure: decode: read |0: file already
closed`. `exec.Cmd.Wait` may release a `StdoutPipe` before the read loop observes
the child exit, making the same crash alternate between a stable process-exit
error and a misleading protocol error. The terminal result must be stable so
the scheduler can classify and recover plugin crashes deterministically.

## Test Evidence

Local checks passed:

`go test ./internal/plugin -run 'TestClientTimeoutAndPluginCrashAreTerminal' -count=500`

`go test -race ./internal/plugin -count=20`

`go test ./... -count=1`

`go test -race ./... -count=1`

`go vet ./...`

`go build ./...`

`npm --prefix browser-worker test`

`scripts/test_launcher_consumer.sh`

`scripts/test_launcher_ui.sh`

`scripts/test_nightly_package.sh`

`scripts/test_build_contract.sh`

`cargo fmt --manifest-path browser-runtime/Cargo.toml -- --check`

`cargo test --locked --manifest-path browser-runtime/Cargo.toml -- --nocapture`

`cargo fmt --manifest-path launcher-ui/Cargo.toml -- --check`

`cargo test --locked --manifest-path launcher-ui/Cargo.toml`

`./scripts/validate_policy_manifest.sh`, `./scripts/validate_quality_profile.sh`,
`./scripts/validate_supply_chain_profile.sh`,
`./scripts/validate_action_pinning.sh`,
`./scripts/validate_repository_shape.sh`,
`./scripts/validate_document_map.py`, and `git diff --check`.

Cross-compilation of the plugin package also passed for Windows amd64 and
Linux arm64. All fixtures use local helper processes, temporary paths, and
synthetic protocol data. No Chromium or Edge is downloaded, and no real
account, credential, Matrix token, production URL, or CDP endpoint is used.

## Risk

The process boundary still launches only the explicitly configured executable
with shell-free arguments. Explicit pipes change ownership and cleanup only;
they do not change protocol messages or operation deadlines. A closed protocol
stream now consistently fails closed as a process exit, while malformed JSON
and invalid envelopes retain protocol-error classification. The existing caller
context remains the only operation wait limit.

## Rollback

Revert the plugin process/client change and its regression test in a later
governance change. No database schema, launcher state, installed component,
credential, or runtime data is changed.

## Breaking Change

None. `internal/plugin` APIs and the `chuzi.adapter/v1` message contract are
unchanged. The change only makes process-exit error classification deterministic
across supported operating systems.

## Backport Target

none
