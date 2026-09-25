# CR-0076: unify test, nightly, and stable release channels

Base: main
Head or Range: feat/release-channel-lifecycle
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: ci: unify release channel lifecycle
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: f7e34c98269e175431c61ae15e0e74c9df2b03de
Head OID: pending
Integrated Result: pending

## Summary

Define separate test, nightly, and stable release channels across the build
workflow, artifact names, release catalogs, acceptance scripts, and operator
documentation. Resolve channel and version once per Actions run.

## Motivation

The workflow previously reused nightly artifact names for test and stable
builds and duplicated version derivation in multiple jobs. That made test
artifacts difficult to consume and allowed the documented promotion lifecycle
to diverge from the actual workflow.

## Test Evidence

`scripts/test_release_channels.sh`

`scripts/test_release_catalog.sh`

`scripts/test_build_contract.sh`

`./scripts/validate_quality_profile.sh`

`./scripts/validate_supply_chain_profile.sh`

`./scripts/validate_action_pinning.sh`

`./scripts/validate_repository_shape.sh`

`scripts/test_nightly_package.sh`

`scripts/test_nightly_artifact_validator.sh`

`git diff --check`

The local environment does not provide actionlint or a YAML library; the
workflow was inspected for pinned actions, dependency expressions, and all
channel-specific artifact paths.

## Risk

Actions artifact consumers and release retry inputs must use the new explicit
channel prefixes. The retry validator is intentionally restricted to stable
tag runs. No application runtime or persisted data format changes.

## Rollback

Revert this change record and the channel resolver, workflow, catalog publisher,
acceptance script, and documentation changes. Existing nightly package formats
remain unchanged.

## Breaking Change

Actions artifact names for test and stable builds become explicit
`chuzi-test-*` and `chuzi-stable-*` names. Consumers must select the matching
channel prefix.

## Backport Target

none
