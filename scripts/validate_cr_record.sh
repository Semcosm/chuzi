#!/usr/bin/env bash
set -euo pipefail

script_dir="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
. "$script_dir/ugs_errors.sh"

if [ "$#" -ne 1 ]; then
  echo "usage: $0 <cr-record-file>" >&2
  exit 2
fi

cr_file="$1"
model_file="$(mktemp)"
parser_error="$(mktemp)"
trap 'rm -f "$model_file" "$parser_error"' EXIT

fail() {
  local message="$1"
  local code="UGS-CR-999"
  case "$message" in
    "record file does not exist"*) code="UGS-CR-001" ;;
    "CR object ID is not a commit"*) code="UGS-CR-008" ;;
    "integrated CRs must have"*) code="UGS-CR-009" ;;
    "Integrated Result must match"*) code="UGS-CR-010" ;;
    "integrated result is not a commit"*) code="UGS-CR-011" ;;
    "integrated result is not reachable"*) code="UGS-CR-012" ;;
    "Base OID must be an ancestor"*) code="UGS-CR-013" ;;
  esac
  ugs_fail "$code" "$message"
}

[ -f "$cr_file" ] || fail "record file does not exist: $cr_file"
if ! "$script_dir/cr_model.py" --json "$cr_file" >"$model_file" 2>"$parser_error"; then
  parser_code="$(jq -r '.code // empty' "$parser_error" 2>/dev/null || true)"
  parser_message="$(jq -r '.message // empty' "$parser_error" 2>/dev/null || true)"
  if [ -n "$parser_code" ] && [ -n "$parser_message" ]; then
    ugs_fail "$parser_code" "$parser_message"
  fi
  parser_output="$(cat "$parser_error")"
  if [[ "$parser_output" =~ ^(UGS-[A-Z0-9-]+):[[:space:]](.*)$ ]]; then
    ugs_fail "${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}"
  fi
  fail "$parser_output"
fi

base_oid="$(jq -r '.source.base_oid' "$model_file")"
head_oid="$(jq -r '.source.head_oid' "$model_file")"
status="$(jq -r '.status' "$model_file")"
strategy="$(jq -r '.integration.strategy // empty' "$model_file")"
review_evidence="$(jq -r '.integration.review_evidence // empty' "$model_file")"
target_ref="$(jq -r '.integration.target_ref' "$model_file")"
result_oid="$(jq -r '.integration.result_oid // empty' "$model_file")"

for oid in "$base_oid" "$head_oid"; do
  git cat-file -e "$oid^{commit}" 2>/dev/null \
    || fail "CR object ID is not a commit in this repository: $oid"
done

if [ -z "$result_oid" ]; then
  [ "$status" != "integrated" ] \
    || fail "integrated CRs must have a <target-ref>@<full commit OID> result"
else
  git cat-file -e "$result_oid^{commit}" 2>/dev/null \
    || fail "integrated result is not a commit in this repository: $result_oid"
  target_object=""
  for candidate in "refs/heads/$target_ref" "refs/remotes/origin/$target_ref"; do
    if git rev-parse --quiet --verify "$candidate^{commit}" >/dev/null 2>&1; then
      target_object="$candidate"
      break
    fi
  done
  if [ -z "$target_object" ] && [ "$target_ref" = "main" ] && git rev-parse --quiet --verify HEAD^{commit} >/dev/null 2>&1; then
    target_object="HEAD"
  fi
  [ -n "$target_object" ] \
    || fail "validation history is required to verify integrated provenance"
  git merge-base --is-ancestor "$result_oid" "$target_object" \
    || fail "integrated result is not reachable from $target_ref"
  git merge-base --is-ancestor "$base_oid" "$result_oid" \
    || fail "integrated result must descend from Base OID"

  case "$strategy" in
    "") ;;
    rebase-ff)
      if [ "$result_oid" != "$head_oid" ]; then
        source_digest="$(git diff --no-ext-diff --no-textconv --binary --full-index --no-renames \
          "$base_oid" "$head_oid" -- . ':(exclude)cr/CR-*.md' | sha256sum | awk '{print $1}')"
        result_digest="$(git diff --no-ext-diff --no-textconv --binary --full-index --no-renames \
          "$base_oid" "$result_oid" -- . ':(exclude)cr/CR-*.md' | sha256sum | awk '{print $1}')"
        [ "$source_digest" = "$result_digest" ] \
          || fail "Integrated Result must match Head OID or be patch-equivalent to Head OID for rebase-ff"
      fi
      ;;
    merge)
      [ "$(git cat-file commit "$result_oid" | sed -n '/^$/q; /^parent /p' | wc -l)" -ge 2 ] \
        || fail "merge integration must produce a merge commit"
      git merge-base --is-ancestor "$head_oid" "$result_oid" \
        || fail "merge result must contain Head OID"
      ;;
    squash)
      [ "$result_oid" != "$head_oid" ] \
        || fail "squash integrated result must differ from Head OID"
      if git merge-base --is-ancestor "$head_oid" "$result_oid"; then
        fail "squash result must not contain Head OID as an ancestor"
      fi
      ;;
    *) fail "Integration Strategy is invalid" ;;
  esac

  if [ "$review_evidence" = "trailers" ]; then
    git cat-file commit "$result_oid" | grep -Eq '^Reviewed-by: .+$' \
      || fail "integrated result must carry Reviewed-by trailer"
    git cat-file commit "$result_oid" | grep -Eq '^Tested-by: .+$' \
      || fail "integrated result must carry Tested-by trailer"
  fi
fi

git merge-base --is-ancestor "$base_oid" "$head_oid" \
  || fail "Base OID must be an ancestor of Head OID"
