#!/usr/bin/env python3
"""Materialize release artifacts and per-target catalogs for the UI."""

from __future__ import annotations

import argparse
import json
import shutil
from datetime import datetime, timezone
from pathlib import Path


TARGETS = ("windows-amd64", "linux-amd64", "linux-arm64", "darwin-arm64")
CHANNELS = ("test", "stable", "nightly")
CATALOG_FORMAT = "chuzi-release-catalog/v1"


def read_json(path: Path) -> dict:
    value = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(value, dict):
        raise SystemExit(f"JSON object expected: {path}")
    return value


def write_json(path: Path, value: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=False) + "\n", encoding="utf-8")


def source_dir(root: Path, channel: str, target: str) -> Path:
    candidate = root / f"chuzi-{channel}-{target}"
    if not candidate.is_dir():
        raise SystemExit(f"{channel} artifact directory is missing for {target}: {candidate}")
    return candidate


def load_catalog(path: Path, channel: str, target: str) -> dict:
    if not path.exists():
        return {"format": CATALOG_FORMAT, "channel": channel, "target": target, "releases": []}
    catalog = read_json(path)
    if catalog.get("format") != CATALOG_FORMAT or catalog.get("channel") != channel or catalog.get("target") != target:
        raise SystemExit(f"existing catalog metadata is invalid: {path}")
    if not isinstance(catalog.get("releases"), list):
        raise SystemExit(f"existing catalog releases are invalid: {path}")
    return catalog


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--artifact-root", required=True, type=Path)
    parser.add_argument("--output-root", required=True, type=Path)
    parser.add_argument("--channel", required=True, choices=CHANNELS)
    parser.add_argument("--version", required=True)
    parser.add_argument("--commit", required=True)
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--published-at", default="")
    args = parser.parse_args()

    artifact_root = args.artifact_root.resolve()
    output_root = args.output_root.resolve()
    if len(args.commit) != 40 or any(c not in "0123456789abcdefABCDEF" for c in args.commit):
        raise SystemExit("commit must be a full 40-character SHA-1")
    version_prefix = {"test": "test-", "nightly": "nightly-", "stable": "v"}[args.channel]
    if not args.version.startswith(version_prefix):
        raise SystemExit(f"{args.channel} catalog versions must start with {version_prefix}")

    published_at = args.published_at.strip() or datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")
    base_url = args.base_url.rstrip("/")
    for target in TARGETS:
        source = source_dir(artifact_root, args.channel, target)
        index_name = f"chuzi-{args.version}-{target}.index.json"
        index_path = source / index_name
        if not index_path.is_file():
            raise SystemExit(f"release index is missing: {index_path}")
        index = read_json(index_path)
        for key, expected in (("format", "chuzi-release-index/v1"), ("channel", args.channel),
                              ("version", args.version), ("commit", args.commit), ("target", target)):
            if index.get(key) != expected:
                raise SystemExit(f"release index {key} mismatch for {target}")

        release_dir = output_root / args.channel / target / args.version
        release_dir.mkdir(parents=True, exist_ok=True)
        for item in source.iterdir():
            if item.is_file():
                shutil.copy2(item, release_dir / item.name)

        catalog_path = output_root / args.channel / f"{target}.json"
        catalog = load_catalog(catalog_path, args.channel, target)
        releases = [item for item in catalog["releases"] if isinstance(item, dict)]
        releases = [item for item in releases if not (item.get("version") == args.version and item.get("commit") == args.commit)]
        releases.append({
            "channel": args.channel,
            "version": args.version,
            "commit": args.commit,
            "target": target,
            "index_url": f"{base_url}/{args.channel}/{target}/{args.version}/{index_name}",
            "published_at": published_at,
            "prerelease": args.channel != "stable",
        })
        releases.sort(key=lambda item: (str(item.get("published_at", "")), str(item.get("version", ""))), reverse=True)
        catalog["releases"] = releases
        write_json(catalog_path, catalog)
        print(f"published {args.channel} {args.version} for {target}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
