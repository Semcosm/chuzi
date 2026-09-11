# CR-0019: make Unix artifact checksums portable

Base: main
Head or Range: 2c84b88c86f98ded1349027c0159ec1aa40e6901
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: fix(build): write portable Unix artifact checksums
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 2c84b88c86f98ded1349027c0159ec1aa40e6901
Head OID: 2c84b88c86f98ded1349027c0159ec1aa40e6901
Integrated Result: pending

## Summary

Write Unix `.sha256` sidecars with artifact basenames instead of runner-absolute
paths, and verify every Unix package sidecar in CI immediately after packaging.

## Motivation

The first componentized nightly package contained correct SHA-256 values, but
the Unix sidecars named files using the GitHub runner's absolute workspace path.
After downloading an artifact, a normal `sha256sum -c <file>.sha256` therefore
reported missing files even though the digests were correct. Windows already
writes portable basenames; Unix packaging should provide the same contract.

## Test Evidence

The package script is syntax-checked locally and a temporary stage produces all
component archives whose sidecars pass `sha256sum -c` after packaging. The build
contract validator and `git diff --check` pass locally. The required GitHub
Actions matrix must verify the Unix checksum step on Linux amd64, Linux arm64,
and macOS arm64.

## Risk

This changes only checksum sidecar path text; digest values and archive contents
remain unchanged. The added CI step fails closed if any Unix artifact is missing
or its sidecar does not validate.

## Rollback

Revert the packaging and workflow changes in a subsequent fix. Existing archives
remain readable; only newly generated sidecars use the portable format.

## Breaking Change

None. The sidecar format becomes portable while retaining the standard two-space
SHA-256 filename convention.

## Backport Target

none
