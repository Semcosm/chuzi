using System.Diagnostics;
using System.Text.Json;

namespace Chuzi.Native.Windows;

public enum CoreStatus
{
    Missing,
    Stopped,
    Starting,
    Running,
    Unavailable,
}

public sealed record CoreSnapshot(CoreStatus Status, int? ProcessId, bool OwnedByThisWindow, string Message);

internal sealed class CoreServiceController : IDisposable
{
    private readonly LauncherClient _launcher;
    private Process? _process;
    private bool _ownsProcess;

    public CoreServiceController(LauncherClient launcher) => _launcher = launcher;

    public async Task<CoreSnapshot> GetStatusAsync(CancellationToken cancellationToken)
    {
        var executable = FindServiceExecutable();
        if (executable is null)
        {
            return new CoreSnapshot(CoreStatus.Missing, null, false, "Core is not installed.");
        }
        if (_process is { HasExited: false })
        {
            return await ProbeAsync(CoreStatus.Running, _process.Id, true, cancellationToken);
        }
        return await ProbeAsync(CoreStatus.Stopped, null, false, cancellationToken);
    }

    public async Task<CoreSnapshot> InstallAndStartAsync(CancellationToken cancellationToken)
    {
        await _launcher.InstallCoreAsync(cancellationToken);
        return await StartAsync(cancellationToken);
    }

    public async Task<CoreSnapshot> StartAsync(CancellationToken cancellationToken)
    {
        var existing = await GetStatusAsync(cancellationToken);
        if (existing.Status == CoreStatus.Running) return existing;
        var executable = FindServiceExecutable();
        if (executable is null)
        {
            return new CoreSnapshot(CoreStatus.Missing, null, false, "Install Core before starting the service.");
        }

        Directory.CreateDirectory(_launcher.DataRoot);
        var configPath = await EnsureConfigAsync(cancellationToken);
        var startInfo = new ProcessStartInfo
        {
            FileName = executable,
            WorkingDirectory = _launcher.DataRoot,
            UseShellExecute = false,
            CreateNoWindow = true,
            RedirectStandardOutput = true,
            RedirectStandardError = true,
        };
        startInfo.ArgumentList.Add("-config");
        startInfo.ArgumentList.Add(configPath);
        startInfo.Environment["CHUZI_DATA_DIR"] = _launcher.DataRoot;
        var logPath = Path.Combine(_launcher.DataRoot, "core.log");
        var log = new StreamWriter(logPath, append: true) { AutoFlush = true };
        var process = new Process { StartInfo = startInfo, EnableRaisingEvents = true };
        process.Exited += (_, _) => log.Dispose();
        if (!process.Start())
        {
            log.Dispose();
            throw new LauncherException("Core service could not be started.");
        }
        _process = process;
        _ownsProcess = true;
        _ = DrainAsync(process.StandardOutput, log);
        _ = DrainAsync(process.StandardError, log);

        var deadline = DateTime.UtcNow + TimeSpan.FromSeconds(15);
        while (DateTime.UtcNow < deadline)
        {
            cancellationToken.ThrowIfCancellationRequested();
            if (process.HasExited)
            {
                return new CoreSnapshot(CoreStatus.Unavailable, null, true, "Core stopped during startup. Check core.log for details.");
            }
            var probe = await ProbeAsync(CoreStatus.Starting, process.Id, true, cancellationToken);
            if (probe.Status == CoreStatus.Running) return probe;
            await Task.Delay(250, cancellationToken);
        }
        return new CoreSnapshot(CoreStatus.Unavailable, process.Id, true, "Core did not become ready. Check core.log for details.");
    }

    public async Task<CoreSnapshot> StopAsync(CancellationToken cancellationToken)
    {
        if (_process is null || _process.HasExited || !_ownsProcess)
        {
            return await GetStatusAsync(cancellationToken);
        }
        try
        {
            _process.Kill(entireProcessTree: true);
            await _process.WaitForExitAsync(cancellationToken);
        }
        catch (InvalidOperationException) { }
        finally
        {
            _process.Dispose();
            _process = null;
            _ownsProcess = false;
        }
        return new CoreSnapshot(CoreStatus.Stopped, null, false, "Core is stopped.");
    }

    private async Task<CoreSnapshot> ProbeAsync(CoreStatus fallback, int? processId, bool owned, CancellationToken cancellationToken)
    {
        try
        {
            using var client = CoreApiClient.FromDeploymentEnvironment();
            await client.ConnectAsync(cancellationToken);
            return new CoreSnapshot(CoreStatus.Running, processId, owned, "Core is ready.");
        }
        catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested) { throw; }
        catch (Exception exception) when (exception is OperationCanceledException or IOException or TimeoutException or CoreApiException)
        {
            return new CoreSnapshot(fallback, processId, owned, fallback == CoreStatus.Starting ? "Waiting for Core..." : "Core is not reachable.");
        }
    }

    private async Task<string> EnsureConfigAsync(CancellationToken cancellationToken)
    {
        var path = Path.Combine(_launcher.DataRoot, "core-config.json");
        if (File.Exists(path)) return path;
        var config = new
        {
            data_dir = _launcher.DataRoot,
            credentials = new { key_env = "CHUZI_CREDENTIAL_KEY", key_id_env = "CHUZI_CREDENTIAL_KEY_ID", history_env = "CHUZI_CREDENTIAL_KEYS" },
            health = new { listen = "127.0.0.1:8080" },
            observability = new { metrics_listen = "", log_path = Path.Combine(_launcher.DataRoot, "core-service.log"), log_max_bytes = 10485760, log_max_files = 5 },
        };
        await File.WriteAllTextAsync(path, JsonSerializer.Serialize(config), cancellationToken);
        return path;
    }

    private string? FindServiceExecutable()
    {
        var installed = Path.Combine(_launcher.DataRoot, "chuzi.exe");
        return File.Exists(installed) ? installed : null;
    }

    private static async Task DrainAsync(StreamReader reader, StreamWriter log)
    {
        try
        {
            while (await reader.ReadLineAsync() is { } line) await log.WriteLineAsync(line);
        }
        catch (ObjectDisposedException) { }
        finally { reader.Dispose(); }
    }

    public void Dispose()
    {
        // Core is a user-installed background component. Closing the UI must
        // not terminate it; StopAsync is the explicit lifecycle action.
        _process?.Dispose();
        _process = null;
        _ownsProcess = false;
    }
}
