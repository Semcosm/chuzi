# Screen Specifications

## Current application shell

The main window always provides the titlebar, Sessions navigation item,
Sessions content, and contextual Inspector. The titlebar appearance control
cycles through the supported theme choices. A bounded feedback bar and the
local-diagnostic overlay handle operation status and local export. Core
installation and startup controls appear only when Core is unavailable.

The old Overview, Accounts, Jobs, Adapters, and RDP login pages are
removed from the application. Do not recreate them as hidden pages or fallback
routes. Later screens must use the shared shell and current object patterns.

Settings is a current-shell destination. It presents appearance, Core
lifecycle, launcher update behavior, startup, and local diagnostic export and
privacy controls.
The Core section can opt into starting an already installed Core service when
the application launches; it never installs Core implicitly.
launcher behavior settings remain separate from the UI-local theme preference.

### Job-pool and environment operations

Settings also contains structured job-pool cards and environment cards. Each
pool card shows the redacted pool and environment identity, desired/ready/
leased/quarantined/draining/provisioning/retiring counts, effective capacity,
environment readiness, reconcile state, last failure class, config revision,
and any current operation ID. Apply, scale, drain, resume, delete, refresh, and
operation polling remain launcher/Core operations; the UI does not read Store
or choose a runtime slot.

Pool writes read the current revision immediately before dispatch and derive a
deterministic idempotency key from the operation payload. Apply, scale, drain,
delete, and environment changes use a confirmation dialog. Delete drains active
leases and waits for managed slot retirement before removing the pool
configuration; it has no force variant. The operation panel shows
the Core state while polling (requested, validating, draining, provisioning,
health_check, committing, applied, or failed) and maps stable failure classes
to bounded recovery text. A stale revision refreshes the projection before
retry.

Environment cards and the environment form expose install, verify, trust,
enable, disable, health, upgrade, and rollback. Package input is an opaque
service-owned reference; local paths, commands, executable names, credentials,
Windows identities, RDP endpoints, and named pipes are rejected or never
represented. Audit history remains outside this screen until Core provides a
redacted audit projection.

## Sessions

~~~text
Sessions
Core request explanation
Search | All / Running / Queued / Failed | Refresh
Request list                         Session Inspector
~~~

Rows use the redacted account label, request ID, business state, attempt,
updated time, and one contextual action. Fields absent from the Core projection
remain omitted. Selection updates the Inspector in place. The Inspector shows
redacted identity, business state, safe failure class when available, the
primary action, and the refresh menu where supported.

Loading, empty, unavailable, and error states preserve the shell. Search and
status filters apply only to loaded pages. Read-only browser snapshots are
requested on demand and never accept input.

## Session Workspace and floating host

An authorized interactive RDP session opens in a Session Workspace inside the
main window. The workspace keeps Session identity and business status in a
compact header, gives the remote surface most of the space, and keeps controls
hidden until requested. `DesktopRdpWindow` can host the same runtime as a
floating window for a second monitor or side-by-side work. Dock, Float, Hide,
and Stop are explicit actions; close on the floating host maps to Hide.

RDP credential handling, framebuffer ownership, input handling, and performance
capture remain behind a host-neutral runtime boundary. The read-only
`get_browser_view` preview remains a separate capability and must not be
presented as interactive RDP.

## Responsive behavior

At compact widths, the Inspector collapses to a toggle/overlay and the Sidebar
collapses to its compact rail. The Sessions list retains usable row width and
primary-action visibility.
