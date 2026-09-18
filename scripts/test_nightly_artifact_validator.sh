#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_root="$(mktemp -d)"
trap 'rm -rf "$test_root"' EXIT

python_command="${PYTHON:-python3}"
command -v "$python_command" >/dev/null 2>&1 || { echo "nightly artifact validator test requires Python 3" >&2; exit 1; }

"$python_command" - "$test_root" "$repo_root" <<'PY'
import hashlib
import json
import sys
import tarfile
from pathlib import Path

root = Path(sys.argv[1])
artifact = root / "linux-artifact"
artifact.mkdir()
commit = "a" * 40
version = "nightly-500-aaaaaaaaaaaa"
files = {
    "launcher": {"chuzi-launcher": b"launcher", "release-manifest.json": None},
    "service": {"chuzi": b"service"},
    "browser-worker": {"browser-worker/index.mjs": b"worker"},
}
manifest = {
    "format": "chuzi-release/v1", "channel": "nightly", "version": version,
    "commit": commit, "target": "linux-amd64", "generated_at": "2026-01-01T00:00:00Z",
    "components": [
        {"id": "launcher", "version": version, "required": True, "resources": [
            {"path": "chuzi-launcher", "sha256": hashlib.sha256(b"launcher").hexdigest(), "size": 8},
        ], "artifact": f"chuzi-{version}-linux-amd64-launcher.tar.gz"},
        {"id": "service", "version": version, "required": False, "resources": [
            {"path": "chuzi", "sha256": hashlib.sha256(b"service").hexdigest(), "size": 7},
        ], "artifact": f"chuzi-{version}-linux-amd64-service.tar.gz"},
        {"id": "browser-worker", "version": version, "required": False, "resources": [
            {"path": "browser-worker/index.mjs", "sha256": hashlib.sha256(b"worker").hexdigest(), "size": 6},
        ], "artifact": f"chuzi-{version}-linux-amd64-browser-worker.tar.gz"},
    ], "plugins": [],
}
for name, members in files.items():
    members["release-manifest.json"] = json.dumps(manifest).encode() if name == "launcher" else members.get("release-manifest.json")
    members = {path: data for path, data in members.items() if data is not None}
    archive_name = f"chuzi-{version}-linux-amd64-{name}.tar.gz"
    archive_path = artifact / archive_name
    with tarfile.open(archive_path, "w:gz") as archive:
        for path, data in members.items():
            info = tarfile.TarInfo(path)
            info.mode = 0o700
            info.size = len(data)
            archive.addfile(info, __import__("io").BytesIO(data))
    digest = hashlib.sha256(archive_path.read_bytes()).hexdigest()
    members["__artifact__"] = archive_name
    files[name] = members

artifacts = []
for name in ["bundle", "launcher", "service", "browser-worker"]:
    source = files["launcher"] if name == "bundle" else files[name]
    if name == "bundle":
        archive_name = f"chuzi-{version}-linux-amd64.tar.gz"
        archive_path = artifact / archive_name
        with tarfile.open(archive_path, "w:gz") as archive:
            build = {"target": "linux-amd64", "version": version, "commit": commit}
            data = json.dumps(build).encode()
            info = tarfile.TarInfo("build-manifest.json")
            info.mode = 0o600
            info.size = len(data)
            archive.addfile(info, __import__("io").BytesIO(data))
            for component in files.values():
                for path, data in component.items():
                    if path == "__artifact__":
                        continue
                    info = tarfile.TarInfo(path)
                    info.mode = 0o700
                    info.size = len(data)
                    archive.addfile(info, __import__("io").BytesIO(data))
    else:
        archive_name = source["__artifact__"]
    archive_path = artifact / archive_name
    digest = hashlib.sha256(archive_path.read_bytes()).hexdigest()
    artifacts.append({"component": name, "target": "linux-amd64", "version": version, "path": archive_name, "size": archive_path.stat().st_size, "sha256": digest})
    (artifact / f"{archive_name}.sha256").write_text(f"{digest}  {archive_name}\n", encoding="ascii")

index = {"format": "chuzi-release-index/v1", "channel": "nightly", "version": version, "commit": commit, "target": "linux-amd64", "generated_at": manifest["generated_at"], "manifest": manifest, "artifacts": artifacts}
(artifact / f"chuzi-{version}-linux-amd64.index.json").write_text(json.dumps(index), encoding="utf-8")
index_path = artifact / f"chuzi-{version}-linux-amd64.index.json"
index_digest = hashlib.sha256(index_path.read_bytes()).hexdigest()
(artifact / f"{index_path.name}.sha256").write_text(f"{index_digest}  {index_path.name}\n", encoding="ascii")
(artifact / f"chuzi-{version}-linux-amd64.manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
PY

artifact_dir="$test_root/linux-artifact"
commit="$(python3 -c 'print("a" * 40)')"
version="nightly-500-aaaaaaaaaaaa"
"$python_command" "$repo_root/scripts/validate_nightly_artifact.py" --artifact "$artifact_dir" --target linux-amd64 --commit "$commit" --version "$version" >/dev/null

cp -R "$artifact_dir" "$test_root/invalid"
printf '%s  %s\n' "$(printf '0%.0s' {1..64})" "chuzi-${version}-linux-amd64-service.tar.gz" > "$test_root/invalid/chuzi-${version}-linux-amd64-service.tar.gz.sha256"
if "$python_command" "$repo_root/scripts/validate_nightly_artifact.py" --artifact "$test_root/invalid" --target linux-amd64 --commit "$commit" --version "$version"; then
  echo "validator accepted a corrupted artifact checksum" >&2
  exit 1
fi

echo "nightly artifact validator contract passed"
