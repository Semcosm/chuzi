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
  dev|dev-*|nightly-*|test-*|v[0-9]*.[0-9]*.[0-9]*) ;;
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

launcher_binary="chuzi-launcher"
if [ "$target" = "windows-amd64" ]; then
  launcher_binary="chuzi-launcher.exe"
fi
CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build \
  -trimpath \
  -ldflags "-s -w -X main.version=$version" \
  -o "$stage_dir/$launcher_binary" \
  "$repo_root/cmd/launcher"

npm --prefix "$repo_root/browser-worker" ci --ignore-scripts
npm --prefix "$repo_root/browser-worker" run build
cp -R "$repo_root/browser-worker/dist/." "$stage_dir/browser-worker/"

commit="${GITHUB_SHA:-$(git -C "$repo_root" rev-parse HEAD)}"
printf '{\n  "target": "%s",\n  "version": "%s",\n  "commit": "%s",\n  "goos": "%s",\n  "goarch": "%s",\n  "cgo": false\n}\n' \
  "$target" "$version" "$commit" "$goos" "$goarch" > "$stage_dir/build-manifest.json"

channel=nightly
case "$version" in
  test-*) channel=test ;;
  v[0-9]*.[0-9]*.[0-9]*) channel=stable ;;
esac
python3 "$repo_root/scripts/generate_release_manifest.py" \
  --stage "$stage_dir" --target "$target" --version "$version" --commit "$commit" \
  --channel "$channel"

echo "built $target at $stage_dir"
