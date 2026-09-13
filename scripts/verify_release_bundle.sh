#!/usr/bin/env bash
set -euo pipefail
if [ "$#" -lt 3 ] || [ "$#" -gt 5 ]; then echo "usage: $0 <release-tag> <commit-sha> <dist-dir> [attestation-dir] [repository]" >&2; exit 2; fi
tag="$1"; commit="$2"; dist_dir="$3"; attestation_dir="${4:-$dist_dir/attestations}"; repository="${5:-${CHUZI_RELEASE_REPOSITORY:-Semcosm/chuzi}}"; repo_root="$(git rev-parse --show-toplevel)"
fail() { echo "release bundle verification failed: $1" >&2; exit 1; }
[ -d "$dist_dir" ] || fail "distribution directory does not exist: $dist_dir"; [ -d "$attestation_dir" ] || fail "attestation directory does not exist: $attestation_dir"; command -v jq >/dev/null 2>&1 || fail "jq is required"
"$repo_root/scripts/validate_release_tag.sh" "$tag"
count=0
while IFS= read -r -d '' attestation; do
  count=$((count + 1)); "$repo_root/scripts/validate_release_attestation.sh" "$attestation" "$tag" "$commit" true "$repository"
  name="$(jq -r '.artifact.name' "$attestation")"; expected="$(jq -r '.artifact.digest' "$attestation")"; artifact="$dist_dir/$name"; [ -f "$artifact" ] || fail "artifact referenced by attestation is missing: $name"
  if command -v sha256sum >/dev/null 2>&1; then actual="sha256:$(sha256sum "$artifact" | awk '{print $1}')"; elif command -v shasum >/dev/null 2>&1; then actual="sha256:$(shasum -a 256 "$artifact" | awk '{print $1}')"; else fail "no SHA256 utility is available"; fi
  [ "$actual" = "$expected" ] || fail "artifact digest mismatch: $name"; echo "verified $name"
done < <(find "$attestation_dir" -maxdepth 1 -type f -name '*.json' -print0 | sort -z)
[ "$count" -gt 0 ] || fail "no attestation files found"; echo "release bundle verification passed ($count artifact(s))"
