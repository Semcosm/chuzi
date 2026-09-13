# CR-0022: implement launcher component and plugin management

Base: main
Head or Range: bdb10819990b7cfe53f3c8cce8d757089a88a223
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(launcher): implement component and plugin management
Revision: 2
Status: accepted
Decision: accepted
Policy Version: v0.3
Base OID: 76ccc4f794f64cd4e41c00479c6405636d11116d
Head OID: 76ccc4f794f64cd4e41c00479c6405636d11116d
Integrated Result: pending

## Summary

Implement the existing UI-neutral `internal/launcher` interfaces. The first
backend provides a local manifest source and update checker, hash/size based
resource repair, dependency-aware component installation and enablement, and
plugin archive installation with explicit signer trust. State is persisted as
metadata under the installation root. Resource and archive replacement use
temporary paths and atomic rename; failed state commits restore the previous
files and metadata.

## Motivation

The release manifest and launcher contracts currently support only display and
read-only verification. A service or future UI needs deterministic background
operations before a GUI is selected. This increment keeps all filesystem and
trust policy behind Go interfaces and deliberately avoids coupling to HTTP,
GUI toolkits, package managers, or plugin execution.

## Test Evidence

Added tests for update target/channel validation and version comparison,
trailing manifest rejection, dependency/resource ambiguity, staged repair with
no partial commit, component dependency installation and state reload,
required-component protection, state-commit rollback, plugin archive hash
verification, explicit trust before enablement, trust persistence, and safe
archive extraction. Local checks pass:

`go test ./...`, `go vet ./...`, `./scripts/test_build_contract.sh`, and
`git diff --check`.

The CLI supports manifest display and verification plus local `check-update`,
`repair`, component install/remove/enable/disable/list, and plugin
install/remove/enable/disable/trust/untrust/list commands. Tests use temporary
directories and synthetic archives only. No network source, live account,
credential, browser, or plugin process is used.

## Risk

The backend mutates an operator-selected installation root, so all manifest
resource paths, plugin IDs, archive entries, and state paths are constrained to
safe relative or root-contained paths. Installable plugins require an archive
SHA-256 and remain disabled/untrusted until a declared signer is explicitly in
the configured allowlist. Updating a plugin clears prior trust. Archive
extraction rejects non-regular entries. State files are mode 0600 and contain
no credentials. The implementation does not execute plugin code or download
updates; network signature verification remains a later boundary.

## Rollback

Disable launcher management commands and continue using manifest display and
read-only verification. Revert the launcher backend, CLI flags, contract
extension, tests, build-contract checks, and documentation in a later
governance change. No database migration or runtime cleanup is required; the
launcher state file and plugin directory can be removed by the operator after
stopping the launcher.

## Breaking Change

None to the service, browser Worker, automation adapter, or release package
format. `PluginManager` gains the additive `SetTrusted` operation required for
explicit trust; existing manifest fields remain compatible, while malformed
installable plugin descriptors are now rejected before use.

## Backport Target

none
