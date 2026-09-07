#!/usr/bin/env bash
set -euo pipefail

# End-to-end profile gate.  Every profile is tested in a disposable consumer
# repository so generated files, hooks, and lifecycle checks are exercised
# together rather than inferred from the source checkout.
root_dir="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
temp_dir="$(mktemp -d)"
trap 'rm -rf "$temp_dir"' EXIT

expect_failure() {
  if "$@" >/dev/null 2>&1; then
    echo "expected failure unexpectedly passed: $*" >&2
    return 1
  fi
}

exercise_lifecycle() {
  local repo="$1"
  local message="$temp_dir/message"
  local base head result old_main new_main

  printf '%s\n' 'bad message' > "$message"
  expect_failure "$repo/.githooks/commit-msg" "$message"
  printf '%s\n' 'docs(profile): valid hook message' '' 'Refs: profile-fixture' > "$message"
  "$repo/.githooks/commit-msg" "$message"

  git -C "$repo" config user.name 'UGS Profile Fixture'
  git -C "$repo" config user.email 'profile-fixture@example.invalid'
  printf '%s\n' base > "$repo/file"
  git -C "$repo" add file
  git -C "$repo" commit --quiet -F "$message"
  base="$(git -C "$repo" rev-parse HEAD)"
  git -C "$repo" checkout --quiet -b docs/profile-fixture
  printf '%s\n' head >> "$repo/file"
  git -C "$repo" add file
  git -C "$repo" commit --quiet -m 'docs(profile): exercise CR evidence' -m 'Refs: profile-fixture' -m 'Reviewed-by: Fixture Reviewer <reviewer@example.invalid>' -m 'Tested-by: scripts/test_profile_conformance.sh'
  head="$(git -C "$repo" rev-parse HEAD)"
  git -C "$repo" checkout --quiet main
  git -C "$repo" merge --quiet --ff-only docs/profile-fixture
  result="$(git -C "$repo" rev-parse HEAD)"
  cat > "$repo/profile-cr.md" <<EOF
# CR-9001: Portable profile conformance fixture

Base: main
Head or Range: docs/profile-fixture
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: docs(profile): exercise CR evidence
Revision: 1
Status: integrated
Decision: accepted
Policy Version: v0.3
Base OID: $base
Head OID: $head
Integrated Result: main@$result

## Summary

Exercise a valid CR in a disposable consumer repository.

## Motivation

Ensure the profile gate validates CR provenance.

## Test Evidence

The profile conformance fixture passed.

## Risk

Fixture only.

## Rollback

Delete the disposable repository.

## Breaking Change

None.

## Backport Target

None.
EOF
  (cd "$repo" && "$root_dir/scripts/validate_cr_record.sh" profile-cr.md)

  # Exercise all three declared integration modes and protected-ref behavior.
  git -C "$repo" checkout --quiet -b docs/rebase-profile main
  printf '%s\n' rebase >> "$repo/file"
  git -C "$repo" add file && git -C "$repo" commit --quiet -m 'docs(profile): rebase' -m 'Refs: profile-fixture'
  git -C "$repo" rebase --quiet main
  git -C "$repo" checkout --quiet main
  git -C "$repo" checkout --quiet -b docs/merge-profile main
  printf '%s\n' merge >> "$repo/file"
  git -C "$repo" add file && git -C "$repo" commit --quiet -m 'docs(profile): merge' -m 'Refs: profile-fixture'
  git -C "$repo" checkout --quiet main
  git -C "$repo" merge --quiet --no-ff docs/merge-profile -m 'docs(profile): merge integration'
  git -C "$repo" checkout --quiet -b docs/squash-profile main
  printf '%s\n' squash >> "$repo/file"
  git -C "$repo" add file && git -C "$repo" commit --quiet -m 'docs(profile): squash' -m 'Refs: profile-fixture'
  git -C "$repo" checkout --quiet main
  git -C "$repo" merge --quiet --squash docs/squash-profile >/dev/null
  git -C "$repo" commit --quiet -m 'docs(profile): squash integration' -m 'Refs: profile-fixture'
  old_main="$(git -C "$repo" rev-parse HEAD^)"
  new_main="$(git -C "$repo" rev-parse HEAD)"
  (cd "$repo" && "$root_dir/scripts/validate_ref_update.sh" "$old_main" "$new_main" refs/heads/main)
  expect_failure bash -c "cd '$repo' && '$root_dir/scripts/validate_ref_update.sh' '$new_main' '$old_main' refs/heads/main"
  expect_failure bash -c "cd '$repo' && '$root_dir/scripts/validate_ref_update.sh' '$new_main' '0000000000000000000000000000000000000000' refs/heads/main"
}

