#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: $0 <target> <version>" >&2
  exit 2
fi

target="$1"
version="$2"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
stage_dir="$repo_root/dist/$target/stage"
artifact="$repo_root/dist/chuzi-${version}-${target}.tar.gz"

[ -d "$stage_dir" ] || { echo "build stage does not exist: $stage_dir" >&2; exit 1; }
tar -czf "$artifact" -C "$stage_dir" .
for component in launcher service browser-worker desktop-runtime; do
  component_dir="$(mktemp -d)"
  case "$component" in
    launcher) cp "$stage_dir/chuzi-launcher" "$component_dir/"; cp "$stage_dir/release-manifest.json" "$component_dir/" ;;
    service) cp "$stage_dir/chuzi" "$component_dir/" ;;
    browser-worker) cp -R "$stage_dir/browser-worker" "$component_dir/" ;;
    desktop-runtime) cp "$stage_dir/chuzi-browser-runtime" "$component_dir/" ;;
  esac
  tar -czf "$repo_root/dist/chuzi-${version}-${target}-${component}.tar.gz" -C "$component_dir" .
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
for component_artifact in "$repo_root/dist/chuzi-${version}-${target}-"*.tar.gz; do
  [ "$component_artifact" = "$artifact" ] && continue
  write_sha256 "$component_artifact"
done
echo "packaged $artifact"
