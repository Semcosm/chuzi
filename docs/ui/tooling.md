# Linux + Codex UI Tooling

## Scope

The Windows client is developed from an Arch Linux terminal with Codex. The
workflow does not require VS Code or another desktop IDE. Codex may use the
local compiler, test runner, language servers, and deterministic Slint test
examples directly.

The runtime dependency remains pinned in ui/windows/Cargo.toml. Development
tools may be newer patch releases within the same Slint minor line, but they
must be checked when the runtime version changes.

## Installed baseline

The current workstation provides:

- rust-analyzer from the system toolchain;
- slint-lsp 1.18.1 installed with Cargo;
- cargo-nextest 0.9.143 installed with Cargo;
- just 1.58.0 installed with Cargo.

Cargo user binaries live in ~/.cargo/bin. The login shell adds that directory
to PATH; a new shell should expose all four commands directly.

Do not add a VS Code extension requirement. Do not install generic Git,
filesystem, or Cargo MCP wrappers for this project unless a later change
record identifies a concrete capability that the local toolchain cannot supply.

## Slint language tooling

Use slint-lsp for .slint syntax, type, property, and binding diagnostics. Use
rust-analyzer for Rust code, generated Slint bindings, and launcher/Core
integration. Keep the Slint LSP in the same 1.18.x line as the application
runtime when upgrading the UI dependency.

The canonical installation command is:

~~~bash
cargo install slint-lsp --locked
~~~

## Optional Slint MCP

Slint MCP is a development-only runtime inspection path. It is useful when a
running window needs element-tree inspection, screenshots, or deterministic
interaction. It is not a release dependency and must not be enabled in the
packaged Windows client.

Compile-check the feature with:

~~~bash
cargo check --manifest-path ui/windows/Cargo.toml --features slint/mcp --locked
~~~

Run a local debug instance with:

~~~bash
SLINT_EMIT_DEBUG_INFO=1 SLINT_MCP_PORT=8080 cargo run --manifest-path ui/windows/Cargo.toml --features slint/mcp --locked
~~~

The MCP process must remain local. Never expose its port outside the developer
host, and never use it to read credentials, Profile paths, raw browser data,
or production session contents.

Playwright remains a browser-worker/web-surface tool. It is not the primary
verification path for the native Slint window. Figma MCP remains optional;
the current source of truth is docs/ui plus the checked-in reference image.

## Verification workflow

Run the fast Rust checks first:

~~~bash
cargo fmt --manifest-path ui/windows/Cargo.toml -- --check
cargo nextest run --manifest-path ui/windows/Cargo.toml --locked
cargo check --manifest-path ui/windows/Cargo.toml --features slint/mcp --locked
~~~

Generate the deterministic layout matrix with the existing example:

~~~bash
for state in mixed empty loading error unavailable mixed-selected settings \
  compact-inspector more-menu cancel-confirmation keyboard-focus disabled-action; do
  cargo run --manifest-path ui/windows/Cargo.toml --features layout-snapshot --locked --example layout_snapshot -- --session-state "$state" --theme both --output dist/ui-sessions
done
~~~

The snapshot probe verifies dimensions and rejects blank renders. It is not a
golden-image diff yet; visual pixel comparison remains a follow-up tool task.
Windows-only UI Automation smoke tests should run in the Windows CI job and
cover launch, selection, search/filter, Inspector visibility, theme switching,
and keyboard focus behavior.

## Tool selection rules

- Prefer repository-local Rust/Slint examples over Storybook for components.
- Keep reusable component fixtures in ui/windows/examples/.
- Keep screenshots and generated output under dist/; do not commit them.
- Keep MCP and debug instrumentation out of release feature sets.
- Treat cargo clippy --all-targets -- -D warnings as advisory until the
  existing RDP dead-code and lint baseline is cleaned up.
- When adding a new UI capability, update the corresponding design contract,
  snapshot state, and this tooling document in the same change.
