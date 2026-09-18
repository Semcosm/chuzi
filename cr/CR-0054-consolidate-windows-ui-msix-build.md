# CR-0054: consolidate Windows UI and MSIX build

Base: main
Head or Range: test/winui-msix-20260918
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: chore(windows): consolidate UI and MSIX build
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: ac82dce85add95fae54821311fd683a51889b73f
Head OID: pending
Integrated Result: pending

## Summary

Consolidate the Windows native client and MSIX packaging paths into one
`chuzi-build-windows-ui` job. The job now creates package assets and the CI
certificate, invokes the shared Windows UI build script in `PackagedMsix` mode,
runs the native Core named-pipe test, and uploads the self-contained MSIX
artifact. The script retains an explicit `UnpackagedZip` mode for local
diagnostics.

## Motivation

The previous workflow checked out and restored the same Windows project twice:
once for the unpackaged UI zip and again for the MSIX. The zip was not consumed
by `assemble_target`, so the extra job only duplicated work and added a second
failure surface. A single packaging job keeps the artifact contract aligned with
the tested deployment mode and removes an unused dependency from target assembly.

## Test Evidence

The implementation was verified with:

`./scripts/validate_policy_manifest.sh`

`./scripts/validate_quality_profile.sh`

`./scripts/validate_supply_chain_profile.sh`

`./scripts/validate_action_pinning.sh`

`./scripts/validate_repository_shape.sh`

`./scripts/test_build_contract.sh`

`git diff --check`

The Windows MSIX build remains authoritative on the GitHub Actions
`windows-2022` runner; this Linux checkout cannot execute the native publish.

## Risk

The CI Windows artifact changes from an unpackaged zip to the signed
`chuzi-windows-msix-self-contained` package plus its test certificate. Local
diagnostic callers must use `-Mode UnpackagedZip`; the service/Core API boundary
and target release assembly are unchanged.

## Rollback

Revert this change record to restore separate Windows UI zip and MSIX jobs. No
runtime data, credentials, or storage migration is involved.

## Breaking Change

No service or Core API breaking change. The CI artifact name and the default
Windows UI build workflow are packaging-contract changes; consumers of the old
`ci-ui-windows` zip must move to the MSIX artifact or invoke the explicit local
diagnostic mode.

## Backport Target

none
