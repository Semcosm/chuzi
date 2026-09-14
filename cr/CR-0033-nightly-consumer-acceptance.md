# CR-0033: validate the first nightly build as a real consumer

Base: main
Head or Range: 934d56dc6adddcbeaeae86f5be6cca8be01f61e7..09084d757097cfcb2267066b689b51ce48ac1958
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: test(launcher): validate nightly artifacts as a consumer
Revision: 5
Status: integrated
Decision: accepted
Policy Version: v0.3
Base OID: b9a1ba65ba0e9f56c4ce0dc9502e32282458547d
Head OID: b9a1ba65ba0e9f56c4ce0dc9502e32282458547d
Integrated Result: main@b9a1ba65ba0e9f56c4ce0dc9502e32282458547d

## Summary

Add a run-scoped nightly consumer acceptance tool. It verifies a successful
`workflow_dispatch` build on `main`, binds the run and every matrix job to one
full commit SHA, downloads all four target artifacts, and validates the release
index, manifest, sidecar checksums, component archives, bundle contents, and
resource hashes. On a compatible host it extracts the real launcher component
and drives initialization, network-selected dependency installation, component
listing, and initialization completion through a loopback-only local catalog.

## Motivation

The existing package and synthetic consumer tests prove the launcher contracts,
but they do not consume an actual GitHub Actions artifact. A run-scoped check is
needed before publishing the first nightly test build so an old run, incomplete
matrix, missing artifact, stale manifest, or corrupted archive cannot be
mistaken for a working release. The check also demonstrates the intended first
launch flow for a standalone downloaded launcher while keeping target-specific
execution limits explicit.

## Test Evidence

Local synthetic artifact validator:

`./scripts/test_nightly_artifact_validator.sh`

Existing offline nightly package validator:

`./scripts/test_nightly_package.sh`

Real prior nightly consumer run used to validate the new tool before this CR is
merged:

`./scripts/accept_nightly_run.sh 34786996598 ae8b014871bb43a2b9bc87719c040053173bbc1e`

The run was `workflow_dispatch`, run number `226`, version
`nightly-226-ae8b014871bb`, and all four target jobs plus aggregate
`chuzi-build` passed. The tool verified the four artifact digests and retention
metadata, every release index and component/bundle archive, then on Linux amd64
ran the real launcher CLI through initialization, service dependency install,
component listing, and initialization completion. Running the same command with
the current `main` SHA rejected the older run before downloading artifacts,
confirming the commit binding.

Post-merge nightly consumer acceptance:

`https://github.com/Semcosm/chuzi/actions/runs/34849658210`

The `workflow_dispatch` run completed from `main` at
`cd9b6b8bbe0a9c4f6e9ca4d410ddc21464eb700d` with run number `257` and version
`nightly-257-cd9b6b8bbe0a`. The four target jobs and aggregate `chuzi-build`
passed; `stable-release` was skipped. The run was consumed only after its
commit binding matched the merged main SHA.

The uploaded Actions artifacts are retained until the dates below and have
these GitHub artifact IDs, sizes, and digests:

`chuzi-nightly-windows-amd64` (10351305240), 13983654 bytes, expires
`2026-09-28T13:37:52Z`:
`sha256:bbaed5d2ac595d716851c4d62ec1307b315ab7024b77648eecd2c704c9cf3b42`

`chuzi-nightly-linux-amd64` (10350950255), 14093431 bytes, expires
`2026-09-28T13:36:55Z`:
`sha256:88b4a3fa799d5ffce396abd00d8980c36d3a454d576238deb9799827cf5e5c7f`

`chuzi-nightly-linux-arm64` (10350960217), 12975144 bytes, expires
`2026-09-28T13:36:44Z`:
`sha256:c2cb1b3947887e0fa606862cb4f5f1d435754d6c10cf3853e2d954c5430e161e`

`chuzi-nightly-darwin-arm64` (10349796370), 13564753 bytes, expires
`2026-09-28T13:33:41Z`:
`sha256:2b910b922445fe1a0d093bf0d88b0f8f8a3e54d1e3ce35197c344378260d1c77`

`scripts/accept_nightly_run.sh` downloaded and validated every target release
index, manifest, sidecar checksum, component archive, bundle archive, resource
hash, and safe extraction path. On Linux amd64 it then ran the real launcher
from the downloaded artifact through version, initialize, service dependency
install, component-list, and initialize-complete phases against a temporary
loopback catalog. No Chromium, real account, credential, Matrix token,
production URL, CDP endpoint, Git tag, or GitHub Release was used.

The following repository checks are required before integration:

`go test ./... -count=1`

`go vet ./...`

`go build ./...`

`npm --prefix browser-worker test`

`./scripts/test_launcher_consumer.sh`

`./scripts/test_launcher_ui.sh`

`./scripts/test_nightly_package.sh`

`./scripts/test_nightly_artifact_validator.sh`

`./scripts/test_build_contract.sh`

`./scripts/validate_policy_manifest.sh`, `./scripts/validate_quality_profile.sh`,
`./scripts/validate_supply_chain_profile.sh`, `./scripts/validate_action_pinning.sh`,
`./scripts/validate_repository_shape.sh`, and `git diff --check`.

No Chromium or Edge is downloaded or packaged. The acceptance never uses a real
account, credential, Cookie, Matrix token, production URL, CDP endpoint, or
GitHub Release. All downloads and installation state are temporary. Non-host
targets receive archive and metadata validation only; no foreign executable is
run. Nightly artifacts remain untagged, unsigned Actions artifacts with the
workflow's 14-day retention.

## Risk

The acceptance tool uses the GitHub CLI and downloads temporary artifacts, so it
requires an authenticated local `gh` session and enough temporary disk space.
Archive extraction is performed only after path, type, size, digest, manifest,
and target checks; symlinks and traversal entries are rejected. The launcher
network path is explicitly limited to a loopback HTTP test server in the local
host execution. The validator does not establish release signing or plugin
trust, and it does not claim that a desktop WebView or browser runtime is
usable merely because its archive exists.

## Rollback

Stop invoking `scripts/accept_nightly_run.sh` and continue using the existing
synthetic package/consumer checks. Revert this script, validator, test, workflow
phase, and CR in a later governance change. No application state, schema,
release artifact, or installed user data is modified by the acceptance.

## Breaking Change

None. This adds validation-only scripts and one CI contract phase. Existing
launcher, release index, package, service, browser-worker, Rust runtime, and UI
interfaces are unchanged.

## Backport Target

none
