# CR-0057: replace Windows MSIX with installer EXE

Base: main
Head or Range: feat/windows-installer-exe
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(windows): replace MSIX with installer EXE
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 7e27d79a54447a2ee3263954e599895e95ed9abe
Head OID: 7e27d79a54447a2ee3263954e599895e95ed9abe
Integrated Result: pending

## Summary

Replace the Windows MSIX distribution path with a conventional self-contained
EXE installer. The installer publishes the WinUI 3 client unpackaged, carries
the .NET and Windows App SDK runtime files, installs under Program Files, and
creates Start Menu and optional desktop shortcuts.

## Motivation

The MSIX flow required a signing certificate and AppX deployment steps that
made first-run installation unnecessarily difficult for local users. The
Windows client already has a working unpackaged WinUI path, so the deployment
surface should use a standard installer while preserving the same Core payload,
launcher boundary, and per-machine data directory behavior.

## Test Evidence

`./scripts/test_build_contract.sh`

`git diff --check`

The Windows installer publish, Inno Setup compilation, and Core named-pipe
contract test remain authoritative on the GitHub Actions `windows-2022` runner.

## Risk

The Windows artifact changes from a signed MSIX to an unsigned installer EXE.
The installer requires administrative rights to write Program Files and leaves
`%ProgramData%\\chuzi` in place during uninstall so Core state is preserved.
The application remains self-contained and continues to use the existing Core
named-pipe and launcher boundaries.

## Rollback

Revert this change record and restore the MSIX project properties, certificate
generation, and `chuzi-windows-msix-self-contained` workflow artifact. No Core
protocol, database, credential, or migration rollback is required.

## Breaking Change

Yes for automation that expects the old MSIX artifact name or certificate. The
new supported Windows distribution artifact is `chuzi-windows-installer-exe`;
the application and Core API behavior are unchanged.

## Backport Target

none
