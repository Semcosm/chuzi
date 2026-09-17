# CR-0044: split CI into a parallel component build DAG

Base: main
Head or Range: 61f2443..fec42a8
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: perf(ci): split cross-platform build into parallel component jobs
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 61f244390bc52de749eda42a706b89560b2f2294
Head OID: fec42a80d0e1770f2233e77dad177473259df7a0
Integrated Result: pending

## Summary

Consolidate the pending implementation work on this topic branch into one UGS
change record and replace the target runner's serial all-in-one build with a
workflow DAG. The branch carries the launcher architecture, production runtime
validation, Genshin Cloud session probe, documentation synchronization, and
the CI reform; these implementation slices are integrated and reviewed as one
protected-branch change because UGS requires one persisted CR to be the exact
PR body. Public contract, Go, Node, and Rust checks run once in parallel. Go
service/launcher cross-builds, the Node Worker build, and native Rust/Wry
helper builds run as independent jobs. Each target runner then downloads the
three component artifacts, assembles and packages its target, and uploads the
existing target artifact name. Nightly and stable runs add a final artifact
integration job which validates every target package before the aggregate
`chuzi-build` gate and release publisher.

The pull request workflow uses the same parallel source-check domains, omits
release compilation, and retains a single `chuzi-build` aggregate required
check. The UGS `ugs-validate` workflow remains an independent governance check.
Required check names stay stable so branch protection does not depend on matrix
expansion details. The full build workflow listens to `main` pushes, release
tags, nightly schedules, and explicit dispatches; ordinary topic-branch pushes
do not start the cross-platform build.

## Motivation

The previous matrix executed the same Go, Node, Rust, runtime, and package
contract suite on every platform runner. That consumed four runner slots for
work that is mostly platform-neutral and delayed feedback behind the slowest
repeated test sequence. The component DAG keeps native work on the runner that
can execute it while allowing independent compilers and validation domains to
use separate runners concurrently. Pull request pushes now run only source
tests and contract checks; full compilation is reserved for main, tags, nightly,
and explicit dispatches.

## Test Evidence

Local evidence for the implementation includes:

- `bash -n scripts/*.sh`
- `scripts/validate_policy_manifest.sh`
- `scripts/validate_quality_profile.sh`
- `scripts/validate_supply_chain_profile.sh`
- `scripts/validate_action_pinning.sh`
- `scripts/validate_repository_shape.sh`
- `scripts/test_build_contract.sh`
- `scripts/test_nightly_package.sh`
- `scripts/test_nightly_artifact_validator.sh`
- `scripts/test_runtime.sh`
- `go test ./...`
- `go vet ./...`
- `npm --prefix browser-worker test`
- `git diff --check`

Remote evidence remains required after push: the PR run must pass `ugs-validate`
and the parallel `chuzi-build` aggregate; a main/tag or manual build must pass
all component, target, artifact-integration, and aggregate jobs.

## Risk

Artifacts are now an explicit boundary between independent jobs. A missing,
stale, or incorrectly named component can produce an incomplete target unless
the assemble job fails closed. The assemble scripts require all three component
inputs, write a commit-bound build manifest, and retain the existing release
index and archive validators. Intermediate artifacts have one-day retention and
are not included by the stable release download pattern. Native WebView builds
and Linux display smoke tests remain on matching native runners. No release
secret is exposed to compile or test jobs.

## Rollback

Revert the CI DAG workflow, component build/assemble scripts, contract checks,
documentation, and this CR together. The existing `scripts/build.sh` and
`scripts/build.ps1` all-in-one local build entrypoints remain available, so
application source and release artifact formats require no rollback migration.

## Breaking Change

No application, storage, protocol, release artifact naming, or required-check
context changes are intended. The internal Actions job graph and intermediate
artifact names change; the four target job display names and final artifact names
remain compatible with nightly acceptance and release retry validators.

## Backport Target

none
