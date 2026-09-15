# CR-0040: adopt the CHUZI design language

Base: main
Head or Range: ef8174463d7485b6dfd11db26594c359200695e7
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(ui): adopt CHUZI theme and material design language
Revision: 4
Status: accepted
Decision: accepted
Policy Version: v0.3
Base OID: ef8174463d7485b6dfd11db26594c359200695e7
Head OID: ef8174463d7485b6dfd11db26594c359200695e7
Integrated Result: pending

## Summary

Add and deeply restructure the CHUZI Design Language specification for Web UI
and the existing Rust/Wry desktop UI stack, then migrate the embedded launcher
to the new contract. The specification defines the
independent Light and Dark themes, Solid, Frosted Glass, Mica, and Liquid Glass
materials, their eight Theme x Material combinations, monochrome authored
tokens, environment-color boundaries, physical transmission/refraction and
edge-only dispersion rules, typography, spacing, radius, borders, shadows,
elevation, material-specific motion, iOS/macOS control language,
accessibility, token architecture, fallback behavior, and visual review
directions. The launcher now consumes `--cz-*` semantic tokens, removes the
legacy colored palette, uses capability-aware material resolution, and applies
material-specific interaction feedback while keeping the existing IPC and
business behavior unchanged. Link it from the project documentation indexes.

## Motivation

The repository already exposes Theme and Material controls in the embedded
launcher page, but the design rules were not documented as a reusable system
and the earlier visual direction used an uncontrolled palette. The revised
specification follows the actual Rust/Wry plus vanilla HTML/CSS/JS stack,
makes the authored layer strictly black/white, permits only real page/host/
desktop environment color to enter transparent materials, and defines Liquid
Glass dispersion as a bounded physical edge effect. The launcher stylesheet
now uses the semantic token layers, system UI font stack, monochrome status
signals, iOS/macOS control geometry, reduced-effects gates, and distinct
Solid/Frosted/Mica/Liquid motion recipes. Material remains behind semantic
surface roles, Solid is a complete fallback, and optical effects never become
a dependency of content, business state, or the launcher IPC contract.

## Test Evidence

`git diff --check`

`./scripts/validate_cr_record.sh cr/CR-0040-chuzi-design-language.md`

`./scripts/validate_policy_manifest.sh`

`./scripts/validate_quality_profile.sh`

`./scripts/validate_supply_chain_profile.sh`

`./scripts/validate_action_pinning.sh`

`./scripts/validate_repository_shape.sh`

`./scripts/test_build_contract.sh`

`go test ./...`

`go vet ./...`

`./scripts/test_launcher_ui.sh`

`rg` checks confirm all four Material sections define visual intent,
background model, opacity, blur, environment saturation, tint, border, shadow,
highlight, elevation, contrast behavior, interaction behavior, accessibility,
recommended usage, and inappropriate usage. JSON token examples parse
successfully; authored-color scans contain only black/white values; the
document links are checked in `README.md` and `docs/README.md`. The launcher
UI contract test and JavaScript syntax check pass after the token/material
migration; a palette scan confirms that authored UI colors in the page are
black/white only. Application protocols and business behavior are unchanged.

## Risk

The main risk is WebView variation in backdrop support. The launcher therefore
keeps Solid/Mica-like output as a capability fallback, gates Liquid optical
channels behind runtime and user-preference checks, and keeps appearance state
local to the UI. No credentials, runtime data, browser profiles, or business
protocols are changed.

## Rollback

Revert `docs/design-language.md`, `launcher-ui/src/ui.html`, the two
documentation index links, the `v0.0.4` release note, and this pending CR
through a subsequent change.
No storage or business-protocol migration is required.

## Breaking Change

The launcher surface styles and local appearance resolver change visually, but
the Rust/Wry host, Go IPC, and service behavior remain compatible.

## Backport Target

none
