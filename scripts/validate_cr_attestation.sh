#!/usr/bin/env bash
set -euo pipefail
script_dir="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
if [ "$#" -lt 2 ]; then
  echo "usage: $0 <attestation.json> <cr-record.md> [repository] [--no-signature|--payload]" >&2
  exit 2
fi
args=("$1" --cr-file "$2")
shift 2
if [ "$#" -gt 0 ] && [[ "$1" != -* ]]; then
  args+=(--repository "$1")
  shift
fi
args+=("$@")
exec python3 "$script_dir/cr_attestation.py" "${args[@]}"
