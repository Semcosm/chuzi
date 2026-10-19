# CR-0124: align Windows session requests with signed package generation

Base: main
Head or Range: 238bf7cc79c8b9e25f0efc0f8037af333d45a33d (implementation commit)
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: Windows session package generation fence
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 3fbb027a175118e65ef5d645148d516db4ab16ff
Head OID: 238bf7cc79c8b9e25f0efc0f8037af333d45a33d
Integrated Result: pending

## Summary

Stamp explicit Windows session Start, Inspect, Stop, and service-shutdown
requests with the package generation selected by the provisioner's verified
runtime handoff. A successful runtime refresh now updates the request fence
before Provision starts.

## Motivation

The installed Windows Core recorded `windows_session / provision` at
`request_validation` for a Start operation. The durable controller created a
request with `PackageGeneration == 0`, then refreshed the signed runtime
handoff to generation 1. The Windows provisioner correctly rejected that
mismatch before WTS, Profile, or Agent work began. The generic failure then
left the pool with zero ready capacity and the UI showed no Windows session.

## Test Evidence

- Added a regression test that requires the resolved generation to be used by
  Start, Inspect, and Stop.
- Windows host: `go test -count=1 ./...` passed.
- Windows host: `go test -count=1 -race ./...` passed.
- Windows host: `go vet ./...` passed.
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
  verified. The installer is unsigned because no Windows code-signing
  certificate is configured. The package has not been installed yet.

## Risk

The request now carries the generation from the same signed runtime handoff
that the provisioner validates. No caller-supplied generation or package path
is trusted. This is a Core request-construction fix; it does not prove native
WTS, RDP, Profile, Credential Manager, ACL, Agent, restart, shutdown, or Retire
behavior. The current installed Core also reports the interactive helper as
unavailable and the pool has zero ready slots; those remain runtime checks
after the operator installs this package. Windows WTS, RDP, Profile,
Credential Manager, ACL, Agent, restart, shutdown, and Retire acceptance remain
pending. No native session acceptance is claimed by the local build.

## Rollback

Revert the package-generation accessor, explicit session request stamping,
and its regression test. No Store or on-disk schema migration is needed.

## Breaking Change

None. The existing signed package-generation fence remains enforced.

## Backport Target

None.
