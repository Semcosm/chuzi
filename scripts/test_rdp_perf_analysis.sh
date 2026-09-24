#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

cat > "$tmp_dir/rdp.log" <<'EOF'
noise before
rdp_perf {"elapsed_ms":1000,"dirty_frame_mode":"optimized","frame_width":1920,"frame_height":1080,"rdp_update_batches":10,"dirty_rect_count":20,"dirty_area_pixels":100000,"dirty_union_area_pixels":80000,"dirty_area_ratio":0.0385802469,"full_frame_copy_bytes":82944000,"actual_copy_bytes":10485760,"framebuffer_copy_us":20000,"frame_to_image_us":8000,"ui_handoffs":10,"ui_handoff_interval_us":144000,"ui_handoff_interval_samples_us":[15000,16000,17000],"ui_handoff_time_us":5000,"coalesced_frames":2,"queue_overwrites":2,"ui_ticks":60,"ui_tick_commits":10,"ui_tick_interval_us":944000,"ui_tick_processing_us":12000}
rdp_perf {"elapsed_ms":2000,"dirty_frame_mode":"optimized","frame_width":1920,"frame_height":1080,"rdp_update_batches":30,"dirty_rect_count":60,"dirty_area_pixels":300000,"dirty_union_area_pixels":240000,"dirty_area_ratio":0.0385802469,"full_frame_copy_bytes":248832000,"actual_copy_bytes":31457280,"framebuffer_copy_us":60000,"frame_to_image_us":24000,"ui_handoffs":30,"ui_handoff_interval_us":464000,"ui_handoff_interval_samples_us":[15000,16000,17000],"ui_handoff_time_us":15000,"coalesced_frames":6,"queue_overwrites":6,"ui_ticks":120,"ui_tick_commits":30,"ui_tick_interval_us":1894000,"ui_tick_processing_us":36000}
EOF

summary="$(python3 "$repo_root/scripts/analyze_rdp_perf.py" --json "$tmp_dir/rdp.log")"
SUMMARY="$summary" python3 - <<'PY'
import json
import os

summary = json.loads(os.environ["SUMMARY"])
assert summary["samples"] == 2
assert summary["dirty_frame_mode"] == "optimized"
assert summary["rdp_update_batches"] == 30
assert summary["rdp_updates"] == 30
assert summary["dirty_rect_count"] == 60
assert summary["coalesced_frames"] == 6
assert abs(summary["rdp_update_fps"] - 15.0) < 1e-9
assert abs(summary["ui_commit_fps"] - 15.0) < 1e-9
assert abs(summary["average_copy_time_us"] - 2000.0) < 1e-9
assert abs(summary["average_ui_handoff_us"] - 500.0) < 1e-9
assert abs(summary["coalescing_ratio"] - 6 / 30) < 1e-9
assert abs(summary["ui_delivery_ratio"] - 1.0) < 1e-9
assert abs(summary["actual_copied_bandwidth_mib_s"] - 15.0) < 1e-9
assert abs(summary["full_frame_equivalent_bandwidth_mib_s"] - 118.65234375) < 1e-9
assert abs(summary["ui_handoff_interval_p50_ms"] - 16.0) < 1e-9
assert abs(summary["ui_handoff_interval_p95_ms"] - 17.0) < 1e-9
PY

python3 "$repo_root/scripts/analyze_rdp_perf.py" "$tmp_dir/rdp.log" >/dev/null

cat > "$tmp_dir/presentmon.csv" <<'EOF'
Application,ProcessID,TimeInSeconds,MsBetweenPresents,MsBetweenDisplayChange,MsUntilDisplayed,GPUDuration,CPUDuration,PresentMode,Dropped,AllowsTearing,SyncInterval
Chuzi.Native.Windows.exe,42,1.0,16.0,16.2,3.0,2.0,1.0,Hardware:Independent Flip,0,0,1
Chuzi.Native.Windows.exe,42,1.016,16.5,16.7,3.2,2.2,1.1,Hardware:Independent Flip,0,0,1
Chuzi.Native.Windows.exe,42,1.033,17.0,17.1,3.5,2.4,1.2,Hardware:Independent Flip,1,1,0
Other.exe,7,1.050,40.0,40.0,4.0,8.0,2.0,Composed:Copy with GPU GDI,0,0,1
EOF

summary="$(python3 "$repo_root/scripts/analyze_rdp_perf.py" --json "$tmp_dir/rdp.log" --presentmon "$tmp_dir/presentmon.csv" --process Chuzi.Native.Windows.exe)"
SUMMARY="$summary" python3 - <<'PY'
import json
import os

summary = json.loads(os.environ["SUMMARY"])
presentmon = summary["presentmon"]
assert presentmon["samples"] == 3
assert abs(presentmon["present_fps"] - 90.9090909090909) < 1e-9
assert abs(presentmon["average_ms_between_presents"] - 16.5) < 1e-9
assert abs(presentmon["p95_ms_between_presents"] - 16.95) < 1e-9
assert abs(presentmon["average_gpu_duration_ms"] - 2.2) < 1e-9
assert presentmon["dropped_frames"] == 1
assert abs(presentmon["dropped_ratio"] - 1 / 3) < 1e-9
assert abs(presentmon["allows_tearing_ratio"] - 1 / 3) < 1e-9
assert abs(presentmon["sync_interval_zero_ratio"] - 1 / 3) < 1e-9
assert presentmon["present_modes"]["Hardware:Independent Flip"] == 3
PY

touch "$tmp_dir/empty.log"
if python3 "$repo_root/scripts/analyze_rdp_perf.py" "$tmp_dir/empty.log" >/dev/null 2>&1; then
  echo "analyzer unexpectedly accepted a log without rdp_perf records" >&2
  exit 1
fi

echo "RDP performance analyzer test passed"
