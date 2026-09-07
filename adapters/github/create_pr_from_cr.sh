#!/usr/bin/env bash
set -euo pipefail
if [ "$#" -lt 1 ] || [ "$#" -gt 3 ]; then echo "usage: $0 <cr-file> [head-branch] [repository]" >&2; exit 2; fi
cr_file="$1"; head_branch="${2:-$(git symbolic-ref --quiet --short HEAD)}"; repository="${3:-}"
[ -f "$cr_file" ] || { echo "CR file does not exist: $cr_file" >&2; exit 1; }
case "$cr_file" in cr/CR-*.md) ;; *) echo "CR must be under cr/CR-*.md" >&2; exit 1 ;; esac
title="$(sed -n 's/^Title: //p' "$cr_file")"; base="$(sed -n 's/^Base: //p' "$cr_file")"
[ -n "$title" ] || { echo "CR Title is missing" >&2; exit 1; }
[ -n "$base" ] || { echo "CR Base is missing" >&2; exit 1; }
args=(pr create --base "$base" --head "$head_branch" --title "$title" --body-file "$cr_file")
[ -n "$repository" ] && args+=(--repo "$repository")
gh "${args[@]}"
