# CR-0055: Windows Core, settings, and plugin management UI

Base: main
Head or Range: 026bf7cb67d9591859c7980ea603fc7c2817d0a6
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: chore(governance): align CR-0055 head after main integration
Revision: 2
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 026bf7cb67d9591859c7980ea603fc7c2817d0a6
Head OID: 026bf7cb67d9591859c7980ea603fc7c2817d0a6
Integrated Result: pending

## Summary

Align the implementation record with the rebased main commit so the persisted
change record remains reachable while the final integration result is recorded.

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
