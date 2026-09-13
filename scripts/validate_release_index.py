#!/usr/bin/env python3
"""Validate a release index and every archive it references."""
import argparse
import hashlib
import json
from pathlib import Path
from string import hexdigits


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


def valid_digest(value: object) -> bool:
    return isinstance(value, str) and len(value) == 64 and all(character in hexdigits for character in value)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--index", required=True, type=Path)
    parser.add_argument("--dist", required=True, type=Path)
    args = parser.parse_args()
    index_path = args.index.resolve()
    dist = args.dist.resolve()
    index = json.loads(index_path.read_text(encoding="utf-8"))
    if index.get("format") != "chuzi-release-index/v1":
        raise SystemExit("invalid release index format")
    manifest = index.get("manifest")
    if not isinstance(manifest, dict):
        raise SystemExit("release index has no embedded manifest")
    for field in ("channel", "version", "target", "commit"):
        if index.get(field) != manifest.get(field):
            raise SystemExit(f"index and manifest differ in {field}")
    if index.get("channel") not in ("nightly", "stable"):
        raise SystemExit("release index channel is invalid")
    if not all(isinstance(index.get(field), str) and index.get(field) for field in ("version", "target")):
        raise SystemExit("release index version and target are required")
    commit = index.get("commit")
    if not isinstance(commit, str) or len(commit) != 40 or any(character not in hexdigits for character in commit):
        raise SystemExit("release index commit must be a full 40-character SHA-1")
    artifacts = index.get("artifacts")
    if not isinstance(artifacts, list) or not artifacts:
        raise SystemExit("release index has no artifacts")
    seen = set()
    seen_paths = set()
    for artifact in artifacts:
        if not isinstance(artifact, dict):
            raise SystemExit("release artifact is not an object")
        component = artifact.get("component")
        relative = artifact.get("path")
        if not isinstance(component, str) or not component or component in seen:
            raise SystemExit(f"duplicate or invalid artifact component: {component}")
        seen.add(component)
        if not safe_relative_path(relative) or relative in seen_paths:
            raise SystemExit(f"unsafe artifact path: {relative}")
        seen_paths.add(relative)
        path = (dist / relative).resolve()
        if dist not in path.parents:
            raise SystemExit(f"artifact escapes dist: {relative}")
        if not path.is_file():
            raise SystemExit(f"artifact is missing: {path}")
        if artifact.get("target") != index.get("target") or artifact.get("version") != index.get("version"):
            raise SystemExit(f"artifact metadata mismatch: {component}")
        size = path.stat().st_size
        if not isinstance(artifact.get("size"), int) or isinstance(artifact.get("size"), bool) or size != artifact.get("size") or size <= 0:
            raise SystemExit(f"artifact size mismatch: {component}")
        if not valid_digest(artifact.get("sha256")):
            raise SystemExit(f"artifact digest is invalid: {component}")
        if digest(path).lower() != artifact["sha256"].lower():
            raise SystemExit(f"artifact digest mismatch: {component}")
    manifest_components = manifest.get("components")
    if not isinstance(manifest_components, list):
        raise SystemExit("release manifest has no components")
    artifacts_by_component = {artifact["component"]: artifact["path"] for artifact in artifacts}
    for component in manifest_components:
        if not isinstance(component, dict):
            raise SystemExit("release manifest component is not an object")
        component_id = component.get("id")
        artifact_path = component.get("artifact")
        if not isinstance(component_id, str) or not component_id:
            raise SystemExit(f"release manifest component id is invalid: {component_id}")
        if artifact_path and artifacts_by_component.get(component_id) != artifact_path:
            raise SystemExit(f"manifest component artifact mismatch: {component_id}")
    print(f"validated {index_path}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
