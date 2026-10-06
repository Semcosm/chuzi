# CR-0098: add silent GitHub Actions wait helper

Base: main
Head or Range: a87074a510fd37cdf9ef9ba9aeee836227b060a0
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: fix(ci): add silent GitHub Actions wait helper
Revision: 2
Status: integrated
Decision: accepted
Policy Version: v0.3
Base OID: a547945110537a0a7b8975eba6f25534c3539e84
Head OID: 4d8a4bab6afe83d84fe8bb1b7aeaab198a83402b
Integrated Result: main@4d8a4bab6afe83d84fe8bb1b7aeaab198a83402b

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
