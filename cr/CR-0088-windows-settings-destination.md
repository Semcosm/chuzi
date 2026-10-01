# CR-0088: redesign Windows Settings destination

Base: main
Head or Range: 7ce4ca1ad611aef8fa1fa897063f928274a3b24d
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(ui): redesign Windows Settings destination
Revision: 4
Status: integrated
Decision: accepted
Policy Version: v0.3
Base OID: fd7d5c9063285a1112acafa4a0de87a17384afe9
Head OID: fd7d5c9063285a1112acafa4a0de87a17384afe9
Integrated Result: main@fd7d5c9063285a1112acafa4a0de87a17384afe9

## Summary

Add a current-shell Settings destination for the Windows Slint client. The
page groups appearance, Core lifecycle, launcher update behavior, startup, and
diagnostic privacy guidance with the existing material hierarchy. Launcher
behavior settings load through the settings command and save through the
settings-save command; the UI theme remains a separate local preference.

## Motivation

The refactored client removed the former page-local Settings screen even though
the launcher already had a strict behavior-settings contract. Users need a
focused place to control those supported settings without restoring the old
Overview, Accounts, Tasks, or adapter routes.

## Test Evidence

cargo fmt --manifest-path ui/windows/Cargo.toml -- --check,
cargo nextest run --manifest-path ui/windows/Cargo.toml --locked (19 passed),
cargo check --manifest-path ui/windows/Cargo.toml --features slint/mcp --locked,
go test ./..., go vet ./..., ./scripts/test_build_contract.sh,
./scripts/validate_policy_manifest.sh,
./scripts/validate_quality_profile.sh,
./scripts/validate_supply_chain_profile.sh,
./scripts/validate_action_pinning.sh,
./scripts/validate_repository_shape.sh, and git diff --check passed.
The deterministic Settings layout fixture rendered Light and Dark at
800x600, 1120x760, and 1440x900; the snapshot probe confirmed dimensions and
non-blank output.

## Risk

The change stays behind the launcher boundary and does not expose credentials,
browser Profiles, raw filesystem paths, or Core storage. The remaining risk is
platform-specific typography and accessibility behavior; Windows UI Automation
and pixel-level golden comparison remain follow-up work.

## Rollback

Revert the CR-0088 implementation commit and remove the Settings snapshot and
documentation updates. Sessions and the existing launcher contract remain
available.

## Breaking Change

None. Sessions remains the default destination and all Core operations remain
asynchronous through the launcher.

## Backport Target

none
