#!/usr/bin/env bash
set -euo pipefail

manifest="${1:-.ugs/policy.json}"
fail() { echo "supply-chain evidence validation failed: $1" >&2; exit 1; }
[ -f "$manifest" ] || fail "manifest does not exist: $manifest"
command -v jq >/dev/null 2>&1 || fail "jq is required"
profile="$(jq -r '.supply_chain.profile // "none"' "$manifest")"
if ! jq -e '.supply_chain? // {} | has("evidence")' "$manifest" >/dev/null; then
  [ "$profile" = "basic" ] || [ "$profile" = "none" ] || fail "$profile requires supply-chain evidence"
  echo "supply-chain evidence not declared"
  exit 0
fi
for field in sbom_paths attestation_paths build_record_paths; do
  while IFS= read -r path; do
    case "$path" in
      /*|*..*) fail "evidence path must be repository-relative: $path" ;;
    esac
    [ -f "$path" ] || fail "evidence file does not exist: $path"
  done < <(jq -r --arg field "$field" '.supply_chain.evidence[$field] // [] | .[]' "$manifest")
done
if [ "$profile" != "basic" ]; then
  [ "$(jq '.supply_chain.evidence.sbom_paths // [] | length' "$manifest")" -gt 0 ] || fail "$profile requires an SBOM evidence path"
  [ "$(jq '.supply_chain.evidence.build_record_paths // [] | length' "$manifest")" -gt 0 ] || fail "$profile requires a build record evidence path"
fi
if [ "$profile" = "high-trust" ]; then
  [ "$(jq '.supply_chain.evidence.attestation_paths // [] | length' "$manifest")" -gt 0 ] || fail "high-trust requires an attestation evidence path"
fi
echo "supply-chain evidence validation passed ($profile)"
