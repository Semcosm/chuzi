#!/usr/bin/env bash
set -euo pipefail

manifest="${1:-.ugs/policy.json}"
fail() { echo "supply-chain profile validation failed: $1" >&2; exit 1; }
[ -f "$manifest" ] || fail "manifest does not exist: $manifest"
command -v jq >/dev/null 2>&1 || fail "jq is required"
jq empty "$manifest" >/dev/null 2>&1 || fail "manifest is not valid JSON"
if ! jq -e 'has("supply_chain")' "$manifest" >/dev/null; then
  echo "supply-chain profile not declared"
  exit 0
fi
profile="$(jq -r '.supply_chain.profile // empty' "$manifest")"
case "$profile" in basic|standard|high-trust) ;; *) fail "invalid supply_chain.profile" ;; esac
for field in action_pinning sbom reproducible_builds release_attestations; do
  value="$(jq -r --arg field "$field" '.supply_chain[$field] // empty' "$manifest")"
  case "$field:$value" in
    action_pinning:none|action_pinning:declared|action_pinning:full_sha|sbom:none|sbom:declared|sbom:release|reproducible_builds:none|reproducible_builds:declared|reproducible_builds:verified|release_attestations:none|release_attestations:declared|release_attestations:signed) ;;
    *) fail "invalid supply_chain.$field" ;;
  esac
done
case "$profile" in
  basic) jq -e 'all([.supply_chain.action_pinning, .supply_chain.sbom, .supply_chain.reproducible_builds, .supply_chain.release_attestations][]; . != "none")' "$manifest" >/dev/null || fail "basic profile requires declared evidence" ;;
  standard) jq -e '.supply_chain.action_pinning == "full_sha" and .supply_chain.sbom == "release" and .supply_chain.reproducible_builds != "none" and .supply_chain.release_attestations == "signed"' "$manifest" >/dev/null || fail "standard profile requirements are incomplete" ;;
  high-trust) jq -e '.supply_chain.action_pinning == "full_sha" and .supply_chain.sbom == "release" and .supply_chain.reproducible_builds == "verified" and .supply_chain.release_attestations == "signed"' "$manifest" >/dev/null || fail "high-trust profile requirements are incomplete" ;;
esac
if [ "$profile" != "basic" ]; then
  jq -e '(.supply_chain.evidence | type == "object") and ((.supply_chain.evidence.sbom_paths // []) | length > 0) and ((.supply_chain.evidence.build_record_paths // []) | length > 0)' "$manifest" >/dev/null \
    || fail "$profile requires SBOM and build evidence paths"
fi
if [ "$profile" != "basic" ]; then
  jq -e '(.supply_chain.evidence.attestation_paths // []) | length > 0' "$manifest" >/dev/null \
    || fail "$profile requires an attestation evidence path"
fi
if jq -e '.supply_chain | has("evidence")' "$manifest" >/dev/null; then
  for field in sbom_paths attestation_paths build_record_paths; do
    jq -e --arg field "$field" '.supply_chain.evidence[$field] // [] | all(.[]; (type == "string") and ((startswith("/")) | not) and ((contains("..")) | not) and (length > 0))' "$manifest" >/dev/null \
      || fail "invalid supply-chain evidence paths: $field"
  done
fi
echo "supply-chain profile validation passed ($profile)"
