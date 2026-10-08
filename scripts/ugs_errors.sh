#!/usr/bin/env bash

# Shared UGS machine-readable error contract for Bash validators.
# Callers may set UGS_ERROR_FORMAT=json to emit a single JSON error object.

UGS_ERROR_FORMAT="${UGS_ERROR_FORMAT:-text}"

ugs_error() {
  local code="$1"
  local message="$2"

  if [ "$UGS_ERROR_FORMAT" = "json" ]; then
    if command -v jq >/dev/null 2>&1; then
      jq -n --arg code "$code" --arg message "$message" \
        '{format:"ugs-error/v1",code:$code,message:$message}'
    else
      printf '{"format":"ugs-error/v1","code":"%s","message":"%s"}\n' \
        "$code" "$message"
    fi
  else
    printf '%s: %s\n' "$code" "$message" >&2
  fi
}

ugs_fail() {
  local code="$1"
  local message="$2"
  ugs_error "$code" "$message"
  exit 1
}

ugs_usage() {
  local message="$1"
  ugs_fail "UGS-CLI-001" "$message"
}
