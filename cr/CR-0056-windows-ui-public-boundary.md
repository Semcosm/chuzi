# CR-0056: expose Windows UI boundary models

Base: main
Head or Range: fix/windows-ui-public-contract
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: fix(windows): expose UI boundary models
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 0cfca60b83bd2fceef358495cfe68c098816d565
Head OID: 0cfca60b83bd2fceef358495cfe68c098816d565
Integrated Result: pending

## Summary

Make the Windows UI model and Core snapshot types public at the page boundary.
This fixes the Windows App SDK build failure caused by public page methods,
events, and event arguments exposing internal types.

## Motivation

The first Windows Core/settings/plugin UI implementation compiled in local
source inspection but failed on the GitHub Windows runner with C# accessibility
errors such as CS0051, CS0050, and CS7025. The page API is intentionally public
because XAML-generated code and the application shell consume it, so the DTOs
and snapshot types must have matching accessibility.

## Test Evidence

`./scripts/test_build_contract.sh`

`go test ./...`

`go vet ./...`

`git diff --check`

The Windows MSIX build remains authoritative on the GitHub Actions
`windows-2022` runner; this Linux checkout cannot execute the native publish.

## Risk

The change only widens the accessibility of UI boundary types. It does not
change Core protocol behavior, storage, credentials, plugin policy, or process
lifecycle semantics.

## Rollback

Revert the change record and restore the internal type declarations. No runtime
data or migration rollback is required.

## Breaking Change

No. The change resolves a build-time accessibility mismatch and preserves the
existing UI behavior and public method signatures.

## Backport Target

none
