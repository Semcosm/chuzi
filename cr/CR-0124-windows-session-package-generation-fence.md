# CR-0124: align Windows session requests with signed package generation

Base: main
Head or Range: implementation commit to follow
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: Windows session package generation fence
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 3fbb027a175118e65ef5d645148d516db4ab16ff
Head OID: pending
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
- Local installer build and SHA-256 evidence pending.

## Risk

The request now carries the generation from the same signed runtime handoff
that the provisioner validates. No caller-supplied generation or package path
is trusted. This is a Core request-construction fix; it does not prove native
WTS, RDP, Profile, Credential Manager, ACL, Agent, restart, shutdown, or Retire
behavior. The current installed Core also reports the interactive helper as
unavailable, which remains a later startup prerequisite.

## Rollback

Revert the package-generation accessor, explicit session request stamping,
and its regression test. No Store or on-disk schema migration is needed.

## Breaking Change

None. The existing signed package-generation fence remains enforced.

## Backport Target

None.
