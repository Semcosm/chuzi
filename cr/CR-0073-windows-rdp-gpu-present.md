# CR-0073: reduce Windows RDP frame presentation overhead

Base: main
Head or Range: feat/windows-ui-client-foundation
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: perf(ui): reduce Windows RDP frame presentation overhead
Revision: 7
Status: integrated
Decision: accepted
Policy Version: v0.3
Base OID: c5558e3fac0531e6253aea730263840411f53f88
Head OID: c5558e3fac0531e6253aea730263840411f53f88
Integrated Result: main@c5558e3fac0531e6253aea730263840411f53f88

## Summary

Compile the Windows Slint client with its WGPU FemtoVG renderer and WGPU 30
support. Configure FreeRDP GDI to produce RGBA32 pixels and add a persistent
CPU framebuffer so the RDP worker copies only the clipped, non-overlapping dirty
union reported by FreeRDP. Publish the newest complete snapshot on the Slint
timer, retain a legacy full-frame mode for A/B comparison, and add opt-in,
redacted RDP performance counters plus a deterministic analyzer/test so local
Windows measurements can be compared with PresentMon without changing the CPU
decode path or adding VSync.

## Motivation

The RDP session was receiving frames, but every update copied a complete desktop
image and handed each intermediate frame to the UI. This change keeps the
RGBA32 fast path, bounds worker copy traffic to dirty regions, coalesces pending
updates to the newest complete image, and makes the normal Windows renderer path
GPU-backed when a suitable adapter is available while retaining the existing
software renderer fallback.

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
the analyzer against synthetic rdp_perf records, including PowerShell-wrapped
multi-line JSON, corrected coalescing/UI-delivery ratios, and PresentMon CSV
metrics filtered to the client process. The UI handoff timer now covers the
frame-to-image conversion and Slint frame handoff instead of measuring only
the conversion call. Unit tests cover dirty rectangle clipping and union
partitioning, bounded partial copies, generation tracking, latest-snapshot
delivery, and unchanged-state suppression. The README records the 60-90 second
static, moving-window, animation/video, and resolution-change capture matrix plus
the required PresentMon CSV fields. Real RDP FPS and final desktop presentation
timing still require a Windows host capture with CHUZI_RDP_PERF=1 plus PresentMon.

## Risk

FreeRDP's RGBA32 GDI format and the generated Windows bindings must remain
compatible with the pinned FreeRDP 3.x package. WGPU renderer initialization
can fail on systems without a usable adapter, so the existing Slint fallback
must remain available. RDP decoding itself remains FreeRDP's existing path and
is not claimed to be hardware-decoded by this change. PresentMon GPUDuration
is a per-present GPU duration signal, not total adapter utilization; conclusions
about GPU utilization require a separate GPU telemetry source.

## Rollback

Revert this change record and the associated Cargo, native-wrapper, and RDP
frame-copy changes. The prior BGRX conversion path remains a functional
fallback.

## Breaking Change

None to the Core API, launcher protocol, storage schema, or persisted runtime
state.

## Backport Target

none
