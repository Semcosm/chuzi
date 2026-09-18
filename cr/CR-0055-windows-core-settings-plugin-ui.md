# CR-0055: Windows Core, settings, and plugin management UI

Base: main
Head or Range: feat/windows-launcher-ui
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(windows): add Core setup, settings, and plugin management UI
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 1259f87ee88295d68f5bebf2fda96f16b7ccab72
Head OID: 6b33800423ba018a170db9f22cf8c62429794cf7
Integrated Result: pending

## Summary

Add the first usable WinUI 3 application surface for the Windows client. The
client provides Core installation and lifecycle status, launcher behavior
settings, and plugin list/install/trust/enable/disable/remove actions. The
Windows MSIX now embeds the matching Go service, launcher, browser worker, and
release manifest as a verified Core payload.

## Motivation

The initial Windows package only proved that the WinUI startup path worked. A
user could install it but had no supported way to install or start Core, edit
launcher settings, or manage plugins. These workflows must remain behind the
existing launcher and Core boundaries rather than giving the UI direct access to
storage, credentials, profiles, or plugin trust state.

## Test Evidence

The implementation was verified with:

`./scripts/test_build_contract.sh`

`git diff --check`

`go test ./...`

The Windows MSIX publish is authoritative on the GitHub Actions `windows-2022`
runner; this Linux checkout does not have the .NET/Windows App SDK toolchain.

## Risk

The Windows UI artifact now contains the Core payload and is larger than the
previous UI-only package. Core startup uses the installed package payload and a
deployment-derived data directory; service errors are classified before being
shown to users. The service remains a separate process and the UI never reads
its database or secrets.

## Rollback

Revert this change record and restore the previous smoke UI and UI-only MSIX
workflow. No Core protocol, database schema, credential, or runtime migration
is introduced.

## Breaking Change

No Core API or service protocol breaking change. The Windows MSIX contents and
first-run workflow are expanded; unpackaged diagnostic builds still require a
separately supplied Core payload or service.

## Backport Target

none
