#!/usr/bin/env bash
set -euo pipefail

manifest="${1:-.ugs/policy.json}"

fail() {
  echo "policy manifest validation failed: $1" >&2
  exit 1
}

warn() {
  echo "policy manifest warning: $1" >&2
}

[ -f "$manifest" ] || fail "manifest does not exist: $manifest"
command -v jq >/dev/null 2>&1 || fail "jq is required"
jq empty "$manifest" >/dev/null 2>&1 || fail "manifest is not valid JSON"

require() {
  local expression="$1"
  local message="$2"
  jq -e "$expression" "$manifest" >/dev/null || fail "$message"
}

require 'type == "object"' "top level must be an object"
require '([keys[] | select(. != "$schema" and . != "format" and . != "schema_version" and . != "policy_version" and . != "conformance_level" and . != "migration" and . != "branching" and . != "commits" and . != "review" and . != "automation" and . != "releases" and . != "exceptions" and . != "quality" and . != "supply_chain" and . != "repository_shape" and . != "extensions")] | length) == 0' "unknown top-level field; use extensions.x-* for extensions"
require '(."$schema" == "schema/policy.schema.json")' 'unsupported $schema'
require '.format == "ugs-policy/v0.3"' "unsupported format"
require '.schema_version == 1' "unsupported schema_version"
require '.policy_version == "0.3"' "unsupported policy_version"
require '.conformance_level | IN("baseline", "standard", "high-trust")' "invalid conformance_level"
require '(.conformance_level == "baseline") or ((.branching.protected_refs | length > 0) and (.automation.required_checks | length > 0) and .review.test_evidence_required and .releases.annotated_tags_required and .releases.trusted_signatures_required)' "standard levels require protected refs, checks, test evidence, and signed annotated releases"
require '(.conformance_level != "high-trust") or (.commits.signing_level == "high-trust-commits-signed")' "high-trust requires trusted signed commits"
require '.migration.legacy_policy == "REPOSITORY_POLICY.md" and .migration.legacy_policy_mode == "warn"' "migration must retain REPOSITORY_POLICY.md in warn mode"
require '.branching.profile | IN("continuous", "release")' "invalid branching.profile"
require '.branching.merge_strategy | IN("rebase-ff", "merge", "squash")' "invalid branching.merge_strategy"
require '(.branching.protected_refs | type == "array" and length > 0 and all(.[]; type == "string" and length > 0))' "branching.protected_refs must be a non-empty string array"
require '(.branching.topic_prefixes | type == "array" and length > 0 and all(.[]; type == "string" and endswith("/")))' "branching.topic_prefixes must contain slash-terminated strings"
require '.commits.signing_level | IN("unsigned", "commits-signed", "high-trust-commits-signed")' "invalid commits.signing_level"
require '(.commits.core_types | type == "array" and length > 0 and length == (unique | length) and all(.[]; type == "string" and test("^[a-z]+$")))' "commits.core_types must be unique lowercase tokens"
require '(.commits.extended_types | type == "array" and length == (unique | length) and all(.[]; type == "string" and test("^[a-z][a-z0-9-]*$")))' "commits.extended_types must be unique tokens"
require '.commits.trailers_required | type == "boolean"' "commits.trailers_required must be boolean"
require '.review.model | IN("change-level", "commit-level")' "invalid review.model"
require '(.review.human_review.maintainer_authored | type == "boolean") and (.review.human_review.external_contributions | type == "boolean") and (.review.test_evidence_required | type == "boolean") and (.review.maintainer_ack_sensitive_paths | type == "boolean")' "review boolean declarations are invalid"
require '.review.conclusion_storage | IN("trailers", "cr-record")' "invalid review.conclusion_storage"
require '(.automation.required_checks | type == "array" and length > 0 and unique == . and all(.[]; type == "string" and length > 0))' "automation.required_checks must be a non-empty unique string array"
require '.automation.hooks_path == ".githooks"' "unsupported automation.hooks_path"
require '.releases.versioning == "semver" and .releases.tag_pattern == "v<major>.<minor>.<patch>" and .releases.notes_path == "releases/"' "unsupported release declaration"
require '(.releases.annotated_tags_required | type == "boolean") and (.releases.trusted_signatures_required | type == "boolean")' "release requirements must be boolean"
require '(.exceptions.emergency_direct_push | type == "boolean") and (.exceptions.post_event_review_required | type == "boolean")' "exception capabilities must be boolean"
if jq -e 'has("quality")' "$manifest" >/dev/null; then
  require '.quality | type == "object" and (.profile | IN("basic", "standard"))' "invalid quality.profile"
  require '(.quality.required_documents | type == "array" and length == (unique | length) and all(.[]; type == "string" and length > 0))' "quality.required_documents must be unique non-empty strings"
  require '(.quality.test_entrypoints | type == "array" and length > 0 and length == (unique | length) and all(.[]; type == "string" and length > 0))' "quality.test_entrypoints must be unique non-empty strings"
  require '(.quality.profile != "standard") or ((.quality.required_documents | index("LICENSE") != null) and (.quality.required_documents | index("SECURITY.md") != null) and (.quality.required_documents | index("CODE_OF_CONDUCT.md") != null) and (.quality.required_documents | index("SUPPORT.md") != null))' "standard quality profile requires license, security, conduct, and support documents"
  while IFS= read -r path; do
    [ -f "$path" ] || fail "quality required document does not exist: $path"
  done < <(jq -r '.quality.required_documents[]' "$manifest")
  while IFS= read -r path; do
    [ -x "$path" ] || fail "quality test entrypoint must be executable: $path"
  done < <(jq -r '.quality.test_entrypoints[]' "$manifest")
