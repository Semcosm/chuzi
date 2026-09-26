# Windows desktop client

The Windows client is a Rust + Slint desktop application. It is a client of
the launcher boundary; the launcher owns Core lifecycle and `chuzi.core/v1`
transport calls. The UI does not open the bbolt store, named pipe, credentials,
or browser Profile directories.

The UI is intentionally small and operational. It uses one window with a
compact navigation rail, grouped content cards, and a palette that can follow
the operating-system color scheme:

```text
┌─────────────────────────────────────────────────────────────────────────────┐
│ Chuzi                                                                       │
├───────────────┬─────────────────────────────────────────────────────────────┤
│ Overview      │                                                             │
│ Plugins       │                 selected Slint page                          │
│ Accounts      │                                                             │
│ Tasks         │                                                             │
│               │                                                             │
│               │                                                             │
│ Settings      │                                                             │
└───────────────┴─────────────────────────────────────────────────────────────┘
```

## Pages

### Overview

The default page is the Core status center. It shows the current state, the
resolved data directory, lifecycle actions, and the first-run checklist.

```text
Welcome to Chuzi
Review Core status, configure components and plugins, and personalize the client from one place.

┌ Core status ────────────────────────────────────────────────┐
│ Core is running / Core is stopped / Core is not installed    │
│ status details and data directory                            │
└──────────────────────────────────────────────────────────────┘

[Start Core] [Stop Core] [Refresh]

┌ First-run checklist ────────────────────────────────────────┐
│ 1. Install Core from Settings > Components                    │
│ 2. Start Core to unlock account and plugin actions            │
│ 3. Review components and plugins                              │
└──────────────────────────────────────────────────────────────┘
```

### Settings

Settings is divided into Core, Components, Updates, Appearance, and Startup groups. It
exposes Core lifecycle actions, update preferences, login startup,
close-to-tray behavior, and a save action. Appearance provides System default,
Light, and Dark modes.
The update selector exposes the independent Test, Nightly, and Stable channels
and persists the selected channel through the launcher's validated settings.
The selected mode is applied through Slint 1.18's `Palette.color-scheme`, so
native controls and the content surface update together. The UI-only choice is
stored in `.chuzi-ui-settings.json` below the resolved Chuzi data directory;
launcher behavior settings remain in the launcher's validated settings file.

The snapshot example also accepts `--page overview|plugins|accounts|tasks|rdp|settings`
so each page can be checked at the supported window sizes.

### Components

Component management is grouped under Settings. It reads the release manifest
through the launcher and exposes component listing, installation, enablement,
disablement, and removal. Core lifecycle status and start/stop actions remain
on Overview and in the Settings Core group; component operations do not require
Core to be running.

### Plugins

Plugins displays a security-oriented summary and lifecycle actions. Core must
be running before refresh, install, trust, enable, or remove actions are
enabled. All actions are delegated to the launcher; the UI never opens the Core
endpoint. The current release exposes launcher package/trust state. A separate
Core adapter registry and runtime health projection still require the adapter
manager increment described in the roadmap.

### Accounts

Accounts accepts an authorized account ID, then asks the launcher to perform
`get_account` or `submit_request` through Core. Only the redacted account state
is shown; credentials are never stored by the client.

### Tasks

Tasks accepts a request ID and asks the launcher to perform `get_request` or
`cancel_request`. The page
shows the redacted request ID, account, state, attempt number, and failure text. While the request has an
active headless browser session, `View page` calls `get_browser_view` to fetch one bounded JPEG frame. The
frame is read-only and on demand; the client cannot navigate, click, type, or access a browser endpoint.

### Diagnostics and support reports

When Core, RDP, or another client operation reports an error or warning, the
client asks whether to send diagnostics. The consent dialog describes the
collection scope before any report is created. Accepting sends only a bounded
classification, a redacted user-visible summary, recent redacted operational
events, and platform/version metadata. Passwords, tokens, cookies, page content,
screenshots, RDP credentials, and raw paths are excluded. Refusing or cancelling
does not make a network request. If the developer HTTPS endpoint is unavailable,
Core stores the report in its owner-only local queue and retries later.

