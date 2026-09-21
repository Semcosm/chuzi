# CR-0069: add Windows remote-login flow

Base: main
Head or Range: feat/windows-ui-client-foundation
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(ui): add Windows remote-login flow
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 6f92f28bb70d52adbd9576fcdfef4103c30d0898
Head OID: 5fc7a2fc78453933cce02ce46497481593049bf9
Integrated Result: pending

## Summary

Add a Remote page to the Windows Slint client. The page asks for a host or IP,
Windows username, and password, clears the password field after submission,
and starts the Windows Remote Desktop client. The Windows implementation stores
the credential as a session-scoped `TERMSRV/<host>` Credential Manager entry,
starts `mstsc.exe`, and removes the temporary entry when the client exits.

Non-Windows builds keep the page available for shared UI testing but fail the
operation with a platform-specific error. This change does not modify
BetterGI, create Windows sessions, or launch `BetterGI.exe` yet.

## Motivation

The first remote-control workflow needs an explicit Windows login boundary
before a later step can launch BetterGI with `--instance childSession` inside
the selected RDP session. Keeping the password out of process arguments and
removing the session credential after use limits exposure in the initial
prototype.

## Test Evidence

`cargo fmt --manifest-path ui/windows/Cargo.toml -- --check`

`cargo check --manifest-path ui/windows/Cargo.toml`

`cargo test --manifest-path ui/windows/Cargo.toml --no-fail-fast`

`cargo run --manifest-path ui/windows/Cargo.toml --example layout_snapshot -- --output /tmp/chuzi-windows-layout`

`cargo run --manifest-path ui/windows/Cargo.toml --example layout_snapshot -- --page remote --output /tmp/chuzi-windows-remote-layout`

`git diff --check`

The current Linux environment does not have a Rust Windows target/toolchain,
so the `cfg(windows)` Credential Manager path still needs a native Windows
build check.

## Risk

The prototype depends on Windows Remote Desktop being enabled and the supplied
user being permitted to log in. Credential Manager uses one target per entered
host, so simultaneous logins to the same host with different credentials
should be serialized until the embedded/session-aware host is implemented. The
UI does not enumerate users or persist passwords. `mstsc.exe` remains an
external window; BetterGI process/session orchestration is a follow-up change.

## Rollback

Revert the commit containing this CR and the Windows UI/`windows-sys` changes.
The existing launcher/Core pages remain available; no persisted schema or
service data migration is involved.

## Breaking Change

None to the Core API, launcher protocol, or persisted storage. The Windows
client gains a platform-specific Remote page and requires the normal Windows
Remote Desktop client for that operation.

## Backport Target

none
