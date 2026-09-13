# CR-0021: implement the first headless-CDP automation adapter

Base: main
Head or Range: d532dbcc2da9203ffbde79275c4a308b1569e7fe..7ed60fbe317e7181c9981892be4ec04c790841ae
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(adapter): implement first headless-CDP local test adapter
Revision: 3
Status: integrated
Decision: accepted
Policy Version: v0.3
Base OID: cb612692d779460ebd6f5b30cf0016cb65bb0598
Head OID: cb612692d779460ebd6f5b30cf0016cb65bb0598
Integrated Result: main@cb612692d779460ebd6f5b30cf0016cb65bb0598

## Summary

Add the first `chuzi.adapter/v1` implementation on top of the existing Node
headless-CDP lifecycle. The adapter starts the lifecycle worker, validates its
loopback CDP metadata, connects to the verified WebSocket endpoint, serves only
an in-repository local test page, and executes a deterministic
`local.test_page_probe` operation for a fake account. It reports success only
after the page marker and account identifier are read through CDP. Endpoint
discovery alone is never a business success.

## Motivation

CR-0017 established browser process and CDP endpoint discovery, while CR-0020
established the transport-neutral automation contract. This increment connects
those boundaries with one narrow operation so the operation lifecycle,
cancellation, error classification, and success semantics are testable without
production accounts, credentials, network services, or a bundled browser.

## Test Evidence

Added a fake local CDP WebSocket fixture and Node contract tests covering
capability negotiation, local test-page success with a fake account, unsupported
operations after endpoint discovery, marker failure, cancellation, and clean
shutdown. Added Go `internal/plugin.Client` coverage that launches the adapter
as a real child process and verifies the same success/failure facts. The build
contract requires the adapter and local page, and the worker manifest records
the adapter entry point. The adapter keeps stdin alive before the first
request; the test reader queues every JSONL message and reports child
`error`/`exit` instead of waiting indefinitely; every wait also records its
expected message, recent protocol trace, child status, and stderr tail, with
periodic reports enabled by `CHUZI_TEST_DEBUG=1`. Contract tests run serially
with opt-in, stderr-only diagnostics, while production worker stderr remains
discarded by default. Local evidence includes `node --check`, repeated Node
contract runs with diagnostics, `GOCACHE=/tmp/chuzi-go-cache go test -count=1
-v ./...`, `go vet ./...`, Rust tests with `--nocapture`, `git diff --check`,
and `./scripts/test_build_contract.sh`. No Chromium is downloaded or
packaged.

## Risk

The adapter launches only the existing service-configured headless worker and
passes arguments directly. It binds to loopback, accepts service-derived
absolute Profile paths, serves a fixed local page, rejects credential-shaped
operation parameters, and emits only stable classified failures and redacted
facts. The adapter does not access credentials, navigate to production URLs,
write account state, or infer business success from a CDP endpoint.

## Rollback

Stop selecting the adapter entry point and retain the CR-0017 headless runtime
and CR-0020 contract. Remove the adapter files, fixture, manifest entry, tests,
and documentation in a subsequent governance change; no migration or runtime
data cleanup is required.

## Breaking Change

None to the existing browser Worker protocol or default service backend. The
new adapter operation and manifest entry are additive and intentionally limited
to local test-page validation.

## Backport Target

none
