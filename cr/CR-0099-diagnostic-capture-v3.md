# CR-0099: enrich local diagnostic capture metadata

Base: main
Head or Range: 72b7e9a4f3979628acac3d8e6c648ec7cf56a3d3
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(support): enrich local diagnostic capture metadata
Revision: 1
Status: integrated
Decision: accepted
Policy Version: v0.3
Base OID: 12edebc6cbcb0110d0b4454e2f329f8cae355e6b
Head OID: 72b7e9a4f3979628acac3d8e6c648ec7cf56a3d3
Integrated Result: main@a7b9609143cb719a03e514e26eca84d8804ff95d

## Summary

Extend the Windows local chuzi.diagnostic/v2 export with bounded capture
metadata, stable capture stages and error classes, a response-shape summary,
and a non-sensitive response fingerprint. When Core is unavailable or returns
an invalid projection, the UI writes a synthetic redacted failure event so the
saved artifact explains where capture stopped without copying raw launcher
output.

## Motivation

The v2 artifact identifies the user-visible failure and Core lifecycle state,
but a support engineer cannot tell whether capture failed during the launcher
call, JSON decoding, schema negotiation, or required-field validation. The
additional fields make those failure modes distinguishable while preserving the
redaction boundary and keeping local export offline-only.

## Test Evidence

Focused checks:

~~~text
cargo test --manifest-path ui/windows/Cargo.toml --locked
go test ./...
go vet ./...
./scripts/validate_policy_manifest.sh
./scripts/validate_quality_profile.sh
./scripts/validate_supply_chain_profile.sh
./scripts/validate_action_pinning.sh
./scripts/validate_repository_shape.sh
./scripts/test_build_contract.sh
git diff --check
~~~

Rust coverage includes missing schema, non-object responses, non-string schema,
unsupported schema, malformed v2 field types, missing required Core status,
valid v2 acceptance, malformed non-JSON metadata, stable error classes and
fingerprint redaction. The UI writes no raw response body, path, request ID,
credential, or launcher error text.

## Risk

The transport contract remains additive. Snapshot fields are bounded strings,
counts, enums, timing, and a one-way fingerprint. Invalid Core payloads fall
back to the existing v2 UI artifact with one synthetic event. No network request
or submission-queue write is introduced.

## Rollback

Revert the UI model, capture validation, documentation, and tests in this CR.
The existing v2 snapshot export remains available after rollback.

## Breaking Change

No. Existing Core methods and v2 fields remain compatible; new fields have
serde defaults for older snapshots.

## Backport Target

None.
