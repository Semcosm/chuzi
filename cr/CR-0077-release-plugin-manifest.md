# CR-0077: publish built-in Genshin adapter metadata

Base: main
Head or Range: feat/windows-ui-client-foundation
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: fix(release): publish built-in Genshin adapter metadata
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 9ef24664f349daaad7a4aaaabf8109cb37af803c
Head OID: 9cfc349fb57f356ae983ca63443c2aef1b73e035
Integrated Result: pending

## Summary

Export the built-in headless-CDP adapter from the browser worker manifest into
the release manifest consumed by the launcher and Windows Plugins page. Keep it
visible as a non-installable adapter because it ships inside browser-worker.

## Motivation

Test channel packages currently generate an empty plugins array even though
the browser worker already declares and implements the Genshin Cloud Game
adapter. The launcher therefore has no adapter row to display after refresh.

## Test Evidence

scripts/test_nightly_package.sh, scripts/test_build_contract.sh, go test ./...,
go vet ./..., npm --prefix browser-worker test, scripts/test_release_catalog.sh,
scripts/test_launcher_consumer.sh, and git diff --check pass. A real Linux Test
build generated a valid release manifest and chuzi-launcher plugin-list returned
chuzi.headless-cdp with genshin-cloudgame@1.

## Risk

Release assembly now fails closed when browser-worker adapter metadata is
missing, malformed, duplicated, or points outside the staged worker payload.
The built-in adapter remains non-installable, so no new download or trust path
is introduced.

## Rollback

Revert the release manifest generator and package contract fixture changes. The
launcher will return to an empty adapter list until a replacement metadata path
is available.

## Breaking Change

None to runtime adapter or launcher command protocols. Release manifests now
include the built-in adapter descriptor.

## Backport Target

none
