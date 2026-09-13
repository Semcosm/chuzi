# CR-0025: harden observability and operational recovery

Base: main
Head or Range: 9c3bf03032cd6532394eecb27b65449db0502f0b..2dc39888fb5728b4d01c7f7f60188a5fedaa1d6d
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(ops): add redacted observability and recovery checks
Revision: 2
Status: accepted
Decision: accepted
Policy Version: v0.3
Base OID: 9c3bf03032cd6532394eecb27b65449db0502f0b
Head OID: 823c2852a3a98266076a6e64af3fc52713ddf9b3
Integrated Result: pending

## Summary

Complete the next increment after CR-0023 by adding a deployment-neutral,
redaction-safe observability boundary and repeatable operational checks. The
service gains structured JSON logging with bounded rotation, in-memory metrics
with a local Prometheus-compatible endpoint, parallel health probes, a read-only
global audit view, database integrity validation for backup/restore, and
diagnostics for leases, queued work, and the Matrix outbox. The CLI and example
deployment document the checks without adding a network dependency or changing
the account state machine.

## Motivation

The service currently emits ad-hoc process logs, has no durable metric view, and
requires callers to inspect account and credential audit buckets separately.
Health checks execute serially behind a fixed timeout, while restore validates
only the schema version. These gaps make a stuck worker or partial restore hard
to distinguish from a healthy idle service. This increment makes failure modes
visible while keeping identifiers, credentials, cookies, page content, and
provider errors out of logs, metrics, HTTP responses, and audit output.

## Test Evidence

Local verification passed:

- `go test -count=1 ./...`
- `go test -race -count=1 ./...`
- `go vet ./...`
- `go build ./...`
- `npm ci --ignore-scripts && npm run build && npm test` in `browser-worker/`
- `cargo fmt --all -- --check && cargo test --all-targets` in `browser-runtime/`
- `./scripts/validate_policy_manifest.sh`
- `./scripts/validate_quality_profile.sh`
- `./scripts/validate_supply_chain_profile.sh`
- `./scripts/validate_action_pinning.sh`
- `./scripts/validate_repository_shape.sh`
- `./scripts/test_build_contract.sh`
- `git diff --check`

New tests use temporary bbolt directories, fake senders/runners, `httptest`,
and in-memory writers only; they do not use real accounts, Matrix credentials,
Chromium, or external services. Backup tests reopen and fully validate a
copied database, reject symlink targets, and exercise failure before atomic
replacement. Node headless tests use only a fake CDP browser and local test
page; endpoint discovery alone is not treated as business success.

## Risk

The new logger and metrics endpoints are optional and do not become a source of
business state. Log rotation failures are surfaced to the caller instead of
silently discarding events. Health probes run concurrently, propagate caller
cancellation, and bound non-cooperative probe results so a cancelled check
does not block on result delivery; probe implementations are still required to
honor context cancellation. Restore rejects corrupt or incomplete databases
before replacing the active file, and backup publication never writes through
an existing symlink.

## Rollback

Disable the optional metrics listener and structured log output, then revert the
topic change. No schema migration is introduced and no existing database,
credential, browser profile, or Matrix protocol data is rewritten. A failed
restore leaves the active database in place.

## Breaking Change

None to the account, worker, Matrix, launcher, or storage wire contracts.
Health JSON gains only optional duration metadata, and the service's diagnostic
CLI output is additive.

## Backport Target

none
