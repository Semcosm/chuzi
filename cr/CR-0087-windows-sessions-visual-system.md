# CR-0087: refine Windows Sessions visual system

Base: main
Head or Range: c44a608ea13793d5413b01f58915f178a98a0215
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(ui): refine Windows Sessions visual system
Revision: 2
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 3c977e42773f6ef2e9db831e58c19f3787502a7a
Head OID: c44a608ea13793d5413b01f58915f178a98a0215
Integrated Result: pending

## Summary

Refine the Windows Slint Sessions surface with a semantic Light/Dark visual
system, stronger material hierarchy, explicit status colors, responsive
Inspector behavior, keyboard focus treatment, and destructive-action
confirmation. Add deterministic fixtures and document the Arch Linux plus
Codex UI development and verification workflow.

## Motivation

The Sessions surface had the correct Core boundary but still read as a flat
control panel at narrow widths and did not consistently distinguish primary,
secondary, disabled, or destructive actions. The visual system now gives the
main window, sidebar, list rows, Inspector, overlays, and confirmation sheet
shared tokens and predictable hierarchy while preserving Core-owned state and
launcher action boundaries.

## Test Evidence

`cargo fmt --manifest-path ui/windows/Cargo.toml -- --check`,
`cargo nextest run --manifest-path ui/windows/Cargo.toml --locked` (19 passed),
`cargo check --manifest-path ui/windows/Cargo.toml --features slint/mcp --locked`,
`go test ./...`, `go vet ./...`, `./scripts/test_build_contract.sh`,
`./scripts/validate_policy_manifest.sh`,
`./scripts/validate_quality_profile.sh`,
`./scripts/validate_supply_chain_profile.sh`,
`./scripts/validate_action_pinning.sh`,
`./scripts/validate_repository_shape.sh`, and `git diff --check` passed.
The deterministic layout matrix rendered 66 images for 11 states in Light and
Dark at 800x600, 1120x760, and 1440x900 under
`dist/ui-sessions-final-20261001-v2/`.

## Risk

The change is presentation-focused and adds no Core DTO fields, credential
paths, browser Profile access, or new production capabilities. The main risk
is visual regression at platform-specific font metrics or Windows accessibility
rendering; the snapshot probe checks dimensions and non-blank output, while
Windows UI Automation remains a CI-only follow-up. Existing RDP dead-code
warnings remain outside this change.

## Rollback

Revert the CR-0087 implementation commit and remove the associated UI tooling
documentation and snapshot fixture changes. The existing Sessions Core and
launcher boundaries remain intact.

## Breaking Change

None. Sessions remains the only main-window destination and all destructive
actions continue through confirmation before reaching the existing Core action
boundary.

## Backport Target

none
