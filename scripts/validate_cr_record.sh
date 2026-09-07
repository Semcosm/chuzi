#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: $0 <cr-record-file>" >&2
  exit 2
fi

cr_file="$1"

fail() {
  echo "cr record validation failed: $1" >&2
  exit 1
}

require_metadata_line() {
  local key="$1"
  local value

  value="$(sed -n "s/^${key}: //p" "$cr_file")"
  [ -n "$value" ] || fail "missing ${key}: line"

  if printf '%s\n' "$value" | grep -Eq '^<[^>]+>$'; then
    fail "${key}: must not use an unfilled template placeholder"
  fi
}

require_section() {
  local header="$1"
  local status

  if awk -v header="$header" '
    $0 == header {
      found = 1
      in_section = 1
      next
    }

    /^## / && in_section {
      exit has_content ? 0 : 2
    }

    in_section && NF {
      if ($0 !~ /^<[^>]+>$/) {
        has_content = 1
      }
    }

    END {
      if (!found) {
        exit 1
      }

      if (!has_content) {
        exit 2
      }
    }
  ' "$cr_file"; then
    return 0
  else
    status=$?
  fi

  case "$status" in
    1) fail "missing section: ${header}" ;;
    2) fail "section must include non-placeholder content: ${header}" ;;
    *) fail "unable to validate section: ${header}" ;;
  esac
}

[ -f "$cr_file" ] || fail "record file does not exist: $cr_file"

title_line="$(sed -n '1p' "$cr_file")"
printf '%s\n' "$title_line" | grep -Eq '^# CR-[0-9]{4}: .+$' \
  || fail "title must match # CR-XXXX: <title>"

require_metadata_line "Base"
require_metadata_line "Head or Range"
require_metadata_line "Title"
require_metadata_line "Revision"
require_metadata_line "Status"
require_metadata_line "Decision"
require_metadata_line "Policy Version"
require_metadata_line "Base OID"
require_metadata_line "Head OID"
require_metadata_line "Integrated Result"
integration_strategy="$(sed -n 's/^Integration Strategy: //p' "$cr_file")"
if [ -n "$integration_strategy" ]; then
  printf '%s\n' "$integration_strategy" | grep -Eq '^(rebase-ff|merge|squash)$' \
    || fail "Integration Strategy is invalid"
fi
review_evidence="$(sed -n 's/^Review Evidence: //p' "$cr_file")"
if [ -n "$review_evidence" ]; then
  printf '%s\n' "$review_evidence" | grep -Eq '^trailers$' \
    || fail "Review Evidence is invalid"
fi

revision="$(sed -n 's/^Revision: //p' "$cr_file")"
printf '%s\n' "$revision" | grep -Eq '^[1-9][0-9]*$' \
  || fail "Revision must be a positive integer"

status="$(sed -n 's/^Status: //p' "$cr_file")"
printf '%s\n' "$status" | grep -Eq '^(pending|accepted|integrated|rejected|superseded)$' \
  || fail "Status is invalid"

decision="$(sed -n 's/^Decision: //p' "$cr_file")"
printf '%s\n' "$decision" | grep -Eq '^(accepted|rejected|superseded|pending)$' \
  || fail "Decision is invalid"

policy_version="$(sed -n 's/^Policy Version: //p' "$cr_file")"
printf '%s\n' "$policy_version" | grep -Eq '^v(0\.2|0\.3(-[0-9]+)?|0\.3-(draft-[0-9]+|rc-[0-9]+))$' \
  || fail "Policy Version is invalid"

base_oid="$(sed -n 's/^Base OID: //p' "$cr_file")"
head_oid="$(sed -n 's/^Head OID: //p' "$cr_file")"
integrated_result="$(sed -n 's/^Integrated Result: //p' "$cr_file")"
for oid in "$base_oid" "$head_oid"; do
  printf '%s\n' "$oid" | grep -Eq '^[0-9a-f]{40}$' \
    || fail "CR object IDs must be full lowercase SHA-1 values"
  git cat-file -e "$oid^{commit}" 2>/dev/null \
    || fail "CR object ID is not a commit in this repository: $oid"
done

if [ "$integrated_result" = "pending" ]; then
  [ "$status" != "integrated" ] \
    || fail "integrated CRs must have a main@<full commit OID> result"
else
  printf '%s\n' "$integrated_result" | grep -Eq '^main@[0-9a-f]{40}$' \
    || fail "Integrated Result must match main@<full commit OID>"
  integrated_oid="${integrated_result#main@}"
  git cat-file -e "$integrated_oid^{commit}" 2>/dev/null \
    || fail "integrated result is not a commit in this repository: $integrated_oid"
  printf '%s\n' "$integrated_oid" | grep -Eq '^[0-9a-f]{40}$' \
    || fail "Integrated Result must contain a full lowercase SHA-1"
  main_ref=""
  if git rev-parse --quiet --verify refs/heads/main^{commit} >/dev/null 2>&1; then
    main_ref="refs/heads/main"
  elif git rev-parse --quiet --verify refs/remotes/origin/main^{commit} >/dev/null 2>&1; then
    main_ref="refs/remotes/origin/main"
  elif git rev-parse --quiet --verify HEAD^{commit} >/dev/null 2>&1; then
    main_ref="HEAD"
  fi
  [ -n "$main_ref" ] || fail "validation history is required to verify integrated provenance"
  git merge-base --is-ancestor "$integrated_oid" "$main_ref" \
    || fail "integrated result is not reachable from main"
  git merge-base --is-ancestor "$base_oid" "$integrated_oid" \
    || fail "integrated result must descend from Base OID"

  if [ -n "$integration_strategy" ]; then
    case "$integration_strategy" in
      rebase-ff)
        [ "$integrated_oid" = "$head_oid" ] \
          || fail "rebase-ff integrated result must equal Head OID"
        ;;
      merge)
        [ "$(git cat-file commit "$integrated_oid" | sed -n '/^$/q; /^parent /p' | wc -l)" -ge 2 ] \
          || fail "merge integration must produce a merge commit"
        git merge-base --is-ancestor "$head_oid" "$integrated_oid" \
          || fail "merge result must contain Head OID"
        ;;
      squash)
        [ "$integrated_oid" != "$head_oid" ] \
          || fail "squash integrated result must differ from Head OID"
        if git merge-base --is-ancestor "$head_oid" "$integrated_oid"; then
          fail "squash result must not contain Head OID as an ancestor"
        fi
        ;;
    esac
  fi

  if [ "$review_evidence" = "trailers" ]; then
    git cat-file commit "$integrated_oid" | grep -Eq '^Reviewed-by: .+$' \
      || fail "integrated result must carry Reviewed-by trailer"
    git cat-file commit "$integrated_oid" | grep -Eq '^Tested-by: .+$' \
      || fail "integrated result must carry Tested-by trailer"
  fi
fi
git merge-base --is-ancestor "$base_oid" "$head_oid" \
  || fail "Base OID must be an ancestor of Head OID"

require_section "## Summary"
require_section "## Motivation"
require_section "## Test Evidence"
require_section "## Risk"
require_section "## Rollback"
require_section "## Breaking Change"
require_section "## Backport Target"
