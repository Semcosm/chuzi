# CR-0071: replace desktop clone with Rust FreeRDP RDP client

Base: main
Head or Range: feat/windows-ui-client-foundation
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(ui): replace desktop clone with Rust FreeRDP RDP client
Revision: 4
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 6f92f28bb70d52adbd9576fcdfef4103c30d0898
Head OID: a0ecf9e2d0108f99b3cc4bc4a52ae9930f90c096
Integrated Result: pending

## Summary

Replace the Windows desktop-clone placeholder with a Rust-owned RDP client
using FreeRDP for TLS/NLA negotiation, protocol handling, and graphics
decoding. `desktop_rdp.rs` owns the background event loop, receives connection
parameters from the RDP page, initializes FreeRDP GDI as BGRX32, copies
`primary_buffer` during `EndPaint`, and publishes an owned framebuffer to the Slint
`DesktopRdpWindow`. The UI page, callbacks, snapshot selector, and build
dependencies now use RDP terminology; no MSTSC, ActiveX, Win32 child-window
embedding, or desktop capture is used. The embedded Windows client initializes
Winsock for the FreeRDP session before hostname resolution and socket creation.

The current vertical slice displays the remote framebuffer and reports
connection, resize, failure, and disconnect states. The RDP page collects the
host, port, domain, username, and password directly; it does not open a
Windows CredUI prompt, and asynchronous connection errors are mirrored back to
that page. Keyboard and mouse input forwarding remains a separate
input-boundary change.

## Motivation

The previous window was only a visual shell and its historical native-client
path was not the intended transport boundary. FreeRDP provides the protocol
implementation and keeps the Rust client focused on session lifetime,
credential handling, framebuffer ownership, and Slint presentation. Copying
the buffer at `EndPaint` prevents the UI from retaining FreeRDP-managed memory
and preserves the full current frame across partial RDP updates.

## Test Evidence

`cargo fmt --manifest-path ui/windows/Cargo.toml -- --check`

`cargo check --manifest-path ui/windows/Cargo.toml --all-targets`

`cargo test --manifest-path ui/windows/Cargo.toml --no-fail-fast`

`cargo run --manifest-path ui/windows/Cargo.toml --example layout_snapshot -- --page rdp --output /tmp/chuzi-rdp-layout-final-2`

`go test ./...`

`go vet ./...`

`./scripts/validate_policy_manifest.sh && ./scripts/validate_quality_profile.sh && ./scripts/validate_supply_chain_profile.sh && ./scripts/validate_action_pinning.sh && ./scripts/validate_repository_shape.sh && ./scripts/test_build_contract.sh`

`git diff --check`

Windows runtime diagnosis confirmed that native `mstsc` and TCP connectivity
were healthy while the embedded FreeRDP path failed at `getaddrinfo`; the
session now owns the required Winsock startup and cleanup lifecycle.

The repository Linux host cannot execute the Windows MSVC/vcpkg build. The
Windows workflow installs `freerdp[client]` through vcpkg, generates bindgen
FFI declarations, and builds the Slint client with the static MSVC CRT; that
runner result is required before integration.

## Risk

FreeRDP and bindgen add a Windows-native dependency and the workflow depends
on the runner's Visual Studio SDK, LLVM/libclang, and vcpkg installation. The
wrapper limits the native surface to the FreeRDP session, settings, GDI, and
event-loop functions, but a Windows runner build is still required to verify
the exact 3.x ABI and static link closure. The RDP certificate behavior is not
disabled or unconditionally trusted. Credentials are copied into the in-memory
session target, cleared from the UI password field after submission, and
password strings are wiped when their Rust owner is dropped. FreeRDP errors
are shown on the RDP page without displaying credential values.

The display slice does not forward keyboard or mouse input, and the current
frame is copied on each completed paint batch, so high-update-rate sessions
may use more CPU and memory than a dirty-rectangle transport.

## Rollback

Revert the implementation commit and restore the previous Slint-only window
and page names. Remove the FreeRDP vcpkg/bindgen preparation from the Windows
workflow and revert the Windows-only dependency additions. No Core API,
launcher protocol, storage schema, credential-store record, or runtime data
migration is involved.

## Breaking Change

The internal Windows UI module and page contract changes from
`desktop_clone`/`desktop-clone` and the old remote callback to
`desktop_rdp`/`rdp` and `connect-rdp`. Snapshot callers must use
`--page rdp`. The Core API and persisted service contracts are unchanged.

## Backport Target

none
