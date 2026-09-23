#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

cat > "$tmp_dir/rdp.log" <<'EOF'
noise before
rdp_perf {"elapsed_ms":1000,"paint_batches":10,"frame_intervals":9,"frame_interval_us":450000,"coalesced_frames":2,"copied_bytes":10485760,"copy_time_us":20000,"ui_handoffs":10,"ui_handoff_time_us":5000}
rdp_perf {"elapsed_ms":2000,"paint_batches":30,"frame_intervals":29,"frame_interval_us":1450000,"coalesced_frames":6,"copied_bytes":31457280,"copy_time_us":60000,"ui_handoffs":30,"ui_handoff_time_us":15000}
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
assert abs(summary["coalescing_ratio"] - 6 / 36) < 1e-9
assert abs(summary["copied_bandwidth_mib_s"] - 15.0) < 1e-9
PY

python3 "$repo_root/scripts/analyze_rdp_perf.py" "$tmp_dir/rdp.log" >/dev/null
touch "$tmp_dir/empty.log"
if python3 "$repo_root/scripts/analyze_rdp_perf.py" "$tmp_dir/empty.log" >/dev/null 2>&1; then
  echo "analyzer unexpectedly accepted a log without rdp_perf records" >&2
  exit 1
fi

echo "RDP performance analyzer test passed"
