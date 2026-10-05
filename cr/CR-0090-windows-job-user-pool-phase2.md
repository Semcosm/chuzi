# CR-0090: implement Windows job user pool and controlled slot agent

Base: main
Head or Range: 1fd826029ce456f254907d8c4a57819c4307a5b6
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(service): add Windows job user pool and controlled slot agent
Revision: 4
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 2400241812dda68ee2000b4e38e935b783de11ef
Head OID: ead5e74bb6935bfd282b4bc6689cba44d2678013
Integrated Result: pending

## Summary

Implement the Phase 2 Windows execution boundary: durable logical slots, a
Windows-only managed local-user provisioner, session-aware process startup,
per-slot Job Objects, a closed named-pipe user-agent protocol, lifecycle
reconciliation, profile ACL grants, RDP capability revocation, and redacted
Core and operational projections. Non-Windows builds keep the logical slot
interfaces and return `slotwindows.ErrUnsupported` for OS-backed provisioning.
The logical implementation is complete enough to start Phase 3, but this CR
does not claim production acceptance until the Windows native smoke gate and a
real credential-boundary RDP authorizer are available.

## Motivation

A logical execution slot needs a service-owned Windows identity, isolated paths,
validated session and desktop state, and bounded process cleanup before it can
run a browser worker or adapter. The boundary must keep usernames, SIDs,
passwords, Profiles, endpoints, and native error text out of Core, Matrix,
worker payloads, and logs.

## Test Evidence

`go test ./...`, `go test -race ./...`, `go vet ./...`,
`GOOS=windows GOARCH=amd64 go build ./...`, `GOOS=windows GOARCH=amd64 go vet ./...`,
and Windows `browser`/`slotwindows`/`slotagent` test-binary compilation passed.
Browser-worker `npm test` passed (22 tests), as did the policy, quality,
supply-chain, action-pinning, repository-shape, and build-contract validators,
`scripts/validate_cr_record.sh`, and `git diff --check`. The optional
document-map validator was not applicable because this checkout has no
`.ugs/document-map.json`. Native Windows provisioning, ACL, RDP, and
disposable-user smoke tests require a Windows runner and were not available on
this Linux host.
The closeout also validates account/request and account/slot lease projection
links in `ValidateDatabase`; lifecycle reconcile failures now emit the stable
`slot/reconcile` classification, `chuzi_slot_reconcile_errors_total`, and a
failing `slot_lifecycle` readiness check.

Revision 4 records the later Windows native smoke evidence in the phase 4
acceptance record: the managed user, Profile and ACL, named pipe, desktop,
process launch, Job Object, RDP session, worker lifecycle, and retirement
checks passed. Production RDP authorization remains a separate deployment gate.

## Risk

The OS boundary is Windows-only and uses service-derived paths, identities,
desktop names, runtimes, and leases. Managed user deletion requires ownership
metadata and cleanup ordering; unknown users and reparse points are refused.
Service and worker Job Objects use kill-on-close fencing so restart cannot leave
an agent process tree behind. Native Windows behavior remains the primary
platform-specific verification risk until a Windows CI smoke runner is used.
The production service still wires `credential.DenyRDPAuthorizer{}` by default,
so RDP capability issuance remains unavailable. Generic environment manifests,
trust-store verification, plugin injection, rolling upgrade, and rollback are
explicit Phase 3 scope.

## Rollback

Revert the Phase 2 implementation and migration changes, then leave existing
logical slot records disabled or migrate them through the durable store rollback
procedure. Do not remove users or directories without the provisioner's
ownership checks.

## Breaking Change

None for deployments without `windows_job_pool.enabled`; the new configuration
and Core projections are additive. Enabling the Windows pool requires the
packaged Windows user-agent, packaged `node.exe` runtime, and a matching
logical job-pool configuration. Production enablement remains gated on native
Windows smoke evidence and an approved RDP authorizer integration.

## Backport Target

none
