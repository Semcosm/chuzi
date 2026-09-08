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
sha256sum "$artifact" > "$artifact.sha256"
echo "packaged $artifact"