exercise_init_safety() {
  local profile="$1"
  local migrate="$temp_dir/$profile-migrate"
  local dry_run="$temp_dir/$profile-dry-run"
  mkdir -p "$dry_run"
  "$root_dir/scripts/ugs_init.sh" --profile "$profile" --dry-run "$dry_run" | grep -Fqx 'would write .ugs/policy.json'
  git init --quiet -b main "$migrate"
  git -C "$migrate" config user.name 'UGS Migration Fixture'
  git -C "$migrate" config user.email 'migration-fixture@example.invalid'
  printf '%s\n' keep > "$migrate/keep.txt"
  "$root_dir/scripts/ugs_init.sh" --profile "$profile" --migrate --no-commit "$migrate" >/dev/null
  before="$(find "$migrate" -type f -printf '%P %s\n' | sort)"
  "$root_dir/scripts/ugs_init.sh" --profile "$profile" --migrate --no-commit "$migrate" >/dev/null
  [ "$before" = "$(find "$migrate" -type f -printf '%P %s\n' | sort)" ]
}

run_profile() {
  local profile="$1"
  local repo="$temp_dir/$profile"
  "$root_dir/scripts/ugs_init.sh" --profile "$profile" --no-commit "$repo" >/dev/null
  [ "$(jq -r .conformance_level "$repo/.ugs/policy.json")" = "$profile" ]
  [ "$(git -C "$repo" config --get core.hooksPath)" = '.githooks' ]
  (cd "$repo" && scripts/validate_policy_manifest.sh .ugs/policy.json)
  exercise_init_safety "$profile"
  exercise_lifecycle "$repo"

  case "$profile" in
    baseline) ;;
    standard)
      (cd "$repo" && scripts/validate_quality_profile.sh .ugs/policy.json >/dev/null && scripts/validate_supply_chain_profile.sh .ugs/policy.json >/dev/null && scripts/validate_action_pinning.sh .ugs/policy.json .github/workflows >/dev/null && scripts/validate_repository_shape.sh .ugs/policy.json >/dev/null)
      [ -f "$repo/.github/workflows/ugs-validate.yml" ]
      ;;
    high-trust)
      (cd "$repo" && scripts/validate_signer_roles.sh >/dev/null && scripts/validate_action_pinning.sh .ugs/policy.json .github/workflows >/dev/null)
      [ -f "$repo/keys/allowed_signers" ]
      [ -z "$(rg -l -- '-----BEGIN (OPENSSH|RSA|EC|DSA) PRIVATE KEY-----' "$repo" || true)" ]
      signer_key="$temp_dir/$profile-key"
      ssh-keygen -q -t ed25519 -N '' -C profile-fixture@example.invalid -f "$signer_key"
      printf 'profile-fixture@example.invalid namespaces="git,ugs-attestation" %s\n' "$(cat "$signer_key.pub")" > "$repo/keys/allowed_signers"
      : > "$repo/keys/revoked_signers"
      git -C "$repo" config gpg.format ssh
      git -C "$repo" config user.signingKey "$signer_key"
      printf '%s\n' signed >> "$repo/file"
      git -C "$repo" add file
      git -C "$repo" commit --quiet -S -m 'docs(profile): signed evidence' -m 'Refs: profile-fixture'
      signed="$(git -C "$repo" rev-parse HEAD)"
      (cd "$repo" && scripts/validate_commit_signatures.sh "$signed" && scripts/validate_commit_signatures.sh "$signed^..$signed")
      expect_failure bash -c "cd '$repo' && '$root_dir/scripts/validate_commit_signatures.sh' '$signed^'"
      printf '%s\n' "$(cat "$signer_key.pub")" > "$repo/keys/revoked_signers"
      expect_failure bash -c "cd '$repo' && '$root_dir/scripts/validate_commit_signatures.sh' '$signed'"
      : > "$repo/keys/revoked_signers"
      git -C "$repo" tag -s v0.0.0 -m 'profile release'
      mkdir -p "$repo/releases"
      printf '%s\n' '# v0.0.0' > "$repo/releases/v0.0.0.md"
      (cd "$repo" && scripts/validate_release_tag.sh v0.0.0 >/dev/null)
      digest="sha256:0123456789012345678901234567890123456789012345678901234567890123"
      jq -n --arg commit "$signed" --arg digest "$digest" \
        '{schema_version:1,type:"ugs-release-attestation",repository:"profile-fixture",release_tag:"v0.0.0",commit:$commit,artifact:{name:"profile",digest:$digest},builder:{id:"profile"},built_at:"2026-09-05T00:00:00Z"}' > "$temp_dir/attestation.json"
      jq -cS 'del(.signature)' "$temp_dir/attestation.json" > "$temp_dir/payload"
      ssh-keygen -Y sign -f "$signer_key" -n ugs-attestation < "$temp_dir/payload" > "$temp_dir/attestation.sig" 2>/dev/null
      signature="$(base64 -w0 "$temp_dir/attestation.sig")"
      jq --arg signature "$signature" '.signature={format:"ssh",namespace:"ugs-attestation",principal:"profile-fixture@example.invalid",value:$signature}' "$temp_dir/attestation.json" > "$temp_dir/signed-attestation.json"
      (cd "$repo" && UGS_ALLOWED_SIGNERS_FILE=keys/allowed_signers scripts/validate_release_attestation.sh "$temp_dir/signed-attestation.json" v0.0.0 "$signed" true profile-fixture >/dev/null)
      ;;
  esac
  printf '%s: pass\n' "$profile"
}

for profile in baseline standard high-trust; do
  run_profile "$profile"
done
