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
The unpackaged UI now constructs the navigation window and all five pages in
C# because loading even minimal page XAML reproducibly crashes in
`Microsoft.UI.Xaml` on the Windows CI runner. The XAML files remain aligned
layout references for the WinUI Gallery-based implementation.

## Motivation

Users could reach a Core operation with a stale or transitional pipe and see
the platform error `pipe hasn't been connected yet`. The previous navigation
layout and each content page could fail during XAML loading, preventing the
user from reaching Core controls at all. A Windows host investigation also
confirmed that Core starts and completes the `chuzi.core/v1` named-pipe
handshake; the installed UI on that host predated the transport retry fix.

## Test Evidence

Local checks:

`go test ./...`

`go vet ./...`

`./scripts/test_build_contract.sh`

`./scripts/validate_repository_shape.sh`

`git diff --check`

Windows host validation: `dotnet build` succeeded after setting
`WindowsSdkPackageVersion=10.0.19041.38`; the initial attempt with the host's
older .NET SDK failed before compilation because it selected an older Windows
SDK reference package. The GitHub Actions Windows build and installed-client
smoke test remain authoritative for the final package.

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
