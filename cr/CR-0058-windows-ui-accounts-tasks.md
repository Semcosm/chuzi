# CR-0058: add Windows account and task workspace

Base: main
Head or Range: fix/windows-ui-startup
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(windows): add account and task workspace
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: b4cd426af69ad1d075b40f4e06235e730d0300fe
Head OID: 7cc475456592e5b4e7f5114beaa02bca1309111c
Integrated Result: pending

## Summary

Extend the native Windows workspace with Accounts and Tasks pages. Accounts
can look up an authorized account through `chuzi.core/v1` and submit a request;
Tasks can refresh or cancel a request by ID. The UI reuses the existing Core
transport and keeps credentials, storage, and browser profiles behind Core.

## Motivation

The Windows client already provides first-run Core lifecycle controls, settings,
and plugin management, but users still need a supported path from an installed
client to account status and request execution. These pages complete the next
usable workflow without inventing a client-side account inventory or expanding
the Core protocol.

## Test Evidence

The implementation was verified with:

`./scripts/test_build_contract.sh`

`git diff --check`

The local checkout does not contain the .NET/Windows App SDK toolchain, so the
Windows build, named-pipe contract test, and installed-client smoke test remain
authoritative on the GitHub Actions Windows runner.

## Risk

The new UI calls only the existing redacted Core API methods and accepts an
authorized account ID or request ID from the user. It does not persist
credentials, open the store, access browser profiles, or bypass plugin and Core
authorization boundaries. The main remaining risk is native XAML/C# compile
compatibility, covered by the Windows CI build.

## Rollback

Revert the account/task UI commit and this CR. Existing Overview, Settings, and
Plugins workflows remain unchanged, and no Core protocol or storage migration
is introduced.

## Breaking Change

No. The change is additive to the Windows UI and uses existing `chuzi.core/v1`
methods.

## Backport Target

none
