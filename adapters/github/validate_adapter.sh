#!/usr/bin/env bash
set -euo pipefail
repo_root="$(git rev-parse --show-toplevel)"
fail() { echo "GitHub adapter validation failed: $1" >&2; exit 1; }
for file in .github/pull_request_template.md .github/CODEOWNERS .github/workflows/ugs-validate.yml; do
  [ -f "$repo_root/$file" ] || fail "missing GitHub adapter file: $file"
done
grep -Fq "## Summary" "$repo_root/.github/pull_request_template.md" || fail "PR template is incomplete"
grep -Fq "scripts/validate_commit_signatures.sh" "$repo_root/.github/workflows/ugs-validate.yml" || fail "workflow misses signature validation"
"$repo_root/adapters/github/validate_action_pinning.sh" .ugs/policy.json .github/workflows
echo "GitHub adapter validation passed"
