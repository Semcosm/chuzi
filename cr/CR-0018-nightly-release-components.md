# CR-0018: first componentized nightly release

Base: main
Head or Range: c4eecb0afaec975067e82ed890f636eef10c3ade
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: build: add componentized nightly release and launcher contract
Revision: 3
Status: integrated
Decision: accepted
Policy Version: v0.3
Base OID: e1ac295237354b298b34d1ce0bb148142f137f93
Head OID: e1ac295237354b298b34d1ce0bb148142f137f93
Integrated Result: main@e1ac295237354b298b34d1ce0bb148142f137f93

## Summary

Add the first release workflow without creating a semantic-version tag or a
GitHub Release. Scheduled and manually dispatched builds publish four target
Actions artifacts with a `nightly-<run-number>` version. Each target includes a
minimal launcher, the existing service/worker/runtime bundle, a release
manifest, checksums, and independently packaged component archives.

## Motivation

chuzi is a composable product: an installer should not require every service,
browser runtime, or future adapter plugin. The UI is intentionally deferred,
so the release manifest and launcher-facing interfaces must be stable without
choosing a GUI toolkit, network protocol, update host, or plugin execution
model.

## Test Evidence

`scripts/test_build_contract.sh`, shell syntax checks, Python syntax checks,
`git diff --check`, and Rust format/tests pass locally. A temporary-stage
manifest generation check passes for Linux amd64 and Windows amd64 naming.
Go and npm are unavailable in this checkout; the required GitHub Actions matrix
must run Go, Node, native Rust, WebView smoke, packaging, and artifact checks.

Implementation PR #47 passed `ugs-validate` and the four-target `chuzi-build`
matrix, then rebase-ff integration produced main commit
`408a1eecb72da92b9314ac1a394469d11aa8f95b`. The post-merge push workflow
`34604284393` passed with all target jobs and the aggregate check. Manual nightly
run `34604395276` also passed for Windows amd64, Linux amd64, Linux arm64, and
Darwin arm64. Its four artifacts were present with 14-day retention; the
Darwin arm64 `nightly-118` manifest listed launcher, service, browser-worker,
and desktop-runtime components, and the downloaded archive hashes matched the
sidecars. Unix sidecars currently retain runner-absolute filenames, so making
plain `sha256sum -c` portable is tracked as a separate follow-up fix.

Acceptance PR #48 recorded the verified implementation result and passed all
required checks. Its rebase-ff integration produced main commit
`e1ac295237354b298b34d1ce0bb148142f137f93` with the required governance
trailers. The post-merge main workflow `34606814297` passed `ugs-validate`,
Windows amd64, Linux amd64, Linux arm64, Darwin arm64, and the aggregate
`chuzi-build` check. This closure records that verified main result as the
integrated outcome.

## Risk

Nightly artifacts expire after 14 days and are not immutable releases. The
launcher currently only reads and verifies a manifest; update downloads,
repair writes, component/plugin installation, signing trust, and plugin
execution are intentionally adapter work for later CRs. The manifest contains
no credentials or user data.

## Rollback

Disable the workflow schedule and revert this CR. Existing service and worker
runtime behavior is unchanged; remove the launcher/component artifacts from
the next build if necessary.

## Breaking Change

None to the service or Worker protocol. The new `chuzi-release/v1` manifest is
an additive launcher/package contract.

## Backport Target

none
