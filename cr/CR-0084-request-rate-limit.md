# CR-0084: add service level request rate limiting

Base: main
Head or Range: 6cfe46c2d7f9a7fdcb6507362ac91c6de5b4f305..0ae2fe442549ad1b062dd69ff757f90a748d0acd
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(request): add deterministic service level request rate limiting
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 6cfe46c2d7f9a7fdcb6507362ac91c6de5b4f305
Head OID: 0ae2fe442549ad1b062dd69ff757f90a748d0acd
Integrated Result: pending

## Summary

Add a Request Service sliding window limiter for new submissions. The limiter
supports global, actor, Matrix room, and account dimensions, uses an injected
clock for deterministic behavior, exempts idempotent retries from quota, and
rolls back reservations when durable submission fails. Configuration is loaded
from the redaction safe deployment config and public errors are mapped through
Core and the local JSONL transport.

## Motivation

The request queue already bounds active work, but it does not protect the
submission boundary from bursts or repeated calls. A service level limiter
keeps admission control close to Request Service, preserves the account state
machine as the business state authority, and gives Matrix and native clients a
stable rate_limited classification. Queries and cancellations remain available
while submissions are limited.

## Test Evidence

Focused and repository checks pass on the working tree: gofmt, go test ./...,
go vet ./..., go test -race ./internal/request ./internal/matrix ./internal/observability,
git diff --check, scripts/validate_policy_manifest.sh,
scripts/validate_quality_profile.sh, scripts/validate_supply_chain_profile.sh,
scripts/validate_action_pinning.sh, scripts/validate_repository_shape.sh, and
scripts/test_build_contract.sh. Tests cover sliding window expiry, independent
dimensions, idempotent retries, durable failure rollback, idempotency conflicts,
Matrix continuation after a rate limited event, Core error mapping, configuration
validation, and the dedicated Prometheus counter. No live accounts, credentials,
Matrix tokens, or external browser services are used.

## Risk

Limiter state is process local and resets when the service restarts; the durable
request transaction remains authoritative for idempotency and conflict handling.
The configured dimensions use bounded in memory buckets guarded by one mutex.
Public responses expose only the stable classification and generic message;
operational events and metrics keep request and room identifiers redacted.

## Rollback

Stop the service before rollback, preserve the database and backup files, and
revert the limiter wiring, configuration fields, error code, metrics, and docs
through a later CR. Existing request data remains readable because the limiter
does not add a database schema migration.

## Breaking Change

The Core v1 error enumeration gains rate_limited; clients must preserve
unknown-code handling and may display the generic message. The example
configuration enables global, actor, and room limits for new deployments.

## Backport Target

None.
