# Windows native client

This is the first native client for the `chuzi.core/v1` boundary. It uses WinUI 3
for the window and controls and `NamedPipeClientStream` for the local Core
transport. It only submits, reads, and cancels requests; it never opens the
bbolt database or reads credentials/Profile directories.

The endpoint is derived from `CHUZI_DATA_DIR` when supplied by deployment, or
from `%ProgramData%\chuzi` otherwise. The client derives the same owner-only
pipe name as `internal/coretransport` and performs `hello` version negotiation
before any business call. The UI displays classified Core errors and never
renders raw transport or storage error text.

Build on a Windows host with the Windows App SDK workload:

```powershell
./scripts/build_windows_ui.ps1 -Configuration Release -OutputDir "$PWD/dist/windows-ui"
```

The script publishes a self-contained UI payload only as a zip artifact. The
service remains a separately managed process and must already be running.
