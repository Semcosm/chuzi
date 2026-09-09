# CR-0009: implement session runner and browser-worker lifecycle

Base: main
Head or Range: pending
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(browser): implement session runner lifecycle
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: c8e9ca2ad846276981bad6683393d4e719956dcd
Head OID: c8e9ca2ad846276981bad6683393d4e719956dcd
Integrated Result: pending

## Summary

Implement the Session Runner boundary between the durable queue and the
versioned Node.js browser-worker protocol. The change adds service-generated
per-account Profile directories, in-process Profile exclusion, lease
heartbeats, bounded worker cancellation and shutdown, process crash mapping,
durable cancellation observation, and fake-worker plus protocol lifecycle
tests. The current Node worker reports a classified deferred-runtime failure or
synthetic test outcome; no browser binary or real account is introduced.

## Motivation

The queue can claim and retry work, but its Runner boundary has no reusable
worker lifecycle implementation. Without one, worker timeout, process crash,
lease expiry, and cancellation can leave the durable request state disconnected
from the process that owns the account Profile. A narrow runner keeps runtime
facts below the account state machine and gives later browser automation a
stable protocol and recovery contract.

## Test Evidence

The change adds deterministic fake-worker tests for successful completion,
classified failure, crash, context timeout/cancellation, expired leases,
service-generated Profile isolation, durable request cancellation, and queue
integration. Node protocol tests cover session start, cancellation, deferred
runtime failure, and shutdown. CI must run `gofmt`, `go test ./...`,
`go test -race ./...`, `go vet ./...`, `npm --prefix browser-worker test`, the
repository validators, and the four-target build/self-test matrix. The local
restricted shell has no Go or Node toolchain, so local execution evidence is
limited to source and diff checks.

## Risk

Worker lifecycle and protocol messages become a compatibility contract. A
heartbeat or cancellation race can otherwise produce a stale terminal event;
the runner therefore binds every operation to the claimed lease and the queue
ignores terminal failure after a durable cancellation. Profile directories are
hashed and service-derived, but they remain local runtime data and must retain
deployment file permissions. The Node implementation is synthetic/deferred and
does not claim real browser coverage.

## Rollback

Stop the service and preserve the database and Profile directories. Revert via
a subsequent CR; requests in STARTING or LOGGING_IN must be recovered by the
existing queue lease-recovery path before deploying the previous binary. No
schema migration is added by this change.

## Breaking Change

The Worker protocol v1 gains session lifecycle message types and the service
entry point now uses the shared process adapter for its smoke/self-test path.
Existing hello/ping/shutdown messages remain compatible. No real browser
runtime or credential interface is enabled.

## Backport Target

none
