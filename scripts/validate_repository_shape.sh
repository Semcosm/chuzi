#!/usr/bin/env bash
set -euo pipefail

manifest="${1:-.ugs/policy.json}"
fail() { echo "repository shape validation failed: $1" >&2; exit 1; }
[ -f "$manifest" ] || fail "manifest does not exist: $manifest"
command -v jq >/dev/null 2>&1 || fail "jq is required"
jq empty "$manifest" >/dev/null 2>&1 || fail "manifest is not valid JSON"
if ! jq -e 'has("repository_shape")' "$manifest" >/dev/null; then
  echo "repository shape not declared"
  exit 0
fi
jq -e '.repository_shape | type == "object" and (.model | IN("single", "monorepo")) and (.submodules | IN("none", "allowed", "required")) and (.generated_files | IN("none", "tracked", "regenerated")) and (.large_files | IN("normal", "declared", "lfs"))' "$manifest" >/dev/null \
  || fail "invalid repository_shape declaration"
echo "repository shape validation passed"
