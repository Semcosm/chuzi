# CR-0073: reduce Windows RDP frame presentation overhead

Base: main
Head or Range: feat/windows-ui-client-foundation
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: perf(ui): reduce Windows RDP frame presentation overhead
Revision: 2
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 6f92f28bb70d52adbd9576fcdfef4103c30d0898
Head OID: pending
Integrated Result: pending

## Summary

Compile the Windows Slint client with its WGPU FemtoVG renderer and WGPU 30
support. Configure FreeRDP GDI to produce RGBA32 pixels so the RDP worker can
copy complete rows directly into the Slint image buffer without a scalar
BGRX-to-RGBA conversion for every pixel. Add opt-in, redacted RDP performance
counters and a deterministic analyzer/test so local Windows measurements can
be compared with PresentMon without changing the CPU decode path or adding
VSync.

## Motivation

The RDP session was receiving frames, but presenting a full desktop frame
required a CPU channel conversion before Slint could display it. This change
removes that avoidable per-pixel work and makes the normal Windows renderer
path GPU-backed when a suitable adapter is available, while retaining the
existing software renderer fallback.

## Test Evidence

cargo fmt --manifest-path ui/windows/Cargo.toml

cargo check --manifest-path ui/windows/Cargo.toml --all-targets

cargo test --manifest-path ui/windows/Cargo.toml --no-fail-fast

go test ./...

go vet ./...

./scripts/validate_policy_manifest.sh && ./scripts/validate_quality_profile.sh && ./scripts/validate_supply_chain_profile.sh && ./scripts/validate_action_pinning.sh && ./scripts/validate_repository_shape.sh && ./scripts/test_build_contract.sh

git diff --check

Windows GitHub Actions must additionally verify FreeRDP 3.x binding generation,
the installer, and the installed Slint smoke test.

The CI contract also runs scripts/test_rdp_perf_analysis.sh, which validates
the analyzer against synthetic rdp_perf records. Real RDP FPS and final
desktop presentation timing still require a Windows host capture with
CHUZI_RDP_PERF=1 plus PresentMon.

## Risk

FreeRDP's RGBA32 GDI format and the generated Windows bindings must remain
compatible with the pinned FreeRDP 3.x package. WGPU renderer initialization
can fail on systems without a usable adapter, so the existing Slint fallback
must remain available. RDP decoding itself remains FreeRDP's existing path and
is not claimed to be hardware-decoded by this change.

## Rollback

Revert this change record and the associated Cargo, native-wrapper, and RDP
frame-copy changes. The prior BGRX conversion path remains a functional
fallback.

## Breaking Change

None to the Core API, launcher protocol, storage schema, or persisted runtime
state.

## Backport Target

none
