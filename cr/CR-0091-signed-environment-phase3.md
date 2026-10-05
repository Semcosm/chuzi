# CR-0091: implement signed environment packages and phase 3 runtime lifecycle

Base: main
Head or Range: ef44b3550cefa146a0d5a117443210a41e1b1184
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(service): add signed environment package lifecycle
Revision: 4
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 2400241812dda68ee2000b4e38e935b783de11ef
Head OID: adc938f199faa194425f2c9d89c9782c45a06c79
Integrated Result: pending

## Summary

Add the `chuzi-environment/v1` manifest and package validation boundary, explicit
signature/digest/signer trust gates, durable environment lifecycle records, and
phase 3 generation and runtime fencing across slots, agent commands, RDP
capabilities, Core projections, and metrics. The existing `chuzi-adapter/v1`
and browser-worker boundaries remain separate.

The manager exposes explicit `Upgrade`, `Rollback`, and `PromoteReady`
boundaries. `Upgrade` stages and revalidates a signed tree before an atomic
rename, `Rollback` validates both package trees and advances the environment
generation, and `PromoteReady` is the only path that copies a manager-ready
record into the durable Store authority used by service startup. Service-owned
maintenance flags now install, trust, enable, health-check, disable, rollback,
and promote records from the deployment trust store. Windows startup resolves
only the signed manifest's closed worker entrypoint; the package root is passed
to the slot agent while Node and the user-agent binary stay fixed service-owned
runtime files.

## Motivation

The phase 2 logical and Windows slot boundaries need a general trusted
manifest source that cannot be replaced by caller requirements or arbitrary
runtime paths. Environment installation must survive restart, preserve an old
version on failure, and keep installed, verified, trusted, enabled, healthy,
and ready as separate facts.

## Test Evidence

`go test ./...`, `go test -race ./...`, `go vet ./...`, `GOOS=windows
GOARCH=amd64 go build ./...`, `GOOS=windows GOARCH=amd64 go vet ./...`,
`npm --prefix browser-worker test` (22 tests), `git diff --check`, the policy,
quality, supply-chain, action-pinning, repository-shape, and build-contract
validators passed. The schema migration is repeatable and store validation
covers environment package records, pool/slot/lease generation fences, and
request/account/slot lease consistency; focused tests cover trusted-pool
startup gating, staged-tree and current trust-store revalidation, restore and
transaction consistency, provision/inspect health ordering, executable-header
rejection, external-modification detection, and checked rollback. Native
Windows user, ACL, RDP, and smoke behavior remains unavailable on this Linux
host; cross-compilation is not native Windows validation. The package manager's
verified Record must still be promoted through the Store API before service
startup; service assembly intentionally fails closed when that durable ready
record is absent. The service revalidates manager health and synchronizes the
Store projection before every Windows slot reconcile, and capacity projections
re-check durable environment readiness. Expired Windows leases require a
confirmed agent fence before Store recovery; logical slots retain the direct
recovery path.

Latest integration validation at HEAD `ef44b3550cefa146a0d5a117443210a41e1b1184`
passed the Go test and race suites, `go vet ./...`, Windows amd64 build/vet,
Windows package test compilation, the 22-test browser-worker suite, repository
policy/quality/supply-chain/action-pinning/shape validators, the build contract,
and CR validation. Native Windows user, ACL, RDP/session-broker, and signed
package end-to-end acceptance remain pending.

## Risk

The package lifecycle uses an explicit Ed25519 trust allowlist and refuses
shell/executable resources, symlinks, irregular entries, undeclared files,
invalid digests, and unknown entrypoints. A deployment still needs to provide
trusted public keys and a real Windows authorizer/RDP broker before enabling
production interactive access. Package installation and trust-key provisioning
remain deployment integration work; the scheduler will not infer trust from
configuration metadata. The RDP bridge is deny-by-default, and the Windows
provisioner/agent remain behind platform-specific boundaries with fake or
logical implementations used for non-Windows tests. Phase 2 residuals remain
the absence of live-account automation, a production Matrix deployment, and
broader operations integration.

## Rollback

For a package failure, stop or disable the pool, use the manager `Rollback`
operation only after current and rollback trees pass signer, digest, resource,
and trust-store validation, then re-run health and explicitly re-enable the
record. For a code rollback, revert the phase 3 implementation and migration
changes while keeping the durable schema migration in place for existing
stores. Disable the logical pool before removing an environment package.
Never delete a Windows user or package root unless ownership and
external-modification checks pass.

## Breaking Change

The bbolt schema advances to version 7 with an additive environment package
bucket. Pool requirement selection is stricter: a request may narrow
capabilities but cannot replace the pool environment, signer, digest, version,
or trust policy.

## Backport Target

none
