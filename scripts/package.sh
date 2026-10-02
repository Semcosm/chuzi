#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: $0 <target> <version>" >&2
  exit 2
fi

target="$1"
version="$2"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
case "$target" in
  windows-amd64) archive_extension=zip; service_binary=chuzi.exe; launcher_binary=chuzi-launcher.exe ;;
  linux-amd64|linux-arm64|darwin-arm64) archive_extension=tar.gz; service_binary=chuzi; launcher_binary=chuzi-launcher ;;
  *) echo "unsupported package target: $target" >&2; exit 2 ;;
esac
stage_dir="$repo_root/dist/$target/stage"
artifact="$repo_root/dist/chuzi-${version}-${target}.${archive_extension}"

[ -d "$stage_dir" ] || { echo "build stage does not exist: $stage_dir" >&2; exit 1; }
mkdir -p "$repo_root/dist"
rm -f "$artifact" "$artifact.sha256" "$repo_root/dist/chuzi-${version}-${target}.manifest.json" \
  "$repo_root/dist/chuzi-${version}-${target}.index.json" "$repo_root/dist/chuzi-${version}-${target}.index.json.sha256"
if [ "$archive_extension" = "zip" ]; then
  python3 - "$stage_dir" "$artifact" <<'PY'
import sys, zipfile
from pathlib import Path
source = Path(sys.argv[1]).resolve(); output = Path(sys.argv[2]).resolve()
with zipfile.ZipFile(output, "w", compression=zipfile.ZIP_DEFLATED) as archive:
    for path in sorted(source.rglob("*")):
        if path.is_file(): archive.write(path, path.relative_to(source).as_posix())
PY
else
  tar -czf "$artifact" -C "$stage_dir" .
fi
for component in launcher service browser-worker; do
  component_dir="$(mktemp -d)"
  case "$component" in
    launcher) cp "$stage_dir/$launcher_binary" "$component_dir/"; cp "$stage_dir/release-manifest.json" "$component_dir/" ;;
    service)
      cp "$stage_dir/$service_binary" "$component_dir/"
      if [ "$target" = "windows-amd64" ]; then cp "$stage_dir/chuzi-browser-launcher.exe" "$stage_dir/chuzi-user-agent.exe" "$stage_dir/node.exe" "$component_dir/"; fi
      ;;
    browser-worker) cp -R "$stage_dir/browser-worker" "$component_dir/" ;;
  esac
  component_artifact="$repo_root/dist/chuzi-${version}-${target}-${component}.${archive_extension}"
  if [ "$archive_extension" = "zip" ]; then
    python3 - "$component_dir" "$component_artifact" <<'PY'
import sys, zipfile
from pathlib import Path
source = Path(sys.argv[1]).resolve(); output = Path(sys.argv[2]).resolve()
with zipfile.ZipFile(output, "w", compression=zipfile.ZIP_DEFLATED) as archive:
    for path in sorted(source.rglob("*")):
        if path.is_file(): archive.write(path, path.relative_to(source).as_posix())
PY
  else
    tar -czf "$component_artifact" -C "$component_dir" .
  fi
  rm -rf "$component_dir"
done
cp "$stage_dir/release-manifest.json" "$repo_root/dist/chuzi-${version}-${target}.manifest.json"
write_sha256() {
  local file="$1"
  local directory
  local filename
  directory="$(dirname "$file")"
  filename="$(basename "$file")"
  if command -v sha256sum >/dev/null 2>&1; then
    (cd "$directory" && sha256sum "$filename" > "$filename.sha256")
  elif command -v shasum >/dev/null 2>&1; then
    (cd "$directory" && shasum -a 256 "$filename" > "$filename.sha256")
  else
    echo "no SHA256 utility is available" >&2
    exit 1
  fi
}

write_sha256 "$artifact"
for component_artifact in "$repo_root/dist/chuzi-${version}-${target}-"*.$archive_extension; do
  [ "$component_artifact" = "$artifact" ] && continue
  write_sha256 "$component_artifact"
done
adapter_source="$repo_root/dist/$target/chuzi-${version}-${target}-genshin-cloudgame.${archive_extension}"
adapter_artifact="$repo_root/dist/chuzi-${version}-${target}-genshin-cloudgame.${archive_extension}"
if [ -f "$adapter_source" ]; then
  if [ "$adapter_source" != "$adapter_artifact" ]; then cp "$adapter_source" "$adapter_artifact"; fi
  write_sha256 "$adapter_artifact"
else
  echo "Genshin adapter archive is missing: $adapter_source" >&2
  exit 1
fi
channel=nightly
case "$version" in
  v[0-9]*.[0-9]*.[0-9]*) channel=stable ;;
  test-*) channel=test ;;
esac
python3 "$repo_root/scripts/generate_release_index.py" \
  --manifest "$stage_dir/release-manifest.json" \
  --dist "$repo_root/dist" \
  --target "$target" \
  --version "$version" \
  --commit "${GITHUB_SHA:-$(git -C "$repo_root" rev-parse HEAD)}" \
  --channel "$channel"
write_sha256 "$repo_root/dist/chuzi-${version}-${target}.index.json"
echo "packaged $artifact"
