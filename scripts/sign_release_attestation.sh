#!/usr/bin/env bash
set -euo pipefail
if [ "$#" -ne 4 ]; then echo "usage: $0 <unsigned-attestation.json> <signing-key> <principal> <signed-output.json>" >&2; exit 2; fi
input="$1"; key="$2"; principal="$3"; output="$4"
fail() { echo "release attestation signing failed: $1" >&2; exit 1; }
[ -f "$input" ] || fail "attestation does not exist: $input"
[ -f "$key" ] || fail "signing key does not exist: $key"
[ -n "${principal//[[:space:]]/}" ] && [[ "$principal" == *@* ]] || fail "principal must contain @"
command -v jq >/dev/null 2>&1 || fail "jq is required"; command -v ssh-keygen >/dev/null 2>&1 || fail "ssh-keygen is required"
jq empty "$input" >/dev/null 2>&1 || fail "attestation is not valid JSON"; jq -e '.signature == null' "$input" >/dev/null || fail "attestation is already signed"
temp_dir="$(mktemp -d)"; trap 'rm -rf "$temp_dir"' EXIT
jq -cS 'del(.signature)' "$input" > "$temp_dir/payload"
ssh-keygen -Y sign -f "$key" -n ugs-attestation < "$temp_dir/payload" > "$temp_dir/signature" 2>/dev/null || fail "ssh-keygen could not sign attestation"
if base64 --help 2>&1 | grep -q -- '-w'; then signature="$(base64 -w0 "$temp_dir/signature")"; else signature="$(base64 < "$temp_dir/signature" | tr -d '\n')"; fi
[ -n "$signature" ] || fail "ssh-keygen returned an empty signature"; umask 077
jq -S --arg principal "$principal" --arg value "$signature" '. + {signature:{format:"ssh",namespace:"ugs-attestation",principal:$principal,value:$value}}' "$input" > "$output" || fail "could not write signed attestation"
