#!/usr/bin/env bash
set -euo pipefail
repo_root="$(git rev-parse --show-toplevel 2>/dev/null || true)"
[ -n "$repo_root" ] || { echo "UGS: GitHub adapter requires a Git work tree" >&2; exit 1; }
adapter="$repo_root/adapters/github/create_pr_from_cr.sh"
[ -x "$adapter" ] || {
  echo "UGS: optional GitHub adapter is not installed; initialize or migrate with --profile standard or --profile high-trust to enable this command" >&2
  exit 1
}
exec "$adapter" "$@"
