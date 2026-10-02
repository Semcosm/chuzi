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

if [ "$target" = "windows-amd64" ]; then
  CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build \
    -trimpath \
    -o "$stage_dir/chuzi-browser-launcher.exe" \
    "$repo_root/cmd/browser-launcher"
  CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build \
    -trimpath \
    -o "$stage_dir/chuzi-user-agent.exe" \
    "$repo_root/cmd/user-agent"
fi

npm --prefix "$repo_root/browser-worker" ci --ignore-scripts
npm --prefix "$repo_root/browser-worker" run build
cp -R "$repo_root/browser-worker/dist/." "$stage_dir/browser-worker/"
if [ "$target" = "windows-amd64" ]; then
  node_runtime="${CHUZI_NODE_RUNTIME:-}"
  if [ -z "$node_runtime" ] || [ ! -f "$node_runtime" ] || [ "${node_runtime##*/}" != "node.exe" ]; then
    echo "windows-amd64 packaging requires CHUZI_NODE_RUNTIME to point to node.exe" >&2
    exit 1
  fi
  cp "$node_runtime" "$stage_dir/node.exe"
fi
adapter_extension=tar.gz
if [ "$target" = "windows-amd64" ]; then
  adapter_extension=zip
fi
adapter_archive="$target_dir/chuzi-${version}-${target}-genshin-cloudgame.${adapter_extension}"
adapter_manifest="$target_dir/genshin-cloudgame-adapter-manifest.json"
python3 "$repo_root/scripts/build_adapter_package.py" \
  --source "$repo_root/browser-worker/adapters/genshin-cloudgame" \
  --output "$adapter_archive" --manifest-output "$adapter_manifest" --version "$version"

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
  --channel "$channel" --adapter-archive "$adapter_archive" --adapter-manifest "$adapter_manifest"

echo "built $target at $stage_dir"
