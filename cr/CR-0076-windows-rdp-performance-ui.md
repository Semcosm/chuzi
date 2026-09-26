# CR-0076: add integrated RDP performance capture

Base: main
Head or Range: feat/windows-ui-client-foundation
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(ui): add integrated RDP performance capture
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 6f92f28bb70d52adbd9576fcdfef4103c30d0898
Head OID: 1e8706fac458042b37c4617b89f7227497c4d2ed
Integrated Result: pending

## Summary

Add independent RDP performance HUD and data recording options to the Windows
session UI. Bundle the SHA-256-pinned PresentMon 2.6.0 executable as a required
Windows release component, and store session logs under the Chuzi ProgramData
directory. The Windows uninstaller asks whether to retain user data and defaults
to keeping it.

## Motivation

RDP performance measurement currently requires manually starting PresentMon and
collecting stderr in a shell. Integrating capture into the session makes the
measurements available during normal use and keeps the output with Chuzi's other
data so uninstall cleanup has one well-defined scope.

## Test Evidence

`cargo test --manifest-path ui/windows/Cargo.toml --locked` passed (15 tests);
`cargo fmt --manifest-path ui/windows/Cargo.toml -- --check`, `go test ./...`,
and the browser-worker test suite (20 tests) passed. Overview, Settings, and RDP
login snapshots passed at 1280x800; Settings and RDP login also rendered at
500x800. Policy, quality, supply-chain, action-pinning, repository-shape, and
build-contract validators passed, as did `git diff --check`. PresentMon 2.6.0's
upstream troubleshooting documentation confirms its ETW access requirement.
Windows Rust target and Inno Setup are unavailable on this host, so the Windows
binary and installer still require the Windows Actions build for compile-time
and packaging verification.

## Risk

PresentMon may not be able to collect presentation events on every Windows
configuration. The HUD reports collector status and the required ETW access
while internal RDP counters remain available; Chuzi does not auto-elevate the
collector. Recorded output contains redacted performance counters and
PresentMon CSV, not remote desktop pixels or RDP credentials. Removing the
ProgramData directory is destructive only after the user explicitly chooses to
discard data in the uninstaller.

## Rollback

Revert this CR and the Windows RDP performance UI, capture, packaging, and
uninstaller changes. Existing performance logs can be deleted from
`%ProgramData%\chuzi\data\rdp-performance` after they are no longer needed.

## Breaking Change

None to Core, launcher, or storage contracts. Windows release manifests gain a
required `presentmon` component.

## Backport Target

none
