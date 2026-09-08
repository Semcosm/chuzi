# CR-0003: remove GitHub Actions runtime warnings

Base: main
Head or Range: 41adfbdac92f836405e570aa11c9cd823608df11..4ec47337896050a329425691902b245514ccb979
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: ci: remove GitHub Actions runtime warnings
Revision: 2
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 41adfbdac92f836405e570aa11c9cd823608df11
Head OID: 4ec47337896050a329425691902b245514ccb979
Integrated Result: pending

## Summary

Update the checked-in GitHub workflows to use the Node.js 24-compatible major
releases of the official checkout, Go setup, and Node setup actions, pinned to
their current full commit SHAs. Disable the Go module cache because this
repository has no `go.sum` and does not declare external Go modules yet. Make
the build-contract validator check the required full-SHA shape instead of
freezing one historical checkout commit.

## Motivation

The build workflow currently emits GitHub Actions warnings because its action
versions run on the retiring Node.js 20 action runtime, and because `setup-go`
attempts to restore a module cache without a `go.sum`. The build-contract
validator also rejects any future legitimate checkout pin because it compares
against one historical SHA. Correcting all three stale assumptions keeps CI
output actionable while preserving the existing four-target build matrix and
the project's Node.js 20 worker compatibility boundary.

## Test Evidence

Planned and required before integration: validate all action references with
`scripts/validate_action_pinning.sh`, run the repository policy, quality,
supply-chain, repository-shape, adapter, and build-contract checks, and verify
`git diff --check`. GitHub pull-request and post-integration main-branch runs
must pass both required checks.

## Risk

The action major upgrades change only the JavaScript runtime used by the
official setup actions; their workflow inputs and the build toolchain versions
remain unchanged. Disabling Go caching may make setup slightly slower, but it
avoids a misleading cache lookup and does not change build output. The
validator now accepts any full SHA pin for checkout while the dedicated
action-pinning check continues to enforce pinning. The worker continues to
target Node.js 20, and browser automation dependencies are out of scope.

## Rollback

Revert this change through a subsequent UGS CR, restoring the previous action
SHAs and Go cache setting if the hosted runners expose an incompatibility.
No application data, credentials, or published artifacts are changed.

## Breaking Change

None. This changes CI action runtimes and cache behavior only; application
interfaces, target platforms, artifact names, and runtime requirements remain
unchanged.

## Backport Target

None.
