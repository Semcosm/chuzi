#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 3 ]; then
  echo "usage: $0 <old-object> <new-object> <ref-name>" >&2
  exit 2
fi

old_object="$1"
new_object="$2"
ref_name="$3"
zeros="0000000000000000000000000000000000000000"
repo_root="${UGS_REPOSITORY_ROOT:-}"
if [ -z "$repo_root" ]; then
  repo_root="$(git rev-parse --show-toplevel 2>/dev/null || true)"
fi

fail() {
  echo "ref update validation failed: $1" >&2
  exit 1
}

case "$ref_name" in
  refs/heads/main)
    [ "$new_object" != "$zeros" ] || fail "deleting main is not allowed"
    [ "$old_object" != "$zeros" ] || exit 0
    git merge-base --is-ancestor "$old_object" "$new_object" \
      || fail "main updates must be fast-forward"
    ;;
  refs/tags/v*)
    [ "$new_object" != "$zeros" ] || fail "deleting formal release tags is not allowed"
    if [ "$old_object" != "$zeros" ] && [ "$old_object" != "$new_object" ]; then
      fail "formal release tags cannot be replaced"
    fi
    tag_name="${ref_name#refs/tags/}"
    [ -n "$repo_root" ] || fail "release tag validation requires UGS_REPOSITORY_ROOT or a Git work tree"
    [ -x "$repo_root/scripts/validate_release_tag.sh" ] || fail "release tag validator is unavailable; install the high-trust profile before enforcing formal release tags"
    "$repo_root/scripts/validate_release_tag.sh" "$tag_name"
    ;;
esac
