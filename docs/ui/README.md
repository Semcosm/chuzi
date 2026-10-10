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
Settings renders structured redacted job-pool cards and environment cards from
`list_job_pools` and `list_environments`. Operators can apply a pool, scale,
drain, resume, delete, refresh, inspect operation status, and run environment lifecycle
gates through the fixed typed Core method allowlist. Writes read the current
revision, generate a per-action idempotency key, require confirmation for
drain, scale, delete, environment changes, and package changes, and poll the
returned operation through its terminal state. Delete first drains all leases
and waits for managed slot cleanup before removing the pool; it has no force
variant. Long-running deletion remains in the polling state and can be
refreshed by operation ID. Revision conflicts refresh the
projection and show `stale revision`; package and trust/health failures use
stable redacted failure classes. Package input is an opaque service-owned
reference only. The UI never accepts paths, commands, executables, profiles,
credentials, Windows identities, RDP endpoints, named pipes, or tokens.

Settings includes a Windows user pool mode card above Job pools. Saved mode
comes from launcher status; active execution mode comes from Core pool status.
The operator scales every pool to zero, waits for cleanup, refreshes/selects the
signed environment pool, stops Core, and confirms Enable Windows users or Use
logical test mode. The fixed offline launcher command delegates Store locking,
revision, cleanup, and installed runtime checks to Core. Saving requires a
restart and does not install the deployment login service. Existing deployment
configuration survives launcher restarts. Controls require known stopped Core
status and obey the shared busy guard.

Each pool card also offers an explicit Start basic session action. It calls
start_slot_session through the launcher/Core boundary with the current pool
revision, actor, and deterministic idempotency key; Core chooses the first
available execution slot. The UI polls get_slot_session_operation and shows
only slot ordinal, redacted slot/session state, environment generation, agent
readiness, idempotent result, and stable failure classes. This execution slot
session is separate from the business request Sessions list and does not expose
Windows identities, Profile paths, RDP endpoints, or credentials.

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
the data states plus selected, compact, More menu, cancellation and pool-delete
confirmation, keyboard focus, and disabled-action fixtures. Each state is rendered in Light
and Dark at 800x600, 1120x760, and 1440x900:

~~~bash
for state in mixed empty loading error unavailable mixed-selected settings \
  compact-inspector more-menu cancel-confirmation delete-confirmation keyboard-focus disabled-action; do
  cargo run --manifest-path ui/windows/Cargo.toml --features layout-snapshot \
    --example layout_snapshot -- --session-state "$state" \
    --theme both --output dist/ui-sessions
done
~~~

Images are written under `dist/ui-sessions/sessions/<state>/<theme>/`. Fixture
content is synthetic and exists only in the example. The current Store scan
cost and page-local search/filter scope remain known limits.
