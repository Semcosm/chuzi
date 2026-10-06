# CR-0098: add silent GitHub Actions wait helper

Base: main
Head or Range: implementation commit
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: fix(ci): add silent GitHub Actions wait helper
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: a547945110537a0a7b8975eba6f25534c3539e84
Head OID: a547945110537a0a7b8975eba6f25534c3539e84
Integrated Result: pending

## Summary

Document a bounded CI waiting protocol for coding agents and add a small
`scripts/ci-wait` helper that discovers the exact commit run internally, waits
silently, and emits one final SUCCESS, FAILED, or TIMEOUT result.

## Motivation

Repeated GitHub Actions polling exposes queued and runner progress to the agent
and couples CI observation to model reasoning. A local blocking helper keeps
that state private while preserving an exact commit association and a hard
timeout.

## Test Evidence

`bash -n scripts/ci-wait`, fake-`gh` success and failure scenarios, missing-`gh`
failure, bounded timeout termination, `git diff --check`,
`./scripts/validate_action_pinning.sh`, `./scripts/validate_repository_shape.sh`,
and `./scripts/test_build_contract.sh`.

## Risk

The helper depends on the GitHub CLI and the platform `timeout` command. It
fails explicitly when either prerequisite is unavailable and treats a local
wait timeout separately from a completed failed workflow.

## Rollback

Revert the implementation commit and remove the silent-wait section from
`AGENTS.md`.

## Breaking Change

None. This changes agent operating guidance and adds an opt-in helper.

## Backport Target

none
