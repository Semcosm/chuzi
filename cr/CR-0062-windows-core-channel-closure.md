# CR-0062: close the Windows Core channel and lifecycle loop

Base: main
Head or Range: feat/windows-core-release-loop
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(windows): close Core channel and lifecycle loop
Revision: 2
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 61c9ad70581eab60c8955d6ab616c7d29158f035
Head OID: 3ca9d05304e6823f9cd1cbaaafef88faaaf2c2b6
Integrated Result: pending

## Summary

Exercise the Windows UI's real Core lifecycle boundary in CI, including
component installation, startup, named-pipe API calls, stop, restart, replace,
and uninstall. Keep the Settings page as the user-facing entry point for the
test/stable Core catalogs and publish test releases from untagged workflow
dispatches before stable annotated tags.

## Motivation

The existing Windows build proved that the Core executable and UI could start,
but it did not prove that the UI's launcher/controller path could complete an
installation or remove it. That left failures such as `pipe hasn't been
connected yet` and stale Core files visible only on a user's machine. A
controller-level smoke test must be authoritative before publishing a new
test-channel release, and the same release catalog must later expose the
stable tag without a second UI implementation.

## Test Evidence

Local checks:

`go test ./...`

`go vet ./...`

`./scripts/test_build_contract.sh`

`./scripts/test_release_catalog.sh`

`./scripts/validate_repository_shape.sh`

`git diff --check`

Windows CI additionally builds `CoreLifecycleSmoke.csproj` and runs the
install/start/API/stop/restart/replace/uninstall sequence against the exact
payload shipped in the installer.

## Risk

The new smoke harness is test-only and does not alter the Core API or persisted
data format. Release catalog publication remains an append/update operation on
the dedicated catalog branch; stable publication still requires an annotated
semver tag and signed release inputs.

## Rollback

Revert the implementation commit. Existing Core manifests, component archives,
and the named-pipe protocol remain compatible.

## Breaking Change

No.

## Backport Target

none
