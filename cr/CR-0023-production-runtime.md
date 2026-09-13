# CR-0023: assemble production runtime

Base: main
Head or Range: 8c3fd957c87b1985252d44238300720b8c18716e..a7ec9e09fe69d42c93c9f3bcb8e7ea238be884f3
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(service): assemble production runtime boundaries
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 9d9cc4793a4d62dacc2133c8f4daf228f7dbb52b
Head OID: a7ec9e09fe69d42c93c9f3bcb8e7ea238be884f3
Integrated Result: pending

## Summary

Assemble the existing credential, Matrix, notification, storage, and runtime
boundaries into a UI-neutral production service. Deployment configuration names
Secret environment variables without accepting secret values; credentials can
be injected through an explicit environment source and encrypted by the normal
credential service. A minimal Matrix Client-Server HTTP client supports
`whoami`, long-poll `sync`, and idempotent `send`; the sync gateway reuses the
existing authorized command adapter, while the notifier worker drains the
durable outbox with stable event IDs and retry claims. A redaction-safe health
checker reports service, storage, queue, worker, and Matrix dependency status.
Database backup restore validates schema, permissions, and the config-derived
backup root before atomic replacement. `deploy/` contains a systemd and secret
boundary example. Stable signed releases remain a later change.

## Motivation

The repository already had tested domain boundaries but the service entry point
did not connect credentials, Matrix transport, notifications, health reporting,
or operational recovery. This increment makes the existing contracts usable in
a deployment without adding a UI, real account fixtures, or a release signing
workflow.

## Test Evidence

Added configuration, credential injection, Matrix HTTP, sync gateway, health,
restore, and service assembly tests. Local checks pass:

`go test ./...`, `go vet ./...`, `go test -race ./internal/matrix ./internal/health ./internal/store ./internal/config ./internal/credential ./cmd/service`, `./scripts/test_build_contract.sh`, and `git diff --check`.

Tests use httptest servers, temporary bbolt directories, fake accounts, and
synthetic credentials only. No production Matrix homeserver, access token,
browser, or real account is used.

## Risk

The service now opens a configured Matrix connection when enabled and keeps the
access token in process memory. The token is read only from the explicitly
named environment variable and is never serialized, logged, or returned in
errors. Matrix commands still pass through the existing room/user allowlist;
the HTTP client exposes only the minimum required endpoints. Restore is
restricted to 0600 regular files below the derived backup directory and must
run while the service is stopped. Health responses expose statuses, not error
details. The service remains single-node bbolt and does not execute plugins or
download browsers.

## Rollback

Disable Matrix and health settings, stop the notification/sync workers, and
continue using the previously assembled scheduler entry point. Revert the
runtime assembly, HTTP client, health, restore, deployment examples, tests, and
documentation in a later governance change. Existing database backups remain
valid and no schema migration is introduced.

## Breaking Change

None to the browser Worker, launcher, account state machine, or storage schema.
`config.Config` gains additive deployment sections and `Notifier` gains an
additive `Run` loop. Matrix-enabled deployments must provide the configured
access-token environment variable; default configurations remain offline.

## Backport Target

none
