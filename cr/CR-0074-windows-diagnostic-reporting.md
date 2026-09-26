# CR-0074: add consented diagnostics and fault reporting

Base: main
Head or Range: feat/windows-ui-client-foundation
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(support): add consented diagnostics and fault reporting
Revision: 2
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 2a69ed603dd8acbb2a61af16f25999f7b7b0946f
Head OID: 2a69ed603dd8acbb2a61af16f25999f7b7b0946f
Integrated Result: pending

## Summary

Add an installed-client support path that asks for consent after a warning or
error, builds a bounded redacted report in Core, persists it locally when the
developer endpoint is unavailable, and retries HTTPS delivery in the service.

## Motivation

Users need a safe way to report RDP, Core, and client failures without copying
logs that may contain credentials, page content, or machine-specific paths.
The report boundary must be explicit, auditable, cancellable, and useful even
when the machine is offline.

## Test Evidence

go test ./...

go vet ./...

cargo fmt --manifest-path ui/windows/Cargo.toml -- --check

cargo check --manifest-path ui/windows/Cargo.toml --all-targets

git diff --check

The diagnostics package tests allow-listing, secret/path redaction, owner-only
queue files, successful upload removal, and retry backoff. The Windows client
build checks the consent overlay and additive Core method wiring.

## Risk

The endpoint is disabled unless configured and accepts only HTTPS (loopback HTTP
is allowed for local tests). Reports are bounded and contain no free-form raw
logs. A corrupt queue item is ignored rather than uploaded. The additional
Core method is optional for older in-process API embedders.

## Rollback

Disable `diagnostics.enabled`, remove the configured endpoint, or revert this
change record. Existing queued files are ordinary 0600 local artifacts and may
be removed after confirming no support submission is needed.

## Breaking Change

None to existing Core methods or business-state/storage schemas. The additive
`submit_diagnostic_report` method is advertised by `chuzi.core/v1` servers.

## Backport Target

none
