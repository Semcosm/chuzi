# CR-0096: add local diagnostic snapshot export

Base: main
Head or Range: working tree
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(support): add local diagnostic snapshot export
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: dec5dcb6a3ecaa6331fb5c13eebb0258434b61fb
Head OID: 88d71418b730b7fffc9879c3d0d4323d55ed5558
Integrated Result: pending

## Summary

Add a read-only `get_diagnostic_snapshot` Core method and a Windows UI export
flow for bounded, redaction-safe local diagnostic JSON files. The snapshot is
generated from the existing service event buffer and version/platform metadata;
it never queues or uploads a report. The UI saves the allow-listed response in
the user-local application data directory and writes a minimal UI-only fallback
when Core is unavailable.

The existing remote `submit_diagnostic_report` API remains available for a
future product decision but is no longer presented as the immediate error
recovery action in the Windows UI.

## Motivation

The current error overlay asks about sending diagnostics, but users cannot
obtain a local artifact when remote reporting is disabled, unavailable, or not
yet configured. This leaves support without a reproducible redacted report and
makes the consent flow appear broken. Local export must remain useful without
deciding where a future remote report will be sent.

## Test Evidence

Focused checks:

~~~text
go test ./...
go vet ./...
GOOS=windows GOARCH=amd64 go build ./...
GOOS=windows GOARCH=amd64 go vet ./...
cargo test --manifest-path ui/windows/Cargo.toml --locked
cargo check --manifest-path ui/windows/Cargo.toml --locked
git diff --check
~~~

Coverage includes disabled-remote snapshot generation without queue writes,
Core field projection and event redaction, transport method negotiation and
round-trip calls, and the UI local/fallback export path. Full repository
validation remains to be run before integration.

## Risk

The new transport method is additive. Local files are bounded to 256 KiB and
contain only typed snapshot fields. The UI does not read logs, bbolt, Profiles,
credentials, named pipes, or RDP data. The fallback intentionally omits Core
events if the service cannot be reached.

## Rollback

Revert the Core transport/UI snapshot changes and remove this CR. The existing
remote submission API and service queue can remain unchanged for a later
decision.

## Breaking Change

No. Existing Core clients and the remote submission method remain compatible.

## Backport Target

None.
