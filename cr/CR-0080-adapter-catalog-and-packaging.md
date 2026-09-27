# CR-0080: distinguish built-in and installable adapter catalog entries

Base: main
Head or Range: feat/adapter-catalog-and-packaging
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(adapter): distinguish built-in and package catalog entries
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 0a8f0c04e5fc12778f259e4fe8bb0a1c09416233
Head OID: 0a8f0c04e5fc12778f259e4fe8bb0a1c09416233
Integrated Result: pending

## Summary

Expose adapters as a release catalog with an explicit built-in or package
distribution. Show the Genshin Cloud Game capability as included with the
Browser Worker, keep package lifecycle controls limited to independently
installable entries, and rename the Windows sidebar and page from Plugins to
Adapters. Add release validation and layout snapshots for empty, built-in, and
package catalog states.

## Motivation

The current client presents the adapter surface as Plugins and reports the
Browser Worker capability as an unavailable, non-installable package. This
makes the Genshin adapter hard to find and suggests that an included capability
needs a download. The release index also needs one consistent rule for which
entries have downloadable artifacts.

## Test Evidence

go test ./..., go vet ./..., npm --prefix browser-worker test, Windows native UI
tests, the layout snapshot example, scripts/test_build_contract.sh,
scripts/test_release_catalog.sh, scripts/test_nightly_package.sh,
scripts/test_nightly_artifact_validator.sh, scripts/test_launcher_consumer.sh,
and git diff --check pass. Manual adapter snapshots are non-empty at 800x600,
1120x760, and 1440x900 for empty, built-in, and package states.

## Risk

The launcher accepts the additive adapter distribution metadata while retaining
legacy plugin command and storage names. Built-in entries follow the health of
their declared source component and cannot be installed, trusted, enabled, or
removed independently. Package entries still require an archive, digest, and
the existing explicit trust flow. Release validators fail closed on malformed
source-component or package-artifact metadata.

## Rollback

Revert the adapter catalog contract, release generator and validator changes,
Windows UI projection and snapshot changes, and the related workflow checks.
The launcher and UI will return to the prior plugin terminology and empty or
package-only catalog behavior.

## Breaking Change

None to launcher command names, persisted state keys, browser-worker protocol,
or existing package installation semantics. The Windows user-facing label
changes from Plugins to Adapters.

## Backport Target

none
