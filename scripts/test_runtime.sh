#!/usr/bin/env bash
set -euo pipefail

# Phase-four verification is deliberately local and deterministic in CI. The
# controlled Matrix test is opt-in and requires a disposable test homeserver;
# it must never inherit production credentials from a developer shell.
go test ./tests/runtime -run '^TestProduction' -count=1 -v

required=(
  CHUZI_MATRIX_TEST_HOMESERVER
  CHUZI_MATRIX_TEST_BOT_TOKEN
  CHUZI_MATRIX_TEST_BOT_USER
  CHUZI_MATRIX_TEST_ACTOR_TOKEN
  CHUZI_MATRIX_TEST_ACTOR_USER
  CHUZI_MATRIX_TEST_ROOM
  CHUZI_MATRIX_TEST_ACCOUNT
)
configured=1
for name in "${required[@]}"; do
  if [[ -z "${!name:-}" ]]; then
    configured=0
    break
  fi
done

if [[ "${CHUZI_RUN_CONTROLLED_MATRIX:-0}" == "1" ]]; then
  if [[ "$configured" != "1" ]]; then
    printf '%s\n' 'controlled Matrix homeserver test requested but required CHUZI_MATRIX_TEST_* variables are incomplete' >&2
    exit 1
  fi
  go test ./tests/runtime -run '^TestControlledMatrixHomeserverSyncSend$' -count=1 -v
else
  printf '%s\n' 'controlled Matrix homeserver test not run (set CHUZI_RUN_CONTROLLED_MATRIX=1 with disposable test credentials)'
fi
