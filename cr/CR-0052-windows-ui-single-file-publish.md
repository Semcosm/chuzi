# CR-0052: publish Windows UI as a self-contained single file

Base: main
Head or Range: pending
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: fix(ui): publish WinUI single-file self-contained client
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: pending
Head OID: pending
Integrated Result: pending

## Summary

Enable the Windows App SDK unpackaged single-file publish targets and keep the
Windows UI self-contained. The generated application PRI remains in the
artifact.

## Motivation

The Windows 11 26200 host terminates the multi-file unpackaged client during
WinUI resource initialization with `0xC000027B` in `Microsoft.UI.Xaml.dll`.
Windows App SDK requires MSIX tooling and single-file self-extract for this
deployment shape; the previous publish omitted that bootstrap contract.

## Test Evidence

The Windows Actions UI build and the consumer Windows host launch must pass.

`git diff --check`

## Risk

The UI archive changes shape and is larger. Core service behavior and persisted
data are unchanged.

## Rollback

Revert this change and restore the previous unpackaged publish properties.

## Breaking Change

None for the Core service. The Windows UI artifact requires the new publish
layout.

## Backport Target

none
