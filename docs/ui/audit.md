# Windows UI Audit

## Audit scope

The audit covers the Windows client in ui/windows, the launcher/Core boundary
used by that client, and the domain documents that constrain presentation. It
was performed on branch feat/ui-refactor before UI implementation changes.

## Current architecture

| Area | Current implementation | Consequence |
| --- | --- | --- |
| Window | ui/windows/ui/main.slint exports MainWindow and a separate DesktopRdpWindow. | The shell and the RDP workspace are mixed into one markup file. |
| Navigation | MainWindow.page switches between overview, adapters, accounts, tasks, rdp, and settings. | Navigation is page-driven rather than persistent spatial context. |
| Content | Each page is a ScrollView with GroupBox sections and page-local controls. | The UI reads like an operational form/dashboard and has no shared object vocabulary. |
| State bridge | ui/windows/src/main.rs owns AppState, launcher calls, background work, error mapping, settings, Core status, and most callbacks. | Existing asynchronous and redaction behavior is reusable, but the file is a large presentation/controller seam. |
| DTOs | ui/windows/src/models.rs contains redacted account/request, adapter, component, browser-view, and settings models. | DTO models are a useful start; they need a list/selection view model rather than page-specific strings. |
| Runtime workspace | desktop_rdp.rs owns FreeRDP lifecycle, framebuffer handoff, input, and performance capture. | Keep this boundary intact; redesign its host/HUD separately. |
| Visual system | Slint Palette plus scattered literal colors, sizes, and text styles. | Theme support exists, but visual tokens are not centralized. |
| Verification | examples/layout_snapshot.rs renders fixed viewports and checks non-blank output. | Keep the probe and add deterministic states for the new shell and session selection. |

## Reusable assets

- AppState::run_launcher, core_call, and the existing background-operation
  helpers preserve the launcher/Core boundary and must not be duplicated in
  Slint callbacks.
- models.rs projections, friendly_error, and Core availability handling
  already prevent many low-level errors from reaching the user.
- Palette.color-scheme, the layout snapshot example, and the existing Rust
  unit tests provide a useful verification path.
- DesktopRdpController and its performance tests are an independent runtime
  boundary. The redesign should change its surrounding workspace/HUD only.
- Adapter/component lifecycle remains a launcher-owned capability. Its former
  main-window pages and callback glue are removed; any future UI must use the
  shared Inspector/action presentation.

## Components to refactor

- Replace the page-local GroupBox composition with a persistent shell:
  titlebar, sidebar, content host, Inspector, sheets, and overlays.
- Move page state from many unrelated string properties to typed or grouped
  view-model projections where Slint supports them, while keeping domain DTOs
  in Rust.
- Replace the account/request form pages with list + selection + Inspector
  flows. Preserve explicit Core operations and redacted fields.
- Centralize colors, spacing, radii, typography, elevation, and transitions in
  reusable components/tokens instead of adding more literals to main.slint.
- Keep the separate RDP runtime surface behind its own controller. The old
  main-window RDP login page is removed; future Sessions Workspace integration
  can add a new entry without restoring that page.

## Components not to remove

- Core lifecycle and launcher facade calls.
- Launcher/Core lifecycle and facade calls.
- Diagnostic consent and redaction rules.
- The separate RDP runtime, credential handling, and performance-capture
  boundary. The old main-window login page is not retained.
- Layout snapshot support and existing deterministic tests.

## Main conflicts with the target contract

1. The primary object is currently Core readiness, adapter packages, or a
   manually entered ID. The target primary object is a selected Session.
2. The current shell replaces the content area on navigation. The target keeps
   Sidebar, Content, and Inspector visible as coordinated spaces.
3. There is no selection model. A request lookup immediately writes strings to
   a page; it does not update a reusable Inspector.
4. Core now exposes `list_requests` as a bounded, redacted request projection.
   The response is paginated, but the Store reader scans the durable request
   set to preserve global creation-time ordering. The UI can render request
   sessions without claiming account-only or browser-runtime facts.
5. Many possible operations are visible together. The target shows one primary
   action and puts secondary or dangerous actions behind context and menus.
6. GroupBox and bordered cards are used as the default layout primitive. The
   target uses Window, Sidebar, Surface, Inspector, Sheet, Overlay, and HUD
   materials with fewer visible boundaries.
7. The current minimum viewport is 800x600 and the preferred viewport is
   1120x760. The new shell must protect usable Content first and collapse the
   Inspector before compressing the list.

## Domain capabilities available to the UI

- Core status, lifecycle, request submission, request lookup/cancellation,
  account lookup, event/notification queries, browser-view capture, and
  diagnostics are exposed through the launcher/Core facade.
- Store already has deterministic ListRequests; the Core DTO/transport layer
  needs an explicit, bounded read-only list method before the UI consumes it.
- Account state is owned by internal/account; UI labels are projections of
  that state and must not implement transitions.
- Browser workers return runtime facts only. UI must not turn a browser fact into
  a business state or status badge without a Core projection.
- Credentials, Profiles, raw paths, room IDs, and unredacted identifiers are
  outside the UI contract.

## Baseline evidence

The following passed before redesign work:

~~~text
go test ./...
go vet ./...
cargo test --manifest-path ui/windows/Cargo.toml
cargo run --manifest-path ui/windows/Cargo.toml --example layout_snapshot -- --output dist/windows-layout-baseline
git diff --check
~~~

The Windows client baseline has 17 Rust tests and snapshots at 800x600,
1120x760, and 1440x900. The RDP compiler warning about an unused constructor is
pre-existing and does not block the baseline.

## Current implementation after Sessions migration

The main window now has only the Sessions destination, plus conditional Core
install/start recovery, a local theme toggle, and diagnostic consent. The old
Overview, Accounts, Tasks, Adapters, Settings, and RDP login pages, their page
callbacks, DTOs, and snapshot routes are removed. `DesktopRdpWindow` remains a
floating host for the same RDP runtime in the next workspace phase. The default
host will be a docked Session Workspace in MainWindow. See `rdp-workspace.md`
for the host-neutral runtime and capability boundaries.
