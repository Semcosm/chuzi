# CR-0107: reconcile logical job pools after Core changes

Base: main
Head or Range: pending
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: fix(service): reconcile logical job pools after Core changes
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: pending
Head OID: pending
Integrated Result: pending

## Summary

Add a platform-neutral logical slot lifecycle for deployments without the
Windows native job-pool provisioner. Discover every durable pool on each
reconcile pass, make logical slots ready for scheduling, and retire them during
drain/delete so Core operations reach a terminal state. Keep signed environment
authority checks for pools that provide manifest metadata.

## Motivation

Core/UI could create a pool after service startup, but the service only started
the lifecycle loop for a statically configured pool and had no provisioner when
`windows_job_pool` was disabled. Such pools left slots permanently
`unprovisioned` or `retiring`; delete operations remained `draining` and the UI
continued polling until timeout.

## Test Evidence

~~~text
go test ./cmd/service ./internal/slotlifecycle ./internal/store
go vet ./cmd/service
GOOS=windows GOARCH=amd64 go test -c -o <temporary-windows-test-binary> ./cmd/service
git diff --check
~~~

## Risk

Logical mode changes only durable slot projections and never creates Windows
users, processes, profiles, endpoints, or credentials. Windows native pools
continue using the existing provisioner. Signed pools still require the durable
environment authority before logical provisioning.

## Rollback

Revert the service logical reconciler and documentation changes. Existing
durable pool records remain readable; a later reconcile can resume cleanup with
the previous service behavior.

## Breaking Change

None. The Core wire contract is unchanged.

## Backport Target

None
