# Windows desktop client

The Windows client is a Rust + Slint application that talks to Core only
through the launcher. The launcher owns Core lifecycle and `chuzi.core/v1`
transport calls. The UI does not open bbolt, the named pipe, credentials, or
browser Profile directories.

## Current UI

The main window has one destination: **Sessions**. It loads redacted Core
requests, with local search and All/Running/Queued/Failed filters, selection,
a contextual Inspector, and on-demand read-only browser previews. Core
installation/start recovery controls appear in the Sessions view when needed.
The titlebar appearance control changes and saves the UI-local theme. Diagnostic
consent is shown only before submitting a report.

The former Overview, Accounts, Tasks, Adapters, Settings, and RDP login pages
and their compatibility routes have been removed. The main window does not
keep hidden legacy pages. New destinations must use the current shell and
projections.

Core `list_requests` does not enumerate accounts without requests and does not
provide game, region, runtime, or elapsed-time facts. The UI omits those fields.
Search and status filters operate on already loaded request pages; the list
loads 100 requests at a time. The current Store reader still scans and sorts all
persisted requests before Core applies the page window.

## Diagnostics and support reports

Before a Core operation submits a report, the client asks for consent and
explains the collection scope. Reports contain a bounded classification, a
redacted user-visible summary, recent redacted operational events, and
platform/version metadata. Passwords, tokens, cookies, page content, screenshots,
RDP credentials, and raw paths are excluded. Refusing or cancelling does not
make a network request. If the developer HTTPS endpoint is unavailable, Core
stores the report in its owner-only local queue and retries later.

## RDP Session Workspace

The default host for an authorized interactive RDP session is the main Sessions
workspace. `DesktopRdpWindow` remains the optional floating host for the same
runtime. Docking, floating, hiding, and stopping are explicit host or lifecycle
actions; switching hosts must not create a second FreeRDP worker or implicitly
reconnect. The legacy RDP login page stays removed.

The current build includes the shared workspace surface and host lifecycle, but
the production service has not yet wired a deployment-specific RDP authorizer
and material handoff. The Sessions Inspector therefore keeps Open RDP hidden
until Core returns a valid short-lived capability. `get_browser_view` remains a
separate, read-only preview.

Keep FreeRDP lifecycle, TLS/NLA negotiation, credential prompting, framebuffer
handling, input events, and performance capture behind a host-neutral runtime
boundary. The main window and `DesktopRdpWindow` should share the workspace
surface and status language. See `docs/ui/rdp-workspace.md` for the staged
integration contract.

The runtime can record paired `rdp-<session>.rdp.log` and
`rdp-<session>.presentmon.csv` files below
`%ProgramData%\chuzi\data\rdp-performance`. Internal records exclude the RDP
host, account, credentials, certificate, and pixel data. PresentMon reads
Windows ETW presentation events; Chuzi does not elevate the collector
automatically. `CHUZI_RDP_DIRTY_FRAME_MODE=optimized` uses dirty-rectangle
updates and publishes the newest complete framebuffer snapshot; `legacy` keeps
the previous full-frame copy path for A/B captures.

For a saved runtime capture, summarize the RDP and PresentMon data from the
repository root:

```powershell
$perf = "$env:ProgramData\chuzi\data\rdp-performance"
python scripts/analyze_rdp_perf.py "$perf\rdp-<session>.rdp.log" `
  --presentmon "$perf\rdp-<session>.presentmon.csv" `
  --process Chuzi.Native.Windows.exe
```

The analyzer reports presentation and client-copy metrics; it does not claim
that GPU duration equals total GPU utilization. CI uses deterministic synthetic
records and does not establish live RDP FPS or GPU timing.

## Runtime and packaging

The Slint executable targets `x86_64-pc-windows-msvc` and is installed by the
Windows EXE installer. The installer carries a matching `CorePayload` directory
with the Go service, launcher, browser worker, PresentMon, and release manifest.
Core remains a separate process after the UI window closes. Uninstall keeps
`%ProgramData%\chuzi` by default; choosing removal deletes its data, including
saved RDP performance logs.

For local Windows builds, install Rust, Cargo, and Inno Setup 6, then run:

```powershell
./scripts/build_windows_slint.ps1 -Configuration Release -OutputDir "$PWD/dist/windows-ui" -CorePayloadDir "$PWD/dist/windows-amd64/stage"
```

The CI workflow uses the same script and publishes the
`chuzi-windows-installer-exe` artifact. The package does not require Windows App
Runtime MSIX packages or a signing certificate.

## Layout diagnostics

Use slint-lsp for syntax, type, property, and binding diagnostics while editing
.slint files. The supported development environment is Arch Linux with Codex
and the terminal toolchain; VS Code is not required.

For installed tool versions, the optional Slint MCP debug path, the snapshot
matrix, and UI verification rules, read docs/ui/tooling.md.

The `layout_snapshot` example renders the current Sessions screen for mixed,
empty, loading, error, unavailable, selected, compact Inspector, More menu,
cancellation confirmation, keyboard focus, and disabled-action fixture states,
in Light and Dark at 800x600, 1120x760, and 1440x900. Fixtures are synthetic
and isolated to the example; production startup always loads Core data.

```bash
cargo run --manifest-path ui/windows/Cargo.toml --features layout-snapshot \
  --example layout_snapshot -- --session-state mixed --theme both \
  --output dist/windows-layout
```

The probe checks output dimensions and rejects a blank render. Use Slint's
`VerticalBox` and `HorizontalBox` for page structure with explicit minimum,
preferred, and maximum sizes, stretch, spacing, and padding.

## Design constraints

- Core owns account and request business state; the client renders redacted
  projections only.
- Keep each Core operation asynchronous from the Slint event loop and show a
  bounded status message when an operation fails.
- Route Core lifecycle and API operations through the launcher façade.
- Keep UI theme state local; do not add presentation-only fields to the Core
  launcher's strict behavior-settings contract.
- Do not add credentials, browser Profile paths, or arbitrary filesystem paths
  to the UI.
