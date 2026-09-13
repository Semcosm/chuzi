# CR-0027: add launcher network component downloads and nightly release catalogs

Base: main
Head or Range: ba087186a1a7c16ab4bad0944b0d0b522f566d6f..c26c28a4d42a94950aa79853dbefc49e16fc091c
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(launcher): add network component downloads
Revision: 3
Status: integrated
Decision: accepted
Policy Version: v0.3
Base OID: c12b08eb200b6985607d2c2ab31e2cd330b28b34
Head OID: c12b08eb200b6985607d2c2ab31e2cd330b28b34
Integrated Result: main@c12b08eb200b6985607d2c2ab31e2cd330b28b34

## Summary

Add the first network-backed component source for the UI-neutral launcher.
`ReleaseIndex` embeds the validated manifest and records each target archive's
path, size, and SHA-256. The HTTP source accepts HTTPS only by default,
restricts redirects and artifacts to the index origin, supports explicit
context cancellation with bounded transport retries, and writes verified
archives through temporary files and atomic rename. Component installation
downloads only the selected dependency closure, extracts into a temporary
source tree, and reuses the existing atomic resource repair and state rollback
implementation. The launcher exposes first-run initialization and keeps
component state available for a future settings UI.

The nightly workflow now uses
`nightly-<run-number>-<commit-short-hash>` versions, publishes per-target
component archives and a release index as expiring Actions artifacts, and does
not create tags or GitHub Releases. Unix and Windows package contracts validate
the generated names, archive digests, index metadata, sidecar checksum, and
unsafe path rejection.

## Motivation

The standalone launcher must be useful without forcing users to install the
service, browser worker, and desktop runtime together. Keeping release
discovery, archive verification, dependency ordering, first-run status, and
filesystem mutation behind the existing Go interfaces lets a later Rust UI
guide optional installation and expose the same component controls from its
settings view. This increment does not choose a UI toolkit or make the launcher
an implicit network client.

## Test Evidence

Local checks passed:

`go test ./... -count=1`

`go test -race ./...`

`go vet ./...`

`go build ./...`

`scripts/test_nightly_package.sh` (synthetic Linux amd64 and Windows amd64
archives, index validation, checksums, and unsafe-path rejection)

`scripts/test_build_contract.sh`

`scripts/validate_policy_manifest.sh`, `scripts/validate_quality_profile.sh`,
`scripts/validate_supply_chain_profile.sh`, `scripts/validate_action_pinning.sh`,
`scripts/validate_repository_shape.sh`, shell/Python syntax checks, and
`git diff --check`

Launcher tests use httptest servers, temporary directories, fake archives, and
synthetic manifests only. They cover retry, cancellation, strict JSON,
cross-origin and scheme-downgrade redirect rejection, archive size/digest
verification, dependency downloads, in-process state freshness, manifest
consistency, safe extraction, and first-run initialization. No Chromium is
downloaded or packaged; no real account, credential, Matrix endpoint, or
production release host is contacted.

## Risk

The launcher can fetch and unpack archives selected by a release index, so
untrusted metadata must fail closed. Index and archive URLs require HTTPS and
same-origin redirects; loopback HTTP is available only through an explicit
test-only flag. Archive sizes, SHA-256 values, target/version metadata, paths,
regular-file entries, duplicate entries, and extracted byte/entry limits are
validated before installation. Downloads use bounded retries, context
cancellation, mode-0600 temporary files, and atomic rename. Component state is
updated only after the existing resource verification and rollback transaction
completes. The release index is an integrity catalog, not a signature; stable
signed release publication remains governed by CR-0024. The workflow keeps
nightly artifacts temporary and does not create tags.

## Rollback

Stop publishing or consuming the release index and continue using the local
manifest/source launcher commands. Revert the launcher download, network
manager, package/index scripts, workflow, tests, and documentation in a later
governance change. Existing installation state and local component archives do
not require a database migration; remove only verified cache files below the
installation root after stopping the launcher.

## Breaking Change

None to the service, browser Worker, Rust helper, account state machine,
storage schema, or existing local launcher commands. The release manifest gains
additive component artifact metadata and the nightly package now includes an
additive release index. Supplying `-release-index` opts into network access;
without it, component repair/install remains local-only.

## Backport Target

none
