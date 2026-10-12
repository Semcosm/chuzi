# CR-0118: Bound Windows installer smoke execution

Base: main
Head or Range: 081ec40864616faa3eb3faea3661377af95c0dd7
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: Bound Windows installer smoke execution
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 7e90153f3d95a95ee8db817b2caf228e569ccab0
Head OID: 081ec40864616faa3eb3faea3661377af95c0dd7
Integrated Result: pending

## Summary

Make the Windows Slint installer smoke test terminate deterministically and keep silent installer failures from waiting for an unattended message box.

## Motivation

The installer smoke test tracked the Inno Setup process tree without a deadline. A setup error shown through a normal message box could therefore leave the hosted Windows job waiting indefinitely even though the installer had already failed.

## Test Evidence

- `./scripts/test_build_contract.sh` passes.
- `./scripts/validate_repository_shape.sh` passes.
- `git diff --check` passes.
- The workflow now records installer diagnostics, bounds the setup process tree to three minutes, force-cleans tracked descendants on timeout, and reports a failing step. Installer error paths use Inno Setup's suppressible message API.

## Risk

An unusually slow Windows installer will fail after three minutes and upload its runtime log for diagnosis. The timeout applies only to the CI smoke check; production installation behavior is unchanged.

## Rollback

Revert the implementation commit and rerun the Windows build workflow.

## Breaking Change

None.

## Backport Target

None.
