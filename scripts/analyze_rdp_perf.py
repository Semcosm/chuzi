#!/usr/bin/env python3
"""Summarize CHUZI_RDP_PERF records from a Windows client log."""

from __future__ import annotations

import argparse
import csv
import json
import re
import sys
from pathlib import Path
from typing import Any


RECORD_MARKER_RE = re.compile(r"rdp_perf\s+")
COUNTER_FIELDS = (
    "frame_width",
    "frame_height",
    "rdp_update_batches",
    "dirty_rect_count",
    "dirty_area_pixels",
    "dirty_union_area_pixels",
    "full_frame_copy_bytes",
    "actual_copy_bytes",
    "framebuffer_copy_us",
    "frame_to_image_us",
    "ui_handoffs",
    "ui_handoff_time_us",
    "ui_handoff_interval_us",
    "coalesced_frames",
    "queue_overwrites",
    "ui_ticks",
    "ui_tick_commits",
    "ui_tick_interval_us",
    "ui_tick_processing_us",
)


def _counter(record: dict[str, Any], name: str, *fallbacks: str) -> int:
    for field in (name, *fallbacks):
        if field in record:
            try:
                return int(record[field])
            except (TypeError, ValueError):
                return 0
    return 0


def _interval_samples(records: list[dict[str, Any]], name: str) -> list[float]:
    samples: list[float] = []
    for record in records:
        value = record.get(name, [])
        if isinstance(value, list):
            samples.extend(float(item) for item in value if isinstance(item, (int, float)))
    return samples


def _extract_json_object(text: str, start: int) -> tuple[str | None, int]:
    depth = 0
    in_string = False
    escaped = False
    for index in range(start, len(text)):
        character = text[index]
        if in_string:
            if escaped:
                escaped = False
            elif character == "\\":
                escaped = True
            elif character == '"':
                in_string = False
            continue
        if character == '"':
            in_string = True
        elif character == "{":
            depth += 1
        elif character == "}":
            depth -= 1
            if depth == 0:
                return text[start : index + 1], index + 1
    return None, start


