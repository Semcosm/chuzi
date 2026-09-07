#!/usr/bin/env bash
set -euo pipefail

manifest="${1:-.ugs/policy.json}"
fail() { echo "quality profile validation failed: $1" >&2; exit 1; }
[ -f "$manifest" ] || fail "manifest does not exist: $manifest"
command -v jq >/dev/null 2>&1 || fail "jq is required"
jq empty "$manifest" >/dev/null 2>&1 || fail "manifest is not valid JSON"
if ! jq -e 'has("quality")' "$manifest" >/dev/null; then
  echo "quality profile not declared"
  exit 0
fi
profile="$(jq -r '.quality.profile // empty' "$manifest")"
case "$profile" in basic|standard) ;; *) fail "invalid quality.profile" ;; esac
while IFS= read -r path; do
  [ -f "$path" ] || fail "required document does not exist: $path"
done < <(jq -r '.quality.required_documents[]' "$manifest")
while IFS= read -r path; do
  [ -x "$path" ] || fail "test entrypoint is not executable: $path"
done < <(jq -r '.quality.test_entrypoints[]' "$manifest")
if [ "$profile" = "standard" ]; then
  for path in LICENSE SECURITY.md CODE_OF_CONDUCT.md SUPPORT.md; do
    jq -e --arg path "$path" '.quality.required_documents | index($path) != null' "$manifest" >/dev/null \
      || fail "standard profile must declare $path"
  done
fi
echo "quality profile validation passed ($profile)"
