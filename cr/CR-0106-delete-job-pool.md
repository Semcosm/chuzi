# CR-0106: add safe job pool deletion

Base: main
Head or Range: 32b302a
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(service): add safe job pool deletion
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 762c6fd8809b4da826fab1a82e62b03023f86930
Head OID: 32b302aa66ca9e5354398deb9365e3b86f9c879c
Integrated Result: pending

## Summary

Add the `delete_job_pool` Core operation and expose it through the launcher and
Windows Settings UI. Deletion is asynchronous: the pool first stops accepting
new work, waits for active leases, retires managed slots, and removes the pool
configuration only after every slot is deleted. Operation, idempotency, and
metadata-only audit records remain queryable.

## Motivation

Job pool creation, scaling, draining, and resume were available, but operators
had no safe way to remove a pool and its platform resources. A direct delete
could orphan a Windows user, agent, Profile, or slot lease. The new path keeps
the durable pool until cleanup completes, has no force-delete escape hatch, and
does not recreate a completed deletion from static deployment configuration on
restart.

## Test Evidence

~~~text
go test ./...
go vet ./...
cargo fmt --manifest-path ui/windows/Cargo.toml -- --check
cargo test --manifest-path ui/windows/Cargo.toml --locked --no-default-features
cargo check --manifest-path ui/windows/Cargo.toml --locked --no-default-features
cargo run --manifest-path ui/windows/Cargo.toml --features layout-snapshot --locked --example layout_snapshot -- --session-state delete-confirmation --theme both --output <temporary-directory>
./scripts/validate_policy_manifest.sh
./scripts/validate_quality_profile.sh
./scripts/validate_supply_chain_profile.sh
./scripts/validate_action_pinning.sh
./scripts/validate_repository_shape.sh
./scripts/test_build_contract.sh
git diff --check
~~~

## Risk

The operation changes the Core method allowlist and introduces a destructive UI
action. The UI requires confirmation and uses the existing revision,
idempotency, and operation polling flow. Store finalization is atomic and only
occurs with no leases and only deleted slots. Cleanup failures leave the pool
draining/quarantined for operator recovery; raw platform errors and sensitive
identifiers are not projected.

## Rollback

Revert the implementation commit and CR. Existing pools and operations remain
durable; no force-delete or migration is needed. A pool already finalized as
deleted can be recreated explicitly with a new apply operation.

## Breaking Change

The Core hello capability list gains `delete_job_pool`; older clients continue
to work through capability negotiation. No existing wire method or stored
record is removed.

## Backport Target

None
