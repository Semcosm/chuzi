# CR-0015: synchronize documentation with implemented boundaries

Base: main
Head or Range: 09a7a6237a97ca6c6e87727cffa4948684f0ad1b
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: docs: synchronize documentation with implementation facts
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 09a7a6237a97ca6c6e87727cffa4948684f0ad1b
Head OID: 09a7a6237a97ca6c6e87727cffa4948684f0ad1b
Integrated Result: pending

## Summary

Synchronize the project documentation with the implementation and checked-in CI
at the current main baseline. The update records that `cmd/service` remains a
temporary Node deferred-worker entry point, that the Go storage/queue/Matrix/
credential boundaries are library-level capabilities not yet assembled by that
entry point, and that the Rust Wry/WebKitGTK helper is built by native CI but is
not the service default. Linux amd64/arm64 WebKitGTK paths have X11/Wayland
smoke coverage; Windows/macOS currently have native Wry compile/package checks
without GUI runtime smoke. It also records the Linux ARM64, profile-retention,
Matrix rendering, and account-deletion facts, and aligns the repository guidance
in `AGENTS.md` with these current boundaries, including the distinction between
 existing artifact packaging scripts and the still-missing production deployment
 workflow.

## Motivation

The existing modules and tests had advanced beyond several design-document and
repository-guidance statements. In particular, the four-target helper build
could be mistaken for default Rust runtime selection, a hidden desktop WebView
could be mistaken for true headless support, and storage/queue capabilities
could be mistaken for a fully assembled persistent service. Keeping these
distinctions explicit makes the roadmap, operational guidance, and repository
instructions auditable against the code and CI without adding runtime behavior.

## Test Evidence

`git diff --check`, the CR validator, the repository policy/quality/supply-chain/
action-pinning/repository-shape and build-contract validators, locked Rust format
and default-feature tests all pass locally. The document-map check is run only
when `.ugs/document-map.json` is present; it is absent in this consumer
checkout. Go and npm are not installed in the local environment, so `go test`,
`go vet`, and the Node worker test cannot run here. Enabling the Rust desktop
feature likewise requires the Linux GTK/WebKitGTK development packages; the
native Wry compile/package checks for Windows/macOS and Linux ARM64
X11/Wayland WebKitGTK smoke evidence remain in the already-integrated
CR-0014-B/C GitHub Actions runs.

## Risk

This is a documentation-only change. The main risk is future wording drift, so
the text explicitly separates normative target contracts from current entry
point behavior and does not claim production browser automation, Matrix
networking, account deletion, or true headless support. No credentials,
database records, profiles, or build artifacts are changed.

## Rollback

Revert the documentation files and this pending record through a subsequent
documentation CR. Existing application code, CI, and integrated CR-0014
records remain unchanged.

## Breaking Change

None. This change clarifies current behavior and planned boundaries only; it
does not alter the Go, Node.js, Rust, storage, or Matrix protocols.

## Backport Target

none
