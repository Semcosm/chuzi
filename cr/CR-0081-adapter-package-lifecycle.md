# CR-0081: package the Genshin adapter and enforce its lifecycle

Base: main
Head or Range: feat/genshin-adapter-package
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(adapter): package Genshin adapter with controlled lifecycle
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: b248ac2986adf356a8effdc51bb289f655d6c843
Head OID: 5307b50b267f3a547554e92267ffd8ce6f5912cb
Integrated Result: pending

## Summary

Move the Genshin Cloud Game business entrypoint out of the built-in Browser
Worker capability list and publish it as a `chuzi-adapter/v1` package. The
package has a self-describing manifest, target archive, resource hashes,
capabilities, permissions, and an explicit signer. The service resolves it
only from its managed adapter directory after all lifecycle gates pass.

The launcher keeps installation, verification, signer trust, enablement,
update, rollback, and removal separate in durable state. The Windows client
uses Adapter terminology and exposes package metadata and lifecycle status.

## Motivation

The previous catalog made the Genshin capability appear as a non-installable
entry attached to Browser Worker and presented the surface as Plugins. This
made the independent package unavailable in the release catalog and obscured
the trust boundary. A package boundary is needed before adding more business
automation adapters.

## Test Evidence

The implementation is covered by `go test ./...`, `go vet ./...`,
`npm --prefix browser-worker test`, `./scripts/test_build_contract.sh`,
`./scripts/test_nightly_package.sh`, `./scripts/test_nightly_artifact_validator.sh`,
`./scripts/test_release_catalog.sh`, Windows Slint tests, adapter lifecycle
tests, package archive safety tests, and `git diff --check`. Tests use fake CDP,
local pages, synthetic archives, and synthetic account data only.

## Risk

The launcher retains legacy `plugin-*` command and persisted state names for
compatibility while presenting adapters to users. A package must be installed,
verified, trusted, and enabled before the service can start it. Archive
validation rejects traversal, symlinks, duplicate members, undeclared files,
and checksum mismatches. No package contains Chromium, Profile data, Cookie,
credentials, or tokens.

## Rollback

Revert the adapter manifest/Registry, launcher package lifecycle, release
archive/index changes, service loading, and Windows Adapter projection. The
prior built-in Genshin capability and legacy UI catalog can then be restored
from the parent commit. Failed package updates already preserve the previous
installed package.

## Breaking Change

No Worker JSONL protocol or launcher command names are removed. The Windows
user-facing sidebar and catalog terminology changes from Plugin to Adapter,
and Genshin is no longer advertised as a Browser Worker built-in capability.

## Backport Target

none