### RDP

RDP opens a Windows-only Rust/FreeRDP session. FreeRDP owns TLS/NLA negotiation
and protocol decoding; the client copies its RGBA32 framebuffer into an owned
Slint image for display. When credentials are not supplied by the caller,
Windows' temporary credential prompt is used with persistence disabled. The UI
does not save or send RDP passwords through Core. Click the remote framebuffer
to give it keyboard focus. Mouse movement, left, right, and middle buttons,
vertical and horizontal wheel input, and keyboard press/release events are
forwarded through the FreeRDP worker. Keys held during focus loss are released
on the remote session.

#### RDP performance capture

The RDP login page has two independent options: `显示性能分析 HUD` shows live
client and PresentMon measurements in the session window, while
`记录性能分析数据` saves raw samples for later analysis. Both are off by
default. The Windows installer bundles PresentMon 2.6.0 as a required release
component, so no separate download or setup is needed.

Recorded files are stored under
`%ProgramData%\chuzi\data\rdp-performance`. Each recorded session writes
paired `rdp-<session>.rdp.log` and `rdp-<session>.presentmon.csv` files. The
internal JSON records contain frame mode, RDP update rate, dirty rectangle
area, copy traffic and timing, queue overwrites, UI tick timing, and UI handoff
intervals. They exclude the RDP host, account, credentials, certificate, and
pixel data. If only the HUD is enabled, its temporary PresentMon CSV is deleted
when the session closes.

The HUD reports the RDP update rate, dirty area, copy bandwidth and time,
snapshot time, UI handoff p50/p95, delivery ratio, PresentMon FPS, and dropped
presents. If the bundled collector cannot start, the HUD displays its status and
the internal RDP metrics remain available. PresentMon reads Windows ETW
presentation events; if it exits for lack of access, run Chuzi elevated or add
the signed-in account to the Windows `Performance Log Users` group and sign in
again. Chuzi does not elevate the collector automatically.

The default `CHUZI_RDP_DIRTY_FRAME_MODE=optimized` path keeps one complete CPU
framebuffer, copies only the clipped union of FreeRDP's dirty rectangles during
EndPaint, and lets the 16 ms Slint timer publish the newest complete snapshot.
This bounds UI-thread work and drops stale snapshots when RDP updates arrive
faster than the UI. Set `CHUZI_RDP_DIRTY_FRAME_MODE=legacy` for an A/B capture
of the previous full-frame copy and queue behavior. Keep the mode fixed for both
runs in a comparison.

For integrated recording, summarize the saved RDP and PresentMon data together
from the repository root:

    $perf = "$env:ProgramData\chuzi\data\rdp-performance"
    python scripts/analyze_rdp_perf.py "$perf\rdp-<session>.rdp.log" `
      --presentmon "$perf\rdp-<session>.presentmon.csv" `
      --process Chuzi.Native.Windows.exe

For legacy developer captures, summarize the stderr log with:

    python scripts/analyze_rdp_perf.py --json .\\rdp-client.stderr.log
    python scripts/analyze_rdp_perf.py .\\rdp-client.stderr.log

For a legacy external PresentMon capture, run it on the same Windows host
against Chuzi.Native.Windows.exe and export its CSV alongside the stderr log.
PresentMon measures the final
desktop-present path, including present interval, display-change interval,
display latency, GPU/CPU duration, dropped presents, PresentMode, tearing flags,
and sync interval. rdp_perf measures the client-side copy and UI handoff path.
Review both sources together with:

    python scripts/analyze_rdp_perf.py .\\rdp-client.stderr.log \\
      --presentmon .\\presentmon.csv \\
      --process Chuzi.Native.Windows.exe

The analyzer reports GPU duration and PresentMon presentation metrics but does
not claim that GPU duration equals total GPU utilization. The repository CI test
uses deterministic synthetic records and does not claim real RDP FPS or GPU
timing.

#### Capture matrix

