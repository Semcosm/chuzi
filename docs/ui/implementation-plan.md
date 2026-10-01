# UI Redesign Implementation Plan

## Objective

Turn the Windows client into a session-first, context-driven desktop surface
with a persistent Sidebar + Content + Inspector shell, while preserving the
launcher/Core boundary and all existing security and domain semantics.

## Non-goals

- No rewrite of internal/account, queue scheduling, Session Runner, browser
  worker, credentials, Matrix, bbolt, or Core business transitions.
- No arbitrary URL, CDP endpoint, Profile path, mouse-control, or keyboard
  control capability added to the client.
- No fake production sessions or screenshot-only implementation presented as a
  working Core integration.
- No macOS/Linux client work in this branch.

## Ordered phases

### Phase 0 - Contract and audit

Deliverables are this docs/ui contract, the reference asset, a current-file
audit, and a recorded baseline. No application behavior changes belong here.

### Phase 1 - Core read-only projections (implemented)

Core exposes `list_requests` through an additive API and transport capability.
It returns deterministic, redacted request DTO pages and documents limits,
empty results, and stable errors. The Store scan is still proportional to all
persisted requests; the response is bounded. The Sessions screen uses this
projection directly and does not perform per-account lookups.

The implementation must not claim runtime fields that the service cannot
provide. If a field is not available, omit it from the DTO rather than inventing
it in Rust/Slint.

### Phase 2 - Design system and shell (implemented)

Create reusable Slint tokens/components and implement the persistent shell:
titlebar, Sessions/Settings sidebar, content list, Inspector, diagnostic
overlay, and responsive collapse behavior. The legacy Overview, Accounts, Jobs,
Adapters, Workspace, and RDP login page routes are removed; Settings is a new
current-shell destination backed by the launcher settings contract.

### Phase 3 - Sessions vertical slice (implemented)

Implement list loading, deterministic fixture states, selection, keyboard
focus, status presentation, contextual primary action, More menu, and a shared
Session Inspector. Selection must update the Inspector without replacing the
window or navigating to a new page. The current list pages through Core
`list_requests` in batches of 100; search and status filters cover loaded rows.
Fixtures render the production Rust view model and Slint components for mixed,
empty, loading, error, and unavailable states in both themes at the three fixed
viewports.

The implemented action mapping follows existing Core calls: queued requests
can be cancelled, in-progress requests can request an on-demand read-only
browser frame, and status refresh reads the current request. A successful login
is presented as Succeeded. Retry, restart, stop, and account-only start actions
remain outside this milestone because the current UI projection does not
provide or authorize those transitions.

### Phase 4 - Future domain surfaces

If account, job, or adapter destinations are added later, implement them in the
current shell with list + selection + Inspector patterns. Do not restore the old
page-local forms or compatibility routes. Preserve explicit trust/enable/remove
semantics and route destructive actions through confirmation sheets/dialogs.

### Phase 5 - Session Workspace

Add the default docked Session Workspace around an authorized interactive
session. Treat `DesktopRdpWindow` as the optional floating host for the same
runtime, not as a second client. Extract a host-neutral RDP runtime before
embedding it; host switching must preserve the worker, framebuffer, input queue,
credentials lifetime, and performance capture. Provide explicit Dock, Float,
Hide, and Stop actions, with closing the floating host mapping to Hide. Keep
runtime content primary, hide controls by default, and expose lightweight HUD
controls. `get_browser_view` remains a separate read-only browser snapshot and
does not become an interactive RDP surface. See `docs/ui/rdp-workspace.md`.

### Phase 6 - Accessibility and visual QA

Continue Light/Dark/System materials, keyboard navigation, focus states,
high-contrast-safe contrast, reduced motion behavior, resizing, and screenshot
QA at fixed viewports. Settings must keep launcher behavior settings separate
from the UI-local theme preference and must not expose credentials or raw paths.

### Phase 7 - Hardening and handoff

Run repository checks, update UI docs/README, remove obsolete page-only code,
record known limitations, and verify that CI/package contracts still see the
required Slint files and launcher boundary.

## File ownership by phase

| Phase | Primary files | Avoid touching |
| --- | --- | --- |
| 1 | internal/coreapi, internal/core, internal/coretransport, internal/store, docs/core-api.md | ui/windows until the contract is tested |
| 2 | ui/windows/ui/main.slint, new token/component Slint files, ui/windows/src view-model glue | Core/domain semantics |
| 3 | Sessions components, models.rs, bounded list callbacks, layout fixtures | RDP internals and launcher protocol implementation |
| 4 | Shared Inspector/action components and account/job/adapter projections | Credentials and Store direct access |
| 5 | Host-neutral RDP runtime, shared workspace surface, dock/float host adapters | New browser input/control paths, duplicated RDP workers |
| 6-7 | UI docs, tests, snapshots, README, packaging checks | Unrelated backend modules |

Phases are sequential. Phases 2-6 edit overlapping UI files and should not be
run concurrently in the shared checkout.

## Acceptance gates

Every phase must leave a buildable checkout and report:

- changed files and a short architecture note;
- commands run and their result;
- screenshots or deterministic snapshot paths for visual work;
- redaction/security implications;
- known limitations and rollback point.

Required final checks remain:

~~~bash
go test ./...
go vet ./...
cargo test --manifest-path ui/windows/Cargo.toml
cargo run --manifest-path ui/windows/Cargo.toml --features layout-snapshot --example layout_snapshot -- --output dist/windows-layout
./scripts/test_build_contract.sh
git diff --check
~~~

For the Sessions visual matrix, enable the example's snapshot feature and pass
`--session-state <mixed|empty|loading|error|unavailable>
--theme both`. The milestone output is stored below
`dist/ui-sessions/sessions/`; the default three viewport sizes are
800x600, 1120x760, and 1440x900. The response from `list_requests` is bounded,
but the current Store scans and sorts all persisted requests before applying
the requested page window.

## Rollback

Each phase is independently revertible. Keep Core contract additions backward
compatible where possible. The previous page routes are removed from the active
client; rollback uses the version-control change set rather than a hidden legacy
route. Keep the separate RDP runtime boundary and diagnostic-consent behavior.
