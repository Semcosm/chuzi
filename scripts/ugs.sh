#!/usr/bin/env bash
set -euo pipefail
root_dir="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"

usage() {
  cat <<'EOF'
usage: scripts/ugs.sh <command> [options]

Commands:
  init       initialize an empty repository or migrate missing files
  install    install a package into a repository
  migrate    preview or apply a complete offline package migration
  upgrade    compatibility alias for migrate
  activate   explicitly activate baseline, standard, or high-trust
  rollback   restore a backup created by install, migrate, upgrade, or activate
  branch close
             safely close or archive a topic branch

Run 'scripts/ugs.sh <command> --help' for command-specific options.
See docs/git/ugs-cli.md for workflow guidance.
EOF
}

if [ "${1:-}" = "--help" ] || [ "${1:-}" = "help" ]; then
  if [ "${1:-}" = "help" ] && [ -n "${2:-}" ]; then
    shift
  else
    usage
    exit 0
  fi
fi

case "${1:-}" in
  init)
    shift
    exec "$root_dir/scripts/ugs_init.sh" "$@"
    ;;
  branch)
    shift
    if [ "${1:-}" != "close" ]; then
      echo "usage: $0 branch close <branch> [options]" >&2
      exit 2
    fi
    shift
    exec python3 "$root_dir/scripts/branch_close.py" "$@"
    ;;
  install|upgrade|migrate|activate|rollback)
    exec python3 "$root_dir/scripts/ugs_upgrade.py" "$@"
    ;;
  *)
    usage >&2
    exit 2
    ;;
esac
