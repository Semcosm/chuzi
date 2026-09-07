#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
roles_file="${1:-$repo_root/keys/signer_roles.json}"
allowed_file="$repo_root/keys/allowed_signers"

fail() {
  echo "signer roles validation failed: $1" >&2
  exit 1
}

[ -f "$roles_file" ] || fail "signer roles file does not exist"
[ -f "$allowed_file" ] || fail "allowed signers file does not exist"
command -v jq >/dev/null 2>&1 || fail "jq is required"
jq empty "$roles_file" >/dev/null 2>&1 || fail "signer roles file is not valid JSON"

require() {
  local expression="$1"
  local message="$2"
  jq -e "$expression" "$roles_file" >/dev/null || fail "$message"
}

require 'type == "object"' "top level must be an object"
require 'keys == ["$schema", "format", "signers"]' "unknown or missing top-level fields"
require '."$schema" == "../.ugs/schema/signer-roles.schema.json"' "unsupported schema"
require '.format == "ugs-signer-roles/v0.3"' "unsupported format"
require '(.signers | length > 0)' "signers must not be empty"
require '(.signers | map(.principal) | length == (unique | length))' "signer principals must be unique"
require '(.signers | map(.key_fingerprint) | length == (unique | length))' "signer fingerprints must be unique"
require 'all(.signers[]; .principal | test("^[^@[:space:]]+@[^@[:space:]]+$"))' "invalid signer principal"
require 'all(.signers[]; .role | IN("maintainer", "reviewer", "release-signer"))' "invalid signer role"
require 'all(.signers[]; .status | IN("active", "revoked"))' "invalid signer status"
require 'all(.signers[]; .key_fingerprint | test("^SHA256:[A-Za-z0-9+/=]+$"))' "invalid key fingerprint"
require 'all(.signers[]; .effective_from | test("^[0-9]{4}-[0-9]{2}-[0-9]{2}$"))' "invalid effective_from date"
require 'all(.signers[]; (.effective_until == null or (.effective_until | test("^[0-9]{4}-[0-9]{2}-[0-9]{2}$"))))' "invalid effective_until date"
require 'all(.signers[]; (.status != "revoked" or .effective_until != null))' "revoked signers require effective_until"

while IFS= read -r principal; do
  grep -Fq "$principal" "$allowed_file" || fail "signer is not present in allowed_signers: $principal"
done < <(jq -r '.signers[].principal' "$roles_file")

echo "signer roles validation passed"
