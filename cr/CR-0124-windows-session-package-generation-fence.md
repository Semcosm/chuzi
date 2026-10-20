# CR-0124: align Windows session requests with signed package generation

Base: main
Head or Range: 418b94e19a727509be7fea4988e48fc43818589b (implementation commit)
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: Windows session package generation fence
Revision: 2
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 3fbb027a175118e65ef5d645148d516db4ab16ff
Head OID: 418b94e19a727509be7fea4988e48fc43818589b
Integrated Result: pending

## Summary

Stamp Windows session Start, Inspect, Stop, service-shutdown, and pool Retire
requests with the package generation selected by the provisioner's verified
runtime handoff. Active pool reconciliation uses the signed environment
record; cleanup-only reconciliation uses the already selected generation
from the provisioner, without requiring the environment authority to remain
available.

## Motivation

The installed Windows Core recorded `windows_session / provision` at
`request_validation` for a Start operation. The durable controller created a
request with `PackageGeneration == 0`, then refreshed the signed runtime
handoff to generation 1. The Windows provisioner correctly rejected that
mismatch before WTS, Profile, or Agent work began.

After installing that fix, deleting `pool-test` reproduced the same fence on
the cleanup path. When the target reached zero, `Reconcile` correctly skipped
environment-authority checks so cleanup could still run after package removal,
but it also left the request's package generation at zero. The existing
provisioner held a valid nonzero runtime handoff and rejected `Retire` during
request validation. The generic reconcile error was logged as `cleanup`, the
slot was quarantined, and Delete operation `op_30a338e8109877b7` remained
`draining`. The unchanged ownership record and absent cleanup journal confirm
that no Windows resource deletion had started. The managed SID and NetAPI
ownership marker matched the durable record; this was a request-construction
failure, not an account or Profile failure.

## Test Evidence

- Added a regression test that requires the resolved generation to be used by
  Start, Inspect, and Stop.
- Added `TestReconcileRetireUsesCurrentPackageGenerationWithoutEnvironmentAuthority`;
  it verifies that cleanup carries the provisioner's selected package
  generation even when the environment authority is unavailable.
- Windows host: `go test -count=1 ./...` passed.
- Windows host: `go test -count=1 -race ./...` passed.
- Windows host: `go vet ./...` passed.
- Windows host: service, session-broker, and slotwindows test binaries compiled.
- Policy and quality validators passed with the local Git Bash `jq` wrapper
  that removes Windows CRLF from piped JSON output. Supply-chain, action
  pinning, repository-shape, build-contract, and `git diff --check` passed.
- Built the Windows Core payload and Slint installer locally with the pinned
  PresentMon 2.6.0 binary and the already-installed vcpkg FreeRDP package.
  Inno Setup 6.7.3 produced
  `dist/session-runtime-package-generation-fix-test-20261019-238bf7c/ChuziSetup.exe`
  for version `test-20261019-238bf7cc79c8`, commit
  `238bf7cc79c8b9e25f0efc0f8037af333d45a33d`. The installer SHA-256 is
  `e494f5117f7564327af502d0732f3e2693b2e1e6723b2a5b2e81b12835016a05`;
  its sidecar and all 16 embedded release-manifest resource hashes/sizes were
  verified. The operator installed this package and reproduced the pool
  deletion failure, which led to the cleanup-path fix below.
- Rebuilt the Windows installer for version `test-20261020-418b94e19a72`,
  commit `418b94e19a727509be7fea4988e48fc43818589b`, at
  `dist/windows-session-cleanup-generation-test-20261020-418b94e19a72/ChuziSetup.exe`.
  Its SHA-256 is
  `b59c2ff05a05eba76eac6224d749ea1fdacb99827db1623606d7973e90530ef0`;
  the sidecar and all 16 release-manifest resource hashes and sizes match.
  The installer is not Authenticode-signed because no Windows code-signing
  certificate is configured. It has not yet been installed for native Retire
  verification.

## Risk

Cleanup-only reconciliation reads only the package generation already held by
the service-owned provisioner; it does not trust caller input or require a
ready environment record. If no verified runtime is loaded, the generation
remains zero and the cleanup request does not claim a runtime handoff. The
new code and tests do not yet prove Windows WTS, RDP, Profile, Credential
Manager, ACL, Agent, restart, shutdown, or Retire acceptance. Those native
checks remain pending until the new installer is installed and the cleanup
operation is observed to complete on this host.

## Rollback

Revert the package-generation accessor, session and cleanup request stamping,
and their regression tests. No Store or on-disk schema migration is needed.

## Breaking Change

None. The existing signed package-generation fence remains enforced.

## Backport Target

None.
