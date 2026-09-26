# CR-0069: add Windows remote-login flow

Base: main
Head or Range: feat/windows-ui-client-foundation
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(ui): embed native Windows RDP in desktop clone
Revision: 2
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 2a69ed603dd8acbb2a61af16f25999f7b7b0946f
Head OID: 2a69ed603dd8acbb2a61af16f25999f7b7b0946f
Integrated Result: pending

## Summary

Add a Windows-only desktop-clone window to the Slint client. The Remote page
accepts a host or IP and opens `mstsc.exe /v:<host> /f /prompt`, following the
native MSTSC launch shape used by the referenced RDP Wrapper project without
copying its code or its system-patching behavior. Chuzi then locates the new
MSTSC top-level window, removes its outer frame, and hosts it inside a native
Win32 child region owned by the Slint desktop-clone window.

The clone window keeps its own title bar, status indicator, hide action, and
disconnect action. Windows owns the sign-in dialog; Chuzi does not collect,
store, or inject the RDP password. The implementation does not modify
BetterGI, use its startup chain, create child sessions, or launch
`BetterGI.exe`.

## Motivation

The first remote-control workflow needs a visible, self-owned window that can
contain the native RDP desktop while preserving Windows' normal authentication
and authorization behavior. Keeping MSTSC as the connection client avoids
reimplementing the RDP protocol and keeps Chuzi independent from BetterGI's
GPL-licensed startup/windowing code.

## Test Evidence

`cargo fmt --manifest-path ui/windows/Cargo.toml -- --check`

`cargo check --manifest-path ui/windows/Cargo.toml`

`cargo test --manifest-path ui/windows/Cargo.toml --no-fail-fast`

`cargo run --manifest-path ui/windows/Cargo.toml --example layout_snapshot -- --output /tmp/chuzi-windows-layout`

`cargo run --manifest-path ui/windows/Cargo.toml --example layout_snapshot -- --page remote --output /tmp/chuzi-windows-remote-layout`

`cargo run --manifest-path ui/windows/Cargo.toml --example layout_snapshot -- --page desktop-clone --output /tmp/chuzi-windows-desktop-clone-layout`

`git diff --check`

The current Linux environment does not have a Rust Windows target/toolchain,
so the `cfg(windows)` HWND discovery and MSTSC embedding path still needs a
native Windows build check.

## Risk

The prototype depends on Windows Remote Desktop being enabled and the selected
account being permitted to log in. MSTSC is an external Windows process and
its top-level window class/shape can vary by Windows release. If multiple
MSTSC windows already exist, the PID-first lookup is authoritative and the
class-name fallback should be tightened before relying on it for unattended
operation. BetterGI process/session orchestration remains out of scope.

## Rollback

Revert the commit containing this CR and the Windows UI/`windows-sys` changes.
The existing launcher/Core pages remain available; no persisted schema,
credential store, or service data migration is involved.

## Breaking Change

None to the Core API, launcher protocol, or persisted storage. The Windows
client gains a platform-specific Remote page and requires the normal Windows
Remote Desktop client for that operation.

## Backport Target

none
