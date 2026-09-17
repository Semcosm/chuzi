#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: $0 <target> <output-dir>" >&2
  exit 2
fi

target="$1"
output_dir="$2"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
host_os="$(uname -s)"
host_arch="$(uname -m)"

case "$target:$host_os:$host_arch" in
  darwin-arm64:Darwin:arm64) ;;
  linux-amd64:Linux:x86_64) ;;
  linux-arm64:Linux:aarch64|linux-arm64:Linux:arm64) ;;
  *) echo "target $target requires a matching native build host, got $host_os/$host_arch" >&2; exit 2 ;;
esac

rm -rf "$output_dir"
mkdir -p "$output_dir"
cargo build --locked --manifest-path "$repo_root/browser-runtime/Cargo.toml" --release --features desktop-webview
cp "$repo_root/browser-runtime/target/release/chuzi-browser-runtime" "$output_dir/chuzi-browser-runtime"
chmod 0755 "$output_dir/chuzi-browser-runtime"
echo "built native browser runtime for $target at $output_dir/chuzi-browser-runtime"
