# CR-0013: run Linux ARM64 builds on a native GitHub runner

Base: main
Head or Range: 3308750ec6e66a6433839c43bcb0ca31b9b6f06a..fe91a203f97424012a8bc3debb39f4b5fcbb40d7
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: chore(ci): run Linux ARM64 builds on a native runner
Revision: 3
Status: integrated
Decision: accepted
Policy Version: v0.3
Base OID: 819b7df879075227d51060ea8bef685b303cbe65
Head OID: 819b7df879075227d51060ea8bef685b303cbe65
Integrated Result: main@819b7df879075227d51060ea8bef685b303cbe65

## Summary

Run the `linux-arm64` build matrix entry on GitHub's public-repository
`ubuntu-24.04-arm` hosted runner. Add explicit checks for the ARM64 host,
Node.js architecture, and Go architecture, and update the architecture,
operations, and roadmap documents to distinguish native ARM64 CI from the
still-unimplemented browser/WebView runtime.

## Motivation

The existing `linux-arm64` entry used an x86_64 Ubuntu runner while forcing
`GOARCH=arm64`, so it validated a cross-compiled artifact but not the host
toolchain or Node.js execution environment for ARM64. GitHub documents
`ubuntu-24.04-arm` as an available standard ARM64 runner for public
repositories, and `Semcosm/chuzi` is public. Native CI reduces this gap before
introducing platform-specific WebView or other native dependencies.

## Test Evidence

Local `scripts/test_build_contract.sh`,
`scripts/validate_repository_shape.sh`, `scripts/validate_cr_record.sh`, and
`git diff --check` pass. PR #29 passed `ugs-validate` and the aggregate
`chuzi-build` run `34432714222`; its Linux ARM64 job passed the explicit
`uname -m=aarch64`, Node.js `process.arch=arm64`, and Go `GOARCH=arm64`
assertions, plus Go/Node tests and packaging. The acceptance PR #30 passed
`ugs-validate` run `34434591926` and `chuzi-build` run `34434591930`; the
post-merge main checks passed as `ugs-validate` run `34434694051` and
`chuzi-build` run `34434694058`. The change does not add a browser binary,
WebView dependency, or real-account test.

## Risk

The ARM64 hosted image or an action's architecture support could change and
make the target job unavailable. The explicit assertions fail closed rather
than silently treating an x86 runner as native. This change also does not
provide Linux WebView libraries, display support, or browser runtime coverage.

## Rollback

Restore the `linux-arm64` matrix entry to `ubuntu-24.04` and revert the
documentation and assertion changes in a subsequent CR. No application data
or schema is changed.

## Breaking Change

The Linux ARM64 CI job now requires GitHub's `ubuntu-24.04-arm` label and
executes tests on an ARM64 host. Release artifact names and the application
protocol remain unchanged; native browser/WebView support is still deferred.

## Backport Target

none
