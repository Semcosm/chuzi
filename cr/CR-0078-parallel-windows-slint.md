# CR-0078: parallelize Windows Slint validation and packaging

Base: main
Head or Range: feat/windows-ui-client-foundation
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: perf(ci): parallelize Windows Slint validation and packaging
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 6f92f28bb70d52adbd9576fcdfef4103c30d0898
Head OID: af4ce9166c132c1f3abc42e74b18080fb9132e6e
Integrated Result: pending

## Summary

Split the Windows Slint layout snapshot check into its own Windows job so it
runs alongside the installer packaging job. Add a bounded timeout to the
installed-client smoke test so a stuck GUI process cannot consume the full job
timeout.

## Motivation

The Windows Slint job performed FreeRDP setup, layout rendering, installer
packaging, and runtime smoke tests serially. The layout check and installer
payload build are independent, so serial execution made this job the slowest
stage of the build. The previous installed-client smoke test could also wait
indefinitely when the executable failed to exit.

## Test Evidence

`scripts/test_build_contract.sh`, `scripts/validate_action_pinning.sh`,
repository shape validation, quality and supply-chain profile validation, and
`git diff --check` pass. The Windows Actions run is required to verify the
parallel job graph, PowerShell syntax, and installer smoke-test behavior.

## Risk

The layout job duplicates the Windows dependency setup on a separate runner,
but its wall-clock work overlaps the installer job. Both jobs retain the same
Rust, FreeRDP, and bindgen inputs. A smoke test that exceeds 60 seconds now
fails explicitly and terminates its child process.

## Rollback

Revert the workflow split and restore the layout step to
`chuzi-build-windows-slint`; remove the smoke-test timeout if the previous
behavior is required for diagnosis.

## Breaking Change

None to release artifacts or application protocols. The aggregate build check
now requires the additional layout validation job.

## Backport Target

none
