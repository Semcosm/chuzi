#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 6 ]; then
  echo "usage: $0 <target> <version> <go-dir> <worker-archive> <runtime-binary> <dist-root>" >&2
  exit 2
fi

target="$1"
version="$2"
go_dir="$3"
worker_archive="$4"
runtime_binary="$5"
dist_root="$6"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

case "$target" in
  windows-amd64) service_binary=chuzi.exe; launcher_binary=chuzi-launcher.exe; runtime_name=chuzi-browser-runtime.exe; goos=windows; goarch=amd64 ;;
  linux-amd64) service_binary=chuzi; launcher_binary=chuzi-launcher; runtime_name=chuzi-browser-runtime; goos=linux; goarch=amd64 ;;
  linux-arm64) service_binary=chuzi; launcher_binary=chuzi-launcher; runtime_name=chuzi-browser-runtime; goos=linux; goarch=arm64 ;;
  darwin-arm64) service_binary=chuzi; launcher_binary=chuzi-launcher; runtime_name=chuzi-browser-runtime; goos=darwin; goarch=arm64 ;;
  *) echo "unsupported build target: $target" >&2; exit 2 ;;
esac

target_dir="$dist_root/$target"
stage_dir="$target_dir/stage"
rm -rf "$target_dir"
mkdir -p "$stage_dir/browser-worker"
cp "$go_dir/$service_binary" "$stage_dir/$service_binary"
cp "$go_dir/$launcher_binary" "$stage_dir/$launcher_binary"
cp "$runtime_binary" "$stage_dir/$runtime_name"
# Artifact archives do not reliably preserve Unix executable bits.
chmod 0755 \
  "$stage_dir/$service_binary" \
  "$stage_dir/$launcher_binary" \
  "$stage_dir/$runtime_name"
tar -xzf "$worker_archive" -C "$stage_dir/browser-worker"

commit="${GITHUB_SHA:-$(git -C "$repo_root" rev-parse HEAD)}"
python3 - "$stage_dir/build-manifest.json" "$target" "$version" "$commit" "$goos" "$goarch" <<'PY'
import json
import sys

path, target, version, commit, goos, goarch = sys.argv[1:]
with open(path, "w", encoding="utf-8") as stream:
    json.dump({
        "target": target,
        "version": version,
        "commit": commit,
        "goos": goos,
        "goarch": goarch,
        "cgo": False,
        "rustHelper": "chuzi-browser-runtime",
        "browserRuntime": "wry-desktop",
    }, stream, indent=2)
    stream.write("\n")
PY

channel=nightly
case "$version" in v[0-9]*.[0-9]*.[0-9]*) channel=stable ;; esac
python3 "$repo_root/scripts/generate_release_manifest.py" \
  --stage "$stage_dir" --target "$target" --version "$version" --commit "$commit" --channel "$channel"

echo "assembled $target at $stage_dir"