fi
if jq -e 'has("supply_chain")' "$manifest" >/dev/null; then
  require '.supply_chain | type == "object" and (.profile | IN("basic", "standard", "high-trust"))' "invalid supply_chain.profile"
  for field in action_pinning sbom reproducible_builds release_attestations; do
    case "$field" in
      action_pinning) values='"none", "declared", "full_sha"' ;;
      sbom) values='"none", "declared", "release"' ;;
      reproducible_builds) values='"none", "declared", "verified"' ;;
      release_attestations) values='"none", "declared", "signed"' ;;
    esac
    require ".supply_chain.${field} | IN(${values})" "invalid supply_chain.${field}"
  done
  require '(.supply_chain.profile != "basic") or (all([.supply_chain.action_pinning, .supply_chain.sbom, .supply_chain.reproducible_builds, .supply_chain.release_attestations][]; . != "none"))' "basic supply-chain profile requires declared evidence"
  require '(.supply_chain.profile != "standard") or (.supply_chain.action_pinning == "full_sha" and .supply_chain.sbom == "release" and .supply_chain.reproducible_builds != "none" and .supply_chain.release_attestations == "signed")' "standard supply-chain profile requires pinned actions, release SBOM, build evidence, and signed attestations"
  require '(.supply_chain.profile != "high-trust") or (.supply_chain.action_pinning == "full_sha" and .supply_chain.sbom == "release" and .supply_chain.reproducible_builds == "verified" and .supply_chain.release_attestations == "signed")' "high-trust supply-chain profile requires verified builds"
  require '(.supply_chain.profile == "basic") or ((.supply_chain.evidence | type == "object") and ((.supply_chain.evidence.sbom_paths // []) | length > 0) and ((.supply_chain.evidence.build_record_paths // []) | length > 0) and ((.supply_chain.evidence.attestation_paths // []) | length > 0))' "standard and high-trust supply-chain profiles require SBOM, build, and attestation evidence paths"
fi
if jq -e 'has("repository_shape")' "$manifest" >/dev/null; then
  require '.repository_shape | type == "object" and (.model | IN("single", "monorepo")) and (.submodules | IN("none", "allowed", "required")) and (.generated_files | IN("none", "tracked", "regenerated")) and (.large_files | IN("normal", "declared", "lfs"))' "invalid repository_shape declaration"
fi
require '(.extensions | type == "object" and all(keys[]; startswith("x-")))' "extensions keys must begin with x-"

if [ -f REPOSITORY_POLICY.md ]; then
  compare_legacy() {
    local legacy="$1"
    local expression="$2"
    if grep -Fq "$legacy" REPOSITORY_POLICY.md && ! jq -e "$expression" "$manifest" >/dev/null; then
      warn "manifest conflicts with REPOSITORY_POLICY.md: $legacy"
    fi
  }
  compare_legacy 'UGS Profile: continuous' '.branching.profile == "continuous"'
  compare_legacy 'Merge Strategy: rebase-ff' '.branching.merge_strategy == "rebase-ff"'
  compare_legacy 'Versioning: semver' '.releases.versioning == "semver"'
  compare_legacy 'Signing Level: high-trust-commits-signed' '.commits.signing_level == "high-trust-commits-signed"'
  compare_legacy 'Protected Long-Lived Branches: main' '.branching.protected_refs | index("main") != null'
  compare_legacy 'Hooks Path: .githooks' '.automation.hooks_path == ".githooks"'
fi