def read_records(path: Path) -> list[dict[str, Any]]:
    text = path.read_text(encoding="utf-8", errors="replace")
    records: list[dict[str, Any]] = []
    cursor = 0
    while True:
        match = RECORD_MARKER_RE.search(text, cursor)
        if not match:
            break
        start = text.find("{", match.end())
        if start < 0:
            line_number = text.count("\n", 0, match.start()) + 1
            raise ValueError(f"truncated rdp_perf JSON at line {line_number}")
        payload, end = _extract_json_object(text, start)
        if payload is None:
            line_number = text.count("\n", 0, match.start()) + 1
            raise ValueError(f"truncated rdp_perf JSON at line {line_number}")
        try:
            record = json.loads(payload)
        except json.JSONDecodeError as exc:
            line_number = text.count("\n", 0, match.start()) + 1
            raise ValueError(f"invalid rdp_perf JSON at line {line_number}: {exc}") from exc
        if not isinstance(record, dict) or "elapsed_ms" not in record:
            line_number = text.count("\n", 0, match.start()) + 1
            raise ValueError(f"rdp_perf record at line {line_number} is missing elapsed_ms")
        records.append(record)
        cursor = end
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
        "dirty_frame_mode": str(latest.get("dirty_frame_mode", "unknown")),
    }
    for field in COUNTER_FIELDS:
        summary[field] = _counter(latest, field)
    # Keep aliases for logs emitted by clients before the staged telemetry
    # schema, while making the new rates unambiguous.
    summary["rdp_updates"] = _counter(latest, "rdp_update_batches", "paint_batches")
    summary["actual_copy_bytes"] = _counter(latest, "actual_copy_bytes", "copied_bytes")
    summary["framebuffer_copy_us"] = _counter(
        latest, "framebuffer_copy_us", "copy_time_us"
    )
    summary["full_frame_copy_bytes"] = _counter(latest, "full_frame_copy_bytes")
    summary["paint_batches"] = _counter(latest, "paint_batches", "rdp_update_batches")
    summary["copied_bytes"] = summary["actual_copy_bytes"]
    summary["frame_intervals"] = _counter(
        latest, "frame_intervals", "rdp_update_batches"
    )
    if "frame_intervals" not in latest:
        summary["frame_intervals"] = max(summary["rdp_updates"] - 1, 0)
    summary["frame_interval_us"] = _counter(
        latest, "frame_interval_us", "ui_handoff_interval_us"
    )
    summary["copy_time_us"] = summary["framebuffer_copy_us"]
    summary["rdp_update_fps"] = (
        summary["rdp_updates"] / elapsed_seconds if elapsed_seconds > 0 else 0.0
    )
    summary["ui_commit_fps"] = (
        summary["ui_handoffs"] / elapsed_seconds if elapsed_seconds > 0 else 0.0
    )
    summary["paint_fps"] = summary["rdp_update_fps"]
    summary["average_frame_interval_ms"] = (
        summary["frame_interval_us"] / summary["frame_intervals"] / 1000.0
        if summary["frame_intervals"] > 0
        else 0.0
    )
    summary["average_copy_time_us"] = (
        summary["framebuffer_copy_us"] / summary["rdp_updates"]
        if summary["rdp_updates"] > 0
        else 0.0
    )
    summary["average_frame_to_image_us"] = (
        summary["frame_to_image_us"] / summary["ui_handoffs"]
        if summary["ui_handoffs"] > 0
        else 0.0
    )
    summary["average_ui_handoff_us"] = (
        summary["ui_handoff_time_us"] / summary["ui_handoffs"]
        if summary["ui_handoffs"] > 0
        else 0.0
    )
    handoff_samples = _interval_samples(records, "ui_handoff_interval_samples_us")
    if not handoff_samples and summary["ui_handoffs"] > 1:
        average_interval = summary["ui_handoff_interval_us"] / (summary["ui_handoffs"] - 1)
        handoff_samples = [average_interval]
    summary["ui_handoff_interval_p50_us"] = _percentile(handoff_samples, 0.50)
    summary["ui_handoff_interval_p95_us"] = _percentile(handoff_samples, 0.95)
    summary["ui_handoff_interval_p50_ms"] = summary["ui_handoff_interval_p50_us"] / 1000.0
    summary["ui_handoff_interval_p95_ms"] = summary["ui_handoff_interval_p95_us"] / 1000.0
    summary["dirty_area_ratio"] = float(
        latest.get("dirty_area_ratio", 0.0)
        or (
            summary["dirty_union_area_pixels"]
            / (summary["frame_width"] * summary["frame_height"] * max(summary["rdp_updates"], 1))
            if summary["frame_width"] and summary["frame_height"]
            else 0.0
        )
    )
    summary["coalescing_ratio"] = (
        summary["queue_overwrites"] / summary["rdp_updates"]
        if summary["rdp_updates"] > 0
        else 0.0
    )
    summary["ui_delivery_ratio"] = (
        summary["ui_handoffs"] / summary["rdp_updates"]
        if summary["rdp_updates"] > 0
        else 0.0
    )
    summary["actual_copied_bandwidth_mib_s"] = (
        summary["actual_copy_bytes"] / 1024.0 / 1024.0 / elapsed_seconds
        if elapsed_seconds > 0
        else 0.0
    )
    summary["full_frame_equivalent_bandwidth_mib_s"] = (
        summary["full_frame_copy_bytes"] / 1024.0 / 1024.0 / elapsed_seconds
        if elapsed_seconds > 0
        else 0.0
    )
    summary["copied_bandwidth_mib_s"] = summary["actual_copied_bandwidth_mib_s"]
    return summary


def _lookup(row: dict[str, str], *names: str) -> str:
    fields = {str(key).lstrip("\ufeff").strip().lower(): value for key, value in row.items()}
    for name in names:
        value = fields.get(name.lower())
        if value is not None:
            return value.strip()
    return ""


