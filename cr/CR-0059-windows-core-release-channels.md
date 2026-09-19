# CR-0059: Windows Core release channels and lifecycle closure

Base: main
Head or Range: 54f73cb544f9af76fba9d202aa3b797803fb1182
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(windows): add Core release channels and lifecycle
Revision: 2
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: ac82dce85add95fae54821311fd683a51889b73f
Head OID: 54f73cb544f9af76fba9d202aa3b797803fb1182
Integrated Result: pending

## Summary

Complete the Windows first-run Core workflow. Settings is placed in the
NavigationView footer and exposes separate test and stable Core catalogs, a
version selector, install/replace/start/stop actions, and explicit uninstall.
The launcher now installs the selected release index and persists the installed
manifest so the UI can recover the selected channel and version after restart.

The build workflow publishes untagged `test-<run>-<sha12>` artifacts to the
test catalog and tagged `v<major>.<minor>.<patch>` artifacts to the stable
catalog. The installer carries a matching Core payload, and the Windows CI
smoke path exercises component install, Core startup, readiness, and stop.

## Motivation

Core operations previously depended on a manually prepared data directory and
did not provide a complete UI-managed install path. A user installing the
Windows client must be able to select a release channel, install the Core
payload, start it, replace it with another catalog release, stop it, and
uninstall it without manually launching the service or aligning directories.

## Test Evidence

Local checks:

`go test -count=1 ./...`

`go vet ./...`

`./scripts/test_build_contract.sh`

`./scripts/test_release_catalog.sh`

`git diff --check`

The authoritative Windows validation is GitHub Actions Run 393
(`35432752682`, https://github.com/Semcosm/chuzi/actions/runs/35432752682), whose `windows-2022` build passed the named-pipe transport
test, native C# Core named-pipe handshake smoke test, Core lifecycle smoke
test, self-contained installer build, and installed UI startup smoke test.

## Risk

The UI starts a separate Core process and persists its PID only to recover a
process belonging to the same installed executable. Startup timeouts terminate
unresponsive processes so a failed attempt cannot leave a pipe or database lock
behind. Explicit uninstall has a narrow fallback for legacy installations that
lack launcher metadata; it removes only known Core binaries and worker files.
Release catalogs are validated for channel, target, version, commit, hashes,
and HTTPS origin before any remote component is installed.

## Rollback

Revert the implementation commit and stop using the test catalog branch. The
existing Core protocol, database schema, and installer data directory remain
compatible; no migration or irreversible data operation is introduced.

## Breaking Change

No Core API or storage protocol breaking change. The Windows UI now expects the
release-catalog branch for remote test/stable version choices, but a matching
bundled payload remains available for local fallback when its manifest matches.

## Backport Target

none
