# Windows native client

This is the native Windows client for the `chuzi.core/v1` boundary. The Windows
surface follows the official WinUI 3 single-project template and provides three
first-run workflows: Core installation/status, launcher settings, and plugin
management. The UI remains a client of the launcher/Core boundaries; it never opens
the bbolt store or reads credentials/Profile directories.

The primary implementation reference is Microsoft's WinUI Gallery:

- Repository: <https://github.com/microsoft/WinUI-Gallery>
- Template source: `WinUIGallery/App.xaml`, `App.xaml.cs`, `MainWindow.xaml`,
  `MainWindow.xaml.cs`, `Package.appxmanifest`, and `WinUIGallery.csproj`
- Reference checkout used during development: WinUI Gallery commit
  `abb8cb4cef04a5080f5c0396f67a7ec502b36179`

When adding a control or page, copy the corresponding WinUI Gallery sample
structure first, then adapt the namespace, assets, and Chuzi behavior. Do not
invent a second startup path or a custom `Application.Start` entry point unless
the deployment mode explicitly requires it. Keep window construction cheap:
`MainWindow` should initialize XAML first and defer Core/network work until the
window is loaded or the user invokes an action.

The installer build follows the Gallery's working WinUI deployment model while
using a conventional EXE installer instead of AppX/MSIX:

- unpackaged WinUI 3 publish with `WindowsPackageType=None`;
- `WindowsAppSDKSelfContained=true` and a self-contained .NET publish, so the
  installer carries the Windows App SDK and .NET runtime files;
- Inno Setup installs the published files under `Program Files\Chuzi`, creates
  Start Menu and optional desktop shortcuts, and registers an uninstaller;
- minimum Windows version `10.0.17763.0` (Windows 10 1809+);
- SDK-generated WinUI entry point, not a hand-written `Program.Main`.

The Core transport implementation remains in `CoreApiClient.cs`. It derives the
endpoint from `CHUZI_DATA_DIR` when supplied by deployment, or from
`%ProgramData%\chuzi` otherwise. It derives the same owner-only pipe name as
`internal/coretransport` and performs `hello` version negotiation before any
business call. It never opens the bbolt database or reads credentials/Profile
directories.

For local unpackaged diagnostics on a Windows host:

```powershell
./scripts/build_windows_ui.ps1 -Mode InstallerExe -Configuration Release -OutputDir "$PWD/dist/windows-ui" -CorePayloadDir "$PWD/dist/windows-amd64/stage"
```

`InstallerExe` requires Inno Setup 6 (`ISCC.exe`) on the build host. GitHub
Actions uses the Windows runner's installed Inno Setup toolchain.

The installer EXE includes a `CorePayload` directory containing the matching
Windows service, launcher, worker files, and release manifest. On first launch,
Overview > Install Core copies the verified service components into the per-machine
data directory, starts the service, and confirms readiness over the Core named pipe.
If the payload is absent (for example in a locally built diagnostics zip), the UI
reports that Core must be supplied by deployment.

For a lightweight local diagnostics bundle, use:

```powershell
./scripts/build_windows_ui.ps1 -Mode UnpackagedZip -Configuration Release -OutputDir "$PWD/dist/windows-ui"
```

The first-run sequence is:

1. Run `ChuziSetup.exe` and launch Chuzi from the Start Menu or desktop shortcut.
2. Use the Gallery-style navigation pane to stay on Overview while setup is in progress.
3. Select **Install Core** on the Overview page and wait for the ready state.
4. Configure plugins and explicitly trust their declared signer before enabling them.
5. Adjust update and startup behavior under Settings.

The Overview page is the first-run status center. It distinguishes missing,
starting, running, stopped, and unavailable Core states, shows the resolved data
directory, disables duplicate actions while a lifecycle operation is running,
and keeps the next setup steps visible until Core is ready.

The service process is owned by the installation, not by the window. Closing the UI
leaves Core running; the Stop Core action is explicit and never stops a service that
was started externally.

The CI path uses the same script in `InstallerExe` mode and uploads the
`chuzi-windows-installer-exe` artifact. The installer is self-contained and does
not require a separate certificate or `Microsoft.WindowsAppRuntime.*.msix` file.
