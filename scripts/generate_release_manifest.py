#!/usr/bin/env python3
"""Generate the UI-neutral manifest shipped with a chuzi nightly package."""
import argparse
import hashlib
import json
import os
import re
from datetime import datetime, timezone
from pathlib import Path

PLUGIN_API = "chuzi.plugin/v1"
ADAPTER_API = "chuzi.adapter/v1"


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def resource(stage: Path, relative: str) -> dict:
    path = stage / relative
    return {"path": relative, "sha256": sha256(path), "size": path.stat().st_size}


def builtin_plugins(stage: Path, version: str) -> list[dict]:
    manifest_path = stage / "browser-worker" / "worker-manifest.json"
    if not manifest_path.is_file():
        raise SystemExit(f"browser worker manifest is missing: {manifest_path}")
    try:
        worker_manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise SystemExit(f"invalid browser worker manifest: {exc}") from exc
    if worker_manifest.get("protocol") != "v1":
        raise SystemExit("browser worker manifest protocol must be v1")
    adapters = worker_manifest.get("adapters")
    if not isinstance(adapters, list):
        raise SystemExit("browser worker manifest adapters must be a list")

    plugins = []
    seen_ids = set()
    for adapter in adapters:
        if not isinstance(adapter, dict):
            raise SystemExit("browser worker adapter descriptors must be objects")
        adapter_id = adapter.get("id")
        adapter_api = adapter.get("api")
        entry = adapter.get("entry")
        capabilities = adapter.get("capabilities", [])
        if not isinstance(adapter_id, str) or not adapter_id.strip():
            raise SystemExit("browser worker adapter id is required")
        if adapter_id in seen_ids:
            raise SystemExit(f"duplicate browser worker adapter: {adapter_id}")
        if adapter_api != ADAPTER_API:
            raise SystemExit(f"browser worker adapter {adapter_id} has unsupported api: {adapter_api}")
        if not isinstance(entry, str) or not entry or \
                entry.startswith(('/', '\\')) or any(part in ('', '.', '..') for part in entry.replace('\\', '/').split('/')):
            raise SystemExit(f"browser worker adapter {adapter_id} entry path is unsafe")
        entry_path = stage / "browser-worker" / entry
        if not entry_path.is_file():
            raise SystemExit(f"browser worker adapter {adapter_id} entry is missing: {entry}")
        if not isinstance(capabilities, list) or any(not isinstance(item, str) or not item.strip() for item in capabilities):
            raise SystemExit(f"browser worker adapter {adapter_id} capabilities are invalid")
        if len(set(capabilities)) != len(capabilities):
            raise SystemExit(f"browser worker adapter {adapter_id} has duplicate capabilities")
        seen_ids.add(adapter_id)
        plugins.append({
            "id": adapter_id,
            "version": version,
            "api": PLUGIN_API,
            "capabilities": capabilities,
            "installable": False,
        })
    return plugins


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--stage", required=True, type=Path)
    parser.add_argument("--target", required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--commit", required=True)
    parser.add_argument("--channel", default="nightly")
    args = parser.parse_args()
    if args.channel not in {"nightly", "test", "stable"}:
        raise SystemExit(f"unsupported release channel: {args.channel}")
    if args.channel == "test" and not re.fullmatch(r"test-[0-9]+-[0-9a-fA-F]{12}", args.version):
        raise SystemExit("test versions must match test-<run>-<sha12>")
    if args.channel == "stable" and not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+", args.version):
        raise SystemExit("stable versions must match v<major>.<minor>.<patch>")
    stage = args.stage.resolve()
    binary = "chuzi.exe" if args.target == "windows-amd64" else "chuzi"
    launcher = "chuzi-launcher.exe" if args.target == "windows-amd64" else "chuzi-launcher"
    browser_launcher = "chuzi-browser-launcher.exe"
    archive_extension = "zip" if args.target == "windows-amd64" else "tar.gz"
    groups = {
        "launcher": ([launcher,], True),
        "service": ([binary] + ([browser_launcher] if args.target == "windows-amd64" else []), False),
        "browser-worker": (sorted(
            "browser-worker/" + str(path.relative_to(stage / "browser-worker")).replace(os.sep, "/")
            for path in (stage / "browser-worker").rglob("*") if path.is_file()
        ), False),
    }
    if args.target == "windows-amd64":
        groups["presentmon"] = (["PresentMon.exe", "PresentMon-LICENSE.txt"], True)
    components = []
    for component_id, (files, required) in groups.items():
        files = files if isinstance(files, list) else list(files)
        missing = [item for item in files if not (stage / item).is_file()]
        if missing:
            raise SystemExit(
                f"component {component_id} is missing resources: {', '.join(missing)}"
            )
        if required and not files:
            raise SystemExit(f"required component has no resources: {component_id}")
        dependencies = ["browser-worker"] if component_id == "service" else []
        components.append({
            "id": component_id,
            "version": args.version,
            "required": required,
            "dependencies": dependencies,
            "artifact": f"chuzi-{args.version}-{args.target}-{component_id}.{archive_extension}",
            "resources": [resource(stage, item) for item in files],
        })
    manifest = {
        "format": "chuzi-release/v1",
        "channel": args.channel,
        "version": args.version,
        "commit": args.commit,
        "target": args.target,
        "generated_at": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
        "components": components,
        "plugins": builtin_plugins(stage, args.version),
    }
    output = stage / "release-manifest.json"
    output.write_text(json.dumps(manifest, indent=2, sort_keys=False) + "\n", encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
