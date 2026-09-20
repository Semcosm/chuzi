# Windows desktop client

The Windows client is a Rust + Slint desktop application. It is a client of
the `chuzi.core/v1` and launcher boundaries; it does not open the bbolt store or
read credentials or browser Profile directories.

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
The selected mode is applied through Slint 1.18's `Palette.color-scheme`, so
native controls and the content surface update together. The UI-only choice is
stored in `.chuzi-ui-settings.json` below the resolved Chuzi data directory;
launcher behavior settings remain in the launcher's validated settings file.

The snapshot example also accepts `--page overview|plugins|accounts|tasks|settings`
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
enabled. The plugin boundary remains responsible for signer and permission
validation.

### Accounts

Accounts accepts an authorized account ID, then sends `get_account` or
`submit_request` through the Core named pipe. Only the redacted account state is
shown; credentials are never stored by the client.

### Tasks

Tasks accepts a request ID and sends `get_request` or `cancel_request`. The page
shows the redacted request ID, account, state, attempt number, and failure text. While the request has an
active headless browser session, `View page` calls `get_browser_view` to fetch one bounded JPEG frame. The
frame is read-only and on demand; the client cannot navigate, click, type, or access a browser endpoint.

## Runtime and packaging

The Slint executable is built for `x86_64-pc-windows-msvc` and installed by the
same conventional EXE installer used by the rest of the Windows distribution.
The installer carries a matching `CorePayload` directory containing the Go
service, launcher, browser worker, and release manifest. Core remains a
separate process and continues running when the UI window closes.

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
- Use the owner-only named pipe for Core API calls. The optional HTTP health
  listener remains disabled by default.
- Keep plugin trust and removal actions explicit; an untrusted plugin cannot be
  enabled.
- Keep theme state UI-local. Do not add presentation-only fields to the Core
  launcher's strict behavior-settings contract.
- Do not add credential fields, browser Profile paths, or arbitrary filesystem
  paths to the UI.
