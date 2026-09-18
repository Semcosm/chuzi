# CR-0056: expose Windows UI boundary models

Base: main
Head or Range: fix/windows-ui-build-naming
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: fix(windows): restore packaged WinUI build
Revision: 2
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 0b036431b7e085cc596eb9f24f1facbd2e44d06c
Head OID: 0b036431b7e085cc596eb9f24f1facbd2e44d06c
Integrated Result: pending

## Summary

Make the Windows UI model and Core snapshot types public at the page boundary.
This fixes the Windows App SDK build failure caused by public page methods,
events, and event arguments exposing internal types.

The packaged XAML build also avoids a generated field name colliding with the
`CoreStatus` enum and removes the unused Win32 exception variable.

## Motivation

The first Windows Core/settings/plugin UI implementation compiled in local
source inspection but failed on the GitHub Windows runner with C# accessibility
errors such as CS0051, CS0050, and CS7025. The page API is intentionally public
because XAML-generated code and the application shell consume it, so the DTOs
and snapshot types must have matching accessibility.

The next packaged build exposed a second compiler error because the settings
page named a `TextBlock` `CoreStatus`, shadowing the enum used by the page
controller. Giving the generated field a distinct name keeps XAML and C# symbol
resolution unambiguous.

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
