#!/usr/bin/env python3
"""Generate the per-target catalog consumed by chuzi-launcher."""
import argparse
import hashlib
import json
import re
from pathlib import Path


INDEX_FORMAT = "chuzi-release-index/v1"
COMPONENTS = ("launcher", "service", "browser-worker")


def digest(path: Path) -> str:
    hasher = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            hasher.update(block)
    return hasher.hexdigest()


def safe_relative_path(value: object) -> bool:
    if not isinstance(value, str) or not value or "\\" in value or value.startswith("/"):
        return False
    parts = value.split("/")
    return all(part not in ("", ".", "..") for part in parts)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--manifest", required=True, type=Path)
    parser.add_argument("--dist", required=True, type=Path)
    parser.add_argument("--target", required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--commit", required=True)
    parser.add_argument("--channel", default="nightly")
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()

    if args.channel not in {"nightly", "test", "stable"}:
        raise SystemExit(f"unsupported release channel: {args.channel}")
    if args.channel == "test" and not re.fullmatch(r"test-[0-9]+-[0-9a-fA-F]{12}", args.version):
        raise SystemExit("test versions must match test-<run>-<sha12>")
    if args.channel == "stable" and not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+", args.version):
        raise SystemExit("stable versions must match v<major>.<minor>.<patch>")

    if len(args.commit) != 40 or any(character not in "0123456789abcdefABCDEF" for character in args.commit):
        raise SystemExit("commit must be a full 40-character SHA-1")

    manifest_path = args.manifest.resolve()
    dist = args.dist.resolve()
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    for key, expected in (("format", "chuzi-release/v1"), ("channel", args.channel),
                          ("version", args.version), ("target", args.target),
                          ("commit", args.commit)):
        if manifest.get(key) != expected:
            raise SystemExit(f"manifest {key} does not match index arguments")

    extension = "zip" if args.target == "windows-amd64" else "tar.gz"
    names = {
        "bundle": f"chuzi-{args.version}-{args.target}.{extension}",
    }
    names.update({component: f"chuzi-{args.version}-{args.target}-{component}.{extension}"
                  for component in COMPONENTS})

    plugins = manifest.get("plugins", [])
    if not isinstance(plugins, list):
        raise SystemExit("manifest plugins must be a list")
    plugin_descriptors = {}
    for plugin in plugins:
        if not isinstance(plugin, dict) or not isinstance(plugin.get("id"), str) or not plugin["id"]:
            raise SystemExit("manifest plugin IDs must be non-empty")
        plugin_id = plugin["id"]
        if plugin_id in names or plugin_id in plugin_descriptors:
            raise SystemExit(f"manifest plugin ID collides with another release item: {plugin_id}")
        plugin_descriptors[plugin_id] = plugin
        if not plugin.get("installable"):
            continue
        archive = plugin.get("archive")
        if not safe_relative_path(archive):
            raise SystemExit(f"installable plugin archive path is unsafe: {plugin_id}")
        names[plugin_id] = archive

    artifacts = []
    artifact_paths = set()
    for component, filename in names.items():
        if filename in artifact_paths:
            raise SystemExit(f"release artifact path is used more than once: {filename}")
        artifact_paths.add(filename)
        path = (dist / filename).resolve()
        if dist not in path.parents:
            raise SystemExit(f"release artifact escapes dist: {filename}")
        if not path.is_file():
            raise SystemExit(f"release artifact is missing: {path}")
        if component in plugin_descriptors:
            expected_digest = plugin_descriptors[component].get("sha256")
            actual_digest = digest(path)
            if not isinstance(expected_digest, str) or expected_digest.lower() != actual_digest.lower():
                raise SystemExit(f"manifest plugin sha256 mismatch: {component}")
        artifacts.append({
            "component": component,
            "target": args.target,
            "version": args.version,
            "path": filename,
            "size": path.stat().st_size,
            "sha256": digest(path),
        })

    manifest_artifacts = {item.get("id"): item.get("artifact")
                          for item in manifest.get("components", [])}
    for component in COMPONENTS:
        expected = names[component]
        if manifest_artifacts.get(component) != expected:
            raise SystemExit(f"manifest component artifact mismatch: {component}")

    index = {
        "format": INDEX_FORMAT,
        "channel": args.channel,
        "version": args.version,
        "commit": args.commit,
        "target": args.target,
        "generated_at": manifest.get("generated_at"),
        "manifest": manifest,
        "artifacts": artifacts,
    }
    output = args.output.resolve() if args.output else dist / f"chuzi-{args.version}-{args.target}.index.json"
    output.write_text(json.dumps(index, indent=2) + "\n", encoding="utf-8")
    index_digest = digest(output)
    checksum = output.with_name(output.name + ".sha256")
    checksum.write_text(f"{index_digest}  {output.name}\n", encoding="ascii")
    print(f"generated {output}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
