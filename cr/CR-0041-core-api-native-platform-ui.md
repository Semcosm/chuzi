# CR-0041: establish the Core API boundary for native platform clients

Base: main
Head or Range: 61f244390bc52de749eda42a706b89560b2f2294..a9fd59f6533e0212da3d51b8f60d08c0571ca237
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(ui): establish Core API boundary for native platform clients
Revision: 2
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 61f244390bc52de749eda42a706b89560b2f2294
Head OID: a9fd59f6533e0212da3d51b8f60d08c0571ca237
Integrated Result: pending

## Summary

Replace the unsuccessful Tauri launcher-shell direction from revision 1 with a
transport-neutral Core API boundary for native platform clients. The boundary
is exposed as `chuzi.core/v1` through `internal/coreapi` and `internal/core`,
while `cmd/launcher` remains UI-neutral.

The target client set is intentionally platform-native: Windows uses WinUI 3,
macOS uses SwiftUI with AppKit where required, and Linux uses GTK. Each client
will consume the same Core API and remain responsible only for views,
interaction, accessibility, and platform lifecycle. This CR establishes the
shared boundary and target contracts; it does not claim that all three client
implementations are complete. The Windows client is the first target; macOS
and Linux clients must be delivered and reviewed in follow-up CRs.

The former Tauri implementation, its embedded launcher UI, and its release
wiring are removed from the active product path. The existing launcher CLI,
service, account state machine, storage, credential, Matrix, and browser
boundaries remain the source of truth.

## Motivation

The revision-1 Tauri migration coupled a web-rendered launcher surface to a
desktop shell and did not provide the platform-native behavior required for
the product. Continuing to extend that shell would preserve the failure mode
and make Windows, macOS, and Linux behavior depend on one WebView-oriented
implementation.

The Core API separates product capabilities from presentation and gives each
native framework a stable, transport-neutral contract. The API returns DTOs,
stable error categories, redacted progress and status, and capability-scoped
commands; it does not expose bbolt objects, credentials, browser Profiles,
filesystem paths, or UI types.

Revision 2 now includes the first callable transport increment: `internal/coretransport`
freezes the JSONL `chuzi.core/v1` envelope, `hello` negotiation, stable error payload,
request-ID multiplexing and transport cancellation. `cmd/service` instantiates the Core
facade and serves it over a data-directory-derived Unix socket (`0600`) or an owner-only
Windows named pipe. All native clients must use this boundary.

The first follow-up client is now included in this revision: `ui/windows` is a WinUI 3
unpackaged client using `NamedPipeClientStream`. It performs the same endpoint derivation,
negotiates `chuzi.core/v1`, and exposes only submit/query/cancel interactions. The
`build_windows_ui.ps1` script and `chuzi-build-windows-ui` Actions job publish it as a
separate Windows zip, leaving the service package and persistence boundary untouched.

## Test Evidence

The implementation evidence includes:

`go test ./...`

`go vet ./...`

`./scripts/test_runtime.sh`

`./scripts/test_build_contract.sh`

`./scripts/validate_policy_manifest.sh`

`./scripts/validate_quality_profile.sh`

`./scripts/validate_supply_chain_profile.sh`

`./scripts/validate_action_pinning.sh`

`./scripts/validate_repository_shape.sh`

`git diff --check`

`go test -race ./internal/coretransport`

`GOOS=windows GOARCH=amd64 go test -c ./internal/coretransport`

`GOOS=windows GOARCH=amd64 go test -c -tags=coretransport_native_test ./internal/coretransport`

`go test -tags=coretransport_native_test ./internal/coretransport` (Windows runner)

The transport tests use a real Unix domain socket and cover handshake/version rejection,
unknown methods, pre-handshake access, concurrent response multiplexing, cancellation,
oversized frames, endpoint ownership, socket permissions, and redacted DTO responses.
Windows source is build-tagged for `go-winio`; the WinUI project is target-native and is
compiled and packaged by the `chuzi-build-windows-ui` Windows runner job.

The Core tests cover deterministic request submission, account and request
queries, cancellation, redacted results, domain events, notification state,
and the complete queue/session/credential/automation pipeline using temporary
storage and injected fakes. The tests do not launch the native client or use
live accounts, credentials, a Matrix endpoint, or an external production service.

## Risk

The primary risk is contract drift between the three native clients. The
versioned `chuzi.core/v1` DTO and error boundary, explicit capability model,
and platform-specific follow-up CRs keep presentation code from reaching
internal storage or security-sensitive services. Until those follow-up CRs
are integrated, the repository intentionally ships only the first Windows client; macOS
and Linux clients remain separate follow-up work.

Removing the Tauri launcher also removes its desktop packaging path. The
UI-neutral launcher CLI and Core API remain available, so native clients can
be implemented incrementally without changing service or data contracts.

## Rollback

Revert the Core API facade, native-client target documentation, build and
release wiring, tests, and this CR in one subsequent change. Continue using
the existing service and UI-neutral launcher boundaries. No account state,
storage schema, credential material, browser Profile, or Matrix data cleanup
is required.

## Breaking Change

The Tauri launcher executable and its `chuzi.launcher-ui/v1` desktop bridge are
no longer shipped. The service, Go launcher commands, account state machine,
storage, credential, Matrix, browser-worker, and release manifest contracts
remain unchanged. The additive `chuzi.core/v1` boundary is the required input
for future native platform clients.

## Backport Target

none
