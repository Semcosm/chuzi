# Information Architecture

## Current shell

~~~text
Titlebar
Sidebar: Sessions | Settings | Content | Inspector
                              \\ Overlay when the Inspector is narrow
~~~

The titlebar owns the appearance toggle. The sidebar contains Sessions and a
Settings destination. Core install/start recovery and local diagnostic export
are conditional flows attached to the current destination. Settings uses the
same shell and owns appearance, Core lifecycle, launcher update behavior,
startup, and diagnostic privacy guidance. The former Overview, Accounts, Jobs,
Adapters, and RDP login routes remain removed.

An authorized interactive RDP session opens in the Sessions Workspace by
default. `DesktopRdpWindow` is the optional floating host for that same runtime.
Dock/Float/Hide/Stop are explicit workspace actions; they do not create a second
worker or reconnect. The runtime boundary remains separate from Core request
projection and UI navigation.

## Sessions filters

~~~text
All | Running | Queued | Failed
~~~

Do not promote CPU, browser, CDP, worker, queue service, Matrix, or storage to
primary navigation. Those are implementation or diagnostics details.

## Session list row

Required when data is available:

~~~text
Redacted account label and request ID
Business status and attempt
Last-updated value
One contextual primary action
More menu when another action is available
~~~

The current Core projection does not provide game, region, adapter, runtime, or
elapsed-time facts. Omit those fields. Never synthesize them from a screenshot
or local path.

## Empty, loading, and error states

- Empty: explain that requests appear after one is submitted for an authorized
  account.
- Loading: preserve shell geometry and use a quiet placeholder.
- Unavailable: state that Core must be started and keep previously loaded safe
  data visible when possible.
- Error: use a bounded, user-facing classification and offer retry in context.

## Responsive priority

When width decreases:

1. Preserve the Sessions list and its primary action.
2. Collapse the Inspector to a toggle or overlay.
3. Collapse the Sidebar to its compact rail.
4. Reduce row metadata only after those steps.
