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

The packaged build follows the Gallery's working deployment model:

- packaged MSIX with `WindowsPackageType=MSIX`;
- `WindowsAppSDKSelfContained=true` so the app carries its Windows App SDK
  runtime and does not require a separately installed Runtime MSIX;
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
./scripts/build_windows_ui.ps1 -Mode UnpackagedZip -Configuration Release -OutputDir "$PWD/dist/windows-ui"
```

The unpackaged zip is a diagnostics-only payload. A packaged MSIX includes a
`CorePayload` directory containing the matching Windows service, launcher, worker
files, and release manifest. On first launch, Overview > Install Core copies the
verified service components into the per-machine data directory, starts the service,
and confirms readiness over the Core named pipe. If the payload is absent (for
example in a locally built unpackaged zip), the UI reports that Core must be supplied
by deployment.

The first-run sequence is:

1. Install the signed MSIX and launch Chuzi.
2. Select **Install Core** on the Overview page.
3. Configure plugins and explicitly trust their declared signer before enabling them.
4. Adjust update and startup behavior under Settings.

The service process is owned by the installation, not by the window. Closing the UI
leaves Core running; the Stop Core action is explicit and never stops a service that
was started externally.

The CI path uses the same script in `PackagedMsix` mode and uploads the
`chuzi-windows-msix-self-contained` artifact. The test-signed package requires
the CI certificate on the target Windows machine, but it does not require a
separate `Microsoft.WindowsAppRuntime.*.msix` file.
