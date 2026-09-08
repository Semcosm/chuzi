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
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum "$artifact" > "$artifact.sha256"
elif command -v shasum >/dev/null 2>&1; then
  shasum -a 256 "$artifact" > "$artifact.sha256"
else
  echo "no SHA256 utility is available" >&2
  exit 1
fi
echo "packaged $artifact"
