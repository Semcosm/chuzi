# CR-0078: parallelize Windows Slint validation and packaging

Base: main
Head or Range: feat/windows-ui-client-foundation
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: perf(ci): parallelize Windows Slint validation and packaging
Revision: 4
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 6f92f28bb70d52adbd9576fcdfef4103c30d0898
Head OID: af4ce9166c132c1f3abc42e74b18080fb9132e6e
Integrated Result: pending

## Summary

Split the Windows Slint workflow into independent compile, layout, packaging,
and runtime boundaries. Compile the Windows UI once and hand the executable to
the packaging job; the layout job uses a feature that skips FreeRDP bindgen.
Wait for Inno Setup through PowerShell's process-tree wait and keep a bounded,
explicit polling timeout only around the installed-client smoke test.

## Motivation

The Windows Slint job performed FreeRDP setup, layout rendering, UI
compilation, installer packaging, and runtime smoke tests in one long chain.
The layout check repeated the same FreeRDP and bindgen setup as the installer
job, and the installer job compiled the UI immediately before packaging it.
The installer smoke test had also been changed to poll only the top-level Inno
Setup process. Inno Setup creates a temporary child executable for the actual
transaction, so that polling could kill a valid install while the child was
still running. PowerShell's `Start-Process -Wait` waits the process tree and is
the behavior used by the last successful Windows Slint run.

## Test Evidence

`scripts/test_build_contract.sh`, `scripts/validate_action_pinning.sh`,
repository shape validation, quality and supply-chain profile validation, and
`git diff --check` pass. Windows Actions runs are required to verify the
compile artifact handoff, dependency-free layout feature, parallel job graph,
PowerShell syntax, process-tree installer waiting, and installer smoke-test
behavior.

## Risk

The compile job remains the only job that installs FreeRDP and runs bindgen. The
layout and packaging jobs consume separate outputs and can run concurrently
with compile where their inputs permit. The installer is waited through its
complete process tree, then the installed executable and shipped core payload
are verified. The client smoke test retains a 60-second diagnostic bound and
terminates only that client process if it fails to exit. The installer stays in
its normal windowed process mode because Inno Setup can leave its temporary
child running with `-NoNewWindow`.

## Rollback

Revert the workflow split and restore the layout and UI compilation steps to
`chuzi-build-windows-slint`; remove the prebuilt binary option and smoke-test
timeouts if the previous behavior is required for diagnosis.

## Breaking Change

None to release artifacts or application protocols. The aggregate build check
now requires the compile, layout, and packaging jobs.

## Backport Target

none
