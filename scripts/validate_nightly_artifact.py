#!/usr/bin/env python3
"""Validate one downloaded chuzi nightly Actions artifact.

The Actions artifact is an outer ZIP produced by upload-artifact. Its files
are the target package archives and their metadata sidecars. This validator
checks the package catalog and archive contents without executing payloads.
"""

import argparse
import hashlib
import json
import re
import tarfile
import zipfile
from pathlib import Path


TARGETS = {
    "windows-amd64": ("zip", ".exe"),
    "linux-amd64": ("tar.gz", ""),
    "linux-arm64": ("tar.gz", ""),
    "darwin-arm64": ("tar.gz", ""),
}
COMPONENTS = ("launcher", "service", "browser-worker", "desktop-runtime")
SHA256_RE = re.compile(r"^[0-9a-fA-F]{64}$")
COMMIT_RE = re.compile(r"^[0-9a-fA-F]{40}$")


def digest(path: Path) -> str:
    hasher = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            hasher.update(block)
    return hasher.hexdigest()


def normalise_member(name: str) -> str:
    """Return a safe POSIX member name, allowing tar's leading ./ prefix."""
    if not name or "\\" in name or "\x00" in name or name.startswith("/") or re.match(r"^[A-Za-z]:", name):
        raise ValueError(f"unsafe archive member {name!r}")
    while name.startswith("./"):
        name = name[2:]
    if not name or name == ".":
        return ""
    parts = name.split("/")
    if any(part in ("", ".", "..") for part in parts):
        raise ValueError(f"unsafe archive member {name!r}")
    return "/".join(parts)


def archive_members(path: Path, extension: str):
    if extension == "zip":
        with zipfile.ZipFile(path) as archive:
            members = archive.infolist()
            seen = set()
            for member in members:
                name = normalise_member(member.filename)
                if member.is_dir():
                    continue
                if not name or name in seen:
                    raise ValueError(f"duplicate or empty archive member {member.filename!r}: {path.name}")
                mode = (member.external_attr >> 16) & 0o170000
                if mode == 0o120000:
                    raise ValueError(f"symlink archive member {member.filename!r}: {path.name}")
                seen.add(name)
                yield name, archive.read(member)
        return
    with tarfile.open(path, "r:gz") as archive:
        members = archive.getmembers()
        seen = set()
        for member in members:
            name = normalise_member(member.name)
            if member.isdir():
                continue
            if not member.isfile():
                raise ValueError(f"non-regular archive member {member.name!r}: {path.name}")
            if not name or name in seen:
                raise ValueError(f"duplicate or empty archive member {member.name!r}: {path.name}")
            stream = archive.extractfile(member)
            if stream is None:
                raise ValueError(f"archive member cannot be read {member.name!r}: {path.name}")
            seen.add(name)
            yield name, stream.read()


def read_json(path: Path):
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise ValueError(f"invalid JSON {path}: {exc}") from exc


def validate_sidecar(path: Path) -> None:
    fields = path.read_text(encoding="ascii").strip().split()
    if len(fields) != 2 or not SHA256_RE.fullmatch(fields[0]) or fields[1] != path.name[:-7]:
        raise ValueError(f"invalid checksum sidecar: {path.name}")
    if digest(path.with_name(fields[1])).lower() != fields[0].lower():
        raise ValueError(f"checksum mismatch: {fields[1]}")


def validate_archive(path: Path, extension: str, manifest: dict, component: str) -> None:
    members = dict(archive_members(path, extension))
    descriptor = next(item for item in manifest["components"] if item.get("id") == component)
    missing = [item["path"] for item in descriptor.get("resources", []) if item["path"] not in members]
    if missing:
        raise ValueError(f"{path.name} missing {component} resources: {', '.join(missing)}")
    for resource in descriptor.get("resources", []):
        data = members[resource["path"]]
        if resource.get("size") != len(data):
            raise ValueError(f"{path.name} resource size mismatch: {resource['path']}")
        if not SHA256_RE.fullmatch(str(resource.get("sha256", ""))) or hashlib.sha256(data).hexdigest().lower() != resource["sha256"].lower():
            raise ValueError(f"{path.name} resource digest mismatch: {resource['path']}")
    if component == "launcher":
        if "release-manifest.json" not in members:
            raise ValueError(f"{path.name} is missing release-manifest.json")
        embedded = json.loads(members["release-manifest.json"].decode("utf-8"))
        if embedded != manifest:
            raise ValueError(f"{path.name} embedded manifest differs from index")


