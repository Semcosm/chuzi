# CR-0002: Establish the GitHub multi-platform build foundation

Base: main
Head or Range: 6ca7dd8fc30d9e2647f718ff164d6cf3e4eb792d..b42aa31f3924536e9f2b0c5e642dadbb738c05cf
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(platform): establish GitHub multi-platform build foundation
Revision: 5
Status: integrated
Decision: accepted
Policy Version: v0.3
Base OID: b42aa31f3924536e9f2b0c5e642dadbb738c05cf
Head OID: b42aa31f3924536e9f2b0c5e642dadbb738c05cf
Integrated Result: main@b42aa31f3924536e9f2b0c5e642dadbb738c05cf

## Summary

Add the first application build contract for chuzi. The repository now has a
Go service boundary, a Node.js browser-worker protocol boundary, reproducible
target-specific build scripts, and a GitHub Actions matrix for the four
supported release targets: `windows-amd64`, `linux-amd64`, `linux-arm64`, and
`darwin-arm64`.

## Motivation

chuzi is intended to be compiled and packaged by GitHub Actions rather than
depending on a developer workstation for platform-specific builds. The
control service and browser worker need a versioned boundary before real
browser automation, queue scheduling, and credential handling are added.
The first worker only validates the protocol and lifecycle contract; it does
not access real accounts or download production browser data.

## Test Evidence

The branch adds static build-contract validation, Go protocol tests, Node.js
worker tests, and a Go-to-Node handshake smoke test. GitHub Actions executed
those checks on Windows, Linux, and macOS runners for the four declared
targets. The pull-request and main-branch runs passed for `ugs-validate` and
`chuzi-build`; the integrated main result is `main@b42aa31f3924536e9f2b0c5e642dadbb738c05cf`.
Local execution of Go and Node checks remains pending because this checkout's
host does not currently have those toolchains.

## Risk

The first workflow uses the hosted `ubuntu-24.04` runner to cross-compile
`linux-arm64`; it does not claim native ARM64 runtime coverage. Playwright and
Chromium are intentionally deferred behind the worker boundary, so browser
availability and ARM64 browser packaging remain a follow-up decision. The
workflow must not receive production credentials.

## Rollback

Revert the build-foundation change through a subsequent UGS CR, remove
`chuzi-build` from the required checks only after branch protection is updated,
and retain any already published artifacts. No runtime account data is
created by this change.

## Breaking Change

No existing service runtime is changed because application runtime code was
not previously present. The repository now declares Go and Node.js as build
toolchains and requires the `chuzi-build` CI check for protected-branch
integration.

## Backport Target

None.
