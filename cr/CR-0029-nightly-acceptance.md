# CR-0029: record the first nightly build acceptance

Base: main
Head or Range: ae8b014871bb43a2b9bc87719c040053173bbc1e
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: chore(governance): record CR-0029 nightly acceptance
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: ae8b014871bb43a2b9bc87719c040053173bbc1e
Head OID: ae8b014871bb43a2b9bc87719c040053173bbc1e
Integrated Result: pending

## Summary

Accept the first manually triggered nightly build after CR-0028. Run
`34786996598` completed from `main` at
`ae8b014871bb43a2b9bc87719c040053173bbc1e` with a successful aggregate
`chuzi-build` check and successful native launcher/runtime checks on all four
targets. The nightly version is `nightly-226-ae8b014871bb` and no tag or
GitHub Release was created.

## Motivation

The launcher, UI, browser worker, Rust runtime, service, component archives,
release manifest, and release index now need one recorded end-to-end acceptance
result before UI work proceeds toward a distributable test build. This record
binds the build SHA, artifact digests, package contents, and known local
platform prerequisites without treating CDP endpoint discovery as business
success or contacting a production service.

## Test Evidence

GitHub Actions run:

`https://github.com/Semcosm/chuzi/actions/runs/34786996598`

`workflow_dispatch` on `main` completed successfully. The four matrix jobs
(`windows-amd64`, `linux-amd64`, `linux-arm64`, and `darwin-arm64`) and the
aggregate `chuzi-build` job passed. The native desktop helper contract passed
on every target; Linux X11 and Wayland WebKitGTK smoke tests passed on both
Linux jobs. `stable-release` was skipped as required.

The uploaded Actions artifacts are retained until 2026-09-27 and have these
GitHub artifact digests:

`chuzi-nightly-windows-amd64` (10327022063):
`sha256:8bbab07c19957d0bbde030fa1062c0fc1d4b6dc7778e287dad046d735b5f2912`

`chuzi-nightly-linux-amd64` (10327465814):
`sha256:7c03e707eae33854f5fcc78f690302103b126a439cc60054c6f50ae6912109d9`

`chuzi-nightly-linux-arm64` (10327426029):
`sha256:cf1e1aa0476477d8ba4a0ea3245ec63119f594f228488924a2a86a316c42677b`

`chuzi-nightly-darwin-arm64` (10327241367):
`sha256:ca45576ca06ca0df5048db1bd6ab7aef1925406ad651bf3e2e9e729128721a90`

Offline artifact checks verified all 24 downloaded sidecar SHA-256 files,
validated all four release indexes with `scripts/validate_release_index.py`,
and checked every bundle and component archive for safe paths, required
launcher/UI/service/worker/runtime payloads, and manifest resource coverage.

Local checks passed:

`go test -count=1 ./...`

`go vet ./...`

`CHUZI_TEST_DEBUG=1 CHUZI_WORKER_DEBUG=1 npm --prefix browser-worker test`
(five consecutive serialized runs)

`scripts/test_launcher_ui.sh`

`cargo fmt --manifest-path launcher-ui/Cargo.toml -- --check`

`cargo test --locked --manifest-path launcher-ui/Cargo.toml`

`cargo fmt --manifest-path browser-runtime/Cargo.toml -- --check`

`cargo test --locked --manifest-path browser-runtime/Cargo.toml -- --nocapture`

`go run ./cmd/service -self-test -worker-command node -worker-script browser-worker/src/worker.mjs`

`scripts/test_build_contract.sh`, `scripts/test_nightly_package.sh`, the
repository policy/profile/action/shape validators, and `git diff --check`.

The local machine lacks `pango`, GTK/WebKitGTK development packages,
`xvfb-run`, and `weston`, so native Linux WebView compilation/smoke was not
run locally. The same feature compilation and X11/Wayland smoke contract passed
on the native GitHub runners. All local browser tests used the checked-in fake
CDP browser and local test page; no Chromium, real account, credential,
Matrix token, or production release host was contacted.

## Risk

Nightly Actions artifacts are temporary (14 days) and are not a stable public
download endpoint. Native launcher UI deployment still depends on the target
platform WebView runtime. The acceptance checks package integrity and the
headless-CDP protocol chain only; it does not claim real-account automation or
stable release signing. The release workflow continues to require a signed
semver tag before publishing a stable release.

## Rollback

No application state or schema changes are introduced. Mark this acceptance
record superseded if a later nightly exposes a regression, stop consuming the
expired artifact, and continue using the existing local launcher/CLI. A future
governance change can remove or revise this record without uninstalling any
component.

## Breaking Change

None. This record does not change the service, launcher, UI, worker, runtime,
release index, credential, Matrix, or account-state contracts.

## Backport Target

none
