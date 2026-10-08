# CR-0100: preserve Core diagnostic failure codes

Base: main
Head or Range: fix/diagnostic-core-error-classification
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: fix(ui): preserve Core diagnostic failure codes
Revision: 5
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 60cc83d3e5aedc745e7351ff7bc85004c35c1410
Head OID: 60121a5457072e8110284acc6f7b2dfd7ab1c737
Integrated Result: pending

## Summary

Extend the Windows local chuzi.diagnostic/v2 fallback with a stable Core
error code, bounded error size, and a one-way error fingerprint. Preserve the
existing redacted capture_error_class while allowing support to distinguish
Core transport failures from invalid_argument, unavailable, and internal
responses.

## Motivation

The current fallback reports every failed get_diagnostic_snapshot call as
core_method_error. A report with a ready Core therefore cannot distinguish an
unsupported method, a rejected request, a temporarily unavailable diagnostic
service, or an internal Core failure. The new fields retain only allow-listed
codes and non-reversible metadata, so the failure remains safe to share.

## Test Evidence

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

Focused Rust coverage verifies stable Core-code classification, malformed
response handling, and that raw error text is absent from the saved metadata.

## Risk

The change is additive. Existing v2 readers can ignore the new fields, and
older Core responses continue to decode because the UI model uses defaults.
Only fixed classifications, a byte count, and FNV-1a64 fingerprints are
persisted; launcher output, paths, credentials, and request data are not
copied.

## Rollback

Revert the UI model, capture classification, documentation, and tests in this
CR. Existing v2 fallback files remain readable after rollback.

## Breaking Change

No. Core transport methods and existing diagnostic fields are unchanged.

## Backport Target

None.
