#!/usr/bin/env python3
"""Generate the per-target catalog consumed by chuzi-launcher."""
import argparse
import hashlib
import json
from pathlib import Path


INDEX_FORMAT = "chuzi-release-index/v1"
COMPONENTS = ("launcher", "service", "browser-worker", "desktop-runtime")


def digest(path: Path) -> str:
    hasher = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            hasher.update(block)
    return hasher.hexdigest()


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

    artifacts = []
    for component, filename in names.items():
        path = dist / filename
        if not path.is_file():
            raise SystemExit(f"release artifact is missing: {path}")
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
