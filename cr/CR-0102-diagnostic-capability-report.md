# CR-0102: make Core diagnostic compatibility failures explainable

Base: main
Head or Range: 8118d20fb28ca7cf740b52b9044f129563ed3cf5
Integration Strategy: rebase-ff
Title: fix(ui): expose diagnostic capability and retry facts
Revision: 3
Status: integrated
Decision: accepted
Policy Version: v0.3
Base OID: 8118d20fb28ca7cf740b52b9044f129563ed3cf5
Head OID: 8118d20fb28ca7cf740b52b9044f129563ed3cf5
Integrated Result: main@8118d20fb28ca7cf740b52b9044f129563ed3cf5

## Summary

Extend the Windows `chuzi.diagnostic/v2` local artifact with enough redacted
metadata to diagnose Core method and compatibility failures. The UI probes the
Core hello capability list, records whether `get_diagnostic_snapshot` is
supported, and records the protocol version and allow-listed method names.
Diagnostic calls now record attempt count, final request shape, compatibility
retry state, and the first failure classification when the legacy retry is
needed.

## Motivation

Recent reports show `ready=true` together with `core_method_error` and
`invalid_argument`, but cannot distinguish a UI/Core version skew, an absent
diagnostic method, or a second failure after the compatibility retry. The new
fields make those cases mechanically distinguishable while retaining only fixed
codes, bounded sizes, and one-way fingerprints.

## Test Evidence

~~~text
CARGO_TARGET_DIR=/home/chen/.cache/chuzi-diagnostic-capabilities-target cargo test --manifest-path ui/windows/Cargo.toml
GOTMPDIR=/home/chen/.cache/chuzi-go-tmp GOCACHE=/home/chen/.cache/chuzi-go-cache go test ./...
GOTMPDIR=/home/chen/.cache/chuzi-go-tmp GOCACHE=/home/chen/.cache/chuzi-go-cache go vet ./...
./scripts/validate_policy_manifest.sh
./scripts/validate_quality_profile.sh
./scripts/validate_supply_chain_profile.sh
./scripts/validate_action_pinning.sh
./scripts/validate_repository_shape.sh
./scripts/test_build_contract.sh
cargo fmt --manifest-path ui/windows/Cargo.toml -- --check
git diff --check
~~~

Focused Rust coverage verifies supported, unsupported, and malformed Core hello
responses, retry metadata retention, stable unsupported-method classification,
Unicode summaries, and the absence of raw error text.

## Risk

The capability probe and diagnostic call are read-only local IPC operations.
The probe adds one hello round trip before export and skips the diagnostic call
only when Core explicitly omits the method. Method names and protocol tokens are
validated and bounded; raw hello output, launcher errors, paths, credentials,
and response bodies are never persisted. Existing v2 readers can ignore the
additive fields, and old Core snapshots continue to decode with defaults.

## Rollback

Revert the diagnostic capability/report commit and remove this CR. Existing
diagnostic files remain readable because all additions are optional v2 fields.

## Breaking Change

No. Core transport methods and request schemas are unchanged.

## Backport Target

None.
