#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_root="$(mktemp -d)"
trap 'rm -rf "$test_root"' EXIT

python3 - "$test_root" "$repo_root" <<'PY'
import json
import subprocess
import sys
from pathlib import Path

root = Path(sys.argv[1])
repo = Path(sys.argv[2])
commit = "0123456789abcdef0123456789abcdef01234567"
targets = ("windows-amd64", "linux-amd64", "linux-arm64", "darwin-arm64")
channels = (("test", "test-123-0123456789ab", True), ("nightly", "nightly-124-0123456789ab", True), ("stable", "v1.2.3", False))
output = root / "catalog"
for channel, version, prerelease in channels:
    for target in targets:
        source = root / f"chuzi-{channel}-{target}"
        source.mkdir()
        index_name = f"chuzi-{version}-{target}.index.json"
        index = {
            "format": "chuzi-release-index/v1",
            "channel": channel,
            "version": version,
            "commit": commit,
            "target": target,
        }
        (source / index_name).write_text(json.dumps(index), encoding="utf-8")
        (source / f"{index_name}.sha256").write_text("placeholder\n", encoding="ascii")
        (source / f"chuzi-{version}-{target}.manifest.json").write_text("{}\n", encoding="utf-8")
        (source / f"chuzi-{version}-{target}.zip").write_bytes(b"artifact")
    subprocess.run([
        sys.executable,
        str(repo / "scripts/publish_release_catalog.py"),
        "--artifact-root", str(root),
        "--output-root", str(output),
        "--channel", channel,
        "--version", version,
        "--commit", commit,
        "--base-url", "https://raw.githubusercontent.com/Semcosm/chuzi/release-catalog",
    ], check=True)

    for target in targets:
        catalog = json.loads((output / channel / f"{target}.json").read_text(encoding="utf-8"))
        assert catalog["format"] == "chuzi-release-catalog/v1"
        assert catalog["channel"] == channel
        assert catalog["target"] == target
        entry = catalog["releases"][0]
        assert entry["version"] == version
        assert entry["commit"] == commit
        assert entry["prerelease"] is prerelease
        assert entry["index_url"].endswith(f"/{channel}/{target}/{version}/chuzi-{version}-{target}.index.json")
        assert (output / channel / target / version / f"chuzi-{version}-{target}.index.json").is_file()

print("release catalog contract passed")
PY
