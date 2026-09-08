#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
  echo "usage: $0 <target> [version]" >&2
  exit 2
fi

target="$1"
version="${2:-dev}"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

case "$target" in
  windows-amd64) goos=windows; goarch=amd64; binary=chuzi.exe ;;
  linux-amd64) goos=linux; goarch=amd64; binary=chuzi ;;
  linux-arm64) goos=linux; goarch=arm64; binary=chuzi ;;
  darwin-arm64) goos=darwin; goarch=arm64; binary=chuzi ;;
  *) echo "unsupported build target: $target" >&2; exit 2 ;;
esac

case "$version" in
  dev|dev-*|v[0-9]*.[0-9]*.[0-9]*) ;;
  *) echo "invalid build version: $version" >&2; exit 2 ;;
esac

target_dir="$repo_root/dist/$target"
stage_dir="$target_dir/stage"
rm -rf "$target_dir"
mkdir -p "$stage_dir/browser-worker"

CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build \
  -trimpath \
  -ldflags "-s -w -X github.com/Semcosm/chuzi/cmd/service.version=$version" \
  -o "$stage_dir/$binary" \
  "$repo_root/cmd/service"

npm --prefix "$repo_root/browser-worker" ci --ignore-scripts
npm --prefix "$repo_root/browser-worker" run build
cp -R "$repo_root/browser-worker/dist/." "$stage_dir/browser-worker/"

commit="${GITHUB_SHA:-$(git -C "$repo_root" rev-parse HEAD)}"
printf '{\n  "target": "%s",\n  "version": "%s",\n  "commit": "%s",\n  "goos": "%s",\n  "goarch": "%s",\n  "cgo": false\n}\n' \
  "$target" "$version" "$commit" "$goos" "$goarch" > "$stage_dir/build-manifest.json"

echo "built $target at $stage_dir"
