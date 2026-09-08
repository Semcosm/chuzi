# CR-0006: implement single-node storage and recovery

Base: main
Head or Range: b351fb828802a6cafb254b252bc8966b1716ff9f..b711455e5d8ece69885e9a6922436aed5b63be65
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(storage): implement single-node state storage and recovery
Revision: 5
Status: integrated
Decision: accepted
Policy Version: v0.3
Base OID: b711455e5d8ece69885e9a6922436aed5b63be65
Head OID: b711455e5d8ece69885e9a6922436aed5b63be65
Integrated Result: main@b711455e5d8ece69885e9a6922436aed5b63be65

## Summary

Choose and implement a single-node embedded bbolt storage topology for chuzi.
Add generated data and backup paths, repeatable schema migrations, durable
accounts, requests, state-transition audit records, event idempotency, and
leases. Keep the storage boundary independent from browsers, Matrix, and
credentials while preserving the pure account state machine as the authority
for business-state transitions.

## Motivation

The account domain now has deterministic transition and lease primitives, but
they are not durable across process restarts and there is no request
idempotency boundary. The next roadmap phase needs a transactionally consistent
single-node foundation before queue scheduling or external adapters are added.
An embedded pure-Go database keeps the four declared CGO-free build targets
available without silently introducing a multi-instance consistency contract.

## Test Evidence

Completed locally with config, migration, and store unit/integration tests;
migration repeatability, atomic account/audit/request writes, restart recovery,
expired-lease recovery, duplicate and conflicting requests, duplicate and
conflicting events, and backup creation/reopen are covered. `go test ./...`,
`go vet ./...`, `npm --prefix browser-worker test` (Node 24), all repository
policy, quality, supply-chain, Action pinning, repository-shape,
build-contract, adapter, CR, signer-role, and `git diff --check` validators
pass. The Windows runner's native filesystem permission semantics are covered
by a portable regular-file assertion; Unix permission-bit validation remains
strict. GitHub Actions remains the integration gate for `ugs-validate` and all
four `chuzi-build` targets. PR #11's first build run
(`34246765349`) exposed the Windows-only permission-bit assumption; commit
`81a0806` corrected it, and replacement run `34247491425` passed all four target
jobs and the aggregate `chuzi-build` check. The synchronized PR run
`34247491425`/`34248488863` passed the aggregate build and UGS checks. GitHub's
rebase integration produced `main@b711455e5d8ece69885e9a6922436aed5b63be65`;
post-merge `ugs-validate` run `34249080848` and `chuzi-build` run
`34249080816` also passed. This closure revision binds the record to the
actual integrated main result.

## Risk

This introduces a persistent local database and a schema that future queue and
request components will consume. bbolt uses process/file locking and this CR
claims single-node ownership only; it does not claim safe concurrent writers
across service instances or shared filesystems. Corrupt or incompatible data
must fail closed rather than being silently recreated. No credentials,
browser profiles, Matrix tokens, or live accounts are used by tests.

## Rollback

Stop the service, preserve the database and backup files, and revert through a
subsequent UGS CR. The storage package can be removed without rewriting
application history because no queue, credential, browser, or Matrix runtime
is connected yet. Restore a compatible database backup only after verifying
the schema version.

## Breaking Change

None for the current service command because storage is not yet wired into the
long-running runtime. The new storage API and schema become the normative
foundation for later request, queue, and recovery components.

## Backport Target

None.
