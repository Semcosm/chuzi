# CR-0068: fix Windows nightly package fixture for browser launcher

Base: main
Head or Range: feat/windows-ui-client-foundation
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: fix(build): include Windows browser launcher in nightly fixture
Revision: 2
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 2a69ed603dd8acbb2a61af16f25999f7b7b0946f
Head OID: 2a69ed603dd8acbb2a61af16f25999f7b7b0946f
Integrated Result: pending

## Summary

Update the synthetic Windows package fixture used by
`scripts/test_nightly_package.sh` to create and include
`chuzi-browser-launcher.exe` in the service component archive.

## Motivation

The configurable headed-CDP runtime makes the Windows browser launcher a
required service resource. The fixture still modeled the older service
payload, so the nightly package contract failed before release validation even
though the real Windows build produced the helper correctly.

## Test Evidence

`scripts/test_nightly_package.sh`

`scripts/test_nightly_artifact_validator.sh`

`scripts/test_release_catalog.sh`

`scripts/test_build_contract.sh`

`git diff --check`

## Risk

The change is limited to synthetic test data and the expected Windows service
archive contents. It does not alter production packaging or release-manifest
generation logic.

## Rollback

Revert the commit containing this CR. The prior fixture can be restored, but
the nightly package contract will again fail against the current Windows
manifest requirements.

## Breaking Change

None.

## Backport Target

none
