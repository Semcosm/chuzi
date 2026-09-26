# CR-0078: fix Windows RDP keyboard focus and add input diagnostics

Base: main
Head or Range: fix/launcher-test-channel
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: fix(ui): restore Windows RDP keyboard focus and diagnostics
Revision: 2
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 2a69ed603dd8acbb2a61af16f25999f7b7b0946f
Head OID: 2a69ed603dd8acbb2a61af16f25999f7b7b0946f
Integrated Result: pending

## Summary

Restore keyboard focus for the embedded Windows RDP framebuffer when its
full-screen pointer surface receives a press. Add opt-in, redacted input-path
diagnostics controlled by `CHUZI_RDP_INPUT_DEBUG=1`, covering focus changes,
Slint key events, queue delivery, and FreeRDP input return values.

## Motivation

The RDP framebuffer wrapped a `FocusScope` around a full-screen `TouchArea`.
The child consumed the mouse press before the parent `FocusScope` could apply
`focus-on-click`, so the RDP window never received keyboard events after a
normal click on the remote desktop. The failure was silent because no input
telemetry showed whether focus, encoding, queue delivery, or the FreeRDP calls
were missing.

## Test Evidence

`cargo fmt --manifest-path ui/windows/Cargo.toml -- --check`

`cargo check --manifest-path ui/windows/Cargo.toml`

`cargo test --manifest-path ui/windows/Cargo.toml` (16 tests)

`git diff --check`

The Windows FreeRDP FFI path must be verified by the GitHub Actions Windows
build. Manual validation should click the remote desktop, type a printable
key, press Backspace or an arrow key, and inspect the generated
`rdp-debug/rdp-input-*.log` file with `CHUZI_RDP_INPUT_DEBUG=1`.

## Risk

The fix changes only focus acquisition and diagnostic plumbing. Diagnostics
record key lengths and Unicode code points, encoded scan-code/Unicode types,
queue outcomes, and native return values; they do not record credentials, raw
key text, screenshots, or remote desktop contents. The debug file is written
only when explicitly enabled.

## Rollback

Revert the focus callback, input diagnostic logger, worker logging, and this
change record. Existing RDP pointer, wheel, and framebuffer behavior remain
available.

## Breaking Change

None to Core API, launcher protocol, storage schema, or persisted runtime
state. The Windows RDP window gains explicit focus acquisition on pointer
press and an opt-in local diagnostic file.

## Backport Target

none
