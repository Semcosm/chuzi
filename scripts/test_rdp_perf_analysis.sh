#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

cat > "$tmp_dir/rdp.log" <<'EOF'
noise before
rdp_perf {"elapsed_ms":1000,"paint_batches":10,"frame_intervals":9,"frame_interval_us":450000,"coalesced_frames":2,"copied_bytes":10485760,"copy_time_us":20000,"ui_handoffs":10,"ui_handoff_time_us":5000}
rdp_perf {"elapsed_ms":2000,"paint_batches":30,"frame_intervals":29,"frame_interval_us":1450000,"coalesced_frames":6,
"copied_bytes":31457280,"copy_time_us":60000,"ui_handoffs":30,"ui_handoff_time_us":15000}
EOF

summary="$(python3 "$repo_root/scripts/analyze_rdp_perf.py" --json "$tmp_dir/rdp.log")"
SUMMARY="$summary" python3 - <<'PY'
import json
import os

summary = json.loads(os.environ["SUMMARY"])
assert summary["samples"] == 2
assert summary["paint_batches"] == 30
assert summary["frame_intervals"] == 29
assert summary["coalesced_frames"] == 6
assert abs(summary["paint_fps"] - 15.0) < 1e-9
assert abs(summary["average_frame_interval_ms"] - 50.0) < 1e-9
assert abs(summary["average_copy_time_us"] - 2000.0) < 1e-9
assert abs(summary["average_ui_handoff_us"] - 500.0) < 1e-9
assert abs(summary["coalescing_ratio"] - 6 / 30) < 1e-9
assert abs(summary["ui_delivery_ratio"] - 1.0) < 1e-9
assert abs(summary["copied_bandwidth_mib_s"] - 15.0) < 1e-9
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