def _float_value(row: dict[str, str], *names: str) -> float | None:
    value = _lookup(row, *names)
    if not value:
        return None
    try:
        return float(value)
    except ValueError:
        return None


def _truthy(value: str) -> bool:
    return value.strip().lower() in {"1", "true", "yes", "y"}


def _percentile(values: list[float], percentile: float) -> float:
    if not values:
        return 0.0
    ordered = sorted(values)
    position = (len(ordered) - 1) * percentile
    lower = int(position)
    upper = min(lower + 1, len(ordered) - 1)
    fraction = position - lower
    return ordered[lower] + (ordered[upper] - ordered[lower]) * fraction


def read_presentmon_records(path: Path, process: str | None = None) -> list[dict[str, str]]:
    with path.open("r", encoding="utf-8-sig", errors="replace", newline="") as handle:
        lines = [line for line in handle if line.strip() and not line.lstrip().startswith("#")]
    if not lines:
        raise ValueError(f"no PresentMon CSV rows found in {path}")

    rows = []
    reader = csv.DictReader(lines)
    for row in reader:
        normalized = {
            str(key).lstrip("\ufeff").strip(): (value or "")
            for key, value in row.items()
        }
        if process:
            application = _lookup(normalized, "Application", "ProcessName", "ExecutableName")
            executable = re.split(r"[\\/]", application)[-1]
            if executable.lower() != process.lower() and application.lower() != process.lower():
                continue
        rows.append(normalized)
    if not rows:
        suffix = f" for process {process!r}" if process else ""
        raise ValueError(f"no PresentMon rows matched{suffix} in {path}")
    return rows


def summarize_presentmon(rows: list[dict[str, str]], process: str | None = None) -> dict[str, Any]:
    present_intervals = [
        value
        for row in rows
        if (value := _float_value(row, "MsBetweenPresents")) is not None and value > 0
    ]
    display_intervals = [
        value
        for row in rows
        if (value := _float_value(row, "MsBetweenDisplayChange")) is not None and value > 0
    ]
    display_latency = [
        value
        for row in rows
        if (value := _float_value(row, "MsUntilDisplayed")) is not None and value >= 0
    ]
    gpu_durations = [
        value
        for row in rows
        if (value := _float_value(row, "GPUDuration")) is not None and value >= 0
    ]
    cpu_durations = [
        value
        for row in rows
        if (value := _float_value(row, "CPUDuration")) is not None and value >= 0
    ]
    timestamps = [
        value
        for row in rows
        if (value := _float_value(row, "TimeInSeconds", "TimeInSecondsRelative")) is not None
    ]
    dropped_frames = sum(_truthy(_lookup(row, "Dropped")) for row in rows)
    allows_tearing = sum(_truthy(_lookup(row, "AllowsTearing")) for row in rows)
    sync_interval_zero = sum(
        (value := _float_value(row, "SyncInterval")) is not None and value == 0
        for row in rows
    )
    present_modes: dict[str, int] = {}
    for row in rows:
        mode = _lookup(row, "PresentMode")
        if mode:
            present_modes[mode] = present_modes.get(mode, 0) + 1

    window_seconds = max(timestamps) - min(timestamps) if len(timestamps) >= 2 else 0.0
    return {
        "samples": len(rows),
        "process": process,
        "window_seconds": window_seconds,
        "present_fps": len(rows) / window_seconds if window_seconds > 0 else 0.0,
        "average_ms_between_presents": sum(present_intervals) / len(present_intervals)
        if present_intervals
        else 0.0,
        "p95_ms_between_presents": _percentile(present_intervals, 0.95),
        "average_ms_between_display_change": sum(display_intervals) / len(display_intervals)
        if display_intervals
        else 0.0,
        "average_ms_until_displayed": sum(display_latency) / len(display_latency)
        if display_latency
        else 0.0,
        "average_gpu_duration_ms": sum(gpu_durations) / len(gpu_durations)
        if gpu_durations
        else 0.0,
        "average_cpu_duration_ms": sum(cpu_durations) / len(cpu_durations)
        if cpu_durations
        else 0.0,
        "dropped_frames": dropped_frames,
        "dropped_ratio": dropped_frames / len(rows) if rows else 0.0,
        "allows_tearing_ratio": allows_tearing / len(rows) if rows else 0.0,
        "sync_interval_zero_ratio": sync_interval_zero / len(rows) if rows else 0.0,
        "tearing_indicators": bool(allows_tearing or sync_interval_zero),
        "present_modes": present_modes,
    }


