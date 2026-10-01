# CR-0089: add automatic Core startup setting

Base: main
Head or Range: 5c06950
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(ui): add automatic Core startup setting
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: b57ce246ea8dbf64ad642414a096f4e9ada3e60a
Head OID: 5c0695093ec2105beb305793ae876c4868fe7b53
Integrated Result: pending

## Summary

Add a durable launcher behavior setting and Windows Settings checkbox for
starting an already installed Core service when the Chuzi application launches.
The client loads the setting before its initial Core status refresh and starts
Core asynchronously when enabled.

## Motivation

Users who keep Chuzi available should not need to start Core manually after
each application launch. Installation remains explicit so the preference cannot
silently modify the installed payload.

## Test Evidence

`go test ./internal/launcher ./cmd/launcher`, `cargo fmt --manifest-path
ui/windows/Cargo.toml -- --check`, `cargo check --manifest-path
ui/windows/Cargo.toml --locked`, the Settings layout snapshot matrix at
800x600, 1120x760, and 1440x900 in Light and Dark, and `git diff --check`
passed. Older settings JSON without the new field defaults to disabled.

## Risk

Automatic startup only invokes `core-start` when Core is already installed; it
does not install components or access credentials. If Core is unavailable, the
existing status and diagnostic handling remains in control.

## Rollback

Revert the implementation commit and remove the new launcher field and Settings
checkbox. Existing settings files remain readable because the new field is
optional on load.

## Breaking Change

None. The JSON field is additive and disabled by default.

## Backport Target

none