def validate_bundle(path: Path, extension: str, manifest: dict, target: str, version: str, commit: str) -> None:
    members = dict(archive_members(path, extension))
    for required in ("release-manifest.json", "build-manifest.json"):
        if required not in members:
            raise ValueError(f"{path.name} is missing {required}")
    if json.loads(members["release-manifest.json"].decode("utf-8")) != manifest:
        raise ValueError(f"{path.name} embedded manifest differs from index")
    build = json.loads(members["build-manifest.json"].decode("utf-8"))
    if build.get("target") != target or build.get("version") != version or str(build.get("commit", "")).lower() != commit.lower():
        raise ValueError(f"{path.name} build manifest metadata mismatch")
    for component in manifest.get("components", []):
        for resource in component.get("resources", []):
            name = resource.get("path")
            if name not in members:
                raise ValueError(f"{path.name} missing resource: {name}")
            data = members[name]
            if resource.get("size") != len(data) or hashlib.sha256(data).hexdigest().lower() != str(resource.get("sha256", "")).lower():
                raise ValueError(f"{path.name} resource digest mismatch: {name}")


def validate(args) -> dict:
    if args.target not in TARGETS:
        raise ValueError(f"unsupported target: {args.target}")
    extension, _ = TARGETS[args.target]
    root = args.artifact.resolve()
    if not root.is_dir():
        raise ValueError(f"artifact directory does not exist: {root}")
    indexes = sorted(root.glob(f"*-{args.target}.index.json"))
    if len(indexes) != 1:
        raise ValueError(f"expected one {args.target} release index, found {len(indexes)}")
    index_path = indexes[0]
    index = read_json(index_path)
    if index.get("format") != "chuzi-release-index/v1":
        raise ValueError("invalid release index format")
    if index.get("channel") != "nightly":
        raise ValueError("nightly artifact has a non-nightly channel")
    if index.get("target") != args.target:
        raise ValueError("release index target mismatch")
    if not isinstance(index.get("version"), str) or not index["version"].startswith("nightly-"):
        raise ValueError("nightly version is missing")
    if not COMMIT_RE.fullmatch(str(index.get("commit", ""))):
        raise ValueError("release index commit must be a full SHA-1")
    if args.commit and index["commit"].lower() != args.commit.lower():
        raise ValueError(f"release index commit {index['commit']} does not match expected {args.commit}")
    if args.version and index["version"] != args.version:
        raise ValueError(f"release index version {index['version']} does not match expected {args.version}")
    manifest_path = root / f"chuzi-{index['version']}-{args.target}.manifest.json"
    if not manifest_path.is_file():
        raise ValueError(f"release manifest sidecar is missing: {manifest_path.name}")
    sidecar_manifest = read_json(manifest_path)
    manifest = index.get("manifest")
    if not isinstance(manifest, dict):
        raise ValueError("release index has no embedded manifest")
    if manifest.get("format") != "chuzi-release/v1" or manifest.get("channel") != "nightly":
        raise ValueError("release manifest format or channel is invalid")
    if not isinstance(manifest.get("components"), list) or not isinstance(manifest.get("plugins"), list):
        raise ValueError("release manifest components or plugins are invalid")
    for field in ("channel", "version", "target", "commit"):
        if index.get(field) != manifest.get(field):
            raise ValueError(f"index and manifest differ in {field}")
    if sidecar_manifest != manifest:
        raise ValueError("release manifest sidecar differs from index manifest")
    artifacts = index.get("artifacts")
    if not isinstance(artifacts, list) or len(artifacts) != len(COMPONENTS) + 1:
        raise ValueError("release index does not contain bundle plus four components")
    artifact_by_component = {}
    expected_names = {"bundle": f"chuzi-{index['version']}-{args.target}.{extension}"}
    expected_names.update({component: f"chuzi-{index['version']}-{args.target}-{component}.{extension}" for component in COMPONENTS})
    for artifact in artifacts:
        if not isinstance(artifact, dict):
            raise ValueError("release artifact is not an object")
        component = artifact.get("component")
        if not isinstance(component, str) or component in artifact_by_component or component not in {"bundle", *COMPONENTS}:
            raise ValueError(f"invalid or duplicate artifact component: {component}")
        artifact_by_component[component] = artifact
        filename = artifact.get("path")
        if not isinstance(filename, str) or Path(filename).name != filename:
            raise ValueError(f"artifact path is not a filename: {filename}")
        if filename != expected_names[component]:
            raise ValueError(f"artifact filename mismatch for {component}: {filename}")
        path = root / filename
        if not path.is_file():
            raise ValueError(f"artifact is missing: {filename}")
        if artifact.get("target") != args.target or artifact.get("version") != index["version"]:
            raise ValueError(f"artifact metadata mismatch: {component}")
        if artifact.get("size") != path.stat().st_size:
            raise ValueError(f"artifact size mismatch: {filename}")
        if not SHA256_RE.fullmatch(str(artifact.get("sha256", ""))):
            raise ValueError(f"artifact SHA-256 is invalid: {filename}")
        if digest(path).lower() != artifact["sha256"].lower():
            raise ValueError(f"artifact digest mismatch: {filename}")
    if set(artifact_by_component) != {"bundle", *COMPONENTS}:
        raise ValueError("release index artifact set is incomplete")
    component_by_id = {item.get("id"): item for item in manifest["components"] if isinstance(item, dict)}
    if set(component_by_id) != set(COMPONENTS) or len(component_by_id) != len(manifest["components"]):
        raise ValueError("release manifest component set is incomplete or contains duplicates")
    for sidecar in sorted(root.glob("*.sha256")):
        validate_sidecar(sidecar)
    for filename in [*expected_names.values(), index_path.name]:
        if not (root / f"{filename}.sha256").is_file():
            raise ValueError(f"checksum sidecar is missing: {filename}.sha256")
    for component in COMPONENTS:
        descriptor = component_by_id.get(component)
        if not isinstance(descriptor, dict):
            raise ValueError(f"release manifest is missing component: {component}")
        if descriptor.get("version") != index["version"]:
            raise ValueError(f"component version mismatch: {component}")
        if descriptor.get("artifact") != expected_names[component]:
            raise ValueError(f"component artifact metadata mismatch: {component}")
        artifact = artifact_by_component[component]
        validate_archive(root / artifact["path"], extension, manifest, component)
    bundle = artifact_by_component["bundle"]
    validate_bundle(root / bundle["path"], extension, manifest, args.target, index["version"], index["commit"])
    return {
        "target": args.target,
        "version": index["version"],
        "commit": index["commit"],
        "index": index_path.name,
        "launcher_archive": artifact_by_component["launcher"]["path"],
        "artifacts": {name: artifact_by_component[name]["path"] for name in sorted(artifact_by_component)},
    }


def extract_launcher(artifact: Path, destination: Path, extension: str) -> None:
    destination = destination.resolve()
    destination.mkdir(parents=True, exist_ok=True)
    if any(destination.iterdir()):
        raise ValueError(f"launcher extraction destination is not empty: {destination}")
    for name, data in archive_members(artifact, extension):
        path = destination / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)
        path.chmod(0o700 if name != "release-manifest.json" else 0o600)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--artifact", required=True, type=Path)
    parser.add_argument("--target", required=True)
    parser.add_argument("--commit", default="")
    parser.add_argument("--version", default="")
    parser.add_argument("--extract-launcher", type=Path)
    args = parser.parse_args()
    try:
        result = validate(args)
        if args.extract_launcher:
            extension = TARGETS[args.target][0]
            extract_launcher(Path(args.artifact).resolve() / result["launcher_archive"], args.extract_launcher, extension)
    except (OSError, ValueError, tarfile.TarError, zipfile.BadZipFile, UnicodeError) as exc:
        raise SystemExit(f"nightly artifact validation failed: {exc}") from exc
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
