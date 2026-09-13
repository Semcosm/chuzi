# CR-0026: add UI-neutral launcher runtime controls

Base: main
Head or Range: 97abff8407ad67a15bd70b264754d10f3311e2bd
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(launcher): add UI-neutral runtime controls
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: dae2e88fa4b53c9054874853dcc03e8d233100cd
Head OID: 97abff8407ad67a15bd70b264754d10f3311e2bd
Integrated Result: pending

## Summary

Extend the existing UI-neutral launcher backend with durable behavior settings,
an inter-process mutation lock, cancellable progress events, and a shell-free
foreground service-process controller. `cmd/launcher` exposes settings,
progress, lock, and signal-cancellation controls while continuing to consume
only a validated local manifest and explicitly selected local source tree.
State and settings writes use restricted permissions and staged replacement;
launcher state rejects trailing JSON, resource repair refuses non-regular
targets, and large file/archive copies check cancellation between bounded
reads. No Rust UI, GUI toolkit, network downloader, credential ingress,
Chromium, or plugin execution is added.

## Motivation

CR-0022 supplied component and plugin management but left lifecycle concerns
needed by a future desktop surface implicit: behavior settings had no durable
store, concurrent launchers could mutate the same installation, and long local
repairs offered no progress or cancellation boundary. This increment keeps
those policies in Go so a later Rust cross-platform UI can call the CLI or a
future IPC adapter without duplicating filesystem, trust, or rollback logic.

## Test Evidence

Local verification passed:

`go test ./... -count=1`

`go test -race ./...`

`go vet ./...`

`go build ./...`

Launcher test binaries compile for `windows-amd64`, `linux-arm64`, and
`darwin-arm64`; tests use temporary directories, synthetic archives, and a
local test-process helper only.

`./scripts/validate_policy_manifest.sh`,
`./scripts/validate_quality_profile.sh`,
`./scripts/validate_supply_chain_profile.sh`,
`./scripts/validate_action_pinning.sh`,
`./scripts/validate_repository_shape.sh`,
`./scripts/test_build_contract.sh`, and `git diff --check` passed.

No network source, real account, credential, browser, Chromium, Matrix server,
or plugin process is used.

## Risk

The launcher can mutate an operator-selected installation root. All manifest,
resource, plugin, archive, settings, and lock paths remain constrained to
validated absolute or root-contained paths. Mutating CLI operations acquire
`.chuzi/launcher.lock`; stale locks are not broken automatically and require
an operator to verify the recorded process before removal. Settings and
launcher metadata are mode `0600` and contain no credentials. Plugins remain
disabled and untrusted until an allowed signer is explicitly trusted, and no
plugin code is executed. `ProcessServiceController` manages only a foreground
child for the caller lifetime; systemd, launchd, and Windows service-manager
integration remain future work.

## Rollback

Stop any launcher process, disable the new settings/component/plugin commands,
and continue using manifest display and read-only verification. Revert the
launcher runtime-control files, CLI flags, tests, build-contract checks, and
documentation in a later governance change. Remove only verified stale lock,
settings, state, or plugin paths under the installation root; no database
migration or service data rewrite is required.

## Breaking Change

None to the service, browser Worker, automation adapter, Matrix boundary,
account state machine, storage schema, or release manifest format. The
launcher contract additions are additive and the existing component/plugin
commands retain their local-only behavior. Future Rust UI code must use these
interfaces rather than copying their filesystem policy.

## Backport Target

none
