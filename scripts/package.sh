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
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum "$artifact" > "$artifact.sha256"
  for component_artifact in "$repo_root/dist/chuzi-${version}-${target}-"*.tar.gz; do
    [ "$component_artifact" = "$artifact" ] && continue
    sha256sum "$component_artifact" > "$component_artifact.sha256"
  done
elif command -v shasum >/dev/null 2>&1; then
  shasum -a 256 "$artifact" > "$artifact.sha256"
  for component_artifact in "$repo_root/dist/chuzi-${version}-${target}-"*.tar.gz; do
    [ "$component_artifact" = "$artifact" ] && continue
    shasum -a 256 "$component_artifact" > "$component_artifact.sha256"
  done
else
  echo "no SHA256 utility is available" >&2
  exit 1
fi
echo "packaged $artifact"
