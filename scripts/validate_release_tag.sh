#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: $0 <release-tag>" >&2
  exit 2
fi

tag="$1"
repo_root="$(git rev-parse --show-toplevel)"

fail() {
  echo "release tag validation failed: $1" >&2
  exit 1
}

printf '%s\n' "$tag" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' \
  || fail "tag must match v<major>.<minor>.<patch>"
git rev-parse --verify "refs/tags/$tag" >/dev/null 2>&1 \
  || fail "tag does not exist: $tag"

tag_object="$(git rev-parse --verify "refs/tags/$tag^{tag}" 2>/dev/null)" \
  || fail "formal release tag must be annotated"
target="$(git rev-parse "refs/tags/$tag^{commit}")"
[ -n "$target" ] || fail "tag must point to a commit"
[ -f "$repo_root/releases/$tag.md" ] \
  || fail "release notes do not exist: releases/$tag.md"

git -c gpg.format=ssh \
  -c gpg.ssh.allowedSignersFile="$repo_root/keys/allowed_signers" \
  -c gpg.ssh.revocationFile="$repo_root/keys/revoked_signers" \
  verify-tag "$tag_object" >/dev/null 2>&1 \
  || fail "tag signature is not trusted"
