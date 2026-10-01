# Screen Specifications

## Current application shell

The main window always provides the titlebar, Sessions navigation item,
Sessions content, and contextual Inspector. The titlebar appearance control
cycles through the supported theme choices. A bounded feedback bar and the
diagnostic-consent overlay handle operation status and user consent. Core
installation and startup controls appear only when Core is unavailable.

The old Overview, Accounts, Jobs, Adapters, Settings, and RDP login pages are
removed from the application. Do not recreate them as hidden pages or fallback
routes. Later screens must use the shared shell and current object patterns.

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
