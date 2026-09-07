#!/usr/bin/env bash
set -euo pipefail
if [ "$#" -ne 2 ]; then echo "usage: $0 <old-sha> <new-sha>" >&2; exit 2; fi
repo_root="${UGS_REPOSITORY_ROOT:-}"
if [ -z "$repo_root" ]; then
  repo_root="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
fi
old="$1"; new="$2"
fail() { echo "main CR range validation failed: $1" >&2; exit 1; }
git rev-parse --verify "$old^{commit}" >/dev/null 2>&1 || fail "invalid old SHA"
git rev-parse --verify "$new^{commit}" >/dev/null 2>&1 || fail "invalid new SHA"
mapfile -t records < <(git diff --name-only "$old..$new" -- 'cr/CR-*.md')
[ "${#records[@]}" -gt 0 ] || fail "main integration range contains no persisted CR"
for record in "${records[@]}"; do
  record_file="$(mktemp)"
  trap 'rm -f "$record_file"' EXIT
  git show "$new:$record" > "$record_file" || fail "cannot read persisted CR from new commit: $record"
  "$repo_root/scripts/validate_cr_record.sh" "$record_file" >/dev/null
  record_head="$(sed -n 's/^Head OID: //p' "$record_file")"
  rm -f "$record_file"
  trap - EXIT
  git merge-base --is-ancestor "$record_head" "$new" || fail "$record Head OID is not reachable from new main"
done
echo "main CR range validation passed (${#records[@]} record(s))"
