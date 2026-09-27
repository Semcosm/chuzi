#!/usr/bin/env python3
"""Build a deterministic, self-describing adapter archive."""
import argparse
import hashlib
import json
import shutil
import tarfile
import tempfile
import zipfile
from pathlib import Path


def digest(path: Path) -> str:
    hasher = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            hasher.update(block)
    return hasher.hexdigest()


def safe_member(path: Path, root: Path) -> str:
    relative = path.relative_to(root).as_posix()
    if not relative or relative.startswith("/") or any(part in ("", ".", "..") for part in relative.split("/")):
        raise ValueError(f"unsafe adapter resource path: {relative}")
    return relative


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--source", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--manifest-output", required=True, type=Path)
    parser.add_argument("--version", required=True)
    args = parser.parse_args()
    source = args.source.resolve()
    if not source.is_dir():
        raise SystemExit(f"adapter source directory is missing: {source}")
    template_path = source / "adapter-manifest.json"
    try:
        template = json.loads(template_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise SystemExit(f"invalid adapter manifest template: {exc}") from exc
    if template.get("format") != "chuzi-adapter/v1" or not template.get("id") or not template.get("entry"):
        raise SystemExit("adapter manifest template is incomplete")
    for path in source.rglob("*"):
        if path.is_symlink():
            raise SystemExit(f"adapter source contains a symlink: {path}")
    files = sorted(path for path in source.rglob("*") if path.is_file() and path.name != "adapter-manifest.json")
    if not files:
        raise SystemExit("adapter package has no resources")
    resources = [{"path": safe_member(path, source), "sha256": digest(path), "size": path.stat().st_size} for path in files]
    if template["entry"] not in {item["path"] for item in resources}:
        raise SystemExit("adapter entry is not a package resource")
    manifest = dict(template)
    manifest["version"] = args.version
    manifest["resources"] = resources
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.manifest_output.parent.mkdir(parents=True, exist_ok=True)
    args.manifest_output.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    with tempfile.TemporaryDirectory(prefix="chuzi-adapter-") as temporary:
        root = Path(temporary)
        for path in files:
            target = root / safe_member(path, source)
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(path, target)
        (root / "adapter-manifest.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
        output = args.output.resolve()
        if output.suffix.lower() == ".zip":
            with zipfile.ZipFile(output, "w", compression=zipfile.ZIP_DEFLATED) as archive:
                for path in sorted(root.rglob("*")):
                    if path.is_file():
                        archive.write(path, path.relative_to(root).as_posix())
        elif output.name.endswith(".tar.gz"):
            with tarfile.open(output, "w:gz") as archive:
                for path in sorted(root.rglob("*")):
                    if path.is_file():
                        archive.add(path, arcname=path.relative_to(root).as_posix(), recursive=False)
        else:
            raise SystemExit("adapter archive must end in .zip or .tar.gz")
    print(output)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
