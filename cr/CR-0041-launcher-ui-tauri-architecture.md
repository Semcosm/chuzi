# CR-0041: modularize and migrate the launcher UI architecture

Base: main
Head or Range: cc5b662f17c7c9c0a432243c19bffd1de68c8258
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: refactor(launcher): modularize UI and migrate desktop shell architecture
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 61f244390bc52de749eda42a706b89560b2f2294
Head OID: cc5b662f17c7c9c0a432243c19bffd1de68c8258
Integrated Result: pending

## Summary

Refactor the launcher UI from the embedded `ui.html` monolith into a modular
TypeScript frontend with explicit app, view, component, feature, state, IPC,
design-token, material-runtime, and utility boundaries. Migrate the desktop
host to a Tauri 2 command boundary while retaining the existing shell-free Go
launcher process contract and `chuzi.launcher-ui/v1` request semantics.

This is an architectural migration only. The existing CHUZI visual design,
Theme/Material model, component behavior, page information architecture,
accessibility behavior, launcher business policy, and release component
semantics remain unchanged. The migration must preserve the current visual and
interaction baseline; any host-specific rendering difference is corrected for
parity rather than used as an opportunity to redesign the UI.

## Motivation

The current launcher page combines markup, CSS, state, request scheduling,
rendering, appearance resolution, material motion, and event wiring in one
embedded document. The Rust host similarly combines desktop setup, IPC
validation, process lifecycle, progress forwarding, cancellation, and
diagnostics in one module. These boundaries make the existing surface hard to
extend under the CHUZI frontend architecture rules and prevent a typed Tauri
command/service boundary.

The existing Go launcher and the design-language implementation are already
stable contracts. Separating the frontend and host responsibilities allows the
UI to evolve without duplicating installation, trust, rollback, or filesystem
policy, while keeping the current visual language as the source of truth.

## Test Evidence

The completed change must include:

`cargo fmt --manifest-path launcher-ui/Cargo.toml -- --check`

`cargo test --locked --manifest-path launcher-ui/Cargo.toml`

`cargo clippy --locked --manifest-path launcher-ui/Cargo.toml --all-targets -- -D warnings`

`npm --prefix launcher-ui ci --ignore-scripts`

`npm --prefix launcher-ui run typecheck`

`npm --prefix launcher-ui test`

`./scripts/test_launcher_ui.sh`

`go test ./... -count=1`

`go vet ./...`

`./scripts/test_build_contract.sh`

`git diff --check`

The UI contract tests cover request ordering, cancellation, error recovery,
state refresh/update scheduling, Theme/Material capability fallback, and the
existing token/material source modules. The migration keeps the existing
overview, components, plugins, and settings markup and CSS as the visual
baseline; viewport and accessibility behavior remain represented by the
existing material runtime tests without introducing a new palette or layout.

## Risk

The largest risks are asset-loading differences between the current inline Wry
document and Tauri's local asset protocol, accidental IPC payload drift, and
platform WebView differences during the shell migration. The frontend service
therefore keeps a typed compatibility adapter for `chuzi.launcher-ui/v1`; views
and components never call Tauri commands directly. Rust commands use a narrow
capability allowlist and continue to validate requests before spawning the
existing Go CLI with direct arguments. No credentials, plugin code, arbitrary
filesystem paths, or external navigation are exposed to the WebView.

The migration is staged inside this single CR: first establish the modular
frontend and shared launcher service, then switch the desktop host and release
build to Tauri 2, and finally remove the obsolete inline bridge after parity
tests pass. The Cargo manifest remains at `launcher-ui/Cargo.toml`; the
frontend is built into the ignored `launcher-ui/frontend/dist` directory before
Tauri compilation.

## Rollback

Revert the launcher UI source, Tauri host, frontend build configuration,
release/build-script changes, tests, and this CR in one subsequent change.
Continue using the existing Wry launcher UI and `chuzi-launcher` CLI. No
storage, credential, account-state, release-manifest, or Go launcher data
migration is permitted.

## Breaking Change

None to the Go launcher commands, `chuzi.launcher-ui/v1` request semantics,
release manifest format, service, browser Worker, account state machine,
credential store, Matrix boundary, or visual design language. The desktop
implementation and build inputs change internally; the shipped launcher UI
must retain the existing user-visible behavior.

## Backport Target

none
