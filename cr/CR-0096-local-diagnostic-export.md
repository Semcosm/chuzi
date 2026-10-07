# CR-0096: add local diagnostic snapshot export

Base: main
Head or Range: feat/diagnostic-v2
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(support): add local diagnostic snapshot export
Revision: 4
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: b61ae085ec08d8f7b5d2c3a7a443627f45afe9ed
Head OID: 8025cd78f12a673d7e4a78f77199a78ce9b6234b
Integrated Result: pending

## Summary

Add a read-only `get_diagnostic_snapshot` Core method and a Windows UI export
flow for bounded, redaction-safe local diagnostic JSON files. The v2 snapshot
records stable error classification and operation, event-window metadata, event
durations, Core lifecycle status, and a stable capture failure class. It never
queues or uploads a report. The UI saves the allow-listed response in the
user-local application data directory and writes a v2 UI fallback when Core is
unavailable or returns an invalid projection.

The existing remote `submit_diagnostic_report` API remains available for a
future product decision but is no longer presented as the immediate error
recovery action in the Windows UI. The destructive native Windows job-pool
smoke remains explicitly opt-in; normal schedules, pushes, tags, and manual
dispatches do not request a self-hosted runner.

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
./scripts/test_build_contract.sh
git diff --check
~~~

Coverage includes disabled-remote snapshot generation without queue writes,
event count/truncation and duration metadata, Core field projection and event
redaction, lifecycle event capture, transport method negotiation and round-trip
calls, and the UI local/fallback export path. Full repository validation is
recorded in the commit trailers and CI result.
The workflow contract also verifies that the native Windows job-pool smoke is
skipped unless the repository opt-in variable or manual boolean input is true.
The `test` channel workflow dispatch for the final CI fix completed successfully
with the native smoke job skipped.

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