def print_text(summary: dict[str, Any]) -> None:
    print(f"RDP performance samples: {summary['samples']}")
    print(f"Elapsed: {summary['elapsed_ms'] / 1000.0:.3f} s")
    print(f"Frame mode: {summary['dirty_frame_mode']}")
    print(f"RDP updates: {summary['rdp_update_fps']:.2f} /s")
    print(f"UI image commits: {summary['ui_commit_fps']:.2f} /s")
    print(f"Dirty rectangles: {summary['dirty_rect_count']} total")
    print(f"Dirty area ratio: {summary['dirty_area_ratio']:.2%}")
    print(f"CPU copied data: {summary['actual_copied_bandwidth_mib_s']:.2f} MiB/s")
    print(f"Full-frame traffic: {summary['full_frame_equivalent_bandwidth_mib_s']:.2f} MiB/s")
    print(f"Average framebuffer copy: {summary['average_copy_time_us']:.2f} us")
    print(f"Average frame-to-image snapshot: {summary['average_frame_to_image_us']:.2f} us")
    print(f"Average UI handoff: {summary['average_ui_handoff_us']:.2f} us")
    print(
        "UI handoff interval p50/p95: "
        f"{summary['ui_handoff_interval_p50_ms']:.2f} / "
        f"{summary['ui_handoff_interval_p95_ms']:.2f} ms"
    )
    print(f"Coalescing ratio: {summary['coalescing_ratio']:.2%}")
    print(f"UI delivery ratio: {summary['ui_delivery_ratio']:.2%}")
    presentmon = summary.get("presentmon")
    if presentmon:
        print("PresentMon / GPU presentation:")
        print(f"  Samples: {presentmon['samples']}")
        print(f"  Present FPS: {presentmon['present_fps']:.2f}")
        print(f"  Average present interval: {presentmon['average_ms_between_presents']:.2f} ms")
        print(f"  P95 present interval: {presentmon['p95_ms_between_presents']:.2f} ms")
        print(
            "  Average display-change interval: "
            f"{presentmon['average_ms_between_display_change']:.2f} ms"
        )
        print(f"  Average until displayed: {presentmon['average_ms_until_displayed']:.2f} ms")
        print(f"  Average GPU duration: {presentmon['average_gpu_duration_ms']:.2f} ms")
        print(f"  Average CPU duration: {presentmon['average_cpu_duration_ms']:.2f} ms")
        print(f"  Dropped presents: {presentmon['dropped_frames']} ({presentmon['dropped_ratio']:.2%})")
        print(f"  Allows tearing: {presentmon['allows_tearing_ratio']:.2%}")
        print(f"  Sync interval zero: {presentmon['sync_interval_zero_ratio']:.2%}")
        print(
            "  Tearing indicators: "
            f"{'detected' if presentmon['tearing_indicators'] else 'not detected'}"
        )
        print(f"  Present modes: {json.dumps(presentmon['present_modes'], sort_keys=True)}")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("log", type=Path, help="stderr or combined log containing rdp_perf records")
    parser.add_argument(
        "--presentmon",
        type=Path,
        help="optional PresentMon CSV exported from the same client run",
    )
    parser.add_argument(
        "--process",
        help="optional executable name used to filter PresentMon rows",
    )
    parser.add_argument("--json", action="store_true", help="emit a machine-readable JSON summary")
    args = parser.parse_args()

    try:
        summary = summarize(read_records(args.log))
        if args.presentmon:
            summary["presentmon"] = summarize_presentmon(
                read_presentmon_records(args.presentmon, args.process), args.process
            )
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
