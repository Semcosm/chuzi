#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: $0 <output-dir>" >&2
  exit 2
fi

output_dir="$1"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

rm -rf "$output_dir"
mkdir -p "$output_dir"
if [ ! -d "$repo_root/browser-worker/node_modules" ]; then
  npm --prefix "$repo_root/browser-worker" ci --ignore-scripts
fi
npm --prefix "$repo_root/browser-worker" run build
tar -czf "$output_dir/browser-worker-dist.tar.gz" -C "$repo_root/browser-worker/dist" .
echo "built browser worker at $output_dir/browser-worker-dist.tar.gz"
