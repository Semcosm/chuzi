#!/usr/bin/env bash
set -euo pipefail
if [ "$#" -ne 4 ]; then echo "usage: $0 <base-sha> <head-sha> <cr-file> <review-body>" >&2; exit 2; fi
base="$1"; head="$2"; cr_file="$3"; review_body="$4"
repo_root="$(git rev-parse --show-toplevel)"
fail() { echo "CR review validation failed: $1" >&2; exit 1; }
git rev-parse --verify "$base^{commit}" >/dev/null 2>&1 || fail "invalid base SHA"
git rev-parse --verify "$head^{commit}" >/dev/null 2>&1 || fail "invalid head SHA"
[ -f "$cr_file" ] || fail "CR file does not exist"
[ -f "$review_body" ] || fail "review body does not exist"
merge_base="$(git merge-base "$base" "$head")"
record_base="$(sed -n 's/^Base OID: //p' "$cr_file")"
record_head="$(sed -n 's/^Head OID: //p' "$cr_file")"
[ "$record_base" = "$merge_base" ] || fail "CR Base OID does not equal review merge-base"
git merge-base --is-ancestor "$record_head" "$head" || fail "CR Head OID is not in the proposed history"
"$repo_root/scripts/validate_cr_record.sh" "$cr_file" >/dev/null
cmp -s "$cr_file" "$review_body" || fail "review body differs from CR source"
echo "CR review validation passed: $cr_file"
