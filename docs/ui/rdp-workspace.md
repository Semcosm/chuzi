# RDP Session Workspace

## Decision

An RDP connection is one session with two presentation hosts:

~~~text
Session Workspace in MainWindow  <->  Floating RDP Window
                 \ same runtime, framebuffer, input queue, and lifecycle
~~~

The main window is the default host. `DesktopRdpWindow` remains useful as the
floating host for users who need the remote desktop beside another application
or on another monitor. It is not a second RDP client and it must not create a
second FreeRDP worker when the host changes.

The host switch is a presentation operation:

- `Dock`: render the active session in the main-window workspace.
- `Float`: detach the same active session into `DesktopRdpWindow`.
- `Hide`: keep the session alive and hide its presentation host.
- `Stop`: explicitly release the RDP runtime and credentials.

Closing the floating window is equivalent to `Hide` unless the user chooses
`Stop`. This prevents an accidental window close from terminating a remote
session. A reconnect is an explicit runtime action and is never implied by a
host switch.

## Why the current window cannot simply be embedded

The current `DesktopRdpController` owns the FreeRDP worker, input channel,
frame timer, framebuffer handoff, performance capture, and callbacks typed to
`DesktopRdpWindow`. `MainWindow` has no RDP surface properties or input callbacks,
and the current Core projection exposes no authorized interactive RDP target.
Embedding the existing component directly would either lose keyboard/pointer
input or duplicate the controller and its worker.

`get_browser_view` is a different capability. It is a bounded, ephemeral,
read-only image from an active headed/headless browser request. It has no input
channel and must not be presented as an interactive RDP session.

## Target boundaries

### Runtime

Extract a host-neutral `RdpSessionRuntime` from `desktop_rdp.rs`. It owns:

- one validated `RdpTarget` and the ephemeral credential lifetime;
- one FreeRDP worker and input queue;
- framebuffer updates and the performance/HUD counters;
- deterministic states: `starting`, `connecting`, `connected`, `failed`,
  `closing`, and `closed`.

The runtime exposes redacted state snapshots and frame updates. It does not
know whether the active host is embedded or floating. It never persists the
password, host, certificate, or pixels.

### Hosts

Create a shared Slint `RdpWorkspaceSurface` with the existing visual tokens,
focus behavior, pointer/scroll/key callbacks, status HUD, and compact controls.
`MainWindow` hosts it in a Session Workspace region. `DesktopRdpWindow` hosts
the same surface with native window chrome and optional always-on-top behavior.

Host adapters own only:

- attaching and detaching the surface;
- window geometry and visibility;
- Dock/Float/Hide/Stop intents;
- forwarding input callbacks to the runtime and runtime frames to Slint.

There must be at most one active host at a time. A hidden session has no host
but keeps its runtime state until `Stop` or an explicit failure cleanup.

### Core and credentials

The current `list_requests` and `get_browser_view` contracts cannot start an
interactive RDP session. The workspace must not derive a host, username,
password, certificate policy, Profile path, or arbitrary endpoint from those
DTOs. Before production launch from Sessions, Core or a credential boundary
must provide an authorized, short-lived RDP capability bound to the selected
session. The UI may collect an explicit connection intent, but it must not
persist raw credentials or bypass Core authorization.

## Current implementation status

The host-neutral runtime and capability contract are implemented.
`RdpSessionRuntime` owns one worker, framebuffer state, input queue, and
performance capture, while `MainWindow` exposes the shared workspace surface
behind a visibility gate. The surface emits Dock, Float, Hide, and Stop intents;
the runtime test verifies that host changes do not create a second worker and
that Stop is terminal. Core exposes `issue_rdp_capability`, and the credential
boundary enforces actor binding, short expiry, revocation, cancellation, and
redacted serialization.

The production service has not yet wired a deployment-specific RDP authorizer
or a material handoff into the Windows runtime. Until that provider exists, the
Sessions Inspector does not show an Open RDP action. This is intentional: a
read-only `get_browser_view` preview remains the only interactive-session
adjacent action available from the current projection.

## Ordered implementation

1. **Shared surface**: move the current RDP visual markup into a reusable Slint
   component and add deterministic `starting`, `connected`, `failed`, docked,
   and floating fixtures. Keep the current floating window behavior intact.
2. **Runtime split**: extract `RdpSessionRuntime` and make the existing
   `DesktopRdpController` a floating-host adapter. Add a MainWindow host adapter
   without changing FreeRDP protocol or security behavior.
3. **Workspace routing**: add a Session Workspace entry from an authorized
   session action. Default to docked mode; provide `Float`, `Dock`, `Hide`, and
   explicit `Stop` actions. Preserve selection and Core status in the compact
   workspace header.
4. **Capability contract**: define the Core/credential response that authorizes
   an interactive RDP target. Add expiry, revocation, redaction, and failure
   handling before enabling production launch.
5. **Visual QA**: cover 800x600 compact docked mode, 1120x760 and 1440x900
   docked mode, floating chrome, host switching, focus loss key release, and
   close-versus-stop behavior in snapshots and unit tests.

## Acceptance criteria

- Selecting `Float` moves the same session without a second worker or reconnect.
- Selecting `Dock` restores the same framebuffer and input focus path.
- Hide and close preserve the runtime; Stop releases worker, credentials, and
  performance capture.
- MainWindow and `DesktopRdpWindow` use the same surface tokens and status
  language.
- No RDP credential, endpoint, certificate, Profile path, or pixel data enters
  Core request DTOs, logs, diagnostics, or persisted UI preferences.
- Read-only browser snapshots remain visibly distinct from interactive RDP.
