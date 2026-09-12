# CR-0020: establish the automation adapter and plugin process contract

Base: main
Head or Range: a0c10f06db53a3b0cce6e39291a799e7b2c82e1c
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(adapter): establish automation adapter and plugin process contract
Revision: 2
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 0230ad8426c7c7b5642c755e48c6c5dc43d0231b
Head OID: a0c10f06db53a3b0cce6e39291a799e7b2c82e1c
Integrated Result: pending

## Summary

Establish the transport-neutral business automation adapter contract and the
isolated plugin process boundary. `internal/automation` defines capabilities,
service-derived sessions, operations, redacted results, stable failure classes,
and the versioned `chuzi.adapter/v1` JSONL messages. `internal/plugin` starts
native or Wine-backed plugin processes without a shell, enforces a service-
derived Wine prefix, negotiates capabilities, and handles execute, cancel,
shutdown, timeout, and process-exit paths. The launcher manifest records plugin
capabilities, and the BetterGI directory documents a future communication
bridge without shipping BetterGI or implementing its automation engine.

## Motivation

The service must support composable installations and keep platform-specific
process details out of business adapters. Windows is the first intended native
plugin target. macOS/Linux Wine launch is only a boundary at this stage and
must not be described as supported until the Wine executable, Windows runtime,
GUI session, target plugin version, and real operation smoke tests are
verified. BetterGI integration needs a stable public/official transport before
its communication plugin can be implemented.

## Test Evidence

Added Go unit and helper-process coverage for contract validation, capability
negotiation, normal and classified failure results, cancellation, timeout,
malformed advertisements, crash/exit handling, idempotent close, shell-free
native/Wine invocation, Wine prefix isolation, and rejection of credential
fields in operation/result payloads. Test fixtures use platform-neutral
absolute temporary paths so the same contract tests run on Windows, macOS,
and Linux. Extended the build contract to require the adapter, plugin, and
BetterGI boundary files. Updated architecture, operations, and roadmap
documentation to match the implementation.

Local evidence is limited because this checkout does not provide Go or
gofmt. `git diff --check`, shell syntax checks, and the build-contract checks
are run locally where their dependencies are available. GitHub Actions remains
the authoritative Go compile/test/vet and four-target build evidence. No live
account, credential, BetterGI binary, Wine runtime, or external service is
used.

## Risk

The plugin boundary starts an executable selected by the host and exchanges
JSONL messages. Arguments are passed directly to the OS, Wine prefixes are
service-derived absolute paths, and reserved credential fields are rejected at
the contract boundary. Plugin failures return stable classifications rather
than native error text. This change does not connect a real automation product,
change account business state, or add a production plugin manager.

## Rollback

Stop selecting the new plugin adapter boundary and continue using the existing
browser Worker backends. Revert the adapter/plugin files, launcher capability
field, build-contract checks, documentation, and this CR; no database migration
or runtime data cleanup is required.

## Breaking Change

None to the existing browser Worker protocol or default service backend. The
new `chuzi.adapter/v1` protocol, plugin capability metadata, and process APIs
are additive. No platform or BetterGI support claim is expanded by this CR.

## Backport Target

none
