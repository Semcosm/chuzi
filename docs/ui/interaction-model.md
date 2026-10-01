# Interaction Model

## Selection path

~~~text
List row focus/click
        -> selected object
        -> Inspector updates in place
        -> contextual action becomes available
~~~

Selection must not reload the whole window or navigate to a detail page. Keep
the selected object visible in the list and expose a clear focus/selection
state.

## Contextual actions

The implemented Sessions action map is deliberately limited to operations
already available through the current Core projection and API:

| Core state | Display label | Primary action | More menu |
| --- | --- | --- | --- |
| `STARTING`, `LOGGING_IN` | In progress | Capture view (`get_browser_view`, on demand) | Refresh status (`get_request`) |
| `QUEUED` | Queued | Cancel request (`cancel_request`) | Refresh status (`get_request`) |
| `LOGIN_SUCCEEDED` | Succeeded | Refresh status (`get_request`) | — |
| `LOGIN_FAILED` | Failed | Refresh status (`get_request`) | — |
| `EXPIRED`, `CANCELLED`, `BLOCKED` | Matching status | Refresh status (`get_request`) | — |
| `NO_REQUEST` | No request | None | — |
| Unknown future state | Unknown state | Refresh status (`get_request`) | — |

Actions are revalidated against the selected Core projection before dispatch.
The UI does not infer transitions from color or browser process state. A
read-only frame can still be unavailable when the request has no active browser
session. Retry, restart, stop, start, and submit are not exposed as Session
actions in this milestone.

## Inspector behavior

The Inspector updates in place when selection changes. In this milestone it
shows the redacted account label and request ID, business status, attempt,
created/updated times, safe failure class when present, the primary action, and
the refresh menu where supported. An on-demand browser frame appears only after
the user requests it. Related account/job/adapter details and recent events
remain future sections because this projection does not provide those facts.

If loading fails, retain the previous safe projection and show a bounded
user-facing error. Search and status filters operate on the loaded pages; the
list loads 100 requests at a time and offers a Load older requests control.
Core's response page is bounded, though the current Store reads and sorts all
persisted requests before Core applies the page window.

## Sheets and dialogs

Use this order of escalation:

~~~text
Inline action > Inspector action > Sheet > Confirmation dialog
~~~

The Sessions milestone exposes cancellation only for queued requests and uses
Core's existing cancellation operation without adding a new stop/restart
command. When future account or session actions are added, route irreversible
or destructive behavior through a confirmation dialog. Ordinary details and
status do not use a dialog.

## Session Workspace

An authorized Open action moves from the list/Inspector shell to a Session
Workspace. The workspace keeps Session identity and status in a compact header,
gives the runtime surface most of the space, and exposes controls as a
hidden-on-idle HUD. The default host is docked in the main window; Float moves
the same runtime to `DesktopRdpWindow`, Dock returns it, Hide preserves it, and
Stop releases it. Host changes do not reconnect or create a second worker.

The current read-only browser snapshot remains on-demand and visually distinct;
it does not gain an input channel. Interactive RDP requires a separate
authorized capability and must not be derived from the browser-view DTO.

## Keyboard and focus

- Sidebar, list rows, Inspector actions, menus, sheets, and dialogs have a
  logical tab order.
- In Sessions, Up/Down and Home/End move the selected row; Enter invokes its
  enabled primary action; Escape closes the compact Inspector overlay.
- Row selection moves keyboard focus to the selected row and updates the
  Inspector in place.
- Focus remains visible in light, dark, and high-contrast environments.
- Destructive commands require the same confirmation path for keyboard and
  pointer input.

## Animation

Animations communicate continuity and feedback only: selection movement,
Inspector replacement, sheet/overlay entry, and status changes. Keep transitions
short and provide a reduced-motion path. Do not use bounce, large zoom, or
long page transitions.
