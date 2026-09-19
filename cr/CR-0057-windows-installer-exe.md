# CR-0057: replace Windows MSIX with installer EXE

Base: main
Head or Range: feat/windows-installer-exe
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(windows): replace MSIX with installer EXE
Revision: 3
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: b4cd426af69ad1d075b40f4e06235e730d0300fe
Head OID: b4cd426af69ad1d075b40f4e06235e730d0300fe
Integrated Result: pending

## Summary

Replace the Windows MSIX distribution path with a conventional self-contained
EXE installer. The installer publishes the WinUI 3 client unpackaged, carries
the .NET and Windows App SDK runtime files, installs under Program Files, and
creates Start Menu and optional desktop shortcuts.

Revision 2 fixes the Inno Setup preprocessor escaping for the stable installer
AppId so the Windows runner can compile the setup executable. Revision 3
restores the WinUI control resource dictionary required by the Settings,
Plugins, and Overview pages, constructs child pages after the main window XAML
is initialized, keeps the unpackaged application resource surface minimal,
copies the application PRI required by unpackaged WinUI startup, adds startup
exception diagnostics, and adds an installed-executable startup smoke test to
CI.

## Motivation

The MSIX flow required a signing certificate and AppX deployment steps that
made first-run installation unnecessarily difficult for local users. The
Windows client already has a working unpackaged WinUI path, so the deployment
surface should use a standard installer while preserving the same Core payload,
launcher boundary, and per-machine data directory behavior.

## Test Evidence

`./scripts/test_build_contract.sh`

`git diff --check`

The Windows installer publish, Inno Setup compilation, Core named-pipe contract
test, and installed-executable startup smoke test remain authoritative on the
GitHub Actions `windows-2022` runner.

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
