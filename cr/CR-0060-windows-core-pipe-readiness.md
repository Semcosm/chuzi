# CR-0060: fix Windows Core pipe readiness and WinUI startup layout

Base: main
Head or Range: fix/windows-ui-startup
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: fix(windows): wait for Core pipe readiness before UI calls
Revision: 5
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: ff980189caebdacac58dd6d86b0edc51cc60ccfa
Head OID: f6a6387b36a313bee82633b30212ad6da184ca0f
Integrated Result: pending

## Summary

Harden the Windows client boundary after a named-pipe connection is reported
complete but before the stream accepts its first write. The client now waits
for the connected state during the `chuzi.core/v1` handshake, confirms that
the stream remains connected after the hello response, and reconnects
automatically before account and task calls when a prior connection ended.
The Windows client also settles the pipe handle after ConnectAsync and uses a
complete synchronous frame write so the platform's transient first-write race
cannot surface as `pipe hasn't been connected yet`.
The UI keeps the full WinUI Gallery-style XAML navigation and page tree used by
the successful installed-client smoke baseline; the failed C#-constructed page
tree experiment is removed. Each write also waits for the stream's connected
state so a transient Windows named-pipe race cannot reach the operation error
surface.

## Motivation

Users could reach a Core operation with a stale or transitional pipe and see
the platform error `pipe hasn't been connected yet`. A Windows host
investigation confirmed that Core starts and completes the `chuzi.core/v1`
named-pipe handshake, so the client must keep the connection readiness check
at the transport boundary. The successful WinUI Gallery-style XAML baseline is
also retained to avoid introducing native XAML startup regressions.

## Test Evidence

Local checks:

`go test ./...`

`go vet ./...`

`./scripts/test_build_contract.sh`

`./scripts/validate_repository_shape.sh`

`git diff --check`

The Windows client write path was updated to avoid the asynchronous first-frame
race; the Windows build and named-pipe smoke remain authoritative for the
platform-specific compile and runtime check.

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
