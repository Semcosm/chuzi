# CR-0123: diagnose Windows Core startup exits and close UI during installer lifecycle

Base: main
Head or Range: 09cd256cab6f802a769e9aa0231a6df3c3e1715a (implementation commit)
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: Windows Core startup diagnostics and installer UI shutdown
Revision: 2
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 3fbb027a175118e65ef5d645148d516db4ab16ff
Head OID: 09cd256cab6f802a769e9aa0231a6df3c3e1715a
Integrated Result: pending

## Summary

Record bounded, redaction-safe Windows service startup stages and classify
early Core startup failures. Bound SCM start/stop waits and return promptly
when an accepted service start returns to Stopped. During installer upgrade,
repair, and uninstall, identify the UI only by its exact installed executable
path, request normal window closure, and wait up to 15 seconds. If it remains
open, stop the lifecycle operation with a clear error; do not force-kill it.

## Motivation

The operator-installed CR-0122 build still reported only `core_unavailable`.
SCM accepted a manual start but the service was Stopped shortly afterward,
with no new Core log or service-specific failure event. The existing package
did not distinguish service preparation, runtime assembly, or dispatch
failures. Uninstall explicitly stopped Core and the broker task, but left the
installed Slint UI process running because `CloseApplications=yes` was the
only UI shutdown mechanism.

## Test Evidence

- Windows host: `go test -count=1 ./...`, `go test -count=1 -race ./...`, and
  `go vet ./...` passed.
- Windows test binaries compiled for `cmd/service`, `cmd/session-broker`, and
  `internal/slotwindows`.
- Policy manifest, quality profile, supply-chain profile, action pinning,
  repository shape, build contract, and `git diff --check` passed. The local
  quality script was run with a Git Bash `jq` wrapper to strip Windows CRLF
  from piped JSON lines; no repository files were changed for this workaround.
- Windows PowerShell parsed `scripts/windows-service-management.ps1`, and the
  new `StopUI` operation passed its no-running-UI smoke check.
- Rebuilt the Windows amd64 Core executables and browser-worker from this
  implementation. Reused the CR-0122 Windows UI binary because `ui/windows/`
  has no source changes after that build.
- Inno Setup 6.7.3 compiled the operator installer successfully at
  `dist/windows-uninstall-fix-test-20261019-09cd256c/installer/ChuziSetup.exe`
  (SHA-256 `b64c6d039976029b0ee9c3901cc1f5901d0e94d7ec7688f1412e0257e505dbe3`).
  The embedded Core version and commit match this implementation, all 16
  release-manifest resource hashes and the installer checksum sidecar were
  verified. The test installer is unsigned because no Windows code-signing
  certificate is configured.
- The actual install/uninstall cycle and a matching-window close integration
  smoke remain pending for operator testing; this local build did not install
  the package.

## Risk

Startup diagnostics contain only fixed stage, outcome, control, and bounded
error-class tokens; they do not record raw errors, paths, identities, or
credentials. Installer lifecycle now fails closed if the exact installed UI
does not exit normally within the wait bound. This can require the operator to
close the app and retry; it avoids deleting files while its UI is still open.
If the operator, Core, or broker does not stop cleanly, uninstall also stops
before removing registrations or files. ProgramData and per-user settings
remain governed by the existing data-retention prompt. The local package build
does not claim a successful live install/uninstall cycle. No Windows session,
WTS, RDP, Profile, Credential Manager, ACL, Agent, restart, shutdown, or Retire
acceptance is claimed by these checks.

## Rollback

Revert the service-host startup diagnostics and bounded SCM wait changes, and
remove `Stop-ChuziUI` from the service-management lifecycle. The persisted
service state and user data formats are unchanged.

## Breaking Change

No Core API or Store schema change. Install and uninstall may pause for up to
15 seconds while the installed UI receives a normal close request; a UI that
does not exit causes the operation to stop with an error.

## Backport Target

None.
