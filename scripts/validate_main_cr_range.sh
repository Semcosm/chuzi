#!/usr/bin/env bash
set -euo pipefail
if [ "$#" -ne 2 ]; then echo "usage: $0 <old-sha> <new-sha>" >&2; exit 2; fi
repo_root="${UGS_REPOSITORY_ROOT:-}"
if [ -z "$repo_root" ]; then
  repo_root="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
fi
old="$1"; new="$2"
fail() { echo "main CR range validation failed: $1" >&2; exit 1; }
old_oid="$(git rev-parse --verify "$old^{commit}" 2>/dev/null)" || fail "invalid old SHA"
new_oid="$(git rev-parse --verify "$new^{commit}" 2>/dev/null)" || fail "invalid new SHA"
git merge-base --is-ancestor "$old_oid" "$new_oid" \
  || fail "old SHA is not an ancestor of new SHA"
mapfile -t records < <(git diff --name-only "$old_oid" "$new_oid" -- 'cr/CR-*.md')
[ "${#records[@]}" -gt 0 ] || fail "main integration range contains no persisted CR"
tree_diff_digest() {
  git diff --no-ext-diff --no-textconv --binary --full-index --no-renames "$1" "$2" \
    -- . ':(exclude)cr/CR-*.md' | sha256sum | awk '{print $1}'
}
literal_count=0
rewritten_count=0
closure_count=0
for record in "${records[@]}"; do
  record_file="$(mktemp)"
  model_file="$(mktemp)"
  trap 'rm -f "$record_file" "$model_file"' EXIT
  git show "$new_oid:$record" > "$record_file" || fail "cannot read persisted CR from new commit: $record"
  "$repo_root/scripts/validate_cr_record.sh" "$record_file" >/dev/null
  "$repo_root/scripts/cr_model.py" --json "$record_file" > "$model_file"
  record_base="$(jq -r '.source.base_oid' "$model_file")"
  record_head="$(jq -r '.source.head_oid' "$model_file")"
  record_status="$(jq -r '.status' "$model_file")"
  record_revision="$(jq -r '.revision' "$model_file")"
  record_target="$(jq -r '.integration.target_ref' "$model_file")"
  strategy="$(jq -r '.integration.strategy // empty' "$model_file")"
  result_oid="$(jq -r '.integration.result_oid // empty' "$model_file")"

  if git merge-base --is-ancestor "$record_head" "$new_oid"; then
    literal_count=$((literal_count + 1))
    rm -f "$record_file" "$model_file"
    trap - EXIT
    continue
  fi

  if [ -n "$result_oid" ]; then
    [ "$strategy" = "rebase-ff" ] \
      || fail "$record closed rewritten rebase must use integration strategy rebase-ff"
    [ "$record_target" = "main" ] \
      || fail "$record closed rewritten rebase must target main"
    [ "${#records[@]}" -eq 1 ] \
      || fail "rewritten rebase closure contains unrelated persisted CR records"
    git diff --quiet --no-ext-diff --no-textconv --binary --full-index --no-renames \
      "$old_oid" "$new_oid" -- . ':(exclude)cr/CR-*.md' \
      || fail "$record closure commit must contain only CR metadata changes"
    git merge-base --is-ancestor "$result_oid" "$old_oid" \
      || fail "$record closed rewritten result must be reachable before the closure commit"

    previous_record_file="$(mktemp)"
    previous_model_file="$(mktemp)"
    if ! git show "$old_oid:$record" > "$previous_record_file"; then
      rm -f "$previous_record_file" "$previous_model_file"
      trap - EXIT
      fail "$record closure has no previous pending CR record"
    fi
    "$repo_root/scripts/validate_cr_record.sh" "$previous_record_file" >/dev/null \
      || fail "$record closure previous CR record is invalid"
    "$repo_root/scripts/cr_model.py" --json "$previous_record_file" > "$previous_model_file"
    previous_base="$(jq -r '.source.base_oid' "$previous_model_file")"
    previous_head="$(jq -r '.source.head_oid' "$previous_model_file")"
    previous_status="$(jq -r '.status' "$previous_model_file")"
    previous_result="$(jq -r '.integration.result_oid // empty' "$previous_model_file")"
    previous_revision="$(jq -r '.revision' "$previous_model_file")"
    [ "$previous_base" = "$record_base" ] \
      || fail "$record closure changed Base OID"
    [ "$previous_head" = "$record_head" ] \
      || fail "$record closure changed Head OID"
    [ "$previous_status" = "pending" ] || [ "$previous_status" = "accepted" ] \
      || fail "$record closure previous CR must be pending or accepted"
    [ -z "$previous_result" ] \
      || fail "$record closure previous CR already has an integrated result"
    [ "$record_status" = "integrated" ] \
      || fail "$record closure must set Status: integrated"
    [ "$record_revision" -gt "$previous_revision" ] \
      || fail "$record closure must advance CR Revision"
    rm -f "$previous_record_file" "$previous_model_file"
    trap - EXIT
    closure_count=$((closure_count + 1))
    continue
  fi

  [ "$strategy" = "rebase-ff" ] \
    || fail "$record Head OID is not reachable from new main and integration strategy is not rebase-ff"
  [ -z "$result_oid" ] \
    || fail "$record rewritten rebase validation requires Integrated Result: pending"
  [ "$record_base" = "$old_oid" ] \
    || fail "$record rewritten rebase Base OID does not equal old main"
  [ "${#records[@]}" -eq 1 ] \
    || fail "rewritten rebase range contains unrelated persisted CR records"
  git merge-base --is-ancestor "$record_base" "$record_head" \
    || fail "$record rewritten rebase Head OID does not descend from Base OID"

  source_digest="$(tree_diff_digest "$record_base" "$record_head")"
  result_digest="$(tree_diff_digest "$old_oid" "$new_oid")"
  [ "$source_digest" = "$result_digest" ] \
    || fail "$record rewritten rebase tree diff does not match old main..new main"
  rewritten_count=$((rewritten_count + 1))
  rm -f "$record_file" "$model_file"
  trap - EXIT
done
echo "main CR range validation passed (${#records[@]} record(s); $literal_count literal fast-forward, $rewritten_count rewritten rebase, $closure_count rewritten closure)"
