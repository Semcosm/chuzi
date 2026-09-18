# Windows native client

This is the native Windows client for the `chuzi.core/v1` boundary. The current
Windows surface intentionally starts from the official WinUI 3 single-project
template and is kept as a small startup smoke app while controls are added back
incrementally.

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

Build on a Windows host with the Windows App SDK workload:

```powershell
./scripts/build_windows_ui.ps1 -Configuration Release -OutputDir "$PWD/dist/windows-ui"
```

The script publishes a self-contained UI payload only as a zip artifact. The
service remains a separately managed process and must already be running.

For CI packaging, use the `chuzi-windows-msix-self-contained` artifact. The
test-signed package requires the CI certificate on the target Windows machine,
but it does not require a separate `Microsoft.WindowsAppRuntime.*.msix` file.
