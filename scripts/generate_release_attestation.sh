#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -lt 4 ] || [ "$#" -gt 5 ]; then
  echo "usage: $0 <release-tag> <commit-sha> <artifact> <builder-id> [output]" >&2
  exit 2
fi
tag="$1"; commit="$2"; artifact="$3"; builder="$4"; output="${5:-}"
fail() { echo "release attestation generation failed: $1" >&2; exit 1; }
printf '%s\n' "$tag" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' || fail "tag must match v<major>.<minor>.<patch>"
printf '%s\n' "$commit" | grep -Eq '^[0-9a-f]{40}$' || fail "commit must be a 40-character lowercase SHA"
[ -f "$artifact" ] || fail "artifact does not exist: $artifact"
[ -n "${builder//[[:space:]]/}" ] || fail "builder id is required"
command -v jq >/dev/null 2>&1 || fail "jq is required"
if command -v sha256sum >/dev/null 2>&1; then
  digest="$(sha256sum "$artifact" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  digest="$(shasum -a 256 "$artifact" | awk '{print $1}')"
else
  fail "no SHA256 utility is available"
fi
artifact_name="$(basename "$artifact")"
built_at="${CHUZI_BUILT_AT:-$(date -u '+%Y-%m-%dT%H:%M:%SZ')}"
printf '%s\n' "$built_at" | grep -Eq '^[0-9]{4}-[0-9]{2}-[0-9]{2}T' || fail "built-at must be an RFC3339 timestamp"
json="$(jq -nS --arg tag "$tag" --arg commit "$commit" --arg name "$artifact_name" --arg digest "sha256:$digest" --arg builder "$builder" --arg built_at "$built_at" '{schema_version:1,type:"ugs-release-attestation",repository:(env.CHUZI_RELEASE_REPOSITORY // "Semcosm/chuzi"),release_tag:$tag,commit:$commit,artifact:{name:$name,digest:$digest},builder:{id:$builder},built_at:$built_at}')"
if [ -n "$output" ]; then umask 077; printf '%s\n' "$json" > "$output"; else printf '%s\n' "$json"; fi
