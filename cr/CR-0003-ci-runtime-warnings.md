# CR-0003: remove GitHub Actions runtime warnings

Base: main
Head or Range: 41adfbdac92f836405e570aa11c9cd823608df11..4ec47337896050a329425691902b245514ccb979
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: ci: remove GitHub Actions runtime warnings
Revision: 3
Status: integrated
Decision: accepted
Policy Version: v0.3
Base OID: 46aca883c154ab664d4af78194e9b33fbce895b4
Head OID: 46aca883c154ab664d4af78194e9b33fbce895b4
Integrated Result: main@46aca883c154ab664d4af78194e9b33fbce895b4

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

Local policy, quality, supply-chain, repository-shape, adapter,
build-contract, CR, Action pinning, and `git diff --check` validations passed.
PR #5 passed `ugs-validate` run `34203334930` and `chuzi-build` run
`34203334911`, including all four target jobs. The integrated main
`chuzi-build` run `34204060499` passed. The first main `ugs-validate` run
`34204060530` correctly rejected the still-pending record because GitHub's
rebase produced `main@46aca883`; this closure revision binds the record to that
integrated result. The closure PR and its resulting main checks are required
to complete the governance record.

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
