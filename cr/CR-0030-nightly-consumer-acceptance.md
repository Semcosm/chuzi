# CR-0030: nightly user install and upgrade acceptance

Base: main
Head or Range: 973b3c3591fac3c30a8350c7377135b70055846c..f1ab8e7173764a5bea7988752a0e351739af2f1e
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: test(launcher): add nightly consumer acceptance
Revision: 3
Status: accepted
Decision: accepted
Policy Version: v0.3
Base OID: 973b3c3591fac3c30a8350c7377135b70055846c
Head OID: 04c6515308d3b2b55e91b29adbf8526f2692899a
Integrated Result: pending

## Summary

Add a local consumer acceptance path for the nightly launcher. The test builds
the real `cmd/launcher` executable, serves synthetic old/new release indexes
and component archives from a loopback HTTP server, and runs the same commands
used by a future Rust UI. It also records the upgrade rule that an installed
older `release-manifest.json` may coexist with a newer candidate index when the
target and channel match.

## Motivation

Package and unit contracts already validated archive shape and individual
download/manager operations, but they did not prove that a clean standalone
launcher can initialize, select a dependency-bearing component, repair a
damaged resource, and upgrade from one nightly to the next. This acceptance
closes that gap before the first UI-driven nightly test build is consumed.

## Test Evidence

Local checks passed:

`go test -count=1 ./...`

`go test -race -count=1 ./...`

`go vet ./...`

`go build ./...`

`scripts/test_launcher_consumer.sh`

`scripts/test_nightly_package.sh`

`scripts/test_build_contract.sh`

`scripts/test_launcher_ui.sh`

`npm --prefix browser-worker test`

`cargo fmt --manifest-path browser-runtime/Cargo.toml -- --check`

`cargo test --locked --manifest-path browser-runtime/Cargo.toml -- --nocapture`

`cargo fmt --manifest-path launcher-ui/Cargo.toml -- --check`

`cargo test --locked --manifest-path launcher-ui/Cargo.toml`

`go run ./cmd/service -self-test -worker-command node -worker-script browser-worker/src/worker.mjs`

`./scripts/validate_policy_manifest.sh`, `./scripts/validate_quality_profile.sh`,
`./scripts/validate_supply_chain_profile.sh`, `./scripts/validate_action_pinning.sh`,
`./scripts/validate_repository_shape.sh`, and `git diff --check`.

The consumer test uses only temporary directories, synthetic manifests, local
tar.gz/zip archives, and an explicit loopback HTTP opt-in. It verifies first-run
component choices, dependency ordering, SHA-256/size checks, cache-backed
idempotent installs, resource repair, update discovery, upgrade with an older
local manifest, failed-archive rollback, and removal of temporary files. The
manager cancellation test verifies that context cancellation leaves no resource,
state, or partial archive. No Chromium or Edge is downloaded, and no real
account, credential, Matrix token, production endpoint, CDP endpoint, or Git
tag/release is used.

GitHub Actions acceptance:

PR `#85` run
`https://github.com/Semcosm/chuzi/actions/runs/34807597760` completed
successfully from head `996ad999bdee5e07be3fa2aafaf0b35cd14a7ca9`. The four
target jobs (`windows-amd64`, `linux-amd64`, `linux-arm64`, and
`darwin-arm64`) and the aggregate `chuzi-build` check passed; Linux X11 and
Wayland WebKitGTK smoke tests also passed. `ugs-validate` run
`34807597538` passed, and `stable-release` was skipped. This pull-request
validation created no tag or GitHub Release.

## Risk

The CLI can consume a release index and unpack its selected archives, so the
existing HTTPS-by-default, same-origin, size, digest, safe-path, temporary-file,
atomic-repair, lock, and rollback checks remain part of the acceptance path.
Nightly artifacts are still temporary Actions artifacts rather than a stable
public distribution endpoint. This change does not make the index a signature
or establish trust for plugins. The test builds a CLI subprocess on the host
but does not execute service, browser, or plugin payloads from the synthetic
archives.

## Rollback

Stop consuming the nightly index or revert this test/CLI change in a later
governance record. No database schema or production installation state is
changed; local test directories are temporary and removed by the test harness.

## Breaking Change

None. The launcher accepts an older local manifest during an explicitly
index-selected upgrade only when target and channel match; local-only commands,
manifest validation, service boundaries, browser worker behavior, credentials,
Matrix integration, and stable signed release requirements are unchanged.

## Backport Target

none
