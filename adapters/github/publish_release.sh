#!/usr/bin/env bash
set -euo pipefail
if [ "$#" -ne 2 ]; then echo "usage: $0 <release-tag> <dist-dir>" >&2; exit 2; fi
tag="$1"
dist_dir="$2"
notes="releases/${tag}.md"
gh release view "$tag" >/dev/null 2>&1 || \
  gh release create "$tag" --verify-tag --title "UGS $tag" --notes-file "$notes"
gh release upload "$tag" "$dist_dir"/* --clobber
