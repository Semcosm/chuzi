# CR-0060: fix Windows Core pipe readiness and WinUI startup layout

Base: main
Head or Range: fix/windows-ui-startup
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: fix(windows): wait for Core pipe readiness before UI calls
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: pending
Head OID: pending
Integrated Result: pending

## Summary

Harden the Windows client boundary after a named-pipe connection is reported
complete but before the stream accepts its first write. The client now waits
for the connected state during the `chuzi.core/v1` handshake and reconnects
automatically before account and task calls when a prior connection ended.
The main window also follows the minimal WinUI Gallery navigation layout and
keeps Settings in the ordinary menu collection for WinUI 3 XAML compatibility.

## Motivation

Users could reach a Core operation with a stale or transitional pipe and see
the platform error `pipe hasn't been connected yet`. The previous navigation
layout could also fail during XAML loading, preventing the user from reaching
Core controls at all.

## Test Evidence

Local checks:

`go test ./...`

`go vet ./...`

`./scripts/test_build_contract.sh`

`./scripts/validate_repository_shape.sh`

`git diff --check`

The Windows build and installed-client smoke test remain authoritative because
the local checkout does not contain the Windows App SDK toolchain.

## Risk

The change is limited to the native client transport and navigation markup. A
failed connection still returns the existing stable `CoreApiException` and does
not alter Core storage, credentials, or the service protocol.

## Rollback

Revert this CR's implementation commit. Core API and persisted data remain
compatible with the previous client.

## Breaking Change

No.

## Backport Target

none
