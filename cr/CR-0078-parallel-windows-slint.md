# CR-0078: parallelize Windows Slint validation and packaging

Base: main
Head or Range: feat/windows-ui-client-foundation
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: perf(ci): parallelize Windows Slint validation and packaging
Revision: 8
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 541fd1f59e773064a3e63b6a856a3279b425fcab
Head OID: 541fd1f59e773064a3e63b6a856a3279b425fcab
Integrated Result: pending

## Summary

Split the Windows Slint workflow into independent compile, layout, packaging,
and runtime boundaries. Compile the Windows UI once and hand the executable to
the packaging job; the layout job uses a feature that skips FreeRDP bindgen.
Run the installer with runtime logging and continuously report its process tree
and log tail while waiting for the complete setup tree to exit. Keep a bounded,
explicit polling timeout only around the installed-client smoke test.
Guard uninstall-only user-data prompts so silent installation cannot wait on a
hidden confirmation dialog.

## Motivation

The Windows Slint job performed FreeRDP setup, layout rendering, UI
compilation, installer packaging, and runtime smoke tests in one long chain.
The layout check repeated the same FreeRDP and bindgen setup as the installer
job, and the installer job compiled the UI immediately before packaging it.
The installer smoke test had also been changed to poll only the top-level Inno
Setup process. Inno Setup creates a temporary child executable for the actual
transaction, so that polling could kill a valid install while the child was
still running. A later run remained stuck even with `Start-Process -Wait`, so
the smoke step now records the installer hash, runtime log, process IDs, parent
IDs, command lines, and installed files while it waits without a business-level
deadline.
The runtime log then showed the installer waiting on the custom user-data
confirmation during installation. The uninstall check now has an explicit
uninstall context and uses `SuppressibleMsgBox`, so installation never prompts
and silent uninstall receives the documented default.

## Test Evidence

`scripts/test_build_contract.sh`, `scripts/validate_action_pinning.sh`,
repository shape validation, quality and supply-chain profile validation, and
`git diff --check` pass. Windows Actions runs are required to verify the
compile artifact handoff, dependency-free layout feature, parallel job graph,
PowerShell syntax, installer runtime diagnostics, process-tree completion, and
installer smoke-test behavior.
The latest run reached the installer and confirmed the previous wait was the
custom user-data prompt; the follow-up run must verify installation proceeds.

## Risk

The compile job remains the only job that installs FreeRDP and runs bindgen. The
layout and packaging jobs consume separate outputs and can run concurrently
with compile where their inputs permit. The installer is observed through its
complete process tree, with live diagnostics and a retained runtime log, then
the installed executable and shipped core payload are verified. The client smoke
test retains a 60-second diagnostic bound and terminates only that client
process if it fails to exit.

## Rollback

Revert the workflow split and restore the layout and UI compilation steps to
`chuzi-build-windows-slint`; remove the prebuilt binary option and smoke-test
timeouts if the previous behavior is required for diagnosis.

## Breaking Change

None to release artifacts or application protocols. The aggregate build check
now requires the compile, layout, and packaging jobs.

## Backport Target

none
