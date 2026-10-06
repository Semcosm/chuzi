# CR-0096: skip dedicated Windows smoke on CI channel

Base: main
Head or Range: c36a42439d2faf2579454a3c2209df7ccaa13069
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: fix(ci): skip native smoke for ci channel
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 4d6b63b2ed5a8dd0348e862ed79e3efe2def8aff
Head OID: c36a42439d2faf2579454a3c2209df7ccaa13069
Integrated Result: pending

## Summary

Correct the Windows job-pool smoke gate so push-to-main `ci` runs do not queue
forever when the dedicated self-hosted runner is unavailable. The native WTS
smoke remains enabled for `nightly` and `stable` release channels.

## Motivation

The repository currently has no registered self-hosted runner with the
`self-hosted`, `windows`, and `chuzi-job-pool` labels. The workflow previously
used `channel != 'test'`, which incorrectly scheduled the smoke for the `ci`
channel produced by a push to `main`. CI and manual test runs already have a
hosted Windows preflight and should finish without waiting for the dedicated
runtime acceptance host.

## Test Evidence

- GitHub API `GET /repos/Semcosm/chuzi/actions/runners` reported
  `total_count: 0`; the affected run `37416714738` remained queued at job
  `112117213181` until it was cancelled.
- `./scripts/test_build_contract.sh` passed.
- `./scripts/test_release_channels.sh` passed.
- `./scripts/validate_action_pinning.sh` passed.
- `git diff --check` passed.

## Risk

`ci` and `test` runs no longer provide native WTS smoke evidence. They retain
the hosted preflight, while `nightly` and `stable` still fail their aggregate
gate if the dedicated native runner is missing or the smoke fails.

## Rollback

Revert commit `c36a42439d2faf2579454a3c2209df7ccaa13069` to restore the prior
channel condition.

## Breaking Change

None. Release-channel behavior is narrowed to match the documented native
acceptance gate.

## Backport Target

none
