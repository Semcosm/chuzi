# CR-0018: first componentized nightly release

Base: main
Head or Range: c4eecb0afaec975067e82ed890f636eef10c3ade
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: build: add componentized nightly release and launcher contract
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: c4eecb0afaec975067e82ed890f636eef10c3ade
Head OID: c4eecb0afaec975067e82ed890f636eef10c3ade
Integrated Result: pending

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
