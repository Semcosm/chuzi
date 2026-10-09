# Component System

## Component layers

The current Slint implementation contains the shell, Sessions list/Inspector,
and diagnostic overlay. Account, Job, Adapter, and Workspace components below
are future patterns; they are not retained legacy pages.

### Shell

- ChuziWindow: window background, titlebar, theme, resize behavior.
- Sidebar: primary navigation and Session filters.
- ContentHost: page header, toolbar, list/surface host.
- InspectorHost: selected-object projection and actions.
- SheetHost: confirmation/edit sheets.
- OverlayHost: transient feedback and diagnostic consent.

### Primitives

- Surface, Separator, IconLabel, StatusIndicator, PrimaryAction,
  SecondaryAction, MoreMenu, SearchField, SegmentedFilter, ListRow,
  KeyValueSection, EmptyState, LoadingState, ErrorState.

### Future object components

- SessionRow and SessionInspector.
- AccountRow and AccountInspector.
- JobRow and JobInspector.
- AdapterRow and AdapterInspector.

### Session Workspace integration

- SessionWorkspaceHeader, RuntimeSurface, WorkspaceHud, and
  ConnectionStatusBar.
- RdpSessionRuntimeHost shared by the docked MainWindow workspace and the
  floating DesktopRdpWindow host.

## Boundary rules

- Slint components render properties and emit callbacks. They do not call Core,
  read files, or interpret domain transitions.
- Rust view-model/controller code maps Core DTOs to display projections,
  handles asynchronous launcher calls, and maps stable errors to bounded UI
  feedback.
- models.rs owns deserialization and redacted DTO shapes. Do not put
  store.Request, bbolt types, credentials, Profile paths, or Core transport
  clients in Slint properties.
- Components accept semantic colors, spacing, and typography tokens; page code
  should not redefine them.
- Destructive actions emit an intent that the shell routes through a
  confirmation dialog; they do not execute on a row click.

## State model for the view

The presentation model should distinguish:

~~~text
Loading
Ready(selected item optional)
Unavailable(classified error)
~~~

Selection is a UI concern. Business status is a Core projection. Runtime facts
and browser-view availability are separate fields and must not overwrite the
business status.

## Testability

Every reusable component should be renderable by the deterministic snapshot
example with fixture data. Fixtures must be clearly marked as fixtures and must
not be used by the production startup path. Snapshot states should cover:

- empty/loading/error;
- running, queued, idle, and failed rows;
- selected Session with Inspector;
- collapsed Inspector at compact width;
- Light and Dark themes;
- menu, sheet, focus, and disabled action states.

The checked-in layout fixture names are `mixed-selected`,
`compact-inspector`, `more-menu`, `cancel-confirmation`, `delete-confirmation`,
`keyboard-focus`, and `disabled-action`. They remain deterministic
presentation fixtures and are not reachable from production startup.
