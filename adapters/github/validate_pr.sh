#!/usr/bin/env bash
set -euo pipefail
if [ "$#" -ne 3 ]; then echo "usage: $0 <base-sha> <head-sha> <github-event-json>" >&2; exit 2; fi
base="$1"; head="$2"; event="$3"
repo_root="$(git rev-parse --show-toplevel)"
fail() { echo "GitHub PR adapter validation failed: $1" >&2; exit 1; }
[ -f "$event" ] || fail "GitHub event file does not exist"
command -v jq >/dev/null 2>&1 || fail "jq is required"
body_file="$(mktemp)"; trap 'rm -f "$body_file"' EXIT
jq -r '.pull_request.body // empty' "$event" > "$body_file"
[ -s "$body_file" ] || fail "PR body is empty"
while [ -s "$body_file" ] && [ -z "$(tail -n 1 "$body_file")" ]; do
  sed -i '$d' "$body_file"
done
mapfile -t records < <(git diff --name-only "$base..$head" -- 'cr/CR-*.md')
[ "${#records[@]}" -gt 0 ] || fail "PR must add or modify a persisted CR"
for record in "${records[@]}"; do
  "$repo_root/scripts/validate_cr_review.sh" "$base" "$head" "$record" "$body_file"
done
echo "GitHub PR adapter validation passed (${#records[@]} record(s))"
