#!/usr/bin/env python3
"""Summarize CHUZI_RDP_PERF records from a Windows client log."""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path
from typing import Any


RECORD_RE = re.compile(r"rdp_perf\s+(\{.*\})\s*$")
COUNTER_FIELDS = (
    "paint_batches",
    "frame_intervals",
    "frame_interval_us",
    "coalesced_frames",
    "copied_bytes",
    "copy_time_us",
    "ui_handoffs",
    "ui_handoff_time_us",
)


def read_records(path: Path) -> list[dict[str, Any]]:
    records: list[dict[str, Any]] = []
    for line_number, line in enumerate(path.read_text(encoding="utf-8", errors="replace").splitlines(), 1):
        match = RECORD_RE.search(line)
        if not match:
            continue
        try:
            record = json.loads(match.group(1))
        except json.JSONDecodeError as exc:
            raise ValueError(f"invalid rdp_perf JSON at line {line_number}: {exc}") from exc
        if not isinstance(record, dict) or "elapsed_ms" not in record:
            raise ValueError(f"rdp_perf record at line {line_number} is missing elapsed_ms")
        records.append(record)
    if not records:
        raise ValueError(f"no rdp_perf records found in {path}")
    return records


def summarize(records: list[dict[str, Any]]) -> dict[str, Any]:
    latest = records[-1]
    elapsed_ms = float(latest["elapsed_ms"])
    elapsed_seconds = elapsed_ms / 1000.0

    summary: dict[str, Any] = {
        "samples": len(records),
        "elapsed_ms": elapsed_ms,
    }
    for field in COUNTER_FIELDS:
        summary[field] = int(latest.get(field, 0))

    summary["paint_fps"] = (
        summary["paint_batches"] / elapsed_seconds if elapsed_seconds > 0 else 0.0
    )
    summary["average_frame_interval_ms"] = (
        summary["frame_interval_us"] / summary["frame_intervals"] / 1000.0
        if summary["frame_intervals"] > 0
        else 0.0
    )
    summary["average_copy_time_us"] = (
        summary["copy_time_us"] / summary["paint_batches"]
        if summary["paint_batches"] > 0
        else 0.0
    )
    summary["average_ui_handoff_us"] = (
        summary["ui_handoff_time_us"] / summary["ui_handoffs"]
        if summary["ui_handoffs"] > 0
        else 0.0
    )
    received_frames = summary["paint_batches"] + summary["coalesced_frames"]
    summary["coalescing_ratio"] = (
        summary["coalesced_frames"] / received_frames if received_frames > 0 else 0.0
    )
    summary["copied_bandwidth_mib_s"] = (
        summary["copied_bytes"] / 1024.0 / 1024.0 / elapsed_seconds
        if elapsed_seconds > 0
        else 0.0
    )
    return summary


def print_text(summary: dict[str, Any]) -> None:
    print(f"RDP performance samples: {summary['samples']}")
    print(f"Elapsed: {summary['elapsed_ms'] / 1000.0:.3f} s")
    print(f"Paint FPS: {summary['paint_fps']:.2f}")
    print(f"Average frame interval: {summary['average_frame_interval_ms']:.2f} ms")
    print(f"Average framebuffer copy: {summary['average_copy_time_us']:.2f} us")
    print(f"Average UI handoff: {summary['average_ui_handoff_us']:.2f} us")
    print(f"Coalescing ratio: {summary['coalescing_ratio']:.2%}")
    print(f"Copied bandwidth: {summary['copied_bandwidth_mib_s']:.2f} MiB/s")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("log", type=Path, help="stderr or combined log containing rdp_perf records")
    parser.add_argument("--json", action="store_true", help="emit a machine-readable JSON summary")
    args = parser.parse_args()

    try:
        summary = summarize(read_records(args.log))
    except (OSError, ValueError) as exc:
        print(f"analyze_rdp_perf: {exc}", file=sys.stderr)
        return 2

    if args.json:
        print(json.dumps(summary, sort_keys=True, separators=(",", ":")))
    else:
        print_text(summary)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
