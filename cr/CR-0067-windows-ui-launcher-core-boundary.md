# CR-0067: route Windows UI Core operations through launcher

Base: main
Head or Range: feat/windows-ui-client-foundation
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(launcher): route Windows UI Core calls through launcher
Revision: 2
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 2a69ed603dd8acbb2a61af16f25999f7b7b0946f
Head OID: 2a69ed603dd8acbb2a61af16f25999f7b7b0946f
Integrated Result: pending

## Summary

Make `chuzi-launcher` the single native-client façade for Core lifecycle and
local API operations. The launcher now exposes `core-status`, `core-start`,
`core-stop`, and `core-call`, owns Core process detection/start/stop behavior,
and forwards Core API requests through the existing transport boundary.

The Windows Slint client no longer opens the Core named pipe, starts or stops
the Core process, or embeds Core transport types. Account, task, browser-view,
status, and lifecycle actions all invoke launcher commands. The component,
plugin, and settings projections continue to use the same launcher boundary.

## Motivation

The Windows UI must remain a presentation and interaction layer. Direct UI
access to Core IPC duplicated process ownership and transport details, made
restart behavior dependent on the UI process, and weakened the intended
launcher/Core boundary. Centralizing these operations keeps lifecycle,
endpoint derivation, and future platform clients behind one stable contract.

## Test Evidence

`GOTMPDIR=/home/chen/go-tmp-chuzi GOCACHE=/home/chen/.cache/go-build-chuzi go test ./...`

`GOTMPDIR=/home/chen/go-tmp-chuzi GOCACHE=/home/chen/.cache/go-build-chuzi go vet ./...`

`cargo test --manifest-path ui/windows/Cargo.toml` (5 UI tests)

`GOOS=windows GOARCH=amd64 go test ./internal/launcher -c`

`./scripts/test_build_contract.sh`

`git diff --check`

## Risk

Launcher startup and IPC forwarding become the required path for the Windows
client, so launcher errors are now surfaced where direct UI transport errors
were previously handled. Core process termination remains guarded by the
launcher-owned PID and executable identity checks. No credentials, Profile
paths, bbolt access, or arbitrary endpoint inputs are exposed to the UI.

The plugin page remains a launcher package/trust/enablement projection; a
dynamic Core adapter registry and runtime-health API are separate follow-up
work and are not claimed by this change.

## Rollback

Revert the commit containing this CR and the launcher/UI boundary changes. The
previous Windows client implementation can be restored together with its
direct Core client if an urgent rollback is required.

## Breaking Change

No Core wire-protocol or persistence schema change. Windows UI integrations
must use the launcher commands instead of the deleted direct `core_client`
module; this is an internal client-boundary change.

## Backport Target

none
