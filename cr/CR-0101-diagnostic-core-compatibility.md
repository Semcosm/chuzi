# CR-0101: keep local diagnostic export compatible with older Core

Base: main
Head or Range: 82bf3950d4df424aac81fc4aa61f52336c321aa6
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: fix(ui): retry diagnostic snapshots for older Core
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: dcb72a7d1f3cee56f3c11463878882cfd85649d6
Head OID: 82bf3950d4df424aac81fc4aa61f52336c321aa6
Integrated Result: pending

## Summary

Make Windows local diagnostic export tolerate a UI/Core component version
skew. The UI first sends the current diagnostic request, then retries with the
stable severity/category/summary shape when Core returns `invalid_argument`.
Legacy Core snapshot responses are normalized to the v2 local artifact with
redacted error classification, operation, event count, and Core readiness
metadata. Unicode summaries remain intact.

## Motivation

The latest Windows report showed a ready Core returning `invalid_argument` for
the diagnostic request. The UI had added `error_class` and `operation` fields,
while older Core builds use strict JSON decoding and reject unknown fields.
This made a healthy Core appear to have a failed diagnostic capture and left
support with only a fallback event.

## Test Evidence

~~~text
go test ./...
go vet ./...
CARGO_TARGET_DIR=/home/chen/codex-cargo-target-chuzi cargo test --manifest-path ui/windows/Cargo.toml --locked
./scripts/validate_policy_manifest.sh
./scripts/validate_quality_profile.sh
./scripts/validate_supply_chain_profile.sh
./scripts/validate_action_pinning.sh
./scripts/validate_repository_shape.sh
./scripts/test_build_contract.sh
git diff --check
~~~

Focused Rust coverage verifies preservation of the Chinese summary, removal of
additive fields from the legacy retry request, and normalization of a legacy
response without storing raw error text.

## Risk

The retry is read-only and is attempted only for the stable `invalid_argument`
code. It sends no credentials, paths, logs, or user data beyond the existing
redacted summary. Legacy responses are accepted only when all required bounded
snapshot fields are present; malformed payloads still use the existing v2
fallback classification.

## Rollback

Revert the UI compatibility commit and remove this CR. Existing fallback
diagnostic files remain readable after rollback.

## Breaking Change

No. The Core transport and current diagnostic request remain unchanged.

## Backport Target

None.