Run each scenario for 60-90 seconds after the initial desktop has settled. Use
the same host, client build, network path, power mode, window size, and frame
mode for the optimized and legacy runs. Repeat at 1280x720 and 1920x1080; add
2560x1440 when the host supports it.

| Scenario | Remote activity | What to compare |
| --- | --- | --- |
| Static desktop | Idle desktop with no animation | Dirty area, copy bandwidth, UI tick stability |
| Moving text/windows | Scroll a document and drag overlapping windows | RDP update rate, dirty union ratio, queue overwrites, handoff p50/p95 |
| Animation/video | Play a bounded 30-60 fps clip in a window | Present FPS, dropped presents, present interval p95, copy time |
| Resolution change | Repeat after resizing the session to each target resolution | First-frame full copy, steady-state dirty copy, snapshot latency |

PresentMon exports must be CSV from the same run and retain these columns (or
their current PresentMon equivalents): `Application`, `TimeInSeconds`,
`MsBetweenPresents`, `MsBetweenDisplayChange`, `MsUntilDisplayed`,
`GPUDuration`, `CPUDuration`, `PresentMode`, `Dropped`, `AllowsTearing`, and
`SyncInterval`. Keep the process name in the export and pass
`--process Chuzi.Native.Windows.exe` so unrelated desktop presents do not skew
the result. For integrated captures, preserve the `.rdp.log` and PresentMon CSV
with the summarized JSON for each 60-90 second scenario. Legacy manual captures
can preserve their stderr log and CSV instead.

## Runtime and packaging

The Slint executable is built for `x86_64-pc-windows-msvc` and installed by the
same conventional EXE installer used by the rest of the Windows distribution.
The installer carries a matching `CorePayload` directory containing the Go
service, launcher, browser worker, PresentMon, and release manifest. Core remains
a separate process and continues running when the UI window closes. During
uninstall, Chuzi asks whether to keep `%ProgramData%\chuzi`; keeping user data
is the default. Choosing No removes its data, including saved RDP performance
logs.

For local Windows builds, install Rust, Cargo, and Inno Setup 6, then run:

```powershell
./scripts/build_windows_slint.ps1 -Configuration Release -OutputDir "$PWD/dist/windows-ui" -CorePayloadDir "$PWD/dist/windows-amd64/stage"
```

The CI workflow uses the same script and publishes the
`chuzi-windows-installer-exe` artifact. The package is self-contained and does
not require Windows App Runtime MSIX packages or a signing certificate.

## Layout diagnostics

Install the Slint editor extension or `slint-lsp` for live syntax, type,
property, binding, and compiler diagnostics while editing `.slint` files. Slint
Live Preview can be used for interactive inspection; the repository also has a
headless screenshot probe that renders the Overview page at several fixed
viewports without opening a native window:

```bash
cargo run --manifest-path ui/windows/Cargo.toml --example layout_snapshot -- \
  --output dist/windows-layout
```

The probe writes `800x600.png`, `1120x760.png`, and `1440x900.png`, checks their
dimensions, and rejects a blank render. Keep layout expressed with Slint
`VerticalBox`/`HorizontalBox` containers and explicit `min-*`, `preferred-*`,
`max-*`, stretch, spacing, and padding constraints; avoid manual `x`/`y`
position arithmetic for page structure. CI runs this probe on the Windows
runner as part of the Slint build job.

## Design constraints

- Keep UI state separate from Core business state. Core owns account and request
  state; the client only renders redacted projections.
- Keep each Core operation asynchronous from the Slint event loop and display a
  bounded status message when an operation fails.
- Route Core lifecycle and API operations through the launcher façade. The
  launcher owns the owner-only named pipe; the optional HTTP health listener
  remains disabled by default.
- Keep plugin trust and removal actions explicit; an untrusted plugin cannot be
  enabled.
- Keep theme state UI-local. Do not add presentation-only fields to the Core
  launcher's strict behavior-settings contract.
- Do not add credential fields, browser Profile paths, or arbitrary filesystem
  paths to the UI.
