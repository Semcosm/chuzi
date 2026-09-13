#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
temp_dir="$(mktemp -d)"
trap 'rm -rf "$temp_dir"' EXIT
key="$temp_dir/release-key"
principal="release-test@example.invalid"
printf 'release fixture\n' > "$temp_dir/artifact.tar.gz"
ssh-keygen -q -t ed25519 -N '' -f "$key" -C "$principal" >/dev/null
printf '%s namespaces="git,ugs-attestation" %s\n' "$principal" "$(cat "$key.pub")" > "$temp_dir/allowed_signers"
CHUZI_RELEASE_REPOSITORY="fixture/repo" CHUZI_BUILT_AT="2026-09-13T00:00:00Z" \
  "$repo_root/scripts/generate_release_attestation.sh" v1.2.3 0123456789012345678901234567890123456789 \
  "$temp_dir/artifact.tar.gz" fixture-builder "$temp_dir/unsigned.json"
CHUZI_RELEASE_REPOSITORY="fixture/repo" "$repo_root/scripts/sign_release_attestation.sh" \
  "$temp_dir/unsigned.json" "$key" "$principal" "$temp_dir/signed.json"
UGS_ALLOWED_SIGNERS_FILE="$temp_dir/allowed_signers" \
  "$repo_root/scripts/validate_release_attestation.sh" "$temp_dir/signed.json" v1.2.3 \
  0123456789012345678901234567890123456789 true fixture/repo
if "$repo_root/scripts/validate_release_attestation.sh" "$temp_dir/unsigned.json" v1.2.3 \
  0123456789012345678901234567890123456789 true fixture/repo >/dev/null 2>&1; then
  echo "unsigned attestation unexpectedly passed signature validation" >&2
  exit 1
fi
echo "release signing test passed"
