#!/usr/bin/env bash
set -euo pipefail
manifest="${1:-.ugs/policy.json}"
workflow_dir="${2:-.github/workflows}"
fail() { echo "GitHub Actions pinning validation failed: $1" >&2; exit 1; }
[ -f "$manifest" ] || fail "manifest does not exist: $manifest"
[ -d "$workflow_dir" ] || fail "workflow directory does not exist: $workflow_dir"
command -v jq >/dev/null 2>&1 || fail "jq is required"
profile="$(jq -r '.supply_chain.profile // "none"' "$manifest")"
pinning="$(jq -r '.supply_chain.action_pinning // "none"' "$manifest")"
if [ "$pinning" != "full_sha" ]; then
  echo "action pinning validation not required ($profile/$pinning)"
  exit 0
fi
found=0
while IFS= read -r workflow; do
  while IFS= read -r reference; do
    found=1
    [[ "$reference" = ./* ]] && continue
    [[ "$reference" =~ ^[^/]+/[^/]+@[0-9a-fA-F]{40}$ ]] || fail "action is not pinned to a full commit SHA: $reference"
  done < <(sed -nE 's/^[[:space:]-]*uses:[[:space:]]*([^[:space:]#]+).*/\1/p' "$workflow")
done < <(find "$workflow_dir" -type f \( -name '*.yml' -o -name '*.yaml' \) -print)
[ "$found" -eq 1 ] || fail "no GitHub action references found"
echo "GitHub Actions pinning validation passed"
