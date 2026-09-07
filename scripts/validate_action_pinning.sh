#!/usr/bin/env bash
set -euo pipefail
repo_root="$(git rev-parse --show-toplevel)"
exec "$repo_root/adapters/github/validate_action_pinning.sh" "$@"
