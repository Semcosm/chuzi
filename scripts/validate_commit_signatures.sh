#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: $0 <commit-or-rev-range>" >&2
  exit 2
fi

repo_root="$(git rev-parse --show-toplevel)"
spec="$1"

fail() {
  echo "commit signature validation failed: $1" >&2
  exit 1
}

validate_commit() {
  local commit="$1"

  git rev-parse --quiet --verify "$commit^{commit}" >/dev/null 2>&1 \
    || fail "invalid commit: $commit"

  if ! git cat-file commit "$commit" | grep -q '^gpgsig '; then
    validate_rebased_commit "$commit" \
      || fail "commit is not signed: $commit"
    return 0
  fi

  git -c gpg.format=ssh \
    -c gpg.ssh.allowedSignersFile="$repo_root/keys/allowed_signers" \
    -c gpg.ssh.revocationFile="$repo_root/keys/revoked_signers" \
    verify-commit "$commit" >/dev/null 2>&1 \
    || fail "commit signature is not trusted: $commit"
}

validate_rebased_commit() {
  local target="$1"
  local target_tree target_parents target_author target_message candidate
  local -a target_parent_list candidate_parent_list
  target_tree="$(git show -s --format='%T' "$target")"
  target_parents="$(git show -s --format='%P' "$target")"
  read -r -a target_parent_list <<< "$target_parents"
  target_author="$(git show -s --format='%an%x09%ae%x09%ad' --date=raw "$target")"
  target_message="$(git cat-file commit "$target" | sed '1,/^$/d')"

  while IFS= read -r candidate; do
    [ "$candidate" = "$target" ] && continue
    [ "$(git show -s --format='%T' "$candidate")" = "$target_tree" ] || continue
    candidate_parent_list=()
    read -r -a candidate_parent_list <<< "$(git show -s --format='%P' "$candidate")"
    [ "${#candidate_parent_list[@]}" -eq "${#target_parent_list[@]}" ] || continue
    for parent_index in "${!target_parent_list[@]}"; do
      [ "$(git show -s --format='%T' "${candidate_parent_list[$parent_index]}")" = "$(git show -s --format='%T' "${target_parent_list[$parent_index]}")" ] || continue 2
    done
    [ "$(git show -s --format='%an%x09%ae%x09%ad' --date=raw "$candidate")" = "$target_author" ] || continue
    [ "$(git cat-file commit "$candidate" | sed '1,/^$/d')" = "$target_message" ] || continue
    git cat-file commit "$candidate" | grep -q '^gpgsig ' || continue
    if git -c gpg.format=ssh \
      -c gpg.ssh.allowedSignersFile="$repo_root/keys/allowed_signers" \
      -c gpg.ssh.revocationFile="$repo_root/keys/revoked_signers" \
      verify-commit "$candidate" >/dev/null 2>&1; then
      echo "commit signature validation passed via signed source commit: $candidate" >&2
      return 0
    fi
  done < <(git rev-list --all)
  return 1
}

if [[ "$spec" == *".."* ]]; then
  git rev-list "$spec" >/dev/null
  while IFS= read -r commit; do
    validate_commit "$commit"
  done < <(git rev-list --reverse "$spec")
else
  validate_commit "$spec"
fi
