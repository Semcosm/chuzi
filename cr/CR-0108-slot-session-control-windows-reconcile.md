# CR-0108: durable slot-session control and Windows pool reconciliation

Base: main
Head or Range: 31f9ccb0e31eb4b8e7db79cc58ab6a4d942e6cff
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(service): reconcile Windows pools and expose slot-session operations
Revision: 3
Status: integrated
Decision: accepted
Policy Version: v0.3
Base OID: b84e035560506ee970337f1c9bf55b9759504783
Head OID: b84e035560506ee970337f1c9bf55b9759504783
Integrated Result: main@b84e035560506ee970337f1c9bf55b9759504783

## Summary

Add durable, idempotent slot-session start operations to Store, Core, local
transport, and Launcher. Reconcile every Windows-backed durable pool, including
pools created after service startup, while resolving each signed runtime through
the service-owned environment boundary. Add the fixed, ACL-protected session
broker listener that delegates real session creation to an injected deployment
adapter.

## Motivation

Operators needed a redacted asynchronous operation to request a slot session
and observe its provision result. Windows services also needed to pick up
durable pool changes after startup without accepting runtime paths or commands
from Core inputs.

## Test Evidence

Passed: `go test ./...`; `go test -race ./internal/slotwindows`; `go vet ./...`;
Windows-targeted `go vet` for service, slotwindows, Core, transport, and Store;
Windows-amd64 test compilation for those five packages; policy, quality,
supply-chain, action-pinning, repository-shape, and build-contract validators;
and `git diff --check`. `.ugs/document-map.json` is absent in this checkout, so
the conditional document-map validator was not applicable. Native listener and
WTS smoke require a Windows deployment with a real broker adapter and are not
executable on the Linux development host.

## Risk

The schema advances to version 10 with operation and idempotency buckets.
Windows runtime selection remains service-owned and signed; the listener grants
only the configured service and broker SIDs access to the fixed pipe. A real
session provider remains deployment-owned.

## Rollback

Revert the Core, service, and Windows reconciler changes while retaining the
additive migration. Existing slot-session records remain readable and no
credential, user, profile, or session data is stored by these operations.

## Breaking Change

Core v1 adds discoverable methods and bbolt schema version 10. Existing Core
methods and clients remain compatible.

## Backport Target

None
