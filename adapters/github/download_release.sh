#!/usr/bin/env bash
set -euo pipefail
if [ "$#" -ne 2 ]; then echo "usage: $0 <release-tag> <download-dir>" >&2; exit 2; fi
tag="$1"
download_dir="$2"
mkdir -p "$download_dir"

printf '%s\n' "$tag" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' \
  || { echo "release tag must match v<major>.<minor>.<patch>" >&2; exit 2; }

gh release download "$tag" \
  --pattern "ugs-bootstrap-${tag}.tar.gz" \
  --pattern "ugs-bootstrap-${tag}.tar.gz.sha256" \
  --pattern "ugs-bootstrap-${tag}.tar.gz.manifest.json" \
  --dir "$download_dir"

components_asset="ugs-bootstrap-${tag}.tar.gz.components.json"
asset_names="$(gh release view "$tag" --json assets --jq '.assets[].name')"
if printf '%s\n' "$asset_names" | grep -Fqx -- "$components_asset"; then
  gh release download "$tag" --pattern "$components_asset" --dir "$download_dir"
else
  # v0.3.26 and earlier releases predate the full-component inventory. Keep
  # those immutable packages downloadable, while requiring the sidecar for
  # every release that advertises the upgrade protocol.
  version_parts=()
  IFS=. read -r -a version_parts <<< "${tag#v}"
  major="${version_parts[0]}"
  minor="${version_parts[1]}"
  patch="${version_parts[2]}"
  if [ "$major" -gt 0 ] || [ "$minor" -gt 3 ] || { [ "$major" -eq 0 ] && [ "$minor" -eq 3 ] && [ "$patch" -ge 27 ]; }; then
    echo "release $tag is missing required component manifest: $components_asset" >&2
    exit 1
  fi
  echo "release $tag has no component manifest; legacy initialization remains available" >&2
fi
