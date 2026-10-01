# CHUZI UI Design Source of Truth

This directory defines the Windows UI design and interaction contract. The
reference image at `references/chuzi-ui-reference.png` is visual direction, not
a pixel specification or product data.

Read `audit.md`, `implementation-plan.md`, `design-principles.md`,
`information-architecture.md`, `interaction-model.md`, `visual-system.md`,
`component-system.md`, and `screen-specs.md` before changing the Windows UI.

The contract applies to the Rust + Slint client. It does not change the account
state machine, queue semantics, credential boundary, browser-worker protocol,
Matrix boundary, or Core storage rules.

The Arch Linux + Codex development path, installed toolchain, optional Slint
MCP, and deterministic verification commands are documented in
[tooling.md](tooling.md). The workflow does not require VS Code.

## Product sentence

CHUZI is a session-first desktop control surface. Its current interaction path
is:

~~~text
Sessions list -> selection -> Inspector -> contextual action
~~~

The client renders redacted Core projections. It does not infer business state
from browser facts or read bbolt, credentials, Profiles, or named pipes
 직접.

## Current UI scope

The active main window contains Sessions and a Settings destination. Sessions
provides Core install/start recovery actions when needed, while Settings groups
appearance, Core lifecycle, launcher update behavior, startup, and diagnostic
privacy guidance. Settings can start an already installed Core service when
the application launches; installation remains an explicit action. Launcher behavior settings are read and saved through the
launcher; the UI theme remains a UI-local preference. The old Overview,
Accounts, Tasks, Adapters, and RDP login pages and their compatibility routes
remain removed. Future screens must be implemented in the current shell.

RDP is a Session Workspace capability. The default host is the main window;
`DesktopRdpWindow` is the optional floating host for the same RDP runtime.
Dock/Float/Hide/Stop change presentation or explicitly end the runtime; they do
not create a second worker or reconnect implicitly. The FreeRDP lifecycle,
credential behavior, framebuffer, and performance capture remain isolated from
the Sessions projection. See `rdp-workspace.md` for the host and capability
contract.

## Sessions projection

Core `list_requests` is the Sessions screen's source. Rows represent persisted
Core requests and show only fields from that redacted projection. The list does
not enumerate accounts without requests and does not claim that a request
corresponds to a currently running browser session. Game, region, runtime, and
elapsed-time details remain omitted because Core does not provide them.

`list_requests` is paginated and bounds its response, but the current Store
reader scans and sorts all persisted requests before Core applies the page
window. Search and status filters operate locally on loaded redacted pages.
Deterministic visual fixtures are isolated to the layout snapshot example and
are not used by production startup.

Sessions loads 100 requests at a time, offers local search and All/Running/
Queued/Failed filters, keeps selection in place, and can load older pages.
Arrow keys, Home, End, Enter, and Escape support list selection and Inspector
behavior. At narrow widths the Inspector opens as an overlay and the sidebar
collapses to a compact rail.

The Rust view model maps Core business states to display labels and allowed
actions. Queued requests can be cancelled with `cancel_request`; running
requests can request an on-demand read-only frame with `get_browser_view`; an
interactive RDP workspace requires a separate authorized capability from Core
or the credential boundary. Status refresh calls `get_request`.
`LOGIN_SUCCEEDED` is labeled Succeeded.
Failure details are reduced to an allow-listed class. The list does not invent
account-only rows or runtime facts.

## Snapshot fixtures

The layout example renders the production view model and Slint components for
the data states plus selected, compact, More menu, cancellation confirmation,
keyboard focus, and disabled-action fixtures. Each state is rendered in Light
and Dark at 800x600, 1120x760, and 1440x900:

~~~bash
for state in mixed empty loading error unavailable mixed-selected settings \
  compact-inspector more-menu cancel-confirmation keyboard-focus disabled-action; do
  cargo run --manifest-path ui/windows/Cargo.toml --features layout-snapshot \
    --example layout_snapshot -- --session-state "$state" \
    --theme both --output dist/ui-sessions
done
~~~

Images are written under `dist/ui-sessions/sessions/<state>/<theme>/`. Fixture
content is synthetic and exists only in the example. The current Store scan
cost and page-local search/filter scope remain known limits.
