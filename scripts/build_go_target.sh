#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 3 ]; then
  echo "usage: $0 <target> <version> <output-dir>" >&2
  exit 2
fi

target="$1"
version="$2"
output_dir="$3"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

case "$target" in
  windows-amd64) goos=windows; goarch=amd64; service_binary=chuzi.exe; launcher_binary=chuzi-launcher.exe ;;
  linux-amd64) goos=linux; goarch=amd64; service_binary=chuzi; launcher_binary=chuzi-launcher ;;
  linux-arm64) goos=linux; goarch=arm64; service_binary=chuzi; launcher_binary=chuzi-launcher ;;
  darwin-arm64) goos=darwin; goarch=arm64; service_binary=chuzi; launcher_binary=chuzi-launcher ;;
  *) echo "unsupported build target: $target" >&2; exit 2 ;;
esac

case "$version" in
  dev|dev-*|nightly-*|v[0-9]*.[0-9]*.[0-9]*) ;;
  *) echo "invalid build version: $version" >&2; exit 2 ;;
esac

rm -rf "$output_dir"
mkdir -p "$output_dir"

CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build \
  -trimpath \
  -ldflags "-s -w -X github.com/Semcosm/chuzi/cmd/service.version=$version" \
  -o "$output_dir/$service_binary" \
  "$repo_root/cmd/service"

CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build \
  -trimpath \
  -ldflags "-s -w -X main.version=$version" \
  -o "$output_dir/$launcher_binary" \
  "$repo_root/cmd/launcher"

echo "built Go components for $target at $output_dir"
