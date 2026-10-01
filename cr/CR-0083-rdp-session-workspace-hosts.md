# CR-0083: integrate RDP into the Sessions workspace with a floating host

Base: main
Head or Range: feat/ui-refactor
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(ui): add docked RDP workspace and floating host
Revision: 2
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 0a8f0c04e5fc12778f259e4fe8bb0a1c09416233
Head OID: e6eab3401a2bde6324e6736ad8fdf4573bd5d2f1
Integrated Result: pending

## Summary

Implement one session runtime with two presentation hosts. The main Sessions
window owns the default docked workspace surface, while the existing
`DesktopRdpWindow` remains the optional floating host for the same framebuffer,
input queue, FreeRDP worker, and performance capture. Add the short-lived
capability boundary required before an interactive session can be launched from
Sessions.

## Motivation

The existing `DesktopRdpController` is still valuable, but it currently owns a
window-specific Slint surface and has no route from the new Sessions shell.
Deleting it would discard working input, framebuffer, and performance behavior.
Embedding it directly would couple the worker to `MainWindow` and risks a second
worker during host switching. A shared runtime with docked and floating hosts
preserves the useful window while matching the session-first UI.

## Decision and scope

- Default to a docked Session Workspace in `MainWindow`.
- Keep `DesktopRdpWindow` as a floating host for the same runtime.
- Provide explicit `Dock`, `Float`, `Hide`, and `Stop` actions.
- Treat floating-window close as `Hide`; only `Stop` releases the runtime.
- Extract a host-neutral runtime before adding the MainWindow host.
- Keep `get_browser_view` read-only and visibly separate from interactive RDP.
- Require an authorized, short-lived RDP capability from Core or the credential
  boundary before exposing a production Open action.

No credential, endpoint, certificate policy, Profile path, or pixel data may be
added to Core request DTOs, UI preferences, logs, diagnostics, or Matrix
messages. Host switching must not reconnect or start a second FreeRDP worker.

## Implementation

`RdpSessionRuntime` now owns the host-neutral worker lifecycle and exposes
`Docked`, `Floating`, `Hidden`, and `Stopped` host states. Dock/Float/Hide only
change presentation state; Stop joins the worker and finishes performance
capture. The shared `ChuziSessionWorkspace` surface is mounted in `MainWindow`
behind an explicit visibility gate and includes Dock, Float, Hide, and Stop
callbacks. The capability boundary in `internal/credential` issues opaque
two-minute leases (maximum five minutes), binds them to actor and request, and
keeps connection material in memory only. Core exposes the redaction-safe
`issue_rdp_capability` contract and rejects unauthorized request states.

## Test Evidence

`go test ./...`, `go vet ./...`, `cargo test --manifest-path
ui/windows/Cargo.toml` (19 tests), `./scripts/test_build_contract.sh`, and
`git diff --check` passed. The layout snapshot example generated 30 images under
`dist/windows-layout-final-1790827675/sessions/` for mixed, empty, loading,
error, and unavailable states in both themes at 800x600, 1120x760, and
1440x900. The runtime host-switch test proves one worker is retained across
Dock/Float/Hide and that Stop prevents later host changes. Credential tests
cover issue/resolve, actor binding, expiry, revocation, cancellation, and
redaction. Core and transport tests cover authorized/forbidden states and the
opaque DTO.

## Risk

The service runtime still does not construct a production RDP authorizer from
deployment credentials, so this revision deliberately keeps the Sessions Open
RDP action hidden. The workspace surface is therefore a fail-closed host state
surface until the authorized material handoff is wired. Existing FreeRDP
security and credential lifetime rules remain isolated behind the runtime.

## Rollback

Revert the workspace contract and documentation changes. Keep the existing
`DesktopRdpController` and `DesktopRdpWindow` unchanged until the host-neutral
runtime extraction is complete.

## Breaking Change

None. The existing read-only browser preview remains separate, and no new
interactive action is advertised while the capability provider is unavailable.

## Backport Target

none
