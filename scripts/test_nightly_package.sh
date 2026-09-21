#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_root="$(mktemp -d)"
cleanup() { rm -rf "$test_root"; }
trap cleanup EXIT

if command -v python3 >/dev/null 2>&1; then
  python_command=python3
elif command -v python >/dev/null 2>&1; then
  python_command=python
else
  echo "nightly package test requires Python 3" >&2
  exit 1
fi

write_checksum() {
  local file="$1"
  local directory filename
  directory="$(dirname "$file")"
  filename="$(basename "$file")"
  if command -v sha256sum >/dev/null 2>&1; then
    (cd "$directory" && sha256sum "$filename" >"$filename.sha256")
  elif command -v shasum >/dev/null 2>&1; then
    (cd "$directory" && shasum -a 256 "$filename" >"$filename.sha256")
  else
    echo "nightly package test requires sha256sum or shasum" >&2
    exit 1
  fi
}

for target in linux-amd64 windows-amd64; do
  commit="0123456789abcdef0123456789abcdef01234567"
  version="nightly-123-${commit:0:12}"
  stage="$test_root/$target-stage"
  dist="$test_root/$target-dist"
  mkdir -p "$stage/browser-worker" "$dist"
  if [ "$target" = "windows-amd64" ]; then
    printf '%s' launcher >"$stage/chuzi-launcher.exe"
    printf '%s' service >"$stage/chuzi.exe"
    printf '%s' browser-launcher >"$stage/chuzi-browser-launcher.exe"
  else
    printf '%s' launcher >"$stage/chuzi-launcher"
    printf '%s' service >"$stage/chuzi"
  fi
  printf '%s' worker >"$stage/browser-worker/index.mjs"

  "$python_command" "$repo_root/scripts/generate_release_manifest.py" \
    --stage "$stage" --target "$target" --version "$version" \
    --commit "$commit" --channel nightly

  if [ "$target" = "windows-amd64" ]; then
    "$python_command" - "$stage" "$dist" "$version" "$target" <<'PY'
import sys
import zipfile
from pathlib import Path

stage, dist, version, target = map(Path, sys.argv[1:])
groups = {
    "bundle": [path for path in stage.rglob("*") if path.is_file()],
    "launcher": [stage / "chuzi-launcher.exe", stage / "release-manifest.json"],
    "service": [stage / "chuzi.exe", stage / "chuzi-browser-launcher.exe"],
    "browser-worker": [path for path in (stage / "browser-worker").rglob("*") if path.is_file()],
}
for component, files in groups.items():
    suffix = "" if component == "bundle" else f"-{component}"
    output = dist / f"chuzi-{version}-{target}{suffix}.zip"
    with zipfile.ZipFile(output, "w", compression=zipfile.ZIP_DEFLATED) as archive:
        for source in files:
            archive.write(source, source.relative_to(stage).as_posix())
PY
  else
    tar -czf "$dist/chuzi-${version}-${target}.tar.gz" -C "$stage" .
    for component in launcher service browser-worker; do
      component_dir="$test_root/$target-$component"
      mkdir -p "$component_dir"
      case "$component" in
        launcher) cp "$stage/chuzi-launcher" "$component_dir/"; cp "$stage/release-manifest.json" "$component_dir/" ;;
        service) cp "$stage/chuzi" "$component_dir/" ;;
        browser-worker) cp -R "$stage/browser-worker" "$component_dir/" ;;
      esac
      tar -czf "$dist/chuzi-${version}-${target}-${component}.tar.gz" -C "$component_dir" .
    done
  fi

  "$python_command" "$repo_root/scripts/generate_release_index.py" \
    --manifest "$stage/release-manifest.json" --dist "$dist" \
    --target "$target" --version "$version" --commit "$commit" --channel nightly
  index="$dist/chuzi-${version}-${target}.index.json"
  "$python_command" "$repo_root/scripts/validate_release_index.py" --index "$index" --dist "$dist"
  grep -Fq "nightly-123-${commit:0:12}" "$index"
  test -f "$index.sha256"
  invalid_index="$test_root/$target-invalid-index.json"
  "$python_command" - "$index" "$invalid_index" <<'PY'
import json
import sys

source, destination = sys.argv[1:]
with open(source, encoding="utf-8") as stream:
    index = json.load(stream)
index["artifacts"][0]["path"] = "..\\escape"
with open(destination, "w", encoding="utf-8") as stream:
    json.dump(index, stream)
PY
  if "$python_command" "$repo_root/scripts/validate_release_index.py" --index "$invalid_index" --dist "$dist"; then
    echo "unsafe release artifact path was accepted for $target" >&2
    exit 1
  fi
  write_checksum "$index"
  if command -v sha256sum >/dev/null 2>&1; then
    (cd "$dist" && sha256sum -c "$(basename "$index").sha256")
  else
    (cd "$dist" && shasum -a 256 -c "$(basename "$index").sha256")
  fi
done

echo "nightly package contract passed"
