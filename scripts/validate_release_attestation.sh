#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -lt 1 ] || [ "$#" -gt 5 ]; then
  echo "usage: $0 <attestation.json> [release-tag] [commit] [require-signature] [repository]" >&2
  exit 2
fi
attestation="$1"
expected_tag="${2:-}"
expected_commit="${3:-}"
require_signature="${4:-false}"
expected_repository="${5:-}"
fail() { echo "release attestation validation failed: $1" >&2; exit 1; }
[ -f "$attestation" ] || fail "file does not exist: $attestation"
command -v jq >/dev/null 2>&1 || fail "jq is required"
jq empty "$attestation" >/dev/null 2>&1 || fail "file is not valid JSON"
jq -e '.schema_version == 1 and .type == "ugs-release-attestation" and (.repository | type == "string" and length > 0)' "$attestation" >/dev/null || fail "invalid attestation header"
if [ -n "$expected_repository" ]; then
  [ "$(jq -r '.repository' "$attestation")" = "$expected_repository" ] || fail "attestation repository does not match current repository"
fi
jq -e '.release_tag | type == "string" and test("^v[0-9]+\\.[0-9]+\\.[0-9]+$")' "$attestation" >/dev/null || fail "invalid release tag"
jq -e '.commit | type == "string" and test("^[0-9a-f]{40}$")' "$attestation" >/dev/null || fail "invalid commit SHA"
jq -e '.artifact.name | type == "string" and length > 0' "$attestation" >/dev/null || fail "artifact name is missing"
jq -e '.artifact.digest | type == "string" and test("^sha256:[0-9a-f]{64}$")' "$attestation" >/dev/null || fail "invalid artifact digest"
jq -e '.builder.id | type == "string" and length > 0' "$attestation" >/dev/null || fail "builder identity is missing"
jq -e '.built_at | type == "string" and test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T")' "$attestation" >/dev/null || fail "build timestamp is missing"
tag="$(jq -r '.release_tag' "$attestation")"
commit="$(jq -r '.commit' "$attestation")"
[ -z "$expected_tag" ] || [ "$tag" = "$expected_tag" ] || fail "release tag does not match $expected_tag"
[ -z "$expected_commit" ] || [ "$commit" = "$expected_commit" ] || fail "commit does not match $expected_commit"
if [ -n "$expected_tag" ] && git rev-parse --verify --quiet "refs/tags/$expected_tag^{commit}" >/dev/null; then
  [ "$(git rev-parse "refs/tags/$expected_tag^{commit}")" = "$commit" ] || fail "attestation commit differs from release tag target"
fi
if [ "$require_signature" = "true" ] || jq -e '.signature != null' "$attestation" >/dev/null; then
  jq -e '.signature.format == "ssh" and .signature.namespace == "ugs-attestation" and (.signature.principal | type == "string" and contains("@")) and (.signature.value | type == "string" and length > 0)' "$attestation" >/dev/null || fail "invalid SSH attestation signature metadata"
  command -v ssh-keygen >/dev/null 2>&1 || fail "ssh-keygen is required for signed attestations"
  temp_dir="$(mktemp -d)"
  trap 'rm -rf "$temp_dir"' EXIT
  jq -cS 'del(.signature)' "$attestation" > "$temp_dir/payload"
  printf '%s' "$(jq -r '.signature.value' "$attestation")" | base64 --decode > "$temp_dir/signature" 2>/dev/null || fail "attestation signature is not base64"
  allowed_signers_file="${UGS_ALLOWED_SIGNERS_FILE:-keys/allowed_signers}"
  [ -f "$allowed_signers_file" ] || fail "allowed signers file does not exist"
  ssh-keygen -Y verify -f "$allowed_signers_file" -I "$(jq -r '.signature.principal' "$attestation")" -n ugs-attestation -s "$temp_dir/signature" < "$temp_dir/payload" >/dev/null 2>&1 || fail "SSH attestation signature verification failed"
fi
echo "release attestation validation passed"
