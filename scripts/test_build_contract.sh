#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
fail() { echo "build contract validation failed: $1" >&2; exit 1; }

for target in windows-amd64 linux-amd64 linux-arm64 darwin-arm64; do
  grep -Fq "$target" "$repo_root/scripts/build.sh" || fail "build.sh misses $target"
  grep -Fq "$target" "$repo_root/scripts/build.ps1" || fail "build.ps1 misses $target"
  grep -Fq "$target" "$repo_root/scripts/package.ps1" || fail "package.ps1 misses $target"
done

[ -f "$repo_root/go.mod" ] || fail "go.mod is missing"
[ -f "$repo_root/browser-worker/package.json" ] || fail "browser worker package manifest is missing"
[ -f "$repo_root/browser-worker/package-lock.json" ] || fail "browser worker lockfile is missing"
[ -f "$repo_root/.github/workflows/chuzi-build.yml" ] || fail "build workflow is missing"

grep -Fq 'name: chuzi-build' "$repo_root/.github/workflows/chuzi-build.yml" || fail "aggregate build check is missing"
grep -Fq 'actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683' "$repo_root/.github/workflows/chuzi-build.yml" || fail "workflow action is not pinned"

echo "build contract validation passed"
