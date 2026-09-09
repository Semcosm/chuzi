# CR-0010: implement encrypted credential storage and security audit boundary

Base: main
Head or Range: pending
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(credential): add encrypted credential store and audit boundary
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 22b05fd02ad64cff9c2e1c6241a4e65ec1ae2a76
Head OID: 22b05fd02ad64cff9c2e1c6241a4e65ec1ae2a76
Integrated Result: pending

## Summary

Implement the stage-five Credential Store boundary with AES-GCM encrypted
records, deployment-owned key resolution, short-lived callback access, key
rotation, revocation, and metadata-only security audits. Persist ciphertext and
audits in repeatable bbolt schema v3 migrations; keep plaintext, key material,
and live account credentials out of the repository and logs.

## Motivation

The Session Runner now has a stable lifecycle boundary, but no least-privilege
credential interface for a future authorized browser adapter. A credential
implementation must keep keys separate from the database, fail closed when a
key is missing or ciphertext is tampered with, and give operators auditable
store/access/rotate/revoke events without exposing secrets to queue, Matrix, or
browser lifecycle code.

## Test Evidence

Tests cover AES-GCM round trips, ciphertext-only persistence, callback plaintext
clearing, tamper and missing-key failure, historical-key rotation, idempotent
revocation, fail-closed session invalidation, environment key parsing, bbolt
persistence and restart recovery, schema migration repeatability, and
audit/version conflict handling. CI must
run `gofmt`, `go test ./...`, `go test -race ./...`, `go vet ./...`, the
repository validators, and the four-target build/self-test matrix. The local
restricted shell has no Go or Node toolchain, so local execution evidence is
limited to source and diff checks.

## Risk

The credential record and schema v3 become a durable security boundary. AES-GCM
binds ciphertext to account, version, and key ID; the backend never receives
plaintext. Callback access is intentionally short-lived but still requires
callers not to retain the supplied buffer. Environment key resolution supports
one current key; production rotation must use a deployment keyring that retains
historical keys. No browser, Matrix, or production secret integration is added.

## Rollback

Stop the service and preserve the database and external key material. Revert via
a subsequent CR. Databases migrated to schema v3 must be backed up and restored
with a compatible binary; revoked records cannot be recovered because their
ciphertext is deliberately wiped.

## Breaking Change

The bbolt schema advances to v3 and adds `credentials` and
`credential_audits` buckets. Existing v1/v2 databases upgrade repeatably at
startup. A new internal credential API is available, but no existing command or
worker path begins reading credentials.

## Backport Target

none
