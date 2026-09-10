#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
  echo "usage: $0 <target> [version]" >&2
  exit 2
fi

target="$1"
version="${2:-dev}"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
host_os="$(uname -s)"
host_arch="$(uname -m)"

case "$target" in
  windows-amd64) goos=windows; goarch=amd64; binary=chuzi.exe; runtime_backend=wry-desktop; runtime_features=desktop-webview ;;
  linux-amd64) goos=linux; goarch=amd64; binary=chuzi; runtime_backend=deferred; runtime_features= ;;
  linux-arm64) goos=linux; goarch=arm64; binary=chuzi; runtime_backend=deferred; runtime_features= ;;
  darwin-arm64) goos=darwin; goarch=arm64; binary=chuzi; runtime_backend=wry-desktop; runtime_features=desktop-webview ;;
  *) echo "unsupported build target: $target" >&2; exit 2 ;;
esac

case "$target:$host_os:$host_arch" in
  windows-amd64:*)
    echo "desktop WebView targets must use their native runner: $target on $host_os/$host_arch" >&2
    exit 2
    ;;
  darwin-arm64:Darwin:arm64) ;;
  darwin-arm64:*)
    echo "desktop WebView targets must use their native runner: $target on $host_os/$host_arch" >&2
    exit 2
    ;;
  linux-amd64:Linux:x86_64|linux-arm64:Linux:aarch64|linux-arm64:Linux:arm64) ;;
  *)
    echo "target $target requires a matching native build host, got $host_os/$host_arch" >&2
    exit 2
    ;;
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

cargo_args=(build --locked --manifest-path "$repo_root/browser-runtime/Cargo.toml" --release)
if [ -n "$runtime_features" ]; then
  cargo_args+=(--features "$runtime_features")
fi
cargo "${cargo_args[@]}"
runtime_binary="$repo_root/browser-runtime/target/release/chuzi-browser-runtime"
cp "$runtime_binary" "$stage_dir/chuzi-browser-runtime"

commit="${GITHUB_SHA:-$(git -C "$repo_root" rev-parse HEAD)}"
printf '{\n  "target": "%s",\n  "version": "%s",\n  "commit": "%s",\n  "goos": "%s",\n  "goarch": "%s",\n  "cgo": false,\n  "rustHelper": "chuzi-browser-runtime",\n  "browserRuntime": "%s"\n}\n' \
  "$target" "$version" "$commit" "$goos" "$goarch" "$runtime_backend" > "$stage_dir/build-manifest.json"

echo "built $target at $stage_dir"
