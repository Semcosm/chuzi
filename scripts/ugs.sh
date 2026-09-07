#!/usr/bin/env bash
set -euo pipefail
root_dir="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"

case "${1:-}" in
  init)
    shift
    exec "$root_dir/scripts/ugs_init.sh" "$@"
    ;;
  install|upgrade|activate|rollback)
    exec python3 "$root_dir/scripts/ugs_upgrade.py" "$@"
    ;;
  *)
    echo "usage: $0 {init|install|upgrade|activate|rollback} [options]" >&2
    exit 2
    ;;
esac
