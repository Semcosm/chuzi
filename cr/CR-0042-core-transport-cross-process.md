# CR-0042: verify Core transport across a process boundary

Base: main
Head or Range: 851b73847c55e51d111a603c1605e592de6b0d87..2ea7ef709889e3f8d454452eda3d415e6cb33696
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: test(core): exercise transport across process boundary
Revision: 2
Status: accepted
Decision: accepted
Policy Version: v0.3
Base OID: 3c317c12e22d1b3572365d03adc3457af3e366ec
Head OID: 3c317c12e22d1b3572365d03adc3457af3e366ec
Integrated Result: pending

## Summary

Add a real subprocess contract test for the `chuzi.core/v1` local transport.
The parent test starts an independent helper process, waits for its derived
endpoint, and connects through the same JSONL boundary used by native clients.
The helper assembles the actual Core facade over a temporary bbolt store so
the wire assertions cover the production DTO projection rather than a fake
response shape.

The test covers unsupported protocol versions, successful handshake and raw
wire response inspection, redacted account/request DTOs, owner-only Unix
endpoint permissions, and cancellation propagated to a blocked server call.
The portable test uses Unix sockets on Unix and the existing named-pipe
listener on Windows. The Core API documentation now records this process-level
verification explicitly.

## Motivation

In-process socket tests prove framing and dispatch behavior but cannot prove
that a separately launched service and native client share the endpoint,
handshake, cancellation, and redaction contract. A subprocess test closes
that gap without launching a UI, a real browser, a Matrix homeserver, or live
credentials.

## Test Evidence

`go test ./internal/coretransport -count=1`

`go test -race ./internal/coretransport -count=1`

`go test ./internal/coretransport -run '^TestCrossProcessTransportContract$' -count=10`

`go test ./... -count=1`

`go vet ./...`

`GOOS=windows GOARCH=amd64 go test -c -tags=coretransport_native_test ./internal/coretransport`

`git diff --check`

## Risk

The test starts a child copy of the Go test binary and shares only a temporary
data directory and endpoint string. It terminates the helper during cleanup;
no production data or external service is touched. Windows execution still
depends on the existing native runner and named-pipe implementation, while
Linux verifies the Unix socket mode directly.

## Rollback

Revert the subprocess test and the documentation paragraph. The existing
in-process transport tests and the Core API implementation remain unchanged.

## Breaking Change

None. This change adds verification and documentation only.

## Backport Target

none
